// Package identity binds GitHub App installations to captains and verifies
// that a crew member is the GitHub user a PR comment was addressed to.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/session"
)

const defaultGitHubURL = "https://github.com"

// Install binds a GitHub App installation to the signed-in captain.
type Install struct {
	Sessions *session.Store
	Pool     *pgxpool.Pool
	GH       *github.Client
	AppSlug  string
	Keyring  *crypto.Keyring
	// OAuth is the user-to-server config whose redirect is /github/install/callback.
	OAuth oauth2.Config
	// HTTPClient is used for the token exchange; defaults to http.DefaultClient.
	HTTPClient *http.Client
	// GitHubURL is the web host for the install page; defaults to https://github.com.
	GitHubURL string
}

// installState binds the state to the user and a per-session nonce.
func (i *Install) installState(userID uuid.UUID, nonce []byte) string {
	data := append(append([]byte{}, userID[:]...), nonce...)
	return base64.RawURLEncoding.EncodeToString(i.Keyring.MAC(data))
}

// Start sends a signed-in captain to GitHub's install page with a signed state.
func (i *Install) Start(w http.ResponseWriter, r *http.Request) {
	sess := i.Sessions.Get(r)
	if sess.UserID == uuid.Nil {
		http.Redirect(w, r, "/auth/buffer/start?return_to="+url.QueryEscape("/github/install/start"), http.StatusFound)
		return
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess.InstallState = base64.RawURLEncoding.EncodeToString(nonce)
	if err := i.Sessions.Set(w, sess); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	host := i.GitHubURL
	if host == "" {
		host = defaultGitHubURL
	}
	dest := host + "/apps/" + url.PathEscape(i.AppSlug) + "/installations/new?state=" +
		url.QueryEscape(i.installState(sess.UserID, nonce))
	http.Redirect(w, r, dest, http.StatusFound)
}

var errOwned = errors.New("installation bound to another account")

// Callback records the installation's repos for the captain once three things hold:
// the state matches this session, the person's own GitHub token can access the
// installation, and no other account already owns it.
func (i *Install) Callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := i.Sessions.Get(r)
	nonce, err := base64.RawURLEncoding.DecodeString(sess.InstallState)
	if sess.UserID == uuid.Nil || err != nil || len(nonce) == 0 {
		http.Error(w, "invalid state", http.StatusForbidden)
		return
	}
	state := i.installState(sess.UserID, nonce)
	if !hmac.Equal([]byte(r.URL.Query().Get("state")), []byte(state)) {
		http.Error(w, "invalid state", http.StatusForbidden)
		return
	}
	installID, _ := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	if installID <= 0 {
		installID = sess.InstallID // returning from user authorization
	}
	if installID <= 0 {
		http.Error(w, "missing installation_id", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		// GitHub did not ask the person to authorize; send them through it.
		sess.InstallID = installID
		if err := i.Sessions.Set(w, sess); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, i.OAuth.AuthCodeURL(state), http.StatusFound)
		return
	}
	cctx := ctx
	if i.HTTPClient != nil {
		cctx = context.WithValue(ctx, oauth2.HTTPClient, i.HTTPClient)
	}
	tok, err := i.OAuth.Exchange(cctx, code)
	if err != nil {
		log.Printf("install: exchange: %v", err)
		http.Error(w, "GitHub authorization failed", http.StatusForbidden)
		return
	}
	mine, err := i.GH.ListUserInstallations(ctx, tok.AccessToken)
	if err != nil {
		log.Printf("install: list user installations: %v", err)
		http.Error(w, "could not verify installation access", http.StatusBadGateway)
		return
	}
	if !slices.Contains(mine, installID) {
		http.Error(w, "you do not have access to that installation", http.StatusForbidden)
		return
	}
	i.recordCaptainIdentity(ctx, sess.UserID, tok.AccessToken)
	repos, err := i.GH.ListInstallationRepos(ctx, installID)
	if err != nil {
		log.Printf("install: list repos for installation %d: %v", installID, err)
		http.Error(w, "could not read the installation", http.StatusBadGateway)
		return
	}
	err = db.WithTx(ctx, i.Pool, func(q *sqlc.Queries) error {
		existing, err := q.GetReposByInstallation(ctx, installID)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.OwnerUserID.Valid && e.OwnerUserID.UUID != sess.UserID {
				return errOwned
			}
		}
		for _, repo := range repos {
			if _, err := q.UpsertRepo(ctx, sqlc.UpsertRepoParams{
				InstallationID: installID,
				OwnerUserID:    uuid.NullUUID{UUID: sess.UserID, Valid: true},
				FullName:       repo.FullName,
				GithubRepoID:   pgtype.Int8{Int64: repo.ID, Valid: true},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errOwned) {
		http.Error(w, "This installation is already bound to another account", http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("install: save installation %d: %v", installID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Committed: the state is spent.
	sess.InstallState, sess.InstallID = "", 0
	if err := i.Sessions.Set(w, sess); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/captain/repos", http.StatusFound)
}

// recordCaptainIdentity binds the captain's GitHub account to their user so
// the launch can tell the captain from a contributor. It never fails the
// install: a GitHub account already bound to another user is left alone.
func (i *Install) recordCaptainIdentity(ctx context.Context, userID uuid.UUID, token string) {
	q := sqlc.New(i.Pool)
	ghID, login, err := i.GH.UserByToken(ctx, token)
	if err != nil {
		log.Printf("install: fetch github user: %v", err)
		return
	}
	switch ident, err := q.GetGitHubIdentity(ctx, ghID); {
	case err == nil && ident.UserID != userID:
		log.Printf("install: github account %d is bound to another user; captain %s left unrecognised", ghID, userID)
		return
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		log.Printf("install: look up identity %d: %v", ghID, err)
		return
	}
	if _, err := q.UpsertGitHubIdentity(ctx, sqlc.UpsertGitHubIdentityParams{
		GithubUserID: ghID, UserID: userID, Login: login,
	}); err != nil {
		log.Printf("install: record identity %d: %v", ghID, err)
	}
}
