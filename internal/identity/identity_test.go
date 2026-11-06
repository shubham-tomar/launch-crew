package identity_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/identity"
	"github.com/shubham-tomar/launchcrew/internal/session"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/githubfake"
)

type env struct {
	pool *pgxpool.Pool
	q    *sqlc.Queries
	st   *session.Store
	fake *githubfake.Server
	in   *identity.Install
	ve   *identity.Verify
	ctx  context.Context
}

func setup(t *testing.T) *env {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	kr, err := crypto.ParseKeyring("k1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	fake := githubfake.New("o", "r")
	t.Cleanup(fake.Close)
	gh, err := github.New(1, githubfake.PrivateKeyPEM(), github.WithBaseURL(fake.URL))
	if err != nil {
		t.Fatal(err)
	}
	st := &session.Store{Keyring: kr}
	q := sqlc.New(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM repos WHERE installation_id >= 7000000000`)
	})
	fake.OAuthCode = "good"
	return &env{
		pool: pool, q: q, st: st, fake: fake, ctx: ctx,
		in: &identity.Install{
			Sessions: st, Pool: pool, GH: gh, AppSlug: "launch-crew", Keyring: kr, GitHubURL: "https://gh.test",
			OAuth: oauth2.Config{
				ClientID: "cid", ClientSecret: "sec", RedirectURL: "https://lc.test/github/install/callback",
				Endpoint: oauth2.Endpoint{
					AuthURL:  "https://gh.test/login/oauth/authorize",
					TokenURL: fake.URL + "/login/oauth/access_token",
				},
			},
		},
		ve: &identity.Verify{
			OAuth: oauth2.Config{
				ClientID: "cid", ClientSecret: "sec", RedirectURL: "https://lc.test/github/verify/callback",
				Endpoint: oauth2.Endpoint{
					AuthURL:  "https://gh.test/login/oauth/authorize",
					TokenURL: fake.URL + "/login/oauth/access_token",
				},
			},
			Q: q, Sessions: st, APIURL: fake.URL,
		},
	}
}

func (e *env) newUser(t *testing.T) uuid.UUID {
	t.Helper()
	u, err := e.q.CreateUser(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// cookieFor returns a session cookie for a signed-in user.
func (e *env) cookieFor(t *testing.T, userID uuid.UUID) *http.Cookie {
	t.Helper()
	rr := httptest.NewRecorder()
	if err := e.st.Set(rr, session.Session{UserID: userID}); err != nil {
		t.Fatal(err)
	}
	return rr.Result().Cookies()[0]
}

func do(h http.HandlerFunc, target string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func location(t *testing.T, rr *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	u, err := url.Parse(rr.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func newInstallID() int64 { return 7_000_000_000 + int64(uuid.New().ID()) }

// startInstall signs in user and runs Start, returning the cookie and state.
func (e *env) startInstall(t *testing.T, userID uuid.UUID) (*http.Cookie, string) {
	t.Helper()
	rr := do(e.in.Start, "/github/install/start", e.cookieFor(t, userID))
	if rr.Code != http.StatusFound {
		t.Fatalf("start: %d", rr.Code)
	}
	loc := location(t, rr)
	if loc.Host != "gh.test" || loc.Path != "/apps/launch-crew/installations/new" {
		t.Fatalf("redirect = %s", loc)
	}
	return rr.Result().Cookies()[0], loc.Query().Get("state")
}

func (e *env) repoRows(t *testing.T, install int64) []sqlc.Repo {
	t.Helper()
	var out []sqlc.Repo
	for _, n := range []string{"acme/a", "acme/b", "acme/c"} {
		r, err := e.q.GetRepoByInstallationAndName(e.ctx, sqlc.GetRepoByInstallationAndNameParams{InstallationID: install, FullName: n})
		if err == nil {
			out = append(out, r)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
	}
	return out
}

func TestInstallStartRequiresSession(t *testing.T) {
	e := setup(t)
	rr := do(e.in.Start, "/github/install/start", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("code %d", rr.Code)
	}
	if got, want := rr.Header().Get("Location"), "/auth/buffer/start?return_to=%2Fgithub%2Finstall%2Fstart"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestInstallCallbackBindsToCaptain(t *testing.T) {
	e := setup(t)
	captain := e.newUser(t)
	e.fake.InstallRepos = []githubfake.InstallRepo{{ID: 11, FullName: "acme/a"}}
	cookie, state := e.startInstall(t, captain)

	install := newInstallID()
	e.fake.UserInstallations = []int64{1, install}
	q := url.Values{"installation_id": {fmt.Sprint(install)}, "setup_action": {"install"}, "state": {state}, "code": {"good"}}
	rr := do(e.in.Callback, "/github/install/callback?"+q.Encode(), cookie)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/captain/repos" {
		t.Fatalf("callback: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	rows := e.repoRows(t, install)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if r := rows[0]; r.OwnerUserID.UUID != captain || !r.GithubRepoID.Valid || r.GithubRepoID.Int64 != 11 {
		t.Fatalf("row = %+v", r)
	}
	// The state is spent: replaying it with the cookie that came back must fail.
	next := rr.Result().Cookies()[0]
	rr = do(e.in.Callback, "/github/install/callback?"+q.Encode(), next)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("replay: %d, want 403", rr.Code)
	}
}

func TestInstallCallbackRejectsForeignState(t *testing.T) {
	e := setup(t)
	e.fake.InstallRepos = []githubfake.InstallRepo{{ID: 11, FullName: "acme/a"}}
	_, captainState := e.startInstall(t, e.newUser(t))
	attackerCookie, _ := e.startInstall(t, e.newUser(t))

	install := newInstallID()
	q := url.Values{"installation_id": {fmt.Sprint(install)}, "state": {captainState}}
	rr := do(e.in.Callback, "/github/install/callback?"+q.Encode(), attackerCookie)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code %d, want 403", rr.Code)
	}
	if rows := e.repoRows(t, install); len(rows) != 0 {
		t.Fatalf("wrote %d repo rows", len(rows))
	}
	if len(e.fake.Requests("", "/installation/repositories")) != 0 {
		t.Fatal("must not call GitHub before the state checks out")
	}
	// No state at all, and no session at all.
	for _, c := range []*http.Cookie{attackerCookie, nil} {
		q.Del("state")
		if rr := do(e.in.Callback, "/github/install/callback?"+q.Encode(), c); rr.Code != http.StatusForbidden {
			t.Fatalf("missing state: %d", rr.Code)
		}
	}
}

func TestInstallCreatesOneRowPerRepo(t *testing.T) {
	e := setup(t)
	captain := e.newUser(t)
	e.fake.InstallRepos = []githubfake.InstallRepo{{ID: 1, FullName: "acme/a"}, {ID: 2, FullName: "acme/b"}, {ID: 3, FullName: "acme/c"}}
	cookie, state := e.startInstall(t, captain)
	install := newInstallID()
	e.fake.UserInstallations = []int64{install}
	q := url.Values{"installation_id": {fmt.Sprint(install)}, "state": {state}, "code": {"good"}}
	if rr := do(e.in.Callback, "/github/install/callback?"+q.Encode(), cookie); rr.Code != http.StatusFound {
		t.Fatalf("callback: %d", rr.Code)
	}
	rows := e.repoRows(t, install)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// Re-running is idempotent.
	cookie, state = e.startInstall(t, captain)
	q.Set("state", state)
	do(e.in.Callback, "/github/install/callback?"+q.Encode(), cookie)
	if rows := e.repoRows(t, install); len(rows) != 3 {
		t.Fatalf("after rerun rows = %d, want 3", len(rows))
	}
}

func TestInstallPagesThroughRepos(t *testing.T) {
	e := setup(t)
	for i := 0; i < 150; i++ {
		e.fake.InstallRepos = append(e.fake.InstallRepos, githubfake.InstallRepo{ID: int64(i + 1), FullName: fmt.Sprintf("acme/r%d", i)})
	}
	repos, err := e.in.GH.ListInstallationRepos(e.ctx, newInstallID())
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 150 || repos[149].FullName != "acme/r149" {
		t.Fatalf("got %d repos", len(repos))
	}
}

// crew inserts a launch and a crew member for ghID and returns the member and plain token.
func (e *env) crew(t *testing.T, ghID int64) (sqlc.CrewMember, string) {
	t.Helper()
	owner := e.newUser(t)
	l, err := e.q.CreateLaunch(e.ctx, sqlc.CreateLaunchParams{OwnerUserID: owner, Source: "github",
		HeadlineChanges: []byte(`[]`), Roles: []byte(`[]`), WindowMinutes: 90, CrewCap: 200})
	if err != nil {
		t.Fatal(err)
	}
	tok, hash := crew.NewToken()
	m, err := e.q.InsertCrewMember(e.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: l.ID, GithubUserID: pgtype.Int8{Int64: ghID, Valid: true}, State: "invited",
		InviteTokenHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, tok
}

func newGHID() int64 { return 1_000_000_000 + int64(uuid.New().ID()) }

// startVerify runs Verify.Start and returns the cookie and state.
func (e *env) startVerify(t *testing.T, m sqlc.CrewMember, tok string, c *http.Cookie) (*http.Cookie, string) {
	t.Helper()
	rr := do(e.ve.Start, "/github/verify/start?crew="+m.ID.String()+"&t="+tok, c)
	if rr.Code != http.StatusFound {
		t.Fatalf("verify start: %d", rr.Code)
	}
	state := location(t, rr).Query().Get("state")
	if state == "" || location(t, rr).Host != "gh.test" {
		t.Fatalf("redirect = %s", rr.Header().Get("Location"))
	}
	return rr.Result().Cookies()[0], state
}

func TestVerifyMatchProceeds(t *testing.T) {
	e := setup(t)
	ghID := newGHID()
	m, tok := e.crew(t, ghID)
	e.fake.OAuthCode, e.fake.OAuthUser = "good", githubfake.User{ID: ghID, Login: "octo"}
	cookie, state := e.startVerify(t, m, tok, nil)

	rr := do(e.ve.Callback, "/github/verify/callback?code=good&state="+state, cookie)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/s/"+tok {
		t.Fatalf("callback: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	got, err := e.q.GetCrewMember(e.ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UserID.Valid {
		t.Fatal("crew member not bound to a user")
	}
	ident, err := e.q.GetGitHubIdentity(e.ctx, ghID)
	if err != nil {
		t.Fatal(err)
	}
	if ident.UserID != got.UserID.UUID || ident.Login != "octo" {
		t.Fatalf("identity = %+v, crew user %v", ident, got.UserID.UUID)
	}
	// The browser is now signed in as the verified user.
	req := httptest.NewRequest("GET", "/", nil)
	cs := rr.Result().Cookies()
	req.AddCookie(cs[len(cs)-1])
	if sess := e.st.Get(req); sess.UserID != got.UserID.UUID {
		t.Fatalf("session user = %v, want %v", sess.UserID, got.UserID.UUID)
	}
}

func TestVerifyStartRejectsExpiredToken(t *testing.T) {
	e := setup(t)
	m, tok := e.crew(t, newGHID())
	if _, err := e.pool.Exec(e.ctx, `UPDATE crew_members SET token_expires_at = now() - interval '1 minute' WHERE id = $1`, m.ID); err != nil {
		t.Fatal(err)
	}
	if rr := do(e.ve.Start, "/github/verify/start?crew="+m.ID.String()+"&t="+tok, nil); rr.Code != http.StatusGone {
		t.Fatalf("expired token: %d", rr.Code)
	}
}

func TestVerifyExistingIdentityWinsOverSession(t *testing.T) {
	e := setup(t)
	ghID := newGHID()
	m, tok := e.crew(t, ghID)
	userA, userB := e.newUser(t), e.newUser(t)
	if _, err := e.q.UpsertGitHubIdentity(e.ctx, sqlc.UpsertGitHubIdentityParams{GithubUserID: ghID, UserID: userA, Login: "old"}); err != nil {
		t.Fatal(err)
	}
	e.fake.OAuthUser = githubfake.User{ID: ghID, Login: "octo"}
	cookie, state := e.startVerify(t, m, tok, e.cookieFor(t, userB))
	do(e.ve.Callback, "/github/verify/callback?code=good&state="+state, cookie)
	got, _ := e.q.GetCrewMember(e.ctx, m.ID)
	if got.UserID.UUID != userA {
		t.Fatalf("crew user = %v, want identity's %v", got.UserID.UUID, userA)
	}
	ident, _ := e.q.GetGitHubIdentity(e.ctx, ghID)
	if ident.UserID != userA {
		t.Fatalf("identity user changed to %v", ident.UserID)
	}
}

func TestVerifyNoIdentityUsesSessionUser(t *testing.T) {
	e := setup(t)
	ghID := newGHID()
	m, tok := e.crew(t, ghID)
	userB := e.newUser(t)
	e.fake.OAuthUser = githubfake.User{ID: ghID, Login: "octo"}
	cookie, state := e.startVerify(t, m, tok, e.cookieFor(t, userB))
	do(e.ve.Callback, "/github/verify/callback?code=good&state="+state, cookie)
	got, _ := e.q.GetCrewMember(e.ctx, m.ID)
	ident, err := e.q.GetGitHubIdentity(e.ctx, ghID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID.UUID != userB || ident.UserID != userB {
		t.Fatalf("crew %v identity %v, want %v", got.UserID.UUID, ident.UserID, userB)
	}
}

func TestVerifyCrewBoundToOtherUserIsForeign(t *testing.T) {
	e := setup(t)
	ghID := newGHID()
	m, tok := e.crew(t, ghID)
	userC, userB := e.newUser(t), e.newUser(t)
	if err := e.q.UpdateCrewMemberUser(e.ctx, sqlc.UpdateCrewMemberUserParams{ID: m.ID, UserID: uuid.NullUUID{UUID: userC, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.fake.OAuthUser = githubfake.User{ID: ghID, Login: "octo"}
	cookie, state := e.startVerify(t, m, tok, e.cookieFor(t, userB))
	rr := do(e.ve.Callback, "/github/verify/callback?code=good&state="+state, cookie)
	if rr.Header().Get("Location") != "/s/"+tok+"?foreign=1" {
		t.Fatalf("Location = %s", rr.Header().Get("Location"))
	}
	got, _ := e.q.GetCrewMember(e.ctx, m.ID)
	if got.UserID.UUID != userC {
		t.Fatalf("crew user overwritten: %v", got.UserID.UUID)
	}
	if _, err := e.q.GetGitHubIdentity(e.ctx, ghID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("identity written: %v", err)
	}
}

func TestVerifyMismatchForeign(t *testing.T) {
	e := setup(t)
	m, tok := e.crew(t, newGHID())
	otherID := newGHID()
	e.fake.OAuthCode, e.fake.OAuthUser = "good", githubfake.User{ID: otherID, Login: "someone-else"}
	cookie, state := e.startVerify(t, m, tok, nil)

	rr := do(e.ve.Callback, "/github/verify/callback?code=good&state="+state, cookie)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/s/"+tok+"?foreign=1" {
		t.Fatalf("callback: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	got, _ := e.q.GetCrewMember(e.ctx, m.ID)
	if got.UserID.Valid {
		t.Fatalf("row changed: user_id = %v", got.UserID)
	}
	if _, err := e.q.GetGitHubIdentity(e.ctx, otherID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("identity written on mismatch: %v", err)
	}
}

func TestVerifyStateMismatch400(t *testing.T) {
	e := setup(t)
	ghID := newGHID()
	m, tok := e.crew(t, ghID)
	e.fake.OAuthCode, e.fake.OAuthUser = "good", githubfake.User{ID: ghID, Login: "octo"}
	cookie, _ := e.startVerify(t, m, tok, nil)

	for _, target := range []string{
		"/github/verify/callback?code=good&state=wrong",
		"/github/verify/callback?code=good",
	} {
		if rr := do(e.ve.Callback, target, cookie); rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", target, rr.Code)
		}
	}
	if rr := do(e.ve.Callback, "/github/verify/callback?code=good&state=x", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("no session: %d", rr.Code)
	}
	got, _ := e.q.GetCrewMember(e.ctx, m.ID)
	if got.UserID.Valid {
		t.Fatal("row changed on state mismatch")
	}
}

func TestVerifyStartRejectsBadInput(t *testing.T) {
	e := setup(t)
	if rr := do(e.ve.Start, "/github/verify/start?crew="+uuid.NewString()+"&t=abc", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown crew: %d", rr.Code)
	}
	m, _ := e.crew(t, newGHID())
	for _, tk := range []string{"", "//evil.example", "a/b"} {
		rr := do(e.ve.Start, "/github/verify/start?crew="+m.ID.String()+"&t="+url.QueryEscape(tk), nil)
		if rr.Code != http.StatusBadRequest || strings.Contains(rr.Header().Get("Location"), "evil") {
			t.Fatalf("token %q: %d", tk, rr.Code)
		}
	}
}

func TestVerifyStartRejectsTokenOfAnotherCrew(t *testing.T) {
	e := setup(t)
	a, _ := e.crew(t, newGHID())
	_, tokB := e.crew(t, newGHID())
	if rr := do(e.ve.Start, "/github/verify/start?crew="+a.ID.String()+"&t="+tokB, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("token of another crew: %d", rr.Code)
	}
	if rr := do(e.ve.Start, "/github/verify/start?crew="+a.ID.String()+"&t=made-up-token", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", rr.Code)
	}
}

func (e *env) callback(t *testing.T, c *http.Cookie, install int64, state string, code bool) *httptest.ResponseRecorder {
	q := url.Values{"installation_id": {fmt.Sprint(install)}, "state": {state}}
	if code {
		q.Set("code", "good")
	}
	return do(e.in.Callback, "/github/install/callback?"+q.Encode(), c)
}

func TestInstallCallbackForgedInstallationIDRejected(t *testing.T) {
	e := setup(t)
	e.fake.InstallRepos = []githubfake.InstallRepo{{ID: 1, FullName: "acme/a"}}
	cookie, state := e.startInstall(t, e.newUser(t))
	mine, forged := newInstallID(), newInstallID()
	e.fake.UserInstallations = []int64{mine, mine + 1}
	if rr := e.callback(t, cookie, forged, state, true); rr.Code != http.StatusForbidden {
		t.Fatalf("code %d, want 403", rr.Code)
	}
	if rows := e.repoRows(t, forged); len(rows) != 0 {
		t.Fatalf("wrote %d rows", len(rows))
	}
	if len(e.fake.Requests("", "/installation/repositories")) != 0 {
		t.Fatal("read the forged installation's repos")
	}
}

func TestInstallCallbackDoesNotOverwriteOwner(t *testing.T) {
	e := setup(t)
	userA, userB := e.newUser(t), e.newUser(t)
	install := newInstallID()
	e.fake.UserInstallations = []int64{install}
	e.fake.InstallRepos = []githubfake.InstallRepo{{ID: 1, FullName: "acme/a"}, {ID: 2, FullName: "acme/b"}}
	cookie, state := e.startInstall(t, userA)
	if rr := e.callback(t, cookie, install, state, true); rr.Code != http.StatusFound {
		t.Fatalf("A: %d", rr.Code)
	}
	cookie, state = e.startInstall(t, userB)
	rr := e.callback(t, cookie, install, state, true)
	if rr.Code != http.StatusConflict {
		t.Fatalf("B: %d, want 409", rr.Code)
	}
	for _, r := range e.repoRows(t, install) {
		if r.OwnerUserID.UUID != userA {
			t.Fatalf("%s owner = %v, want A", r.FullName, r.OwnerUserID.UUID)
		}
	}
}

func TestInstallCallbackWithoutCodeAuthorizesFirst(t *testing.T) {
	e := setup(t)
	e.fake.InstallRepos = []githubfake.InstallRepo{{ID: 1, FullName: "acme/a"}}
	install := newInstallID()
	e.fake.UserInstallations = []int64{install}
	cookie, state := e.startInstall(t, e.newUser(t))
	rr := e.callback(t, cookie, install, state, false)
	loc := location(t, rr)
	if rr.Code != http.StatusFound || loc.Host != "gh.test" || loc.Path != "/login/oauth/authorize" || loc.Query().Get("state") != state {
		t.Fatalf("no-code: %d %s", rr.Code, loc)
	}
	if rows := e.repoRows(t, install); len(rows) != 0 {
		t.Fatal("wrote rows before authorization")
	}
	// GitHub returns with code+state only; the installation id comes from the session.
	back := do(e.in.Callback, "/github/install/callback?code=good&state="+url.QueryEscape(state), rr.Result().Cookies()[0])
	if back.Code != http.StatusFound || back.Header().Get("Location") != "/captain/repos" {
		t.Fatalf("resume: %d %s", back.Code, back.Header().Get("Location"))
	}
	if rows := e.repoRows(t, install); len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
}

func TestInstallGitHubFailureWritesNothingAndKeepsState(t *testing.T) {
	e := setup(t)
	install := newInstallID()
	e.fake.UserInstallations = []int64{install}
	e.fake.InstallRepos = []githubfake.InstallRepo{{ID: 1, FullName: "acme/a"}}
	e.fake.FailInstallRepos = true
	cookie, state := e.startInstall(t, e.newUser(t))
	if rr := e.callback(t, cookie, install, state, true); rr.Code != http.StatusBadGateway {
		t.Fatalf("code %d, want 502", rr.Code)
	}
	if rows := e.repoRows(t, install); len(rows) != 0 {
		t.Fatal("rows written on failure")
	}
	// The state survives: the same cookie and state succeed once GitHub recovers.
	e.fake.FailInstallRepos = false
	if rr := e.callback(t, cookie, install, state, true); rr.Code != http.StatusFound {
		t.Fatalf("retry: %d", rr.Code)
	}
	if rows := e.repoRows(t, install); len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
}

func TestVerifyNeverAdoptsRowUser(t *testing.T) {
	e := setup(t)
	ghID := newGHID()
	m, tok := e.crew(t, ghID)
	x := e.newUser(t) // bound before verification, e.g. by a stranger
	if err := e.q.UpdateCrewMemberUser(e.ctx, sqlc.UpdateCrewMemberUserParams{ID: m.ID, UserID: uuid.NullUUID{UUID: x, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.fake.OAuthUser = githubfake.User{ID: ghID, Login: "octo"}
	cookie, state := e.startVerify(t, m, tok, nil)
	rr := do(e.ve.Callback, "/github/verify/callback?code=good&state="+state, cookie)
	if rr.Header().Get("Location") != "/s/"+tok+"?foreign=1" {
		t.Fatalf("Location = %s", rr.Header().Get("Location"))
	}
	ident, err := e.q.GetGitHubIdentity(e.ctx, ghID)
	if err != nil || ident.UserID == x {
		t.Fatalf("identity = %+v, %v; want a fresh user, not %v", ident, err, x)
	}
	if got, _ := e.q.GetCrewMember(e.ctx, m.ID); got.UserID.UUID != x {
		t.Fatalf("row user = %v, want untouched %v", got.UserID.UUID, x)
	}
}

func TestVerifyOnSharedBrowserMakesFreshUser(t *testing.T) {
	e := setup(t)
	ghA, ghB := newGHID(), newGHID()
	userA := e.newUser(t)
	if _, err := e.q.UpsertGitHubIdentity(e.ctx, sqlc.UpsertGitHubIdentityParams{GithubUserID: ghA, UserID: userA, Login: "alice"}); err != nil {
		t.Fatal(err)
	}
	m, tok := e.crew(t, ghB)
	e.fake.OAuthUser = githubfake.User{ID: ghB, Login: "bob"}
	cookie, state := e.startVerify(t, m, tok, e.cookieFor(t, userA))
	rr := do(e.ve.Callback, "/github/verify/callback?code=good&state="+state, cookie)
	if rr.Header().Get("Location") != "/s/"+tok {
		t.Fatalf("Location = %s", rr.Header().Get("Location"))
	}
	got, _ := e.q.GetCrewMember(e.ctx, m.ID)
	identB, err := e.q.GetGitHubIdentity(e.ctx, ghB)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID.UUID == userA || identB.UserID == userA || got.UserID.UUID != identB.UserID {
		t.Fatalf("B bound to %v / %v, A is %v", got.UserID.UUID, identB.UserID, userA)
	}
	if ids, _ := e.q.ListGitHubIdentitiesByUser(e.ctx, userA); len(ids) != 1 || ids[0].GithubUserID != ghA {
		t.Fatalf("A's identities changed: %+v", ids)
	}
	req := httptest.NewRequest("GET", "/", nil)
	cs := rr.Result().Cookies()
	req.AddCookie(cs[len(cs)-1])
	if sess := e.st.Get(req); sess.UserID != identB.UserID {
		t.Fatalf("session user = %v, want B's %v", sess.UserID, identB.UserID)
	}
}

func TestVerifyCaptainSessionMakesFreshUser(t *testing.T) {
	e := setup(t)
	gh := newGHID()
	captain := e.newUser(t)
	if _, err := e.q.CreateLaunch(e.ctx, sqlc.CreateLaunchParams{OwnerUserID: captain, Source: "manual",
		HeadlineChanges: []byte(`[]`), Roles: []byte(`[]`), WindowMinutes: 90, CrewCap: 200}); err != nil {
		t.Fatal(err)
	}
	m, tok := e.crew(t, gh)
	e.fake.OAuthUser = githubfake.User{ID: gh, Login: "dana"}
	cookie, state := e.startVerify(t, m, tok, e.cookieFor(t, captain))
	do(e.ve.Callback, "/github/verify/callback?code=good&state="+state, cookie)
	if ident, err := e.q.GetGitHubIdentity(e.ctx, gh); err != nil || ident.UserID == captain {
		t.Fatalf("identity bound to the captain's session user (%v)", err)
	}
}

func (e *env) install(t *testing.T, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	e.fake.InstallRepos = []githubfake.InstallRepo{{ID: 11, FullName: "acme/a"}}
	install := newInstallID()
	e.fake.UserInstallations = []int64{install}
	cookie, state := e.startInstall(t, userID)
	return e.callback(t, cookie, install, state, true)
}

func TestInstallRecordsCaptainIdentity(t *testing.T) {
	e := setup(t)
	captain := e.newUser(t)
	ghID := newGHID()
	e.fake.OAuthUser = githubfake.User{ID: ghID, Login: "cap"}
	if rr := e.install(t, captain); rr.Code != http.StatusFound {
		t.Fatalf("install: %d", rr.Code)
	}
	got, err := e.q.GetGitHubIdentityByUser(e.ctx, captain)
	if err != nil || got.GithubUserID != ghID || got.Login != "cap" {
		t.Fatalf("identity = %+v, %v", got, err)
	}
	// Installing again refreshes the login and keeps the binding.
	e.fake.OAuthUser = githubfake.User{ID: ghID, Login: "cap-renamed"}
	if rr := e.install(t, captain); rr.Code != http.StatusFound {
		t.Fatalf("reinstall: %d", rr.Code)
	}
	got, err = e.q.GetGitHubIdentityByUser(e.ctx, captain)
	if err != nil || got.UserID != captain || got.Login != "cap-renamed" {
		t.Fatalf("identity after reinstall = %+v, %v", got, err)
	}
}

func TestInstallLeavesIdentityBoundToAnotherUser(t *testing.T) {
	e := setup(t)
	owner, captain := e.newUser(t), e.newUser(t)
	ghID := newGHID()
	if _, err := e.q.UpsertGitHubIdentity(e.ctx, sqlc.UpsertGitHubIdentityParams{GithubUserID: ghID, UserID: owner, Login: "owner"}); err != nil {
		t.Fatal(err)
	}
	e.fake.OAuthUser = githubfake.User{ID: ghID, Login: "someone"}
	if rr := e.install(t, captain); rr.Code != http.StatusFound {
		t.Fatalf("install must still succeed: %d", rr.Code)
	}
	got, err := e.q.GetGitHubIdentity(e.ctx, ghID)
	if err != nil || got.UserID != owner || got.Login != "owner" {
		t.Fatalf("identity = %+v, %v; want untouched", got, err)
	}
	if _, err := e.q.GetGitHubIdentityByUser(e.ctx, captain); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("captain identity err = %v, want no rows", err)
	}
}

func TestInstallSucceedsWhenGitHubUserUnavailable(t *testing.T) {
	e := setup(t)
	captain := e.newUser(t)
	e.fake.OAuthUser = githubfake.User{} // /user answers with no id
	if rr := e.install(t, captain); rr.Code != http.StatusFound {
		t.Fatalf("install: %d", rr.Code)
	}
	if _, err := e.q.GetGitHubIdentityByUser(e.ctx, captain); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want no rows", err)
	}
}
