package api

import (
	"context"
	"errors"
	"net/http"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/mail"
)

// SettingsOption configures NewSettings.
type SettingsOption func(*Settings)

// WithMail lets captains get "draft ready" emails and verify their address.
func WithMail(m mail.Sender) SettingsOption { return func(s *Settings) { s.mail = m } }

const (
	// verifyFor is how long an email verification link works.
	verifyFor = 24 * time.Hour
	// resendAfter is the least time between two verification emails.
	resendAfter = time.Minute
)

// notifyView is how a captain hears that a release made a draft.
type notifyView struct {
	GitHub        bool   `json:"github"`
	ReleaseNotes  bool   `json:"releaseNotes"`
	Email         bool   `json:"email"`
	EmailAddress  string `json:"emailAddress"`
	EmailPending  string `json:"emailPending"`
	MailAvailable bool   `json:"mailAvailable"`
}

func (s *Settings) notifyOf(n sqlc.CaptainNotify, found bool) *notifyView {
	v := &notifyView{GitHub: true, MailAvailable: mail.Available(s.mail)}
	if found {
		v.GitHub, v.ReleaseNotes = n.Github, n.ReleaseNotes
		v.Email = n.Email && n.EmailAddress != ""
		v.EmailAddress, v.EmailPending = n.EmailAddress, n.EmailPending
	}
	return v
}

// notify loads the user's choices; nil when they run no repo or launch.
func (s *Settings) notify(ctx context.Context, uid uuid.UUID, launches int64) (*notifyView, error) {
	repos, err := s.deps.Q.CountReposByOwner(ctx, uuid.NullUUID{UUID: uid, Valid: true})
	if err != nil {
		return nil, err
	}
	if repos == 0 && launches == 0 {
		return nil, nil
	}
	n, err := s.deps.Q.GetCaptainNotify(ctx, uid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return s.notifyOf(n, err == nil), nil
}

func (s *Settings) signedInUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	uid := s.sessions.Get(r).UserID
	if uid == uuid.Nil {
		writeError(w, http.StatusUnauthorized, "sign_in", "sign in with Buffer first")
		return uuid.Nil, false
	}
	return uid, true
}

// setNotify saves which channels are on. Email turns on only for a verified address.
func (s *Settings) setNotify(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.signedInUser(w, r)
	if !ok {
		return
	}
	var body struct {
		GitHub       *bool `json:"github"`
		ReleaseNotes *bool `json:"releaseNotes"`
		Email        *bool `json:"email"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.GitHub == nil || body.ReleaseNotes == nil || body.Email == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "github, releaseNotes and email must be true or false")
		return
	}
	n, err := s.deps.Q.SetCaptainNotifyChannels(r.Context(), sqlc.SetCaptainNotifyChannelsParams{
		UserID: uid, Github: *body.GitHub, ReleaseNotes: *body.ReleaseNotes, Email: *body.Email,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.notifyOf(n, true))
}

// validEmail accepts one bare address.
func validEmail(a string) bool {
	if len(a) > 254 || strings.ContainsAny(a, " <>\r\n") {
		return false
	}
	p, err := netmail.ParseAddress(a)
	return err == nil && p.Address == a && strings.Contains(a[strings.LastIndex(a, "@")+1:], ".")
}

// startEmail sends a verification link to the address. Email stays as it
// was until the link is opened.
func (s *Settings) startEmail(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.signedInUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Address string `json:"address"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	addr := strings.TrimSpace(body.Address)
	if !validEmail(addr) {
		writeError(w, http.StatusBadRequest, "bad_email", "enter one email address, like you@example.com")
		return
	}
	if !mail.Available(s.mail) {
		writeError(w, http.StatusServiceUnavailable, "mail_unavailable", "email is not set up on this server")
		return
	}
	ctx := r.Context()
	if n, err := s.deps.Q.GetCaptainNotify(ctx, uid); err == nil && n.VerifySentAt.Valid && time.Since(n.VerifySentAt.Time) < resendAfter {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "we just sent a link, wait a minute before asking again")
		return
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	tok, hash := crew.NewToken()
	n, err := s.deps.Q.StartCaptainEmailVerify(ctx, sqlc.StartCaptainEmailVerifyParams{UserID: uid, EmailPending: addr, VerifyHash: hash})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	link := strings.TrimRight(s.deps.BaseURL, "/") + "/api/me/notify/verify?token=" + tok
	err = s.mail.Send(ctx, mail.Message{
		To:      addr,
		Subject: "Confirm your email for Launch Crew",
		Text: "Open this link to get an email when a release makes a launch draft:\n" + link +
			"\n\nThe link works for 24 hours. If you did not ask for this, ignore this email.\n",
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.notifyOf(n, true))
}

// verifyEmail is the link in the verification email. The token is the proof,
// so it works in any browser; it lands back on the settings page.
func (s *Settings) verifyEmail(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	result := "expired"
	if tok != "" {
		_, err := s.deps.Q.VerifyCaptainEmail(r.Context(), sqlc.VerifyCaptainEmailParams{
			VerifyHash:   crew.HashToken(tok),
			VerifySentAt: pgtype.Timestamptz{Time: time.Now().Add(-verifyFor), Valid: true},
		})
		switch {
		case err == nil:
			result = "verified"
		case !errors.Is(err, pgx.ErrNoRows):
			s.fail(w, r, err)
			return
		}
	}
	http.Redirect(w, r, "/settings?email="+result, http.StatusSeeOther)
}

// clearEmail forgets the address and turns email off.
func (s *Settings) clearEmail(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.signedInUser(w, r)
	if !ok {
		return
	}
	if err := s.deps.Q.ClearCaptainEmail(r.Context(), uid); err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := s.deps.Q.GetCaptainNotify(r.Context(), uid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.notifyOf(n, err == nil))
}
