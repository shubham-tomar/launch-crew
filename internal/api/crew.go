package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/session"
)

// Crew serves the invite, open-link and approve routes, mounted at /api.
type Crew struct {
	deps     crew.Deps
	sessions *session.Store
	slides   sync.Map // launch id -> rendered PNG; no eviction in v1
}

// NewCrew returns the router for the crew routes. Paths are relative to /api.
func NewCrew(deps crew.Deps, sessions *session.Store) http.Handler {
	c := &Crew{deps: deps, sessions: sessions}
	r := chi.NewRouter()
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)
	r.Get("/s/{token}", c.invite)
	r.Post("/s/{token}/share", c.share)
	r.Get("/l/{openToken}", c.openLink)
	r.Get("/l/{openToken}/show", c.showInfo)
	r.Get("/l/{openToken}/show.png", c.showPNG)
	r.Post("/l/{openToken}/role", c.chooseRole)
	r.Route("/crew/{id}", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(c.authorize(false))
			r.Get("/drafts", c.drafts)
			r.Get("/board", c.board)
			r.Post("/copied", c.copied)
		})
		r.Group(func(r chi.Router) {
			r.Use(c.authorize(true))
			r.Post("/connect", c.connect)
			r.Put("/drafts/{draftID}", c.saveDraft)
			r.Put("/voice", c.voice)
			r.Post("/post", c.post)
			r.Post("/disconnect", c.disconnect)
			r.Post("/remember", c.remember)
			r.Post("/channels/refresh", c.refreshChannels)
		})
	})
	return r
}

type memberKey struct{}

func member(r *http.Request) sqlc.CrewMember {
	return r.Context().Value(memberKey{}).(sqlc.CrewMember)
}

// authorize admits the holder of the member's invite token (X-Crew-Token
// or ?t=) or a session signed in as the member's user; on a bound row,
// mutating routes need the session. Anyone else gets 404, so a member id
// alone reveals nothing.
func (c *Crew) authorize(mutating bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := uuid.Parse(chi.URLParam(r, "id"))
			if err != nil {
				notFound(w, r)
				return
			}
			tok := r.Header.Get("X-Crew-Token")
			if tok == "" {
				tok = r.URL.Query().Get("t")
			}
			m, err := crew.Authorize(r.Context(), c.deps, id, tok, c.sessions.Get(r).UserID, mutating)
			if err != nil {
				c.fail(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), memberKey{}, m)))
		})
	}
}

// fail maps service errors to statuses. Errors never carry tokens.
func (c *Crew) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, crew.ErrNotFound):
		notFound(w, r)
	case errors.Is(err, crew.ErrExpired):
		writeError(w, http.StatusGone, "expired", "this link has expired, ask the captain for a new one")
	case errors.Is(err, crew.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many opens from here, wait a minute")
	case errors.Is(err, crew.ErrCrewFull):
		writeError(w, http.StatusConflict, "crew_full", "this launch's crew is full")
	case errors.Is(err, crew.ErrWindowClosed):
		writeError(w, http.StatusConflict, "window_closed", "the launch window has closed, pick next free slot or save as draft")
	case errors.Is(err, crew.ErrAlreadyQueued):
		writeError(w, http.StatusConflict, "already_queued", "this draft is already in your Buffer")
	case errors.Is(err, crew.ErrInvalidRole):
		writeError(w, http.StatusBadRequest, "invalid_role", "pick one of the roles, or Other with a short hint")
	case errors.Is(err, crew.ErrInvalidVoice):
		writeError(w, http.StatusBadRequest, "invalid_voice", crew.VoiceMessage(err))
	case errors.Is(err, crew.ErrNotVerified):
		writeError(w, http.StatusForbidden, "verify_first", "Verify it's you with GitHub first, then connect Buffer")
	case errors.Is(err, crew.ErrAlreadyClaimed):
		writeError(w, http.StatusConflict, "already_claimed", "this invite was already connected by its owner")
	case errors.Is(err, crew.ErrNotConnected):
		writeError(w, http.StatusUnauthorized, "connect_buffer", "connect Buffer first")
	case errors.Is(err, crew.ErrBuffer), errors.Is(err, buffer.ErrNeedsReconnect):
		writeError(w, http.StatusBadGateway, "buffer_unavailable", "Buffer is not answering, try again")
	default:
		log.Printf("api: %s %s: %v", r.Method, chi.RouteContext(r.Context()).RoutePattern(), err)
		writeError(w, http.StatusInternalServerError, "internal", "something went wrong on our side")
	}
}

func (c *Crew) invite(w http.ResponseWriter, r *http.Request) {
	v, err := crew.Open(r.Context(), c.deps, chi.URLParam(r, "token"), clientIP(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	if r.URL.Query().Get("foreign") == "1" {
		// Someone else's link: show the launch, never as the contributor.
		v.Foreign, v.Verified, v.Member.Verified, v.NeedsVerify = true, false, false, false
	} else if !v.NeedsVerify {
		v.Connected = crew.ConnectedFor(r.Context(), c.deps, v.Member.ID, c.sessions.Get(r).ConnectionID)
	}
	writeJSON(w, http.StatusOK, v)
}

func (c *Crew) share(w http.ResponseWriter, r *http.Request) {
	text, err := crew.Share(r.Context(), c.deps, chi.URLParam(r, "token"))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (c *Crew) openLink(w http.ResponseWriter, r *http.Request) {
	v, err := crew.OpenLink(r.Context(), c.deps, chi.URLParam(r, "openToken"), clientIP(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// showInfo is what the projectable page needs: no open counted, no cap check.
func (c *Crew) showInfo(w http.ResponseWriter, r *http.Request) {
	id, title, shortURL, err := crew.SlideFor(r.Context(), c.deps, chi.URLParam(r, "openToken"), clientIP(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	logo := crew.SlideLogo(r.Context(), c.deps, id)
	writeJSON(w, http.StatusOK, map[string]string{"title": title, "shortUrl": shortURL, "qrLogo": logo.Mode, "qrLogoUrl": logo.URL})
}

// showPNG streams the projectable QR slide, rendered once per launch.
func (c *Crew) showPNG(w http.ResponseWriter, r *http.Request) {
	id, title, shortURL, err := crew.SlideFor(r.Context(), c.deps, chi.URLParam(r, "openToken"), clientIP(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	png, ok := c.slides.Load(id)
	if !ok {
		render := c.deps.RenderSlide
		if render == nil {
			render = cards.RenderQRSlideLogo
		}
		b, err := render(title, shortURL, crew.SlideLogo(r.Context(), c.deps, id))
		if err != nil {
			c.fail(w, r, err)
			return
		}
		png, _ = c.slides.LoadOrStore(id, b)
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(png.([]byte))
}

func (c *Crew) chooseRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role      string `json:"role"`
		Hint      string `json:"hint"`
		TalkTitle string `json:"talkTitle"`
		Other     bool   `json:"other"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	launchID, err := crew.LaunchForOpenLink(r.Context(), c.deps, chi.URLParam(r, "openToken"))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	if body.Other {
		body.Role = "Other"
	}
	id, tok, err := crew.ChooseRole(r.Context(), c.deps, launchID, body.Role, body.Hint, body.TalkTitle, clientIP(r))
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "memberId": id})
}

// connect runs after the Buffer OAuth return. Without a connection in the
// session the front end sends the person to /auth/buffer/start.
func (c *Crew) connect(w http.ResponseWriter, r *http.Request) {
	sess := c.sessions.Get(r)
	if sess.ConnectionID == uuid.Nil {
		c.fail(w, r, crew.ErrNotConnected)
		return
	}
	v, err := crew.Connect(r.Context(), c.deps, member(r).ID, sess.ConnectionID, sess.UserID)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// refreshChannels re-reads the member's Buffer channels and returns the
// approve screen, so a channel added in Buffer after connecting shows up.
func (c *Crew) refreshChannels(w http.ResponseWriter, r *http.Request) {
	v, err := crew.RefreshChannels(r.Context(), c.deps, member(r).ID)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (c *Crew) drafts(w http.ResponseWriter, r *http.Request) {
	ds, ready, err := crew.Drafts(r.Context(), c.deps, member(r).ID)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"drafts": ds, "voicedReady": ready})
}

func (c *Crew) saveDraft(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "draftID"))
	if err != nil {
		notFound(w, r)
		return
	}
	// The draft must belong to the authorised member.
	d, err := c.deps.Q.GetCrewDraft(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && d.CrewMemberID != member(r).ID) {
		notFound(w, r)
		return
	}
	if err != nil {
		c.fail(w, r, err)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := crew.SaveEdit(r.Context(), c.deps, id, body.Text); err != nil {
		c.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Crew) post(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Selections []crew.Selection `json:"selections"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	out, err := crew.Post(r.Context(), c.deps, member(r).ID, body.Selections)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outcomes": out})
}

func (c *Crew) copied(w http.ResponseWriter, r *http.Request) {
	if err := crew.MarkCopied(r.Context(), c.deps, member(r).ID); err != nil {
		c.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Crew) disconnect(w http.ResponseWriter, r *http.Request) {
	if err := crew.Disconnect(r.Context(), c.deps, member(r).ID); err != nil {
		c.fail(w, r, err)
		return
	}
	// The session's connection no longer has tokens.
	if sess := c.sessions.Get(r); sess.ConnectionID != uuid.Nil {
		sess.ConnectionID = uuid.Nil
		if err := c.sessions.Set(w, sess); err != nil {
			log.Printf("api: clear session connection: %v", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Crew) board(w http.ResponseWriter, r *http.Request) {
	v, err := crew.Board(r.Context(), c.deps, member(r).ID)
	if err != nil {
		c.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// remember sets the member's "remember me for future launches" flag.
func (c *Crew) remember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Remember *bool `json:"remember"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Remember == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "remember must be true or false")
		return
	}
	m := member(r)
	if !m.UserID.Valid {
		writeError(w, http.StatusUnauthorized, "connect_buffer", "connect Buffer first")
		return
	}
	if err := c.deps.Q.SetUserRememberMe(r.Context(), sqlc.SetUserRememberMeParams{ID: m.UserID.UUID, RememberMe: *body.Remember}); err != nil {
		c.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
