package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shubham-tomar/launchcrew/internal/launch"
	"github.com/shubham-tomar/launchcrew/internal/session"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

// Captain serves the captain's JSON routes, mounted at /api/captain. Every
// route needs a session; a launch or repo the user does not own is 404.
type Captain struct {
	deps     launch.Deps
	sessions *session.Store
}

// NewCaptain returns the router for the captain routes. Paths are relative to /api/captain.
func NewCaptain(deps launch.Deps, sessions *session.Store) http.Handler {
	c := &Captain{deps: deps, sessions: sessions}
	r := chi.NewRouter()
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)
	r.Use(c.signedIn)
	r.Get("/launches", c.list)
	r.Post("/launches", c.create)
	r.Route("/launches/{id}", func(r chi.Router) {
		r.Use(c.owns)
		r.Get("/", c.detail)
		r.Patch("/", c.patch)
		r.Patch("/drafts/{draftID}", c.editDraft)
		r.Post("/launch", c.launch)
		r.Post("/retry/{jobID}", c.retry)
		r.Post("/channels/refresh", c.refreshChannels)
		r.Post("/crew/{memberID}/retry-comment", c.retryComment)
	})
	r.Get("/repos", c.repos)
	r.Patch("/repos/{id}", c.patchRepo)
	return r
}

type userKey struct{}
type launchKey struct{}

func userID(r *http.Request) uuid.UUID   { return r.Context().Value(userKey{}).(uuid.UUID) }
func launchID(r *http.Request) uuid.UUID { return r.Context().Value(launchKey{}).(uuid.UUID) }

func (c *Captain) signedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := c.sessions.Get(r).UserID
		if uid == uuid.Nil {
			writeError(w, http.StatusUnauthorized, "sign_in", "sign in with Buffer first")
			return
		}
		// A session can outlive its user (deleted data): sign it out.
		if _, err := c.deps.Q.GetUser(r.Context(), uid); errors.Is(err, pgx.ErrNoRows) {
			c.sessions.Clear(w)
			writeError(w, http.StatusUnauthorized, "sign_in", "sign in with Buffer first")
			return
		} else if err != nil {
			log.Printf("captain: load user: %v", err)
			writeError(w, http.StatusInternalServerError, "internal", "something went wrong")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, uid)))
	})
}

func (c *Captain) owns(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			notFound(w, r)
			return
		}
		if _, err := launch.Authorize(r.Context(), c.deps, id, userID(r)); err != nil {
			c.fail(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), launchKey{}, id)))
	})
}

func (c *Captain) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, launch.ErrNotFound):
		notFound(w, r)
	case errors.Is(err, launch.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid", launch.Message(err))
	case errors.Is(err, trigger.ErrEmptyTitle):
		writeError(w, http.StatusBadRequest, "invalid", "the title cannot be empty")
	case errors.Is(err, trigger.ErrBadRoles):
		writeError(w, http.StatusBadRequest, "invalid", "up to 4 roles, names 1 to 24 characters")
	case errors.Is(err, launch.ErrLogoRefused):
		writeError(w, http.StatusBadRequest, "logo_refused", "that logo address cannot be used, try another public image URL under 5 MB")
	case errors.Is(err, launch.ErrLogoNotImage):
		writeError(w, http.StatusBadRequest, "logo_not_image", "that logo URL is not an image")
	case errors.Is(err, launch.ErrHeroRefused):
		writeError(w, http.StatusBadRequest, "hero_refused", "that image address cannot be used, try another public image URL under 5 MB")
	case errors.Is(err, trigger.ErrHeroNotImage):
		writeError(w, http.StatusBadRequest, "hero_not_image", "that URL is not an image")
	case errors.Is(err, launch.ErrNotDraft):
		writeError(w, http.StatusConflict, "not_draft", "this launch has gone out and can no longer be edited")
	case errors.As(err, new(*launch.PreflightError)):
		writeError(w, http.StatusConflict, "preflight_red", err.Error())
	case errors.Is(err, launch.ErrNoBuffer):
		writeError(w, http.StatusConflict, "no_buffer", "connect Buffer first")
	case errors.Is(err, launch.ErrRetryLimit):
		writeError(w, http.StatusConflict, "retry_limit", "this comment was already retried the most times allowed, copy their link instead")
	case errors.Is(err, launch.ErrAlreadyLaunched):
		writeError(w, http.StatusConflict, "already_launched", "this launch has already gone out")
	default:
		log.Printf("api: %s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusInternalServerError, "internal", "something went wrong on our side, try again")
	}
}

func (c *Captain) list(w http.ResponseWriter, r *http.Request) {
	ls, err := launch.List(r.Context(), c.deps, userID(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ls)
}

// createBody is the manual launch form; timing fields are optional.
type createBody struct {
	Title         string         `json:"title"`
	Summary       string         `json:"summary"`
	Roles         []trigger.Role `json:"roles"`
	HeroURL       string         `json:"heroUrl"`
	QRLogo        string         `json:"qrLogo"`
	QRLogoURL     string         `json:"qrLogoUrl"`
	LaunchAt      *time.Time     `json:"launchAt"`
	WindowMinutes *int           `json:"windowMinutes"`
	CrewCap       *int           `json:"crewCap"`
}

func (c *Captain) create(w http.ResponseWriter, r *http.Request) {
	var b createBody
	if !readJSON(w, r, &b) {
		return
	}
	timing := launch.Patch{LaunchAt: b.LaunchAt, WindowMinutes: b.WindowMinutes, CrewCap: b.CrewCap}
	if err := timing.Validate(time.Now()); err != nil {
		c.fail(w, r, err)
		return
	}
	l, _, err := launch.CreateManual(r.Context(), c.deps, userID(r), trigger.ManualInput{
		Title: b.Title, Summary: b.Summary, Roles: b.Roles, HeroURL: b.HeroURL,
		QRLogo: b.QRLogo, QRLogoURL: b.QRLogoURL,
	})
	if err != nil {
		c.fail(w, r, err)
		return
	}
	if b.LaunchAt != nil || b.WindowMinutes != nil || b.CrewCap != nil {
		if err := launch.Update(r.Context(), c.deps, l.ID, timing); err != nil {
			c.fail(w, r, err)
			return
		}
	}
	c.writeDetail(w, r, l.ID, http.StatusCreated)
}

func (c *Captain) writeDetail(w http.ResponseWriter, r *http.Request, id uuid.UUID, status int) {
	v, err := launch.Detail(r.Context(), c.deps, id)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

func (c *Captain) detail(w http.ResponseWriter, r *http.Request) {
	c.writeDetail(w, r, launchID(r), http.StatusOK)
}

func (c *Captain) patch(w http.ResponseWriter, r *http.Request) {
	var p launch.Patch
	if !readJSON(w, r, &p) {
		return
	}
	if err := launch.Update(r.Context(), c.deps, launchID(r), p); err != nil {
		c.fail(w, r, err)
		return
	}
	c.writeDetail(w, r, launchID(r), http.StatusOK)
}

func (c *Captain) editDraft(w http.ResponseWriter, r *http.Request) {
	draftID, err := uuid.Parse(chi.URLParam(r, "draftID"))
	if err != nil {
		notFound(w, r)
		return
	}
	var body launch.DraftPatch
	if !readJSON(w, r, &body) {
		return
	}
	if err := launch.PatchCaptainDraft(r.Context(), c.deps, launchID(r), draftID, body); err != nil {
		c.fail(w, r, err)
		return
	}
	c.writeDetail(w, r, launchID(r), http.StatusOK)
}

func (c *Captain) launch(w http.ResponseWriter, r *http.Request) {
	if err := launch.Launch(r.Context(), c.deps, launchID(r)); err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "launched"})
}

func (c *Captain) retry(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	if err != nil {
		notFound(w, r)
		return
	}
	if err := launch.Retry(r.Context(), c.deps, launchID(r), jobID); err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "retrying"})
}

func (c *Captain) retryComment(w http.ResponseWriter, r *http.Request) {
	memberID, err := uuid.Parse(chi.URLParam(r, "memberID"))
	if err != nil {
		notFound(w, r)
		return
	}
	if err := launch.RetryComment(r.Context(), c.deps, launchID(r), memberID); err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "retrying"})
}

func (c *Captain) refreshChannels(w http.ResponseWriter, r *http.Request) {
	n, err := launch.RefreshChannels(r.Context(), c.deps, launchID(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"channels": n})
}

func (c *Captain) repos(w http.ResponseWriter, r *http.Request) {
	rs, err := launch.Repos(r.Context(), c.deps, userID(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

func (c *Captain) patchRepo(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		notFound(w, r)
		return
	}
	var p launch.RepoPatch
	if !readJSON(w, r, &p) {
		return
	}
	repo, err := launch.UpdateRepo(r.Context(), c.deps, userID(r), id, p)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}
