package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/mail"
	"github.com/shubham-tomar/launchcrew/internal/session"
)

// Settings serves the privacy routes: what is stored, global opt-out and
// deletion. Paths are relative to /api.
type Settings struct {
	deps         crew.Deps
	sessions     *session.Store
	supportEmail string
	mail         mail.Sender
}

// NewSettings returns the router for /me, /me/opt-out (POST to opt out,
// DELETE to undo) and /opt-out.
func NewSettings(deps crew.Deps, sessions *session.Store, supportEmail string, opts ...SettingsOption) http.Handler {
	s := &Settings{deps: deps, sessions: sessions, supportEmail: supportEmail}
	for _, o := range opts {
		o(s)
	}
	r := chi.NewRouter()
	r.NotFound(notFound)
	r.MethodNotAllowed(methodNotAllowed)
	r.Get("/me", s.me)
	r.Post("/me/opt-out", s.optOut)
	r.Delete("/me/opt-out", s.optIn)
	r.Delete("/me", s.delete)
	r.Put("/me/notify", s.setNotify)
	r.Post("/me/notify/email", s.startEmail)
	r.Delete("/me/notify/email", s.clearEmail)
	r.Get("/me/notify/verify", s.verifyEmail)
	r.Post("/opt-out", s.tokenOptOut)
	return r
}

type meView struct {
	SignedIn        bool     `json:"signedIn"`
	GitHubLogins    []string `json:"githubLogins"`
	BufferConnected bool     `json:"bufferConnected"`
	OptedOut        bool     `json:"optedOut"`
	Launches        int64    `json:"launches"`
	CrewRows        int64    `json:"crewRows"`
	SupportEmail    string   `json:"supportEmail"`
	// Notify is how a captain hears about new drafts; absent for crew only.
	Notify *notifyView `json:"notify,omitempty"`
}

func (s *Settings) fail(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("api: %s %s: %v", r.Method, r.URL.Path, err)
	writeError(w, http.StatusInternalServerError, "internal", "something went wrong on our side, try again")
}

func (s *Settings) me(w http.ResponseWriter, r *http.Request) {
	v := meView{GitHubLogins: []string{}, SupportEmail: s.supportEmail}
	uid := s.sessions.Get(r).UserID
	if uid == uuid.Nil {
		writeJSON(w, http.StatusOK, v)
		return
	}
	q := s.deps.Q
	ctx := r.Context()
	ids, err := q.ListGitHubIdentitiesByUser(ctx, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, id := range ids {
		v.GitHubLogins = append(v.GitHubLogins, id.Login)
	}
	v.SignedIn = true
	nullUID := uuid.NullUUID{UUID: uid, Valid: true}
	if v.BufferConnected, err = q.UserHasBufferConnection(ctx, uid); err == nil {
		if v.OptedOut, err = q.UserIsOptedOut(ctx, uid); err == nil {
			if v.Launches, err = q.CountUserLaunches(ctx, nullUID); err == nil {
				if v.CrewRows, err = q.CountUserCrewRows(ctx, nullUID); err == nil {
					v.Notify, err = s.notify(ctx, uid, v.Launches)
				}
			}
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// optOut records every GitHub id linked to the user. Rows already queued or
// posted keep their state; their drafts are in the person's own Buffer.
func (s *Settings) optOut(w http.ResponseWriter, r *http.Request) {
	uid := s.sessions.Get(r).UserID
	if uid == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "sign_in", "sign in with Buffer first")
		return
	}
	n, err := s.deps.Q.CountUserGitHubIDs(r.Context(), uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusConflict, "no_github", "no GitHub account is linked to this session, use the link from a PR comment")
		return
	}
	err = s.inTx(r, func(q *sqlc.Queries) error {
		if err := q.OptOutUser(r.Context(), uid); err != nil {
			return err
		}
		return q.MarkUserCrewOptedOut(r.Context(), uuid.NullUUID{UUID: uid, Valid: true})
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"optedOut": true, "scope": "all"})
}

// optIn undoes optOut: it removes the opt_outs rows for every GitHub id
// linked to the user. Crew rows already marked opted_out keep their state.
func (s *Settings) optIn(w http.ResponseWriter, r *http.Request) {
	uid := s.sessions.Get(r).UserID
	if uid == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "sign_in", "sign in with Buffer first")
		return
	}
	if err := s.deps.Q.DeleteUserOptOuts(r.Context(), uid); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"optedOut": false})
}

// tokenOptOut is the PR comment footer link. The token sits in a public
// comment, so on its own it only opts out the one crew row it names; it never
// checks expiry. The GitHub id is opted out everywhere only when the session
// belongs to the user who has proved that GitHub identity, since otherwise any
// reader of the PR could silence the contributor for good.
func (s *Settings) tokenOptOut(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if tok == "" {
		notFound(w, r)
		return
	}
	ctx := r.Context()
	m, err := s.deps.Q.GetCrewMemberByTokenHash(ctx, crew.HashToken(tok))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := crew.RateLimit(ctx, s.deps, m.LaunchID, clientIP(r)); errors.Is(err, crew.ErrRateLimited) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many tries from here, wait a minute")
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	global, err := s.ownsGitHubID(ctx, s.sessions.Get(r).UserID, m.GithubUserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.inTx(r, func(q *sqlc.Queries) error {
		if err := q.MarkCrewMemberOptedOut(ctx, m.ID); err != nil {
			return err
		}
		if !global {
			return nil
		}
		if err := q.InsertOptOut(ctx, m.GithubUserID.Int64); err != nil {
			return err
		}
		return q.MarkGitHubCrewOptedOut(ctx, m.GithubUserID)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// scope tells the page what happened: "launch" for this row, "all" for the GitHub id.
	scope := "launch"
	if global {
		scope = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{"optedOut": true, "scope": scope})
}

// ownsGitHubID reports whether the signed-in user holds the GitHub identity gh.
func (s *Settings) ownsGitHubID(ctx context.Context, uid uuid.UUID, gh pgtype.Int8) (bool, error) {
	if uid == uuid.Nil || !gh.Valid {
		return false, nil
	}
	ident, err := s.deps.Q.GetGitHubIdentity(ctx, gh.Int64)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ident.UserID == uid, nil
}

// delete removes the user's tokens, drafts and crew rows in one transaction.
// opt_outs stays, so a deleted person is still never commented on.
func (s *Settings) delete(w http.ResponseWriter, r *http.Request) {
	uid := s.sessions.Get(r).UserID
	if uid == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "sign_in", "sign in with Buffer first")
		return
	}
	ctx := r.Context()
	nullUID := uuid.NullUUID{UUID: uid, Valid: true}
	err := s.inTx(r, func(q *sqlc.Queries) error {
		steps := []struct {
			name string
			run  func() error
		}{
			{"voice profiles", func() error { return q.DeleteUserVoiceProfiles(ctx, uid) }},
			{"crew drafts", func() error { return q.DeleteUserCrewDrafts(ctx, nullUID) }},
			{"crew rows", func() error { return q.DeleteUserCrewMembers(ctx, nullUID) }},
			{"buffer connections", func() error { return q.DeleteUserBufferConnections(ctx, uid) }},
			{"captain drafts", func() error { return q.DeleteUserCaptainDrafts(ctx, nullUID) }},
			{"launches", func() error { return q.MarkUserLaunchesDeleted(ctx, nullUID) }},
			{"identities", func() error { return q.DeleteUserIdentities(ctx, uid) }},
			{"user", func() error { return q.DeleteUser(ctx, uid) }},
		}
		for _, st := range steps {
			if err := st.run(); err != nil {
				return fmt.Errorf("delete %s: %w", st.name, err)
			}
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.sessions.Clear(w)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Settings) inTx(r *http.Request, fn func(*sqlc.Queries) error) error {
	ctx := r.Context()
	tx, err := s.deps.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.deps.Q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
