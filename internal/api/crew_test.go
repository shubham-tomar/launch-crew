package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"github.com/shubham-tomar/launchcrew/internal/api"
	"github.com/shubham-tomar/launchcrew/internal/auth"
	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/httpserver"
	"github.com/shubham-tomar/launchcrew/internal/identity"
	"github.com/shubham-tomar/launchcrew/internal/session"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/bufferfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/githubfake"
)

type env struct {
	ctx    context.Context
	q      *sqlc.Queries
	st     *session.Store
	deps   crew.Deps
	kr     *crypto.Keyring
	h      http.Handler
	launch sqlc.Launch
	open   string
}

func setup(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(pool)
	kr, err := crypto.ParseKeyring("k1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := q.CreateUser(ctx)
	l, err := q.CreateLaunch(ctx, sqlc.CreateLaunchParams{
		OwnerUserID: owner.ID, Source: "manual", Title: "Meetup", HeadlineChanges: []byte(`[]`),
		Roles: []byte(`[{"name":"Speaker","hint":"spoke about"}]`), WindowMinutes: 90, CrewCap: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateLaunchStatus(ctx, sqlc.UpdateLaunchStatusParams{ID: l.ID, Status: "launched"}); err != nil {
		t.Fatal(err)
	}
	open, hash := crew.NewToken()
	if err := q.SetLaunchOpenToken(ctx, sqlc.SetLaunchOpenTokenParams{ID: l.ID, OpenTokenHash: hash}); err != nil {
		t.Fatal(err)
	}
	fake := bufferfake.New(t, "unused", "org-1")
	fake.Handle("GetChannels", map[string]any{"channels": []any{}})
	fake.Handle("GetConfiguration", map[string]any{"configuration": map[string]any{"services": []any{}}})
	buf := buffer.New(&http.Client{Timeout: 5 * time.Second}, q, kr, buffer.WithEndpoints(fake.APIURL(), fake.TokenURL()))
	st := &session.Store{Keyring: kr}
	deps := crew.Deps{Q: q, Pool: pool, Buffer: buf, Keyring: kr, BaseURL: "https://lc.test"}
	h := httpserver.New(httpserver.Deps{Crew: api.NewCrew(deps, st)})
	return &env{ctx: ctx, q: q, st: st, deps: deps, kr: kr, h: h, launch: l, open: open}
}

func (e *env) do(method, target, body string, hdr map[string]string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if c != nil {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, req)
	return rr
}

func (e *env) cookie(t *testing.T, s session.Session) *http.Cookie {
	t.Helper()
	rr := httptest.NewRecorder()
	if err := e.st.Set(rr, s); err != nil {
		t.Fatal(err)
	}
	return rr.Result().Cookies()[0]
}

func decode(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rr.Body.String(), err)
	}
	return m
}

func TestForeignTokenView(t *testing.T) {
	e := setup(t)
	tok, hash := crew.NewToken()
	u, _ := e.q.CreateUser(e.ctx)
	if _, err := e.q.InsertCrewMember(e.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: e.launch.ID, Role: "contributor", State: "ticked", InviteTokenHash: hash,
		UserID:         uuid.NullUUID{UUID: u.ID, Valid: true},
		GithubUserID:   pgtype.Int8{Int64: 3_000_000_000 + int64(uuid.New().ID()), Valid: true},
		TokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	rr := e.do("GET", "/api/s/"+tok, "", nil, nil)
	if rr.Code != 200 || decode(t, rr)["verified"] != true || decode(t, rr)["foreign"] != false {
		t.Fatalf("own view: %d %s", rr.Code, rr.Body)
	}
	rr = e.do("GET", "/api/s/"+tok+"?foreign=1", "", nil, nil)
	v := decode(t, rr)
	if rr.Code != 200 || v["verified"] != false || v["foreign"] != true || v["member"].(map[string]any)["verified"] != false {
		t.Fatalf("foreign view: %d %s", rr.Code, rr.Body)
	}

	if rr := e.do("GET", "/api/s/nope", "", nil, nil); rr.Code != 404 {
		t.Fatalf("unknown token: %d", rr.Code)
	}
	old, oldHash := crew.NewToken()
	if _, err := e.q.InsertCrewMember(e.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: e.launch.ID, State: "invited", InviteTokenHash: oldHash,
		TokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	rr = e.do("GET", "/api/s/"+old, "", nil, nil)
	if rr.Code != http.StatusGone || decode(t, rr)["error"] != "expired" {
		t.Fatalf("expired: %d %s", rr.Code, rr.Body)
	}
}

func TestCrewRouteRequiresTokenOrSession(t *testing.T) {
	e := setup(t)
	rr := e.do("POST", "/api/l/"+e.open+"/role", `{"role":"Speaker","talkTitle":"Go at scale"}`, nil, nil)
	if rr.Code != 200 {
		t.Fatalf("choose role: %d %s", rr.Code, rr.Body)
	}
	tok := decode(t, rr)["token"].(string)
	m, err := crew.Resolve(e.ctx, e.deps, tok)
	if err != nil {
		t.Fatal(err)
	}
	_, otherTok, err := crew.ChooseRole(e.ctx, e.deps, e.launch.ID, "Speaker", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	base := "/api/crew/" + m.ID.String()
	routes := []struct{ method, path, body string }{
		{"POST", "/connect", ""},
		{"GET", "/drafts", ""},
		{"PUT", "/drafts/" + uuid.NewString(), `{"text":"x"}`},
		{"POST", "/post", `{"selections":[]}`},
		{"POST", "/copied", ""},
		{"POST", "/disconnect", ""},
		{"POST", "/channels/refresh", ""},
		{"GET", "/board", ""},
	}
	u, _ := e.q.CreateUser(e.ctx)
	stranger := e.cookie(t, session.Session{UserID: u.ID})
	for _, r := range routes {
		for name, try := range map[string]func() *httptest.ResponseRecorder{
			"no credentials": func() *httptest.ResponseRecorder { return e.do(r.method, base+r.path, r.body, nil, nil) },
			"other token": func() *httptest.ResponseRecorder {
				return e.do(r.method, base+r.path, r.body, map[string]string{"X-Crew-Token": otherTok}, nil)
			},
			"other user": func() *httptest.ResponseRecorder { return e.do(r.method, base+r.path, r.body, nil, stranger) },
		} {
			if rr := try(); rr.Code != 404 {
				t.Fatalf("%s %s with %s: %d", r.method, r.path, name, rr.Code)
			}
		}
	}

	if rr := e.do("GET", base+"/drafts", "", map[string]string{"X-Crew-Token": tok}, nil); rr.Code != 200 {
		t.Fatalf("header token: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("GET", base+"/board?t="+tok, "", nil, nil); rr.Code != 200 || decode(t, rr)["total"] != float64(2) {
		t.Fatalf("query token: %d %s", rr.Code, rr.Body)
	}
	// Connect needs a Buffer sign-in in the session.
	if rr := e.do("POST", base+"/connect", "", map[string]string{"X-Crew-Token": tok}, nil); rr.Code != 401 {
		t.Fatalf("connect without session: %d", rr.Code)
	}
	if err := e.q.UpdateCrewMemberUser(e.ctx, sqlc.UpdateCrewMemberUserParams{ID: m.ID, UserID: uuid.NullUUID{UUID: u.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if rr := e.do("GET", base+"/drafts", "", nil, stranger); rr.Code != 200 {
		t.Fatalf("own session: %d", rr.Code)
	}
	if rr := e.do("POST", base+"/copied", "", nil, stranger); rr.Code != 204 {
		t.Fatalf("copied: %d", rr.Code)
	}

	// Open link errors.
	if rr := e.do("GET", "/api/l/"+e.open, "", map[string]string{"Fly-Client-IP": "198.51.100.7"}, nil); rr.Code != 200 {
		t.Fatalf("open link: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("POST", "/api/l/"+e.open+"/role", `{"role":"Wizard"}`, nil, nil); rr.Code != 400 {
		t.Fatalf("bad role: %d", rr.Code)
	}
	if rr := e.do("GET", "/api/nothing", "", nil, nil); rr.Code != 404 || decode(t, rr)["error"] == nil {
		t.Fatalf("unknown api route: %d %s", rr.Code, rr.Body)
	}
}

// conn makes a Buffer connection owned by a fresh user.
func (e *env) conn(t *testing.T) sqlc.BufferConnection {
	t.Helper()
	u, _ := e.q.CreateUser(e.ctx)
	access, keyID, _ := e.kr.Encrypt([]byte("tok"))
	c, err := e.q.UpsertBufferConnection(e.ctx, sqlc.UpsertBufferConnectionParams{
		UserID: u.ID, BufferAccountID: "acct-" + uuid.NewString(), OrgID: pgtype.Text{String: "org-1", Valid: true},
		AccessTokenEnc: access, KeyID: keyID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBoundRowNeedsOwnerSessionToMutate(t *testing.T) {
	e := setup(t)
	id, tok, err := crew.ChooseRole(e.ctx, e.deps, e.launch.ID, "Speaker", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/crew/" + id.String()
	owner := e.conn(t)
	ownerCookie := e.cookie(t, session.Session{UserID: owner.UserID, ConnectionID: owner.ID})
	// Unbound: the token alone connects.
	if rr := e.do("POST", base+"/connect", "", map[string]string{"X-Crew-Token": tok}, ownerCookie); rr.Code != 200 {
		t.Fatalf("first connect: %d %s", rr.Code, rr.Body)
	}
	tokHdr := map[string]string{"X-Crew-Token": tok}
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/connect", ""},
		{"PUT", "/drafts/" + uuid.NewString(), `{"text":"x"}`},
		{"POST", "/post", `{"selections":[]}`},
		{"POST", "/disconnect", ""},
		{"POST", "/channels/refresh", ""},
	} {
		if rr := e.do(r.method, base+r.path, r.body, tokHdr, nil); rr.Code != 404 {
			t.Fatalf("token-only %s %s on bound row: %d", r.method, r.path, rr.Code)
		}
	}
	// A second Buffer account holding the token cannot take the row.
	other := e.conn(t)
	intruder := e.cookie(t, session.Session{UserID: other.UserID, ConnectionID: other.ID})
	if rr := e.do("POST", base+"/connect", "", tokHdr, intruder); rr.Code != 404 {
		t.Fatalf("intruder connect: %d", rr.Code)
	}
	if m, _ := e.q.GetCrewMember(e.ctx, id); m.UserID.UUID != owner.UserID || m.BufferAccountID.String != owner.BufferAccountID {
		t.Fatalf("row re-bound: %+v", m)
	}
	// Reads still work with the token; the owner's session can mutate.
	if rr := e.do("GET", base+"/drafts", "", tokHdr, nil); rr.Code != 200 {
		t.Fatalf("token-only drafts: %d", rr.Code)
	}
	if rr := e.do("POST", base+"/connect", "", nil, ownerCookie); rr.Code != 200 {
		t.Fatalf("owner reconnect: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("POST", base+"/post", `{"selections":[]}`, nil, ownerCookie); rr.Code != 200 {
		t.Fatalf("owner post: %d %s", rr.Code, rr.Body)
	}
}

func TestOpenLinkIPFromFlyHeader(t *testing.T) {
	e := setup(t)
	// X-Forwarded-For is client-controlled: a new value per request does not
	// buy a new budget.
	for i := range 10 {
		hdr := map[string]string{"X-Forwarded-For": "198.51.100." + string(rune('a'+i))}
		if rr := e.do("GET", "/api/l/"+e.open, "", hdr, nil); rr.Code != 200 {
			t.Fatalf("open %d: %d", i+1, rr.Code)
		}
	}
	rr := e.do("GET", "/api/l/"+e.open, "", map[string]string{"X-Forwarded-For": "203.0.113.99"}, nil)
	if rr.Code != http.StatusTooManyRequests || decode(t, rr)["error"] != "rate_limited" {
		t.Fatalf("11th open with spoofed XFF: %d %s", rr.Code, rr.Body)
	}
	// Fly-Client-IP is the address used.
	fly := map[string]string{"Fly-Client-IP": "203.0.113.5"}
	for i := range 10 {
		if rr := e.do("GET", "/api/l/"+e.open, "", fly, nil); rr.Code != 200 {
			t.Fatalf("fly open %d: %d", i+1, rr.Code)
		}
	}
	if rr := e.do("GET", "/api/l/"+e.open, "", fly, nil); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("11th fly open: %d", rr.Code)
	}
}

// A contributor verified on GitHub (user A) whose Buffer account already
// belongs to user B: Buffer sign-in merges A into B, and connect works.
func TestVerifiedContributorWithKnownBufferAccountConnects(t *testing.T) {
	e := setup(t)
	b := e.conn(t) // the account, owned by B
	a, _ := e.q.CreateUser(e.ctx)
	ghID := 5_000_000_000 + int64(uuid.New().ID())
	if _, err := e.q.UpsertGitHubIdentity(e.ctx, sqlc.UpsertGitHubIdentityParams{GithubUserID: ghID, UserID: a.ID, Login: "octo"}); err != nil {
		t.Fatal(err)
	}
	tok, hash := crew.NewToken()
	m, err := e.q.InsertCrewMember(e.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: e.launch.ID, Role: "contributor", State: "opened", InviteTokenHash: hash,
		UserID:         uuid.NullUUID{UUID: a.ID, Valid: true},
		GithubUserID:   pgtype.Int8{Int64: ghID, Valid: true},
		TokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	oauthFake := bufferfake.New(t, b.BufferAccountID, "org-1")
	h := httpserver.New(httpserver.Deps{
		Crew: api.NewCrew(e.deps, e.st),
		BufferOAuth: &auth.BufferOAuth{
			ClientID: "cid", ClientSecret: "secret", BaseURL: "https://lc.test",
			AuthURL: "https://auth.example/auth", TokenURL: oauthFake.TokenURL(), APIURL: oauthFake.APIURL(),
			Q: e.q, Pool: e.deps.Pool, Keyring: e.kr, Sessions: e.st,
		},
	})
	serve := func(method, target string, c *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		if c != nil {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	last := func(rr *httptest.ResponseRecorder) *http.Cookie {
		cs := rr.Result().Cookies()
		return cs[len(cs)-1]
	}

	start := serve("GET", "/auth/buffer/start?return_to=/s/"+tok+"/approve", e.cookie(t, session.Session{UserID: a.ID}))
	loc, _ := neturl.Parse(start.Header().Get("Location"))
	cb := serve("GET", "/auth/buffer/callback?code=abc&state="+neturl.QueryEscape(loc.Query().Get("state")), last(start))
	if cb.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", cb.Code, cb.Body)
	}
	cookie := last(cb)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	if s := e.st.Get(req); s.UserID != b.UserID || s.ConnectionID != b.ID {
		t.Fatalf("session = %+v, want user %v", s, b.UserID)
	}
	if ident, _ := e.q.GetGitHubIdentity(e.ctx, ghID); ident.UserID != b.UserID {
		t.Fatalf("identity user = %v", ident.UserID)
	}
	if r, _ := e.q.GetCrewMember(e.ctx, m.ID); r.UserID.UUID != b.UserID {
		t.Fatalf("crew user = %v", r.UserID.UUID)
	}
	if _, err := e.q.GetUser(e.ctx, a.ID); err == nil {
		t.Fatal("user A still exists")
	}

	if rr := serve("POST", "/api/crew/"+m.ID.String()+"/connect", cookie); rr.Code != 200 || decode(t, rr)["memberId"] != m.ID.String() {
		t.Fatalf("connect: %d %s", rr.Code, rr.Body)
	}
	if rr := serve("GET", "/api/s/"+tok, nil); rr.Code != 200 || decode(t, rr)["verified"] != true {
		t.Fatalf("invite view: %d %s", rr.Code, rr.Body)
	}
}

// full is the whole HTTP surface with GitHub verify and Buffer sign-in
// against fakes; bufferAccount is the account Buffer sign-in returns.
func (e *env) full(t *testing.T, bufferAccount string) (http.Handler, *githubfake.Server) {
	t.Helper()
	gh := githubfake.New("o", "r")
	t.Cleanup(gh.Close)
	gh.OAuthCode = "good"
	oauthFake := bufferfake.New(t, bufferAccount, "org-1")
	return httpserver.New(httpserver.Deps{
		Crew: api.NewCrew(e.deps, e.st),
		BufferOAuth: &auth.BufferOAuth{
			ClientID: "cid", ClientSecret: "secret", BaseURL: "https://lc.test",
			AuthURL: "https://auth.example/auth", TokenURL: oauthFake.TokenURL(), APIURL: oauthFake.APIURL(),
			Q: e.q, Pool: e.deps.Pool, Keyring: e.kr, Sessions: e.st,
		},
		Verify: &identity.Verify{
			OAuth: oauth2.Config{
				ClientID: "cid", ClientSecret: "sec", RedirectURL: "https://lc.test/github/verify/callback",
				Endpoint: oauth2.Endpoint{AuthURL: "https://gh.test/authorize", TokenURL: gh.URL + "/login/oauth/access_token"},
			},
			Q: e.q, Sessions: e.st, APIURL: gh.URL,
		},
	}), gh
}

// browser follows the app's redirects by hand and keeps the last cookie.
type browser struct {
	h      http.Handler
	cookie *http.Cookie
}

func (b *browser) do(method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	rr := httptest.NewRecorder()
	b.h.ServeHTTP(rr, req)
	if cs := rr.Result().Cookies(); len(cs) > 0 {
		b.cookie = cs[len(cs)-1]
	}
	return rr
}

// oauth runs a start/callback pair whose state comes from the start redirect.
func (b *browser) oauth(t *testing.T, start, callback, code string) {
	t.Helper()
	rr := b.do("GET", start, nil)
	loc, _ := neturl.Parse(rr.Header().Get("Location"))
	cb := b.do("GET", callback+"?code="+code+"&state="+neturl.QueryEscape(loc.Query().Get("state")), nil)
	if cb.Code != http.StatusFound {
		t.Fatalf("%s: %d %s", callback, cb.Code, cb.Body)
	}
}

func (e *env) ghRow(t *testing.T, ghID int64) (sqlc.CrewMember, string) {
	t.Helper()
	tok, hash := crew.NewToken()
	m, err := e.q.InsertCrewMember(e.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: e.launch.ID, Role: "contributor", State: "opened", InviteTokenHash: hash,
		GithubUserID:   pgtype.Int8{Int64: ghID, Valid: true},
		TokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, tok
}

// The PR comment's link is public: a stranger holding it cannot bind the
// row; the contributor verifies, then connects.
func TestStrangerCannotClaimGitHubInvite(t *testing.T) {
	e := setup(t)
	ghID := 6_000_000_000 + int64(uuid.New().ID())
	m, tok := e.ghRow(t, ghID)
	base := "/api/crew/" + m.ID.String()
	tokHdr := map[string]string{"X-Crew-Token": tok}

	if v := decode(t, e.do("GET", "/api/s/"+tok, "", nil, nil)); v["needsVerify"] != true {
		t.Fatalf("invite view needsVerify = %v", v["needsVerify"])
	}
	if v := decode(t, e.do("GET", "/api/s/"+tok+"?foreign=1", "", nil, nil)); v["needsVerify"] != false {
		t.Fatalf("foreign view needsVerify = %v", v["needsVerify"])
	}

	// The stranger signs into their own Buffer account and tries to connect.
	strangerConn := e.conn(t)
	h, gh := e.full(t, strangerConn.BufferAccountID)
	stranger := &browser{h: h}
	stranger.oauth(t, "/auth/buffer/start", "/auth/buffer/callback", "abc")
	rr := stranger.do("POST", base+"/connect", tokHdr)
	if rr.Code != http.StatusForbidden || decode(t, rr)["error"] != "verify_first" {
		t.Fatalf("stranger connect: %d %s", rr.Code, rr.Body)
	}
	if r, _ := e.q.GetCrewMember(e.ctx, m.ID); r.UserID.Valid || r.BufferAccountID.Valid {
		t.Fatalf("row bound by stranger: %+v", r)
	}

	// The contributor verifies with GitHub, then signs into a new Buffer account.
	gh.OAuthUser = githubfake.User{ID: ghID, Login: "octo"}
	me := &browser{h: h}
	me.oauth(t, "/github/verify/start?crew="+m.ID.String()+"&t="+tok, "/github/verify/callback", "good")
	ident, err := e.q.GetGitHubIdentity(e.ctx, ghID)
	if err != nil {
		t.Fatal(err)
	}
	e2h, _ := e.full(t, "acct-"+uuid.NewString())
	me.h = e2h
	me.oauth(t, "/auth/buffer/start", "/auth/buffer/callback", "abc")
	if rr := me.do("POST", base+"/connect", nil); rr.Code != 200 {
		t.Fatalf("contributor connect: %d %s", rr.Code, rr.Body)
	}
	r, _ := e.q.GetCrewMember(e.ctx, m.ID)
	if r.UserID.UUID != ident.UserID || !r.BufferAccountID.Valid {
		t.Fatalf("row = %+v, want bound to identity user %v", r, ident.UserID)
	}
	if v := decode(t, e.do("GET", "/api/s/"+tok, "", nil, nil)); v["verified"] != true || v["needsVerify"] != false {
		t.Fatalf("after connect: %v", v)
	}
	if c, _ := e.q.GetBufferConnection(e.ctx, strangerConn.ID); c.UserID != strangerConn.UserID {
		t.Fatal("stranger's connection moved")
	}
}

func TestShareOnUnverifiedGitHubRowKeepsState(t *testing.T) {
	e := setup(t)
	m, tok := e.ghRow(t, 7_000_000_000+int64(uuid.New().ID()))
	rr := e.do("POST", "/api/s/"+tok+"/share", "", nil, nil)
	if rr.Code != 200 || decode(t, rr)["text"] == "" {
		t.Fatalf("share: %d %s", rr.Code, rr.Body)
	}
	if r, _ := e.q.GetCrewMember(e.ctx, m.ID); r.State != "opened" {
		t.Fatalf("state = %s, want opened", r.State)
	}
	// An open-link row still records the copy.
	id, tok2, err := crew.ChooseRole(e.ctx, e.deps, e.launch.ID, "Speaker", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rr := e.do("POST", "/api/s/"+tok2+"/share", "", nil, nil); rr.Code != 200 {
		t.Fatalf("share: %d", rr.Code)
	}
	if r, _ := e.q.GetCrewMember(e.ctx, id); r.State != "copied" {
		t.Fatalf("open-link state = %s, want copied", r.State)
	}
}

func TestRememberFlag(t *testing.T) {
	e := setup(t)
	id, tok, err := crew.ChooseRole(e.ctx, e.deps, e.launch.ID, "Speaker", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/crew/" + id.String()
	tokHdr := map[string]string{"X-Crew-Token": tok}
	if rr := e.do("POST", base+"/remember", `{"remember":true}`, tokHdr, nil); rr.Code != 401 || decode(t, rr)["error"] != "connect_buffer" {
		t.Fatalf("unbound row: %d %s", rr.Code, rr.Body)
	}
	owner := e.conn(t)
	ownerCookie := e.cookie(t, session.Session{UserID: owner.UserID, ConnectionID: owner.ID})
	if rr := e.do("POST", base+"/connect", "", tokHdr, ownerCookie); rr.Code != 200 {
		t.Fatalf("connect: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("POST", base+"/remember", `{"remember":true}`, tokHdr, nil); rr.Code != 404 {
		t.Fatalf("token-only on bound row: %d", rr.Code)
	}
	if u, _ := e.q.GetUser(e.ctx, owner.UserID); u.RememberMe {
		t.Fatal("flag set by a token-only call")
	}
	for _, want := range []bool{true, false} {
		body := `{"remember":false}`
		if want {
			body = `{"remember":true}`
		}
		if rr := e.do("POST", base+"/remember", body, nil, ownerCookie); rr.Code != 204 {
			t.Fatalf("owner remember=%v: %d %s", want, rr.Code, rr.Body)
		}
		if u, _ := e.q.GetUser(e.ctx, owner.UserID); u.RememberMe != want {
			t.Fatalf("remember_me = %v, want %v", u.RememberMe, want)
		}
	}
	if rr := e.do("POST", base+"/remember", `{}`, nil, ownerCookie); rr.Code != 400 {
		t.Fatalf("missing field: %d", rr.Code)
	}
}

func TestDraftsCarryBufferPostID(t *testing.T) {
	e := setup(t)
	id, tok, err := crew.ChooseRole(e.ctx, e.deps, e.launch.ID, "Speaker", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.q.InsertCrewDraftIfAbsent(e.ctx, sqlc.InsertCrewDraftIfAbsentParams{
		CrewMemberID: id, ChannelID: "c1", Network: "twitter", Text: "hi", Thread: []byte(`[]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpdateCrewDraftPosting(e.ctx, sqlc.UpdateCrewDraftPostingParams{
		ID: d.ID, Status: "queued", BufferPostID: pgtype.Text{String: "p-9", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	rr := e.do("GET", "/api/crew/"+id.String()+"/drafts", "", map[string]string{"X-Crew-Token": tok}, nil)
	var got struct {
		Drafts []struct{ Status, BufferPostID string } `json:"drafts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got.Drafts) != 1 {
		t.Fatalf("drafts: %d %s", rr.Code, rr.Body)
	}
	if got.Drafts[0].Status != "queued" || got.Drafts[0].BufferPostID != "p-9" {
		t.Fatalf("draft = %+v", got.Drafts[0])
	}
}

func TestShowPNGRendersOnceAndCaches(t *testing.T) {
	e := setup(t)
	var calls int
	var gotTitle, gotURL string
	deps := e.deps
	deps.RenderSlide = func(title, shortURL string, _ cards.SlideLogo) ([]byte, error) {
		calls++
		gotTitle, gotURL = title, shortURL
		return []byte("\x89PNGfake"), nil
	}
	e.h = httpserver.New(httpserver.Deps{Crew: api.NewCrew(deps, e.st)})

	for i, ip := range []string{"203.0.113.10", "203.0.113.11"} {
		rr := e.do("GET", "/api/l/"+e.open+"/show.png", "", map[string]string{"Fly-Client-IP": ip}, nil)
		if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("request %d: %d %q", i+1, rr.Code, rr.Header().Get("Content-Type"))
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=3600" {
			t.Fatalf("cache-control %q", cc)
		}
		if rr.Body.String() != "\x89PNGfake" {
			t.Fatalf("body %q", rr.Body)
		}
	}
	if calls != 1 {
		t.Fatalf("rendered %d times, want 1", calls)
	}
	if gotTitle != "Meetup" || gotURL != "https://lc.test/l/"+e.open {
		t.Fatalf("render args %q %q", gotTitle, gotURL)
	}
	if rr := e.do("GET", "/api/l/nope/show.png", "", nil, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", rr.Code)
	}
}

func TestShowPNGSharesOpenBudget(t *testing.T) {
	e := setup(t)
	// The budget is per clock minute: pin the clock so the run cannot straddle two.
	deps := e.deps
	at := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	deps.Now = func() time.Time { return at }
	e.h = httpserver.New(httpserver.Deps{Crew: api.NewCrew(deps, e.st)})
	hdr := map[string]string{"Fly-Client-IP": "203.0.113.77"}
	for i := range 5 {
		if rr := e.do("GET", "/api/l/"+e.open+"/show.png", "", hdr, nil); rr.Code != 200 {
			t.Fatalf("png %d: %d", i+1, rr.Code)
		}
		if rr := e.do("GET", "/api/l/"+e.open, "", hdr, nil); rr.Code != 200 {
			t.Fatalf("open %d: %d", i+1, rr.Code)
		}
	}
	rr := e.do("GET", "/api/l/"+e.open+"/show.png", "", hdr, nil)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") != "60" {
		t.Fatalf("11th: %d %v", rr.Code, rr.Header())
	}
}

func TestShowInfoCountsNoOpenAndIgnoresCap(t *testing.T) {
	e := setup(t)
	if _, err := e.deps.Pool.Exec(e.ctx, "UPDATE launches SET crew_cap = 0 WHERE id = $1", e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if rr := e.do("GET", "/api/l/"+e.open, "", nil, nil); rr.Code != http.StatusConflict {
		t.Fatalf("open on a full crew: %d", rr.Code)
	}
	rr := e.do("GET", "/api/l/"+e.open+"/show", "", nil, nil)
	got := decode(t, rr)
	if rr.Code != 200 || got["title"] != "Meetup" || got["shortUrl"] != "https://lc.test/l/"+e.open {
		t.Fatalf("show: %d %s", rr.Code, rr.Body)
	}
	l, err := e.q.GetLaunch(e.ctx, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l.OpenCount != 0 {
		t.Fatalf("open_count %d, want 0", l.OpenCount)
	}
	if rr := e.do("GET", "/api/l/nope/show", "", nil, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", rr.Code)
	}
}

func TestShowInfoAndPNGCarryTheQRLogo(t *testing.T) {
	e := setup(t)
	var gotLogo []cards.SlideLogo
	deps := e.deps
	deps.RenderSlide = func(_, _ string, logo cards.SlideLogo) ([]byte, error) {
		gotLogo = append(gotLogo, logo)
		return []byte("\x89PNGfake"), nil
	}
	e.h = httpserver.New(httpserver.Deps{Crew: api.NewCrew(deps, e.st)})

	// The default is the plane, with no image URL.
	got := decode(t, e.do("GET", "/api/l/"+e.open+"/show", "", nil, nil))
	if got["qrLogo"] != "plane" || got["qrLogoUrl"] != "" {
		t.Fatalf("default show: %v", got)
	}

	// A custom logo is reported with the URL of the stored image, and the PNG is drawn with it.
	if _, err := e.q.InsertLaunchAsset(e.ctx, sqlc.InsertLaunchAssetParams{LaunchID: e.launch.ID, Kind: "qrlogo", R2Url: "https://cdn.example/cards/logo.png", Sha: "l0g0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.deps.Pool.Exec(e.ctx, "UPDATE launches SET qr_logo = 'custom' WHERE id = $1", e.launch.ID); err != nil {
		t.Fatal(err)
	}
	got = decode(t, e.do("GET", "/api/l/"+e.open+"/show", "", nil, nil))
	if got["qrLogo"] != "custom" || got["qrLogoUrl"] != "https://cdn.example/cards/logo.png" {
		t.Fatalf("custom show: %v", got)
	}
	if rr := e.do("GET", "/api/l/"+e.open+"/show.png", "", nil, nil); rr.Code != 200 {
		t.Fatalf("png: %d", rr.Code)
	}
	if len(gotLogo) != 1 || gotLogo[0] != (cards.SlideLogo{Mode: "custom", URL: "https://cdn.example/cards/logo.png"}) {
		t.Fatalf("render logo %+v", gotLogo)
	}

	// None is reported as none, with no URL even though an image is stored.
	if _, err := e.deps.Pool.Exec(e.ctx, "UPDATE launches SET qr_logo = 'none' WHERE id = $1", e.launch.ID); err != nil {
		t.Fatal(err)
	}
	got = decode(t, e.do("GET", "/api/l/"+e.open+"/show", "", nil, nil))
	if got["qrLogo"] != "none" || got["qrLogoUrl"] != "" {
		t.Fatalf("none show: %v", got)
	}
}

func TestInviteConnectedFlag(t *testing.T) {
	e := setup(t)
	own := e.conn(t)
	other := e.conn(t)
	tok, hash := crew.NewToken()
	if _, err := e.q.InsertCrewMember(e.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: e.launch.ID, Role: "contributor", State: "ticked", InviteTokenHash: hash,
		UserID:          uuid.NullUUID{UUID: own.UserID, Valid: true},
		BufferAccountID: pgtype.Text{String: own.BufferAccountID, Valid: true},
		GithubUserID:    pgtype.Int8{Int64: 3_000_000_000 + int64(uuid.New().ID()), Valid: true},
		TokenExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	connected := func(target string, c *http.Cookie) any {
		t.Helper()
		rr := e.do("GET", target, "", nil, c)
		if rr.Code != 200 {
			t.Fatalf("invite: %d %s", rr.Code, rr.Body)
		}
		return decode(t, rr)["connected"]
	}
	ownCookie := e.cookie(t, session.Session{UserID: own.UserID, ConnectionID: own.ID})
	if v := connected("/api/s/"+tok, nil); v != false {
		t.Fatalf("no session: connected = %v", v)
	}
	if v := connected("/api/s/"+tok, ownCookie); v != true {
		t.Fatalf("own connection: connected = %v", v)
	}
	if v := connected("/api/s/"+tok+"?foreign=1", ownCookie); v != false {
		t.Fatalf("foreign: connected = %v", v)
	}
	otherCookie := e.cookie(t, session.Session{UserID: other.UserID, ConnectionID: other.ID})
	if v := connected("/api/s/"+tok, otherCookie); v != false {
		t.Fatalf("another account: connected = %v", v)
	}
	if err := e.q.UpdateConnectionStatus(e.ctx, sqlc.UpdateConnectionStatusParams{ID: own.ID, Status: "needs_reconnect"}); err != nil {
		t.Fatal(err)
	}
	if v := connected("/api/s/"+tok, ownCookie); v != false {
		t.Fatalf("needs_reconnect: connected = %v", v)
	}
}
