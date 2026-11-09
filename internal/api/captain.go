package api

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
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
		r.Post("/images", c.addImage)
		r.Delete("/images/{imageID}", c.removeImage)
		r.Put("/images/order", c.orderImages)
	})
	r.Get("/repos", c.repos)
	r.Patch("/repos/{id}", c.patchRepo)
	r.Post("/repos/{id}/card-preview", c.repoCardPreview)
	r.Get("/templates", c.templates)
	r.Post("/templates", c.createTemplate)
	r.Put("/templates/{id}", c.saveTemplate)
	r.Delete("/templates/{id}", c.deleteTemplate)
	r.Post("/qr-preview", c.qrPreview)
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
	case errors.Is(err, launch.ErrTooBig):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", imageTooLarge)
	case errors.Is(err, launch.ErrImageNotImage):
		writeError(w, http.StatusBadRequest, "not_image", "Use a JPEG, PNG, GIF or WebP image.")
	case errors.Is(err, launch.ErrTooManyImages):
		writeError(w, http.StatusBadRequest, "too_many_images", "A launch can have up to 10 images.")
	case errors.Is(err, launch.ErrImagesLocked):
		writeError(w, http.StatusConflict, "images_locked", "Images are locked once the launch is out.")
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
	TemplateID    string         `json:"templateId"`
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
		QRLogo: b.QRLogo, QRLogoURL: b.QRLogoURL, TemplateID: b.TemplateID,
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

const imageTooLarge = "Each image can be up to 10 MB."

// addImage takes a multipart form with one file field, image, and appends
// it to the launch's images. The body is capped a little above the image
// limit so the multipart framing fits; the file itself must be within it.
func (c *Captain) addImage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, launch.MaxImageBytes+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", imageTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request", "send the image as a multipart form field named image")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	f, hdr, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "send the image as a multipart form field named image")
		return
	}
	defer f.Close()
	if hdr.Size > launch.MaxImageBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", imageTooLarge)
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, launch.MaxImageBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "the image could not be read")
		return
	}
	if len(data) > launch.MaxImageBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", imageTooLarge)
		return
	}
	img, err := launch.AddImage(r.Context(), c.deps, launchID(r), data)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, img)
}

func (c *Captain) removeImage(w http.ResponseWriter, r *http.Request) {
	imageID, err := uuid.Parse(chi.URLParam(r, "imageID"))
	if err != nil {
		notFound(w, r)
		return
	}
	if err := launch.RemoveImage(r.Context(), c.deps, launchID(r), imageID); err != nil {
		c.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Captain) orderImages(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := launch.ReorderImages(r.Context(), c.deps, launchID(r), body.IDs); err != nil {
		c.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// repoCardPreview draws a sample card with unsaved branding, as a data URL.
func (c *Captain) repoCardPreview(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		notFound(w, r)
		return
	}
	var p launch.RepoPatch
	if !readJSON(w, r, &p) {
		return
	}
	png, err := launch.RepoCardPreview(r.Context(), c.deps, userID(r), id, p)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"png": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}

func (c *Captain) templates(w http.ResponseWriter, r *http.Request) {
	v, err := launch.Templates(r.Context(), c.deps, userID(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (c *Captain) createTemplate(w http.ResponseWriter, r *http.Request) {
	var in launch.TemplateInput
	if !readJSON(w, r, &in) {
		return
	}
	t, err := launch.CreateTemplate(r.Context(), c.deps, userID(r), in)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (c *Captain) saveTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		notFound(w, r)
		return
	}
	var in launch.TemplateInput
	if !readJSON(w, r, &in) {
		return
	}
	t, err := launch.UpdateTemplate(r.Context(), c.deps, userID(r), id, in)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (c *Captain) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		notFound(w, r)
		return
	}
	if err := launch.DeleteTemplate(r.Context(), c.deps, userID(r), id); err != nil {
		c.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// qrPreview draws the event slide for the new launch form, as a data URL.
func (c *Captain) qrPreview(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title     string `json:"title"`
		QRLogo    string `json:"qrLogo"`
		QRLogoURL string `json:"qrLogoUrl"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	png, err := launch.QRPreview(r.Context(), c.deps, b.Title, b.QRLogo, b.QRLogoURL)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"png": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}
