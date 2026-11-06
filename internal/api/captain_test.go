package api_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/api"
	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/httpserver"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/launch"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
	"github.com/shubham-tomar/launchcrew/internal/session"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/bufferfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/r2fake"
)

// captainEnv is a draft manual launch owned by owner, with a repo, served
// through the full handler.
type captainEnv struct {
	*env
	pool         *pgxpool.Pool
	owner, other sqlc.User
	launch       sqlc.Launch
	repo         sqlc.Repo
}

func setupCaptain(t *testing.T) *captainEnv {
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
	other, _ := q.CreateUser(ctx)
	repo, err := q.UpsertRepo(ctx, sqlc.UpsertRepoParams{
		InstallationID: 9_000_000_000 + int64(uuid.New().ID()), OwnerUserID: uuid.NullUUID{UUID: owner.ID, Valid: true}, FullName: "acme/rocket",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM repos WHERE id = $1`, repo.ID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1)`, []uuid.UUID{owner.ID, other.ID})
	})
	l, err := q.CreateLaunch(ctx, sqlc.CreateLaunchParams{
		OwnerUserID: owner.ID, Source: "manual", Title: "Meetup", HeadlineChanges: []byte(`[]`), Roles: []byte(`[]`),
		LaunchAt:      pgtype.Timestamptz{Time: time.Now().Add(2 * time.Hour), Valid: true},
		WindowMinutes: 90, CrewCap: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	account := "acct-" + uuid.NewString()
	access, keyID, _ := kr.Encrypt([]byte("tok"))
	if _, err := q.UpsertBufferConnection(ctx, sqlc.UpsertBufferConnectionParams{
		UserID: owner.ID, BufferAccountID: account, OrgID: pgtype.Text{String: "org-1", Valid: true},
		AccessTokenEnc: access, KeyID: keyID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	fake := bufferfake.New(t, account, "org-1")
	fake.Handle("GetChannels", map[string]any{"channels": []any{}})
	fake.Handle("GetConfiguration", map[string]any{"configuration": map[string]any{"services": []any{}}})
	buf := buffer.New(&http.Client{Timeout: 5 * time.Second}, q, kr, buffer.WithEndpoints(fake.APIURL(), fake.TokenURL()))
	st := &session.Store{Keyring: kr}
	r2 := r2fake.New("cards")
	t.Cleanup(r2.Close)
	deps := launch.Deps{
		Q: q, Pool: pool, Buffer: buf, Keyring: kr, BaseURL: "https://lc.test",
		Cards:  cards.NewUploader(r2.URL, "cards", "ak", "sk", "https://cdn.example"),
		Render: func(any, cards.Preset) ([]byte, error) { return []byte("png"), nil },
		Preflight: preflight.Deps{
			Fetch:        func(context.Context, string) (int, string, error) { return 200, "image/png", nil },
			Limits:       buf.DailyPostingLimits,
			Config:       buf.Configuration,
			RateHeadroom: func(context.Context, uuid.UUID) (int, error) { return 100, nil },
		},
	}
	h := httpserver.New(httpserver.Deps{Captain: api.NewCaptain(deps, st)})
	return &captainEnv{pool: pool, env: &env{ctx: ctx, q: q, st: st, kr: kr, h: h}, owner: owner, other: other, launch: l, repo: repo}
}

func TestCaptainRoutesNeedTheOwner(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	theirs := e.cookie(t, session.Session{UserID: e.other.ID})
	base := "/api/captain/launches/" + e.launch.ID.String()

	rr := e.do("GET", "/api/captain/launches", "", nil, nil)
	if rr.Code != http.StatusUnauthorized || decode(t, rr)["error"] != "sign_in" {
		t.Fatalf("signed out: %d %s", rr.Code, rr.Body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", base, ""},
		{"PATCH", base, `{"title":"mine now"}`},
		{"POST", base + "/launch", ""},
		{"POST", base + "/retry/1", ""},
		{"POST", base + "/channels/refresh", ""},
		{"POST", base + "/crew/" + uuid.NewString() + "/retry-comment", ""},
		{"PATCH", "/api/captain/repos/" + e.repo.ID.String(), `{"enabled":false}`},
	} {
		if rr := e.do(c.method, c.path, c.body, nil, theirs); rr.Code != http.StatusNotFound {
			t.Fatalf("%s %s by another user: %d, want 404", c.method, c.path, rr.Code)
		}
	}
	if rr := e.do("GET", "/api/captain/launches/"+uuid.NewString(), "", nil, mine); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown launch: %d", rr.Code)
	}

	rr = e.do("GET", "/api/captain/launches", "", nil, theirs)
	if rr.Code != 200 || rr.Body.String() != "[]\n" {
		t.Fatalf("another user's list: %d %s", rr.Code, rr.Body)
	}
	rr = e.do("GET", "/api/captain/launches", "", nil, mine)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), e.launch.ID.String()) {
		t.Fatalf("own list: %d %s", rr.Code, rr.Body)
	}

	rr = e.do("GET", base, "", nil, mine)
	v := decode(t, rr)
	if rr.Code != 200 || v["id"] != e.launch.ID.String() || v["status"] != "draft" || v["windowMinutes"] != float64(90) {
		t.Fatalf("detail: %d %s", rr.Code, rr.Body)
	}
	for _, k := range []string{"headlines", "chores", "captainDrafts", "crew", "checks", "deadLetters", "counts"} {
		if _, ok := v[k]; !ok {
			t.Fatalf("detail lacks %q: %s", k, rr.Body)
		}
	}

	rr = e.do("PATCH", base, `{"windowMinutes":2}`, nil, mine)
	if rr.Code != http.StatusBadRequest || decode(t, rr)["error"] != "invalid" {
		t.Fatalf("bad patch: %d %s", rr.Code, rr.Body)
	}
	rr = e.do("PATCH", base, `{"title":"Go meetup","windowMinutes":45}`, nil, mine)
	if rr.Code != 200 || decode(t, rr)["title"] != "Go meetup" {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body)
	}

	if rr := e.do("POST", base+"/launch", "", nil, mine); rr.Code != http.StatusAccepted {
		t.Fatalf("launch: %d %s", rr.Code, rr.Body)
	}
	rr = e.do("POST", base+"/launch", "", nil, mine)
	if rr.Code != http.StatusConflict || decode(t, rr)["error"] != "already_launched" {
		t.Fatalf("second launch: %d %s", rr.Code, rr.Body)
	}

	// Retry: only a dead job of this launch.
	row, err := e.q.EnqueueJob(e.ctx, sqlc.EnqueueJobParams{Kind: "launch", Payload: []byte(`{}`), LaunchID: uuid.NullUUID{UUID: e.launch.ID, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobID := row.ID
	retry := fmt.Sprintf("%s/retry/%d", base, jobID)
	if rr := e.do("POST", retry, "", nil, mine); rr.Code != http.StatusNotFound {
		t.Fatalf("retry of a live job: %d", rr.Code)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE jobs SET dead = true, attempts = 5 WHERE id = $1`, jobID); err != nil {
		t.Fatal(err)
	}
	if rr := e.do("POST", retry, "", nil, mine); rr.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", rr.Code, rr.Body)
	}

	rr = e.do("GET", "/api/captain/repos", "", nil, mine)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"fullName":"acme/rocket"`) {
		t.Fatalf("repos: %d %s", rr.Code, rr.Body)
	}
	rr = e.do("PATCH", "/api/captain/repos/"+e.repo.ID.String(), `{"allowPrerelease":true}`, nil, mine)
	if m := decode(t, rr); rr.Code != 200 || m["allowPrerelease"] != true || m["enabled"] != true {
		t.Fatalf("repo patch: %d %s", rr.Code, rr.Body)
	}
}

func TestCaptainCreateManual(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	at := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Minute).Format(time.RFC3339)
	rr := e.do("POST", "/api/captain/launches", `{"title":"Go night","summary":"Talks","roles":[{"name":"Speaker","hint":"spoke"}],"launchAt":"`+at+`","windowMinutes":60}`, nil, mine)
	v := decode(t, rr)
	if rr.Code != http.StatusCreated || v["title"] != "Go night" || v["source"] != "manual" || v["windowMinutes"] != float64(60) || v["launchAt"] != at {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	if v["openUrl"] != "" || v["status"] != "draft" {
		t.Fatalf("draft openUrl %q, want none until launch", v["openUrl"])
	}
	rr = e.do("POST", "/api/captain/launches", `{"title":"  "}`, nil, mine)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty title: %d %s", rr.Code, rr.Body)
	}
}

func TestCaptainSessionForDeletedUserSignsOut(t *testing.T) {
	e := setupCaptain(t)
	gone, err := e.q.CreateUser(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := e.cookie(t, session.Session{UserID: gone.ID})
	if _, err := e.pool.Exec(e.ctx, `DELETE FROM users WHERE id = $1`, gone.ID); err != nil {
		t.Fatal(err)
	}
	rr := e.do("GET", "/api/captain/launches", "", nil, c)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), `"sign_in"`) {
		t.Fatalf("deleted user: %d %s", rr.Code, rr.Body)
	}
	cleared := false
	for _, ck := range rr.Result().Cookies() {
		if ck.Name == c.Name && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("cookie not cleared: %v", rr.Result().Cookies())
	}
}

func TestCaptainEditDraft(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	var id uuid.UUID
	if err := e.pool.QueryRow(e.ctx,
		`INSERT INTO captain_drafts (launch_id, channel_id, text) VALUES ($1, 'ch-1', 'old') RETURNING id`, e.launch.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	url := "/api/captain/launches/" + e.launch.ID.String() + "/drafts/" + id.String()

	if rr := e.do("PATCH", url, `{"text":"  A better post  "}`, nil, mine); rr.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rr.Code, rr.Body)
	}
	var got string
	if err := e.pool.QueryRow(e.ctx, `SELECT text FROM captain_drafts WHERE id = $1`, id).Scan(&got); err != nil || got != "A better post" {
		t.Fatalf("stored text %q, err %v", got, err)
	}
	if rr := e.do("PATCH", url, `{"text":"   "}`, nil, mine); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty text: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("PATCH", "/api/captain/launches/"+e.launch.ID.String()+"/drafts/"+uuid.NewString(), `{"text":"x"}`, nil, mine); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown draft: %d %s", rr.Code, rr.Body)
	}
	theirs := e.cookie(t, session.Session{UserID: e.other.ID})
	if rr := e.do("PATCH", url, `{"text":"hijack"}`, nil, theirs); rr.Code != http.StatusNotFound {
		t.Fatalf("someone else's launch: %d %s", rr.Code, rr.Body)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE captain_drafts SET buffer_post_id = 'p1' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if rr := e.do("PATCH", url, `{"text":"too late"}`, nil, mine); rr.Code != http.StatusBadRequest {
		t.Fatalf("sent draft: %d %s", rr.Code, rr.Body)
	}
}

func TestCaptainSkipRestoreAndLogoFields(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	var id uuid.UUID
	if err := e.pool.QueryRow(e.ctx,
		`INSERT INTO captain_drafts (launch_id, channel_id, text) VALUES ($1, 'ch-1', 'hello') RETURNING id`, e.launch.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	base := "/api/captain/launches/" + e.launch.ID.String()
	status := func() string {
		var s string
		if err := e.pool.QueryRow(e.ctx, `SELECT status FROM captain_drafts WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	if rr := e.do("PATCH", base+"/drafts/"+id.String(), `{"skipped":true}`, nil, mine); rr.Code != http.StatusOK || status() != "skipped" {
		t.Fatalf("skip: %d %s (status %s)", rr.Code, rr.Body, status())
	}
	if rr := e.do("PATCH", base+"/drafts/"+id.String(), `{"text":"edit while skipped"}`, nil, mine); rr.Code != http.StatusBadRequest {
		t.Fatalf("edit a skipped post: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("PATCH", base+"/drafts/"+id.String(), `{}`, nil, mine); rr.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("PATCH", base+"/drafts/"+id.String(), `{"skipped":false}`, nil, mine); rr.Code != http.StatusOK || status() != "draft" {
		t.Fatalf("restore: %d %s (status %s)", rr.Code, rr.Body, status())
	}

	// The detail carries the new fields.
	v := decode(t, e.do("GET", base, "", nil, mine))
	if v["qrLogo"] != "plane" || v["qrLogoUrl"] != "" {
		t.Fatalf("detail logo: %v", v)
	}
	if name, ok := v["captainName"].(string); !ok || name == "" {
		t.Fatalf("detail captainName: %v", v["captainName"])
	}

	// A bad logo choice is a 400 on create and on patch.
	for _, body := range []string{`{"title":"x","qrLogo":"custom"}`, `{"title":"x","qrLogo":"sparkle"}`, `{"title":"x","qrLogo":"custom","qrLogoUrl":"http://example.com/a.png"}`} {
		if rr := e.do("POST", "/api/captain/launches", body, nil, mine); rr.Code != http.StatusBadRequest {
			t.Fatalf("create %s: %d %s", body, rr.Code, rr.Body)
		}
	}
	if rr := e.do("PATCH", base, `{"qrLogo":"none"}`, nil, mine); rr.Code != http.StatusOK {
		t.Fatalf("patch qrLogo none: %d %s", rr.Code, rr.Body)
	}
	if v := decode(t, e.do("GET", base, "", nil, mine)); v["qrLogo"] != "none" {
		t.Fatalf("detail after patch: %v", v["qrLogo"])
	}

	// The list carries the new summary fields.
	rr := e.do("GET", "/api/captain/launches", "", nil, mine)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"crewInitials":[]`) || !strings.Contains(rr.Body.String(), `"prCount":0`) || !strings.Contains(rr.Body.String(), `"repo":""`) {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
}

func TestCaptainRefreshChannels(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	rr := e.do("POST", "/api/captain/launches/"+e.launch.ID.String()+"/channels/refresh", "", nil, mine)
	if rr.Code != http.StatusOK || decode(t, rr)["channels"] != float64(0) {
		t.Fatalf("refresh: %d %s", rr.Code, rr.Body)
	}
}

func TestCaptainRefreshChannelsNeedsBuffer(t *testing.T) {
	e := setupCaptain(t)
	l, err := e.q.CreateLaunch(e.ctx, sqlc.CreateLaunchParams{
		OwnerUserID: e.other.ID, Source: "manual", Title: "No Buffer", HeadlineChanges: []byte(`[]`), Roles: []byte(`[]`),
		LaunchAt:      pgtype.Timestamptz{Time: time.Now().Add(2 * time.Hour), Valid: true},
		WindowMinutes: 90, CrewCap: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	theirs := e.cookie(t, session.Session{UserID: e.other.ID})
	rr := e.do("POST", "/api/captain/launches/"+l.ID.String()+"/channels/refresh", "", nil, theirs)
	if rr.Code != http.StatusConflict || decode(t, rr)["error"] != "no_buffer" {
		t.Fatalf("no buffer: %d %s", rr.Code, rr.Body)
	}
}

// failedMember adds an invited contributor whose PR comment failed.
func failedMember(t *testing.T, e *captainEnv, launchID uuid.UUID) sqlc.CrewMember {
	t.Helper()
	m, err := e.q.InsertCrewMember(e.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: launchID, GithubUserID: pgtype.Int8{Int64: 7_000_000_000 + int64(uuid.New().ID()), Valid: true},
		Role: "contributor", InviteTokenHash: []byte(uuid.NewString()), State: "invited",
		PrNumber: pgtype.Int4{Int32: 12, Valid: true}, PrTitle: pgtype.Text{String: "Faster boosters", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.SetCrewCommentFailed(e.ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCaptainRetryComment(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	m := failedMember(t, e, e.launch.ID)
	path := "/api/captain/launches/" + e.launch.ID.String() + "/crew/" + m.ID.String() + "/retry-comment"

	rr := e.do("POST", path, "", nil, mine)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retry: %d %s", rr.Code, rr.Body)
	}
	var failed bool
	var retries, queued int
	if err := e.pool.QueryRow(e.ctx, `SELECT comment_failed, comment_retries FROM crew_members WHERE id = $1`, m.ID).Scan(&failed, &retries); err != nil {
		t.Fatal(err)
	}
	if failed || retries != 1 {
		t.Fatalf("after retry: failed=%v retries=%d, want false and 1", failed, retries)
	}
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND launch_id = $2`, string(jobs.SendComments), e.launch.ID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued comment jobs = %d, want 1", queued)
	}
	// Not failed any more, so a second press has nothing to retry.
	if rr := e.do("POST", path, "", nil, mine); rr.Code != http.StatusNotFound {
		t.Fatalf("retry of a comment that has not failed: %d, want 404", rr.Code)
	}
}

func TestCaptainRetryCommentIsCapped(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	m := failedMember(t, e, e.launch.ID)
	path := "/api/captain/launches/" + e.launch.ID.String() + "/crew/" + m.ID.String() + "/retry-comment"
	for i := 0; i < launch.MaxCommentRetries; i++ {
		if rr := e.do("POST", path, "", nil, mine); rr.Code != http.StatusAccepted {
			t.Fatalf("retry %d: %d %s", i+1, rr.Code, rr.Body)
		}
		if err := e.q.SetCrewCommentFailed(e.ctx, m.ID); err != nil { // GitHub refused again
			t.Fatal(err)
		}
	}
	rr := e.do("POST", path, "", nil, mine)
	if rr.Code != http.StatusConflict || decode(t, rr)["error"] != "retry_limit" {
		t.Fatalf("over the cap: %d %s", rr.Code, rr.Body)
	}
	// The detail tells the page nothing is left.
	d := e.do("GET", "/api/captain/launches/"+e.launch.ID.String(), "", nil, mine)
	if d.Code != http.StatusOK || !strings.Contains(d.Body.String(), `"commentRetriesLeft":0`) {
		t.Fatalf("detail: %d %s", d.Code, d.Body)
	}
}

func TestCaptainRetryCommentOtherLaunch(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	l2, err := e.q.CreateLaunch(e.ctx, sqlc.CreateLaunchParams{
		OwnerUserID: e.owner.ID, Source: "manual", Title: "Second", HeadlineChanges: []byte(`[]`), Roles: []byte(`[]`),
		WindowMinutes: 90, CrewCap: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := failedMember(t, e, l2.ID)
	// A member of another launch cannot be retried through this launch's URL.
	rr := e.do("POST", "/api/captain/launches/"+e.launch.ID.String()+"/crew/"+m.ID.String()+"/retry-comment", "", nil, mine)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("member of another launch: %d, want 404", rr.Code)
	}
}

func TestCaptainSettingsRoutes(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	theirs := e.cookie(t, session.Session{UserID: e.other.ID})

	if rr := e.do("GET", "/api/captain/settings", "", nil, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", rr.Code)
	}
	rr := e.do("GET", "/api/captain/settings", "", nil, mine)
	v := decode(t, rr)
	if rr.Code != 200 || v["commentTemplate"] != "" || v["draftNotes"] != "" || v["defaultComment"] == "" || v["preview"] == "" {
		t.Fatalf("defaults: %d %s", rr.Code, rr.Body)
	}
	rr = e.do("PUT", "/api/captain/settings", `{"commentTemplate":"Thanks for your PR!"}`, nil, mine)
	if rr.Code != http.StatusBadRequest || decode(t, rr)["error"] != "invalid" {
		t.Fatalf("template without {link}: %d %s", rr.Code, rr.Body)
	}
	rr = e.do("PUT", "/api/captain/settings", `{"commentTemplate":"{captain} shipped {project} {version} with {credited}. Tap {link}","draftNotes":"Mention the Discord."}`, nil, mine)
	v = decode(t, rr)
	if rr.Code != 200 || v["draftNotes"] != "Mention the Discord." || !strings.Contains(v["preview"].(string), "you shipped rocket v2.4.0") || !strings.Contains(v["preview"].(string), "Nothing posts without your tap") {
		t.Fatalf("save: %d %s", rr.Code, rr.Body)
	}
	// A later save of one field keeps the other.
	rr = e.do("PUT", "/api/captain/settings", `{"draftNotes":""}`, nil, mine)
	v = decode(t, rr)
	if rr.Code != 200 || v["draftNotes"] != "" || !strings.HasPrefix(v["commentTemplate"].(string), "{captain} shipped") {
		t.Fatalf("partial save: %d %s", rr.Code, rr.Body)
	}
	// Another captain sees their own defaults, not these.
	if rr := e.do("GET", "/api/captain/settings", "", nil, theirs); rr.Code != 200 || decode(t, rr)["commentTemplate"] != "" {
		t.Fatalf("other captain: %d %s", rr.Code, rr.Body)
	}
}
