package main

// E2E mode: with LAUNCHCREW_E2E=1 the server runs against in-process fakes of Buffer,
// GitHub, Anthropic and R2, and serves test control routes at /e2e. Nothing
// here runs, and nothing is mounted at /e2e, unless LAUNCHCREW_E2E=1.
// It refuses to start on Fly (FLY_APP_NAME set).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/config"
	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/httpserver"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/anthropicfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/bufferfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/githubfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/r2fake"
)

const (
	e2eInstallID     = 4242
	e2eCaptainGitHub = 910010
	e2eOAuthCode     = "e2e-code"
	e2eWebhookSecret = "e2e-webhook-secret"
	// A fixed 32-byte test key, never a real secret.
	e2eKeyring = "e2e:MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
)

// e2eMode holds the fakes and the control state.
type e2eMode struct {
	buf     *bufferfake.Fake
	gh      *githubfake.Server
	ant     *anthropicfake.Fake
	r2      *r2fake.Server
	baseURL string

	mu      sync.Mutex
	account string // Buffer account the next sign-in becomes
	posts   []e2ePost
}

// e2ePost is one createPost the Buffer fake received.
type e2ePost struct {
	ID          string `json:"id"`
	ChannelID   string `json:"channelId"`
	Mode        string `json:"mode"`
	DueAt       string `json:"dueAt,omitempty"`
	SaveToDraft bool   `json:"saveToDraft"`
	Text        string `json:"text"`
	// Assets are the image URLs, from the post or its thread's root item.
	Assets []string `json:"assets"`
}

// postAssets reads the image URLs a createPost input carries: top-level
// assets, else the first thread item's (Buffer reads a thread's root there).
func postAssets(in map[string]any) []string {
	urls := func(v any) []string {
		var out []string
		list, _ := v.([]any)
		for _, a := range list {
			m, _ := a.(map[string]any)
			for _, kind := range []string{"image", "video"} {
				if img, ok := m[kind].(map[string]any); ok {
					if u, ok := img["url"].(string); ok {
						out = append(out, u)
					}
				}
			}
		}
		return out
	}
	if out := urls(in["assets"]); len(out) > 0 {
		return out
	}
	md, _ := in["metadata"].(map[string]any)
	for _, svc := range md {
		m, _ := svc.(map[string]any)
		items, _ := m["thread"].([]any)
		if len(items) > 0 {
			root, _ := items[0].(map[string]any)
			return urls(root["assets"])
		}
	}
	return nil
}

// loadConfig reads the environment, or, with LAUNCHCREW_E2E=1, starts the fakes and
// returns a config pointing at them.
func loadConfig() (config.Config, *e2eMode, error) {
	if os.Getenv("LAUNCHCREW_E2E") != "1" {
		cfg, err := config.Load()
		return cfg, nil, err
	}
	if fly := os.Getenv("FLY_APP_NAME"); fly != "" {
		return config.Config{}, nil, fmt.Errorf("LAUNCHCREW_E2E=1 refused: FLY_APP_NAME=%q says this is a deployed app; E2E mode swaps every API for fakes", fly)
	}
	port := os.Getenv("PORT")
	if port == "" {
		return config.Config{}, nil, fmt.Errorf("LAUNCHCREW_E2E=1 needs PORT")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/launchcrew?sslmode=disable"
	}
	host := os.Getenv("E2E_HOST")
	if host == "" {
		host = "localhost"
	}
	m := &e2eMode{
		buf:     bufferfake.NewServer("captain", "org-captain"),
		gh:      githubfake.New("acme", "rocket"),
		ant:     anthropicfake.New(),
		r2:      r2fake.New("cards"),
		baseURL: "http://" + host + ":" + port,
	}
	m.handleBuffer()
	m.seed()
	log.Printf("E2E mode: fakes up, base %s", m.baseURL)
	return config.Config{
		Port:                port,
		DatabaseURL:         dbURL,
		Keyring:             e2eKeyring,
		BaseURL:             m.baseURL,
		BufferClientID:      "e2e-buffer",
		BufferClientSecret:  "e2e-buffer-secret",
		GitHubAppID:         "1",
		GitHubAppSlug:       "launch-crew-e2e",
		GitHubPrivateKey:    string(githubfake.PrivateKeyPEM()),
		GitHubWebhookSecret: e2eWebhookSecret,
		GitHubClientID:      "e2e-github",
		GitHubClientSecret:  "e2e-github-secret",
		AnthropicAPIKey:     "e2e",
		R2Endpoint:          m.r2.URL,
		R2Bucket:            "cards",
		R2AccessKey:         "e2e",
		R2SecretKey:         "e2e",
		R2PublicBase:        m.r2.URL + "/cards",
		SupportEmail:        "help@launchcrew.test",
		WorkerConcurrency:   2,
	}, m, nil
}

// The option helpers are nil-safe: outside E2E they add nothing.

func (m *e2eMode) bufferOptions() []buffer.Option {
	if m == nil {
		return nil
	}
	return []buffer.Option{buffer.WithEndpoints(m.buf.APIURL(), m.buf.TokenURL())}
}

func (m *e2eMode) githubOptions() []github.Option {
	if m == nil {
		return nil
	}
	return []github.Option{github.WithBaseURL(m.gh.URL)}
}

func (m *e2eMode) claudeOptions() []option.RequestOption {
	if m == nil {
		return nil
	}
	return []option.RequestOption{option.WithBaseURL(m.ant.URL)}
}

// wire points the sign-in flows at the fakes and mounts /e2e.
func (m *e2eMode) wire(d *httpserver.Deps, pool *pgxpool.Pool) {
	if m == nil {
		return
	}
	d.BufferOAuth.AuthURL = m.buf.AuthURL()
	d.BufferOAuth.TokenURL = m.buf.TokenURL()
	d.BufferOAuth.APIURL = m.buf.APIURL()
	gh := oauth2.Endpoint{AuthURL: m.gh.URL + "/login/oauth/authorize", TokenURL: m.gh.URL + "/login/oauth/access_token"}
	d.Install.OAuth.Endpoint = gh
	d.Install.GitHubURL = m.gh.URL
	d.Verify.OAuth.Endpoint = gh
	d.Verify.APIURL = m.gh.URL
	d.E2E = m.routes(pool)
}

// handleBuffer answers every GraphQL operation the app sends. Channels
// depend on the org: an account whose id starts with "empty" has none.
func (m *e2eMode) handleBuffer() {
	ok := func(data any) bufferfake.Response { return bufferfake.Response{Data: data} }
	m.buf.HandleFunc("Account", func(bufferfake.Request) bufferfake.Response {
		m.mu.Lock()
		acct := m.account
		m.mu.Unlock()
		return ok(map[string]any{"account": map[string]any{
			"id": acct, "organizations": []map[string]any{{"id": "org-" + acct}},
		}})
	})
	m.buf.HandleFunc("GetChannels", func(r bufferfake.Request) bufferfake.Response {
		org, _ := r.Vars["organizationId"].(string)
		acct := strings.TrimPrefix(org, "org-")
		chs := []any{}
		if !strings.HasPrefix(acct, "empty") {
			chs = append(chs,
				map[string]any{"id": acct + "-x", "name": "X @" + acct, "displayName": "X @" + acct, "service": "twitter"},
				map[string]any{"id": acct + "-li", "name": "LinkedIn " + acct, "displayName": "LinkedIn " + acct, "service": "linkedin"},
			)
		}
		return ok(map[string]any{"channels": chs})
	})
	m.buf.Handle("GetConfiguration", map[string]any{"configuration": map[string]any{"services": []any{}}})
	m.buf.Handle("GetSentPosts", map[string]any{"posts": map[string]any{"edges": []any{}}})
	m.buf.Handle("GetPostsCreatedBetween", map[string]any{"posts": map[string]any{"edges": []any{}}})
	m.buf.Handle("GetDailyPostingLimits", map[string]any{"dailyPostingLimits": []any{}})
	m.buf.HandleFunc("CreatePost", func(r bufferfake.Request) bufferfake.Response {
		in, _ := r.Vars["input"].(map[string]any)
		p := e2ePost{}
		p.ChannelID, _ = in["channelId"].(string)
		p.Mode, _ = in["mode"].(string)
		p.DueAt, _ = in["dueAt"].(string)
		p.SaveToDraft, _ = in["saveToDraft"].(bool)
		p.Text, _ = in["text"].(string)
		p.Assets = postAssets(in)
		if p.Assets == nil {
			p.Assets = []string{}
		}
		m.mu.Lock()
		p.ID = "post-" + strconv.Itoa(len(m.posts)+1)
		m.posts = append(m.posts, p)
		m.mu.Unlock()
		return ok(map[string]any{"createPost": map[string]any{
			"__typename": "PostActionSuccess", "post": map[string]any{"id": p.ID, "status": "scheduled"},
		}})
	})
}

func e2eUser(id int64, login string) githubfake.User {
	return githubfake.User{ID: id, Login: login, Type: "User"}
}

// seed resets the fakes to the fixture: acme/rocket with PRs #11 (alice)
// and #12 (bob) as the two largest, #13 a chore by carol, release v1.2.0.
func (m *e2eMode) seed() {
	m.mu.Lock()
	m.account = "captain"
	m.posts = nil
	m.mu.Unlock()
	m.r2.SetMissing(false)

	merged := "2026-10-20T10:00:00Z"
	pr := func(n int, title string, u githubfake.User, added int) githubfake.PR {
		return githubfake.PR{
			Number: n, Title: title, State: "closed", Merged: true, MergedAt: &merged,
			MergeCommitSHA: fmt.Sprintf("c%039d", n), Additions: added, User: u,
		}
	}
	g := m.gh
	g.Lock()
	defer g.Unlock()
	g.Commits = []githubfake.Commit{
		githubfake.NewCommit(fmt.Sprintf("a%039d", 1), "Initial commit"),
		githubfake.NewCommit(fmt.Sprintf("c%039d", 11), "Stream large uploads (#11)"),
		githubfake.NewCommit(fmt.Sprintf("c%039d", 12), "Keyboard pane resize (#12)"),
		githubfake.NewCommit(fmt.Sprintf("c%039d", 13), "chore: tidy CI (#13)"),
	}
	g.Tags = []githubfake.Tag{{Name: "v1.2.0", SHA: fmt.Sprintf("c%039d", 13)}}
	g.Releases = []githubfake.Release{{
		ID: 1001, TagName: "v1.2.0", Name: "Rocket 1.2",
		Body: "Stream large uploads, keyboard pane resize.", HTMLURL: "https://github.com/acme/rocket/releases/tag/v1.2.0",
	}}
	g.PRs = []githubfake.PR{
		pr(11, "Stream large uploads without buffering", e2eUser(91001, "alice"), 400),
		pr(12, "Resize panes with the keyboard", e2eUser(91002, "bob"), 200),
		pr(13, "chore: tidy CI config", e2eUser(91003, "carol"), 5),
	}
	g.Comments = map[int][]githubfake.Comment{}
	g.InstallRepos = []githubfake.InstallRepo{{ID: 777, FullName: "acme/rocket"}}
	g.UserInstallations = []int64{e2eInstallID}
	g.FailInstallRepos, g.FailReleaseEdit, g.DenyComments = false, false, false
	g.OAuthCode = e2eOAuthCode
	g.OAuthUser = e2eUser(e2eCaptainGitHub, "captain")
	g.InstallCallback = m.baseURL + "/github/install/callback"
	g.InstallationID = e2eInstallID
}

// routes are the control routes, answered for loopback clients only.
func (m *e2eMode) routes(pool *pgxpool.Pool) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			host, _, _ := net.SplitHostPort(req.RemoteAddr)
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				http.NotFound(w, req)
				return
			}
			next.ServeHTTP(w, req)
		})
	})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		e2eJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		e2eJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	exec := func(w http.ResponseWriter, req *http.Request, sql string, args ...any) {
		tag, err := pool.Exec(req.Context(), sql, args...)
		if err != nil {
			e2eJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		e2eJSON(w, http.StatusOK, map[string]int64{"rows": tag.RowsAffected()})
	}

	r.Post("/reset", func(w http.ResponseWriter, req *http.Request) {
		if err := e2eTruncate(req.Context(), pool); err != nil {
			e2eJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		m.seed()
		e2eJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	r.Post("/release", func(w http.ResponseWriter, req *http.Request) {
		status, err := m.sendRelease(req.Context())
		if err != nil {
			e2eJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		e2eJSON(w, http.StatusOK, map[string]int{"status": status})
	})
	r.Get("/buffer/posts", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		out := append([]e2ePost{}, m.posts...)
		m.mu.Unlock()
		e2eJSON(w, http.StatusOK, out)
	})
	r.Post("/buffer/account", func(w http.ResponseWriter, req *http.Request) {
		var b struct{ ID string }
		if json.NewDecoder(req.Body).Decode(&b) != nil || b.ID == "" {
			e2eJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
			return
		}
		m.mu.Lock()
		m.account = b.ID
		m.mu.Unlock()
		e2eJSON(w, http.StatusOK, b)
	})
	r.Post("/github/user", func(w http.ResponseWriter, req *http.Request) {
		var b struct {
			ID    int64
			Login string
		}
		if json.NewDecoder(req.Body).Decode(&b) != nil || b.ID == 0 {
			e2eJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
			return
		}
		m.gh.Lock()
		m.gh.OAuthUser = e2eUser(b.ID, b.Login)
		m.gh.Unlock()
		e2eJSON(w, http.StatusOK, b)
	})
	r.Get("/github/comments/{pr}", func(w http.ResponseWriter, req *http.Request) {
		n, _ := strconv.Atoi(chi.URLParam(req, "pr"))
		cs := m.gh.IssueComments(n)
		if cs == nil {
			cs = []githubfake.Comment{}
		}
		e2eJSON(w, http.StatusOK, cs)
	})
	r.Post("/r2/missing", func(w http.ResponseWriter, req *http.Request) {
		var b struct{ Missing bool }
		_ = json.NewDecoder(req.Body).Decode(&b)
		m.r2.SetMissing(b.Missing)
		e2eJSON(w, http.StatusOK, b)
	})
	// The window closes through the real CloseWindow job, pulled forward.
	r.Post("/close-window", func(w http.ResponseWriter, req *http.Request) {
		var b struct{ LaunchID uuid.UUID }
		_ = json.NewDecoder(req.Body).Decode(&b)
		exec(w, req, `UPDATE jobs SET run_at = now() WHERE kind = 'close_window' AND launch_id = $1 AND NOT dead`, b.LaunchID)
	})
	r.Post("/dead-letter", func(w http.ResponseWriter, req *http.Request) {
		var b struct{ LaunchID uuid.UUID }
		_ = json.NewDecoder(req.Body).Decode(&b)
		exec(w, req, `INSERT INTO jobs (kind, payload, attempts, dead, last_error, launch_id)
			VALUES ('launch', jsonb_build_object('LaunchID', $2::text), 5, true, 'Buffer answered 502 five times', $1)`, b.LaunchID, b.LaunchID.String())
	})
	r.Post("/expire-invites", func(w http.ResponseWriter, req *http.Request) {
		exec(w, req, `UPDATE crew_members SET token_expires_at = now() - interval '1 day'`)
	})
	r.Get("/launches", func(w http.ResponseWriter, req *http.Request) {
		rows, err := pool.Query(req.Context(), `SELECT id, status, title, launch_at, window_minutes FROM launches ORDER BY created_at`)
		if err != nil {
			e2eJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		type row struct {
			ID            uuid.UUID  `json:"id"`
			Status        string     `json:"status"`
			Title         string     `json:"title"`
			LaunchAt      *time.Time `json:"launchAt"`
			WindowMinutes int32      `json:"windowMinutes"`
		}
		out := []row{}
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.ID, &x.Status, &x.Title, &x.LaunchAt, &x.WindowMinutes); err != nil {
				e2eJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			out = append(out, x)
		}
		e2eJSON(w, http.StatusOK, out)
	})
	return r
}

// sendRelease POSTs a signed release webhook to the app's own route.
func (m *e2eMode) sendRelease(ctx context.Context) (int, error) {
	body, _ := json.Marshal(map[string]any{
		"action":       "published",
		"installation": map[string]any{"id": e2eInstallID},
		"repository":   map[string]any{"id": 777, "name": "rocket", "full_name": "acme/rocket"},
		"release": map[string]any{
			"id": 1001, "tag_name": "v1.2.0", "name": "Rocket 1.2",
			"html_url": "https://github.com/acme/rocket/releases/tag/v1.2.0",
			"body":     "Stream large uploads, keyboard pane resize.",
			"draft":    false, "prerelease": false,
			"author": map[string]any{"id": e2eCaptainGitHub, "login": "captain", "type": "User"},
		},
	})
	mac := hmac.New(sha256.New, []byte(e2eWebhookSecret))
	mac.Write(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/webhooks/github", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "release")
	req.Header.Set("X-GitHub-Delivery", uuid.NewString())
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// e2eTruncate empties every table except goose's.
func e2eTruncate(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT quote_ident(tablename) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, n)
	}
	rows.Close()
	if len(names) == 0 {
		return nil
	}
	_, err = pool.Exec(ctx, "TRUNCATE "+strings.Join(names, ", ")+" RESTART IDENTITY CASCADE")
	return err
}

func e2eJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
