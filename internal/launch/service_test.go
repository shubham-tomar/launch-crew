package launch_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/launch"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/bufferfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/githubfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/r2fake"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

type env struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	q       *sqlc.Queries
	kr      *crypto.Keyring
	buf     *bufferfake.Fake
	gh      *githubfake.Server
	r2      *r2fake.Server
	deps    launch.Deps
	owner   sqlc.User
	captain int64 // the owner's GitHub id
	repo    sqlc.Repo
	launch  sqlc.Launch
	alice   sqlc.CrewMember // ticked, PR 11
	bob     sqlc.CrewMember // ticked, PR 12, title tries to ping a team
	carol   sqlc.CrewMember // suggested, PR 13

	mu      sync.Mutex
	presets []cards.Preset
}

func ghID() int64 { return 4_000_000_000 + int64(uuid.New().ID()) }

func onePixelPNG(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{200, 10, 10, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// setup seeds a captain with a GitHub identity and a Buffer connection with
// two channels, an enabled repo, and a draft GitHub launch two hours out
// with a release card, two ticked contributors and one suggested.
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
	e := &env{ctx: ctx, pool: pool, q: q, kr: kr}
	if e.owner, err = q.CreateUser(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM repos WHERE id = $1`, e.repo.ID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, e.owner.ID) // cascades to launches, jobs
	})
	e.captain = ghID()
	if _, err := q.UpsertGitHubIdentity(ctx, sqlc.UpsertGitHubIdentityParams{GithubUserID: e.captain, UserID: e.owner.ID, Login: "captain"}); err != nil {
		t.Fatal(err)
	}
	install := 8_000_000_000 + int64(uuid.New().ID())
	if e.repo, err = q.UpsertRepo(ctx, sqlc.UpsertRepoParams{
		InstallationID: install, OwnerUserID: uuid.NullUUID{UUID: e.owner.ID, Valid: true}, FullName: "acme/rocket",
	}); err != nil {
		t.Fatal(err)
	}

	// Buffer: the captain's connection and two channels.
	account := "acct-" + uuid.NewString()
	access, keyID, _ := kr.Encrypt([]byte("tok"))
	if _, err := q.UpsertBufferConnection(ctx, sqlc.UpsertBufferConnectionParams{
		UserID: e.owner.ID, BufferAccountID: account, OrgID: pgtype.Text{String: "org-1", Valid: true},
		AccessTokenEnc: access, KeyID: keyID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	e.buf = bufferfake.New(t, account, "org-1")
	e.buf.Handle("GetChannels", map[string]any{"channels": []any{
		map[string]any{"id": "ch-x", "name": "Acme X", "service": "twitter"},
		map[string]any{"id": "ch-li", "name": "Acme LinkedIn", "service": "linkedin"},
	}})
	e.buf.Handle("GetConfiguration", map[string]any{"configuration": map[string]any{"services": []any{}}})
	e.buf.Handle("GetDailyPostingLimits", map[string]any{"dailyPostingLimits": []any{}})
	e.buf.Handle("GetPostsCreatedBetween", map[string]any{"posts": map[string]any{"edges": []any{}}})
	e.buf.Handle("CreatePost", map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "post-1"},
	}})
	bc := buffer.New(&http.Client{Timeout: 10 * time.Second}, q, kr, buffer.WithEndpoints(e.buf.APIURL(), e.buf.TokenURL()))

	// GitHub and R2.
	e.gh = githubfake.New("acme", "rocket")
	t.Cleanup(e.gh.Close)
	e.gh.Releases = []githubfake.Release{{ID: 1001, TagName: "v1.2.0", Name: "Rocket 1.2"}}
	ghc, err := github.New(1, githubfake.PrivateKeyPEM(), github.WithBaseURL(e.gh.URL))
	if err != nil {
		t.Fatal(err)
	}
	e.r2 = r2fake.New("cards")
	t.Cleanup(e.r2.Close)

	tiny := onePixelPNG(t)
	e.deps = launch.Deps{
		Q: q, Pool: pool, Buffer: bc, GitHub: ghc,
		Cards: cards.NewUploader(e.r2.URL, "cards", "ak", "sk", "https://cdn.example"),
		Render: func(_ any, p cards.Preset) ([]byte, error) {
			e.mu.Lock()
			e.presets = append(e.presets, p)
			e.mu.Unlock()
			return tiny, nil
		},
		Preflight: preflight.Deps{
			Fetch:        func(context.Context, string) (int, string, error) { return 200, "image/png", nil },
			Limits:       bc.DailyPostingLimits,
			Config:       bc.Configuration,
			RateHeadroom: func(context.Context, uuid.UUID) (int, error) { return 100, nil },
		},
		Keyring: kr,
		BaseURL: "https://lc.test",
	}

	alice, bob, carol := ghID(), ghID(), ghID()
	changes, _ := json.Marshal([]map[string]any{
		{"pr_number": 11, "title": "Faster boosters", "author_id": alice, "author_login": "alice", "hidden": false},
		{"pr_number": 12, "title": "Ping @acme/everyone on landing", "author_id": bob, "author_login": "bob", "hidden": false},
		{"pr_number": 13, "title": "Bump deps", "author_id": carol, "author_login": "carol", "hidden": true},
		{"pr_number": 14, "title": "Docs", "author_id": e.captain, "author_login": "captain", "hidden": true},
	})
	if e.launch, err = q.CreateLaunch(ctx, sqlc.CreateLaunchParams{
		OwnerUserID: e.owner.ID, RepoID: uuid.NullUUID{UUID: e.repo.ID, Valid: true}, Source: "github",
		Title: "Rocket 1.2", ReleaseTag: pgtype.Text{String: "v1.2.0", Valid: true},
		ReleaseUrl:      pgtype.Text{String: "https://github.com/acme/rocket/releases/tag/v1.2.0", Valid: true},
		HeadlineChanges: changes, Roles: []byte(`[]`),
		LaunchAt:      pgtype.Timestamptz{Time: time.Now().Add(2 * time.Hour).UTC().Truncate(time.Minute), Valid: true},
		WindowMinutes: 90, CrewCap: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetLaunchReleaseID(ctx, sqlc.SetLaunchReleaseIDParams{ID: e.launch.ID, ReleaseID: pgtype.Int8{Int64: 1001, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertLaunchAsset(ctx, sqlc.InsertLaunchAssetParams{LaunchID: e.launch.ID, Kind: "release", R2Url: "https://cdn.example/cards/release.png", Sha: "abc"}); err != nil {
		t.Fatal(err)
	}
	add := func(id int64, pr int, title, state string) sqlc.CrewMember {
		m, err := q.InsertCrewMember(ctx, sqlc.InsertCrewMemberParams{
			LaunchID: e.launch.ID, GithubUserID: pgtype.Int8{Int64: id, Valid: true}, Role: "contributor", State: state,
			PrNumber: pgtype.Int4{Int32: int32(pr), Valid: true}, PrTitle: pgtype.Text{String: title, Valid: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	e.alice = add(alice, 11, "Faster boosters", trigger.StateTicked)
	e.bob = add(bob, 12, "Ping @acme/everyone on landing", trigger.StateTicked)
	e.carol = add(carol, 13, "Bump deps", trigger.StateSuggested)
	add(e.captain, 14, "Docs", trigger.StateSuggested)
	return e
}

func (e *env) handlers() map[jobs.Kind]jobs.Handler {
	return map[jobs.Kind]jobs.Handler{
		jobs.LaunchJob:    launch.LaunchJobHandler(e.deps),
		jobs.SendComments: launch.SendCommentsHandler(e.deps),
		jobs.CloseWindow:  launch.CloseWindowHandler(e.deps),
		jobs.PollStatus:   launch.PollStatusHandler(e.deps),
	}
}

// drain runs this launch's due jobs in id order, calling handlers directly,
// until none are left. It returns the first handler error.
func (e *env) drain(t *testing.T) error {
	t.Helper()
	hs := e.handlers()
	for range 50 {
		var id int64
		var kind string
		var payload []byte
		err := e.pool.QueryRow(e.ctx, `SELECT id, kind, payload FROM jobs
			WHERE launch_id = $1 AND dead = false AND run_at <= now() ORDER BY id LIMIT 1`, e.launch.ID).Scan(&id, &kind, &payload)
		if err != nil {
			return nil // none left
		}
		lid := e.launch.ID
		if err := hs[jobs.Kind(kind)](e.ctx, jobs.Job{ID: id, Kind: jobs.Kind(kind), Payload: payload, LaunchID: &lid}); err != nil {
			return fmt.Errorf("%s: %w", kind, err)
		}
		if _, err := e.pool.Exec(e.ctx, `DELETE FROM jobs WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("jobs never drained")
	return nil
}

func (e *env) jobCount(t *testing.T, kind jobs.Kind) int {
	t.Helper()
	n, err := e.q.CountJobsOfKind(e.ctx, sqlc.CountJobsOfKindParams{Kind: string(kind), LaunchID: uuid.NullUUID{UUID: e.launch.ID, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func (e *env) member(t *testing.T, id uuid.UUID) sqlc.CrewMember {
	t.Helper()
	m, err := e.q.GetCrewMember(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) createInputs() []map[string]any {
	var out []map[string]any
	for _, r := range e.buf.Requests() {
		if r.Op == "CreatePost" {
			out = append(out, r.Vars["input"].(map[string]any))
		}
	}
	return out
}

func TestLaunchTwiceCreatesNothingNew(t *testing.T) {
	e := setup(t)
	// A changesets-style tag must not ping the @acme/rocket team either.
	if _, err := e.pool.Exec(e.ctx, `UPDATE launches SET release_tag = '@acme/rocket@1.2.0' WHERE id = $1`, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); !errors.Is(err, launch.ErrAlreadyLaunched) {
		t.Fatalf("second press: %v, want ErrAlreadyLaunched", err)
	}
	if n := e.jobCount(t, jobs.LaunchJob); n != 1 {
		t.Fatalf("%d launch jobs, want 1", n)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}

	posts := e.createInputs()
	if len(posts) != 2 {
		t.Fatalf("%d captain posts, want 2", len(posts))
	}
	for _, in := range posts {
		if in["mode"] != "customScheduled" || in["dueAt"] != e.launch.LaunchAt.Time.UTC().Format(time.RFC3339) {
			t.Fatalf("captain post not scheduled at launch_at: %v", in)
		}
	}
	ds, _ := e.q.ListCaptainDrafts(e.ctx, e.launch.ID)
	for _, d := range ds {
		if d.Status != "queued" || d.BufferPostID.String != "post-1" {
			t.Fatalf("captain draft %s: %s %q", d.ChannelID, d.Status, d.BufferPostID.String)
		}
	}

	c11, c12 := e.gh.IssueComments(11), e.gh.IssueComments(12)
	if len(c11) != 1 || len(c12) != 1 || len(e.gh.IssueComments(13)) != 0 || len(e.gh.IssueComments(14)) != 0 {
		t.Fatalf("comments: #11 %d, #12 %d, #13 %d, #14 %d", len(c11), len(c12), len(e.gh.IssueComments(13)), len(e.gh.IssueComments(14)))
	}
	if strings.Contains(c12[0].Body, "@acme/everyone") || strings.Contains(c11[0].Body, "@acme/rocket") || strings.Contains(c11[0].Body, "@1.2.0") {
		t.Fatalf("PR title pings a team: %q", c12[0].Body)
	}
	if !strings.Contains(c11[0].Body, "**@captain**") || !strings.Contains(c11[0].Body, "https://lc.test/s/") {
		t.Fatalf("comment body: %q", c11[0].Body)
	}
	a := e.member(t, e.alice.ID)
	if tok, err := e.kr.Decrypt(a.InviteTokenEnc); err != nil || !strings.Contains(c11[0].Body, "https://lc.test/settings?token="+string(tok)) {
		t.Fatalf("footer lacks the opt-out token link: %q (%v)", c11[0].Body, err)
	}
	if a.State != "invited" || !a.PrCommentID.Valid || a.PrCommentID.Int64 != c11[0].ID || len(a.InviteTokenEnc) == 0 || !a.CardAssetID.Valid {
		t.Fatalf("alice after launch: %+v", a)
	}
	if c := e.member(t, e.carol.ID); c.State != trigger.StateSuggested || len(c.InviteTokenHash) != 0 {
		t.Fatalf("suggested carol was invited: %+v", c)
	}

	at := e.launch.LaunchAt.Time
	for kind, want := range map[jobs.Kind]time.Time{jobs.CloseWindow: at.Add(90 * time.Minute), jobs.PollStatus: at.Add(100 * time.Minute)} {
		var runAt time.Time
		if err := e.pool.QueryRow(e.ctx, `SELECT run_at FROM jobs WHERE launch_id = $1 AND kind = $2`, e.launch.ID, string(kind)).Scan(&runAt); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !runAt.Equal(want) {
			t.Fatalf("%s at %v, want %v", kind, runAt, want)
		}
	}

	// A re-run of the job (a retry, or a second worker) changes nothing.
	payload, _ := json.Marshal(launch.LaunchPayload{LaunchID: e.launch.ID})
	if err := launch.LaunchJobHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.LaunchJob, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	if n := len(e.createInputs()); n != 2 {
		t.Fatalf("%d captain posts after re-run, want 2", n)
	}
	if len(e.gh.IssueComments(11)) != 1 || len(e.gh.IssueComments(12)) != 1 {
		t.Fatal("re-run posted more comments")
	}
	if n := e.jobCount(t, jobs.CloseWindow); n != 1 {
		t.Fatalf("%d close jobs after re-run, want 1", n)
	}
}

func TestLaunchJobBlockedByRedPreflight(t *testing.T) {
	e := setup(t)
	notFound := func(context.Context, string) (int, string, error) { return 404, "text/html", nil }
	green := e.deps.Preflight.Fetch

	// Red at the press: refused, still a draft, nothing enqueued.
	e.deps.Preflight.Fetch = notFound
	var pe *launch.PreflightError
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); !errors.As(err, &pe) || len(pe.Red) != 1 || pe.Red[0] != "Card images reachable" {
		t.Fatalf("launch with red preflight: %v", err)
	}
	if l, _ := e.q.GetLaunch(e.ctx, e.launch.ID); l.Status != "draft" || e.jobCount(t, jobs.LaunchJob) != 0 {
		t.Fatalf("refused launch: status %s, %d jobs", l.Status, e.jobCount(t, jobs.LaunchJob))
	}

	// Green at the press, the card gone by the time the job runs.
	e.deps.Preflight.Fetch = green
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	e.deps.Preflight.Fetch = notFound
	err := e.drain(t)
	if err == nil || !strings.Contains(err.Error(), "Card images reachable") {
		t.Fatalf("job error %v, want it to name check 1", err)
	}
	if n := len(e.createInputs()); n != 0 {
		t.Fatalf("%d posts despite red preflight", n)
	}
	if len(e.gh.Requests("POST", "/comments")) != 0 {
		t.Fatal("comments sent despite red preflight")
	}
	if a := e.member(t, e.alice.ID); a.State != trigger.StateTicked {
		t.Fatalf("alice %s, want still ticked", a.State)
	}
}

func TestTwoWorkersOneLaunchJob(t *testing.T) {
	e := setup(t)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	// A duplicate job, as if the enqueue had been delivered twice.
	lid := e.launch.ID
	if _, err := jobs.Enqueue(e.ctx, e.q, jobs.LaunchJob, launch.LaunchPayload{LaunchID: lid}, time.Time{}, &lid); err != nil {
		t.Fatal(err)
	}
	hs := e.handlers()
	pending := func() int {
		var n int
		_ = e.pool.QueryRow(e.ctx, `SELECT count(*) FROM jobs WHERE launch_id = $1 AND dead = false AND run_at <= now()`, lid).Scan(&n)
		return n
	}
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, who := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil && pending() > 0 {
				if handled, _ := jobs.RunOnce(ctx, e.q, hs, who); !handled {
					time.Sleep(20 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	if pending() != 0 {
		var errs []string
		rows, _ := e.pool.Query(e.ctx, `SELECT kind, coalesce(last_error, '') FROM jobs WHERE launch_id = $1`, lid)
		for rows.Next() {
			var k, m string
			_ = rows.Scan(&k, &m)
			errs = append(errs, k+": "+m)
		}
		t.Fatalf("jobs left: %v", errs)
	}
	if n := len(e.createInputs()); n != 2 {
		t.Fatalf("%d captain posts, want 2", n)
	}
	if len(e.gh.IssueComments(11)) != 1 || len(e.gh.IssueComments(12)) != 1 {
		t.Fatalf("comments #11 %d, #12 %d, want 1 each", len(e.gh.IssueComments(11)), len(e.gh.IssueComments(12)))
	}
	if n := len(e.gh.Requests("POST", "/issues/")); n != 2 {
		t.Fatalf("%d comment posts, want 2", n)
	}
}

func TestCommentPermissionFailureMarksRow(t *testing.T) {
	e := setup(t)
	e.gh.Lock()
	e.gh.DenyComments = true
	e.gh.Unlock()
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatalf("a permission failure must not fail the job: %v", err)
	}
	a := e.member(t, e.alice.ID)
	if !a.CommentFailed || a.PrCommentID.Valid {
		t.Fatalf("alice: failed=%v comment=%v", a.CommentFailed, a.PrCommentID)
	}
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var row *launch.CrewRow
	for i := range v.Crew {
		if v.Crew[i].ID == e.alice.ID {
			row = &v.Crew[i]
		}
	}
	if row == nil || !row.CommentFailed || !strings.HasPrefix(row.InviteURL, "https://lc.test/s/") {
		t.Fatalf("detail row: %+v", row)
	}
	// The copied link works.
	tok := strings.TrimPrefix(row.InviteURL, "https://lc.test/s/")
	if m, err := e.q.GetCrewMemberByTokenHash(e.ctx, crew.HashToken(tok)); err != nil || m.ID != e.alice.ID {
		t.Fatalf("invite link does not resolve: %v", err)
	}
}

func TestCloseWindowAndPollMarkPosted(t *testing.T) {
	e := setup(t)
	if err := e.q.UpdateLaunchStatus(e.ctx, sqlc.UpdateLaunchStatusParams{ID: e.launch.ID, Status: "launched"}); err != nil {
		t.Fatal(err)
	}
	// alice connected her own Buffer and queued two posts.
	u, _ := e.q.CreateUser(e.ctx)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID) })
	access, keyID, _ := e.kr.Encrypt([]byte("tok2"))
	account := "acct-" + uuid.NewString()
	if _, err := e.q.UpsertBufferConnection(e.ctx, sqlc.UpsertBufferConnectionParams{
		UserID: u.ID, BufferAccountID: account, OrgID: pgtype.Text{String: "org-2", Valid: true},
		AccessTokenEnc: access, KeyID: keyID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.UpdateCrewMemberConnection(e.ctx, sqlc.UpdateCrewMemberConnectionParams{
		ID: e.alice.ID, UserID: uuid.NullUUID{UUID: u.ID, Valid: true},
		BufferAccountID: pgtype.Text{String: account, Valid: true}, State: "queued",
	}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct{ ch, post string }{{"a-x", "p-sent"}, {"a-bsky", "p-err"}} {
		dr, err := e.q.UpsertCrewDraft(e.ctx, sqlc.UpsertCrewDraftParams{CrewMemberID: e.alice.ID, ChannelID: d.ch, Network: "twitter", Text: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.q.UpdateCrewDraftPosting(e.ctx, sqlc.UpdateCrewDraftPostingParams{
			ID: dr.ID, Status: "queued", BufferPostID: pgtype.Text{String: d.post, Valid: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	e.buf.Handle("GetPostsCreatedBetween", map[string]any{"posts": map[string]any{"edges": []any{
		map[string]any{"node": map[string]any{"id": "p-sent", "channelId": "a-x", "text": "hi", "status": "sent", "externalLink": "https://x.com/alice/status/9", "createdAt": time.Now().Format(time.RFC3339)}},
		map[string]any{"node": map[string]any{"id": "p-err", "channelId": "a-bsky", "text": "hi", "status": "error", "createdAt": time.Now().Format(time.RFC3339)}},
	}}})

	payload, _ := json.Marshal(launch.LaunchPayload{LaunchID: e.launch.ID})
	if err := launch.CloseWindowHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.CloseWindow, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if l, _ := e.q.GetLaunch(e.ctx, e.launch.ID); l.Status != "closed" {
		t.Fatalf("status %s, want closed", l.Status)
	}
	if err := launch.PollStatusHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.PollStatus, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	ds, _ := e.q.ListCrewDrafts(e.ctx, e.alice.ID)
	for _, d := range ds {
		got[d.ChannelID] = d.Status
	}
	if got["a-x"] != "posted" || got["a-bsky"] != "failed_in_buffer" {
		t.Fatalf("draft statuses %v", got)
	}
	if a := e.member(t, e.alice.ID); a.State != "posted" {
		t.Fatalf("alice %s, want posted", a.State)
	}
	// The captain's crew row links to the sent post and names the failed one.
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var alice launch.CrewRow
	for _, r := range v.Crew {
		if r.ID == e.alice.ID {
			alice = r
		}
	}
	links := map[string]string{}
	for _, p := range alice.Posts {
		links[p.Status] = p.ExternalLink
	}
	if len(alice.Posts) != 2 || links["posted"] != "https://x.com/alice/status/9" || links["failed_in_buffer"] != "" {
		t.Fatalf("alice's posts %+v", alice.Posts)
	}
	// Closing again is a no-op; a deleted launch is never reopened.
	if err := launch.CloseWindowHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.CloseWindow, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func TestManualCreateAssignsPresetDeterministically(t *testing.T) {
	e := setup(t)
	for range 2 {
		e.presets = nil
		l, tok, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, trigger.ManualInput{Title: "  Go meetup  ", Summary: "Talks and pizza"})
		if err != nil {
			t.Fatal(err)
		}
		row, err := e.q.GetLaunch(e.ctx, l.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := cards.PresetFor(l.ID)
		if !row.CardPreset.Valid || cards.Presets[row.CardPreset.Int16] != want {
			t.Fatalf("stored preset %v, want %s", row.CardPreset, want.Name)
		}
		if len(e.presets) != 1 || e.presets[0] != want || cards.PresetFor(l.ID) != want {
			t.Fatalf("rendered with %v, want %s", e.presets, want.Name)
		}
		if row.Source != "manual" || row.Status != "draft" || row.Title != "Go meetup" || l.Title != "Go meetup" {
			t.Fatalf("launch row: %+v", row)
		}
		if !bytes.Equal(row.OpenTokenHash, crew.HashToken(tok)) {
			t.Fatal("open token hash does not match the returned token")
		}
		if plain, err := e.kr.Decrypt(row.OpenTokenEnc); err != nil || string(plain) != tok {
			t.Fatalf("open token at rest: %q %v", plain, err)
		}
		if _, err := e.q.GetLatestLaunchAssetByKind(e.ctx, sqlc.GetLatestLaunchAssetByKindParams{LaunchID: l.ID, Kind: "release"}); err != nil {
			t.Fatalf("release card: %v", err)
		}
		v, err := launch.Detail(e.ctx, e.deps, l.ID)
		if err != nil {
			t.Fatal(err)
		}
		// A draft's open link is dead, so none are offered yet.
		if v.OpenURL != "" || v.ShowURL != "" || v.PNGURL != "" {
			t.Fatalf("draft links: %q %q %q", v.OpenURL, v.ShowURL, v.PNGURL)
		}
		if len(v.Checks) != 7 {
			t.Fatalf("%d checks, want 7", len(v.Checks))
		}
		for _, st := range []string{"launched", "closed"} {
			if err := e.q.UpdateLaunchStatus(e.ctx, sqlc.UpdateLaunchStatusParams{ID: l.ID, Status: st}); err != nil {
				t.Fatal(err)
			}
			if v, err = launch.Detail(e.ctx, e.deps, l.ID); err != nil {
				t.Fatal(err)
			}
			if v.OpenURL != "https://lc.test/l/"+tok || v.ShowURL != "https://lc.test/l/"+tok+"/show" || v.PNGURL != "https://lc.test/api/l/"+tok+"/show.png" {
				t.Fatalf("%s links: %q %q %q", st, v.OpenURL, v.ShowURL, v.PNGURL)
			}
		}
	}
}

func TestDetailHidesCaptainAndUpdateValidates(t *testing.T) {
	e := setup(t)
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Crew {
		if r.Login == "captain" {
			t.Fatal("captain listed in their own crew")
		}
	}
	if len(v.Crew) != 3 || len(v.Headlines) != 2 || len(v.Chores) != 2 || len(v.CaptainDrafts) != 2 || len(v.Checks) != 7 {
		t.Fatalf("detail: crew %d headlines %d chores %d drafts %d checks %d", len(v.Crew), len(v.Headlines), len(v.Chores), len(v.CaptainDrafts), len(v.Checks))
	}
	for _, c := range v.Checks {
		if !c.OK {
			t.Fatalf("red check on a good launch: %+v", c)
		}
	}
	// The captain's own posts announce; the crew-voice personal hook is not theirs.
	for _, d := range v.CaptainDrafts {
		if strings.HasPrefix(d.Text, "I came along") {
			t.Fatalf("captain draft in crew voice: %q", d.Text)
		}
	}

	bad := []launch.Patch{
		{WindowMinutes: ptr(4)}, {WindowMinutes: ptr(721)}, {CrewCap: ptr(0)}, {CrewCap: ptr(1001)},
		{LaunchAt: ptr(time.Now().Add(-time.Minute))}, {LaunchAt: ptr(time.Now().Add(31 * 24 * time.Hour))},
		{Title: ptr("  ")},
	}
	for _, p := range bad {
		if err := launch.Update(e.ctx, e.deps, e.launch.ID, p); !errors.Is(err, launch.ErrInvalid) {
			t.Fatalf("patch %+v: %v, want ErrInvalid", p, err)
		}
	}
	at := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Minute)
	if err := launch.Update(e.ctx, e.deps, e.launch.ID, launch.Patch{
		Title: ptr("Rocket 1.2!"), LaunchAt: &at, WindowMinutes: ptr(30), CrewCap: ptr(50),
		HiddenPRs: []int{11, 13}, TickedCrew: []uuid.UUID{e.carol.ID},
	}); err != nil {
		t.Fatal(err)
	}
	v, _ = launch.Detail(e.ctx, e.deps, e.launch.ID)
	if v.Title != "Rocket 1.2!" || !v.LaunchAt.Equal(at) || v.WindowMinutes != 30 || v.CrewCap != 50 {
		t.Fatalf("detail after patch: %+v", v)
	}
	hidden := map[int]bool{}
	for _, c := range append(v.Headlines, v.Chores...) {
		hidden[c.PRNumber] = c.Hidden
	}
	if !hidden[11] || hidden[12] || !hidden[13] || hidden[14] {
		t.Fatalf("hidden %v", hidden)
	}
	if e.member(t, e.alice.ID).State != trigger.StateSuggested || e.member(t, e.carol.ID).State != trigger.StateTicked {
		t.Fatal("ticked crew not moved")
	}

	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := launch.Update(e.ctx, e.deps, e.launch.ID, launch.Patch{Title: ptr("late")}); !errors.Is(err, launch.ErrNotDraft) {
		t.Fatalf("patch after launch: %v, want ErrNotDraft", err)
	}
}

func ptr[T any](v T) *T { return &v }

// --- hero URL guard ---

func TestEditRegeneratesCaptainDrafts(t *testing.T) {
	e := setup(t)
	if _, err := launch.Detail(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	ds, _ := e.q.ListCaptainDrafts(e.ctx, e.launch.ID)
	if len(ds) != 2 {
		t.Fatalf("%d drafts, want 2", len(ds))
	}
	// One draft already went past draft: it must survive the edit as is.
	var sent sqlc.CaptainDraft
	for _, d := range ds {
		if d.ChannelID == "ch-li" {
			sent = d
		}
	}
	if err := e.q.UpdateCaptainDraftStatus(e.ctx, sqlc.UpdateCaptainDraftStatusParams{ID: sent.ID, Status: "queued", BufferPostID: pgtype.Text{String: "p-li", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	// Renaming changes nothing for a repo launch (the project is the repo
	// name); hiding the lead headline does.
	if err := launch.Update(e.ctx, e.deps, e.launch.ID, launch.Patch{Title: ptr("Rocket 1.2 final"), HiddenPRs: []int{11, 13, 14}}); err != nil {
		t.Fatal(err)
	}
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]launch.CaptainDraftView{}
	for _, d := range v.CaptainDrafts {
		got[d.ChannelID] = d
	}
	x := got["ch-x"]
	if x.ID == uuid.Nil || strings.Contains(x.Text, "Faster boosters") || x.Status != "draft" {
		t.Fatalf("ch-x draft not regenerated: %+v", x)
	}
	if li := got["ch-li"]; li.ID != sent.ID || li.Text != sent.Text || li.Status != "queued" {
		t.Fatalf("queued draft touched: %+v", li)
	}

	// A manual launch: the title is the project, so a rename shows up.
	l, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, trigger.ManualInput{Title: "Go meetup"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err = launch.Detail(e.ctx, e.deps, l.ID); err != nil || len(v.CaptainDrafts) == 0 || !strings.Contains(v.CaptainDrafts[0].Text, "Go meetup") {
		t.Fatalf("manual drafts: %v %+v", err, v.CaptainDrafts)
	}
	if err := launch.Update(e.ctx, e.deps, l.ID, launch.Patch{Title: ptr("Rust night")}); err != nil {
		t.Fatal(err)
	}
	v, _ = launch.Detail(e.ctx, e.deps, l.ID)
	for _, d := range v.CaptainDrafts {
		if !strings.Contains(d.Text, "Rust night") || strings.Contains(d.Text, "Go meetup") {
			t.Fatalf("draft after rename: %q", d.Text)
		}
	}
}

func TestLaunchJobAfterLaunchTimeStillInvites(t *testing.T) {
	e := setup(t)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	// The job (or its retry) runs after launch_at has passed.
	late := e.launch.LaunchAt.Time.Add(30 * time.Minute)
	e.deps.Now = func() time.Time { return late }
	if err := e.drain(t); err != nil {
		t.Fatalf("late launch job: %v", err)
	}
	if a := e.member(t, e.alice.ID); a.State != "invited" || !a.PrCommentID.Valid {
		t.Fatalf("alice not invited: %s %v", a.State, a.PrCommentID)
	}
	// The press is still gated on the time.
	e2 := setup(t)
	e2.deps.Now = func() time.Time { return e2.launch.LaunchAt.Time.Add(time.Minute) }
	var pe *launch.PreflightError
	if err := launch.Launch(e2.ctx, e2.deps, e2.launch.ID); !errors.As(err, &pe) || !strings.Contains(err.Error(), "Launch time") {
		t.Fatalf("late press: %v, want a red launch time", err)
	}
}

func TestListAdmitsRepoOwner(t *testing.T) {
	e := setup(t)
	other, _ := e.q.CreateUser(e.ctx)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, other.ID) })
	if ls, _ := launch.List(e.ctx, e.deps, other.ID); len(ls) != 0 {
		t.Fatalf("stranger sees %d launches", len(ls))
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE repos SET owner_user_id = $1 WHERE id = $2`, other.ID, e.repo.ID); err != nil {
		t.Fatal(err)
	}
	ls, err := launch.List(e.ctx, e.deps, other.ID)
	if err != nil || len(ls) != 1 || ls[0].ID != e.launch.ID {
		t.Fatalf("repo owner's list: %v %+v", err, ls)
	}
	if _, err := launch.Authorize(e.ctx, e.deps, e.launch.ID, other.ID); err != nil {
		t.Fatalf("repo owner refused: %v", err)
	}
}

func TestHeroURLGuard(t *testing.T) {
	e := setup(t)
	img := onePixelPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hero.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(img)
		case "/huge.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(make([]byte, 6<<20))
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/meta":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
		}
	}))
	defer srv.Close()

	in := trigger.ManualInput{Title: "Meetup", HeroURL: srv.URL + "/hero.png"}
	if _, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, in); !errors.Is(err, launch.ErrHeroRefused) {
		t.Fatalf("loopback hero: %v, want ErrHeroRefused", err)
	}
	if ls, _ := e.q.ListLaunchesByOwner(e.ctx, uuid.NullUUID{UUID: e.owner.ID, Valid: true}); len(ls) != 1 {
		t.Fatalf("%d launches after a refused hero, want only the seeded one", len(ls))
	}

	e.deps.AllowLoopback = true // tests only: lets the happy path reach httptest
	l, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	row, _ := e.q.GetLaunch(e.ctx, l.ID)
	hero, err := e.q.GetLatestLaunchAssetByKind(e.ctx, sqlc.GetLatestLaunchAssetByKindParams{LaunchID: l.ID, Kind: "hero"})
	if err != nil || !row.HeroAssetID.Valid || row.HeroAssetID.UUID != hero.ID || !strings.HasPrefix(hero.R2Url, "https://cdn.example/cards/") {
		t.Fatalf("hero asset %+v (%v), launch hero %v", hero, err, row.HeroAssetID)
	}
	if _, ok := e.r2.Object("cards/" + hero.Sha + ".png"); !ok {
		t.Fatal("hero not re-uploaded")
	}

	for path, want := range map[string]error{
		"/huge.png": launch.ErrHeroRefused,
		"/page":     trigger.ErrHeroNotImage,
		"/loop":     launch.ErrHeroRefused,
		"/meta":     launch.ErrHeroRefused,
	} {
		in.HeroURL = srv.URL + path
		if _, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, in); !errors.Is(err, want) {
			t.Fatalf("%s: %v, want %v", path, err, want)
		}
	}
}

func TestBlockedAddresses(t *testing.T) {
	for addr, blocked := range map[string]bool{
		"127.0.0.1": true, "::1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true,
		"169.254.169.254": true, "fe80::1": true, "fd00::1": true, "100.64.0.1": true, "0.0.0.0": true,
		"224.0.0.1": true, "ff02::1": true, "fd00:ec2::254": true, "::ffff:10.0.0.1": true,
		"93.184.216.34": false, "2606:4700::1111": false,
	} {
		if got := launch.Blocked(addr, false); got != blocked {
			t.Errorf("%s: blocked=%v, want %v", addr, got, blocked)
		}
	}
	if launch.Blocked("127.0.0.1", true) || !launch.Blocked("10.0.0.1", true) {
		t.Error("allowLoopback must admit loopback only")
	}
}

func TestDeletedLaunchRunsNoJobs(t *testing.T) {
	e := setup(t)
	if err := e.q.UpdateLaunchStatus(e.ctx, sqlc.UpdateLaunchStatusParams{ID: e.launch.ID, Status: "deleted"}); err != nil {
		t.Fatal(err)
	}
	// As after the owner deleted their account: no owner left.
	if _, err := e.pool.Exec(e.ctx, `UPDATE launches SET owner_user_id = NULL WHERE id = $1`, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	lp, _ := json.Marshal(launch.LaunchPayload{LaunchID: e.launch.ID})
	cp, _ := json.Marshal(launch.CommentsPayload{LaunchID: e.launch.ID, MemberID: e.alice.ID})
	rp, _ := json.Marshal(launch.CommentsPayload{LaunchID: e.launch.ID, RollCall: true})
	for name, run := range map[string]func() error{
		"launch": func() error {
			return launch.LaunchJobHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.LaunchJob, Payload: lp})
		},
		"comments": func() error {
			return launch.SendCommentsHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.SendComments, Payload: cp})
		},
		"roll": func() error {
			return launch.SendCommentsHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.SendComments, Payload: rp})
		},
		"close": func() error {
			return launch.CloseWindowHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.CloseWindow, Payload: lp})
		},
		"poll": func() error {
			return launch.PollStatusHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.PollStatus, Payload: lp})
		},
	} {
		if err := run(); err != nil {
			t.Fatalf("%s on a deleted launch: %v", name, err)
		}
	}
	if len(e.gh.Requests("POST", "/comments")) != 0 || len(e.createInputs()) != 0 {
		t.Fatal("a deleted launch posted something")
	}
	if l, _ := e.q.GetLaunch(e.ctx, e.launch.ID); l.Status != "deleted" {
		t.Fatalf("status %s, want deleted", l.Status)
	}
}

func TestOptedOutRowIsNeverInvited(t *testing.T) {
	e := setup(t)
	// alice opted out through another launch's link; her row here is still ticked.
	if err := e.q.InsertOptOut(e.ctx, e.alice.GithubUserID.Int64); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM opt_outs WHERE github_user_id = $1`, e.alice.GithubUserID.Int64)
	})
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	if n := len(e.gh.IssueComments(11)); n != 0 {
		t.Fatalf("%d comments on the opted-out contributor's PR", n)
	}
	if a := e.member(t, e.alice.ID); a.State != "opted_out" || len(a.InviteTokenHash) != 0 {
		t.Fatalf("alice: state %s, invited=%v", a.State, len(a.InviteTokenHash) != 0)
	}
	if n := len(e.gh.IssueComments(12)); n != 1 {
		t.Fatalf("bob got %d comments, want 1", n)
	}
}

func TestOptOutAfterInviteStopsTheComment(t *testing.T) {
	e := setup(t)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(launch.LaunchPayload{LaunchID: e.launch.ID})
	if err := launch.LaunchJobHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.LaunchJob, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	// Invited, comment job queued; now alice opts out.
	if err := e.q.InsertOptOut(e.ctx, e.alice.GithubUserID.Int64); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM opt_outs WHERE github_user_id = $1`, e.alice.GithubUserID.Int64)
	})
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	if n := len(e.gh.IssueComments(11)); n != 0 {
		t.Fatalf("%d comments after the opt-out", n)
	}
	if n := len(e.gh.IssueComments(12)); n != 1 {
		t.Fatalf("bob got %d comments, want 1", n)
	}
}

func TestCommentForDeletedMemberIsDone(t *testing.T) {
	e := setup(t)
	if _, err := e.pool.Exec(e.ctx, `DELETE FROM crew_members WHERE id = $1`, e.alice.ID); err != nil {
		t.Fatal(err)
	}
	cp, _ := json.Marshal(launch.CommentsPayload{LaunchID: e.launch.ID, MemberID: e.alice.ID})
	if err := launch.SendCommentsHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.SendComments, Payload: cp}); err != nil {
		t.Fatalf("missing member must not dead-letter: %v", err)
	}
}

func createPostAnswer(typename, msg string) bufferfake.Response {
	return bufferfake.Response{Data: map[string]any{"createPost": map[string]any{"__typename": typename, "message": msg}}}
}

func captainDraftsByChannel(t *testing.T, e *env) map[string]launch.CaptainDraftView {
	t.Helper()
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]launch.CaptainDraftView{}
	for _, cd := range v.CaptainDrafts {
		out[cd.ChannelID] = cd
	}
	return out
}

func TestCaptainPostSavedAsDraftWhenQueueFull(t *testing.T) {
	e := setup(t)
	// Channels go in id order: ch-li, then ch-x (full queue, then the draft retry).
	ok := bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "post-li"},
	}}}
	draft := bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "post-draft"},
	}}}
	e.buf.Script("CreatePost", ok, createPostAnswer("LimitReachedError", "queue limit"), draft)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	ds := captainDraftsByChannel(t, e)
	if d := ds["ch-li"]; d.Status != "queued" || d.Note != "" {
		t.Fatalf("ch-li: %+v", d)
	}
	if d := ds["ch-x"]; d.Status != "draft" || d.Note != buffer.DraftNote || d.BufferPostID != "post-draft" {
		t.Fatalf("ch-x: %+v, want draft with the queue-full note", d)
	}
	// A re-run sends nothing again.
	payload, _ := json.Marshal(launch.LaunchPayload{LaunchID: e.launch.ID})
	if err := launch.LaunchJobHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.LaunchJob, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.createInputs()); n != 3 {
		t.Fatalf("%d create calls after re-run, want 3", n)
	}
}

func TestOneRejectedCaptainChannelDoesNotBlockLaunch(t *testing.T) {
	e := setup(t)
	ok := bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "post-x"},
	}}}
	e.buf.Script("CreatePost", createPostAnswer("InvalidInputError", "Text too long for LinkedIn"), ok)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatalf("launch job failed: %v", err)
	}
	ds := captainDraftsByChannel(t, e)
	if d := ds["ch-x"]; d.Status != "queued" {
		t.Fatalf("ch-x: %+v", d)
	}
	if d := ds["ch-li"]; d.Status != "failed" || !strings.Contains(d.Note, "Text too long for LinkedIn") {
		t.Fatalf("ch-li: %+v, want failed with a note", d)
	}
	if a := e.member(t, e.alice.ID); a.State != "invited" {
		t.Fatalf("alice %s, want invited", a.State)
	}
	if len(e.gh.IssueComments(11)) != 1 || len(e.gh.IssueComments(12)) != 1 {
		t.Fatal("comments not posted")
	}
	var dead int
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM jobs WHERE launch_id = $1 AND dead`, e.launch.ID).Scan(&dead); err != nil || dead != 0 {
		t.Fatalf("%d dead jobs (%v)", dead, err)
	}
}

func TestTransientCaptainErrorStillRetries(t *testing.T) {
	e := setup(t)
	e.buf.Script("CreatePost", bufferfake.Response{Status: http.StatusBadGateway})
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err == nil {
		t.Fatal("transient error did not fail the job")
	}
	if a := e.member(t, e.alice.ID); a.State != trigger.StateTicked {
		t.Fatalf("alice %s, want still ticked until the retry", a.State)
	}
}

// Once CloseWindow or PollStatus dead-letters, a retried LaunchJob must
// enqueue it again; a dead row is not a scheduled job.
func TestRetriedLaunchJobReplacesDeadWindowJobs(t *testing.T) {
	e := setup(t)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE jobs SET dead = true, attempts = 5 WHERE launch_id = $1 AND kind = $2`, e.launch.ID, string(jobs.CloseWindow)); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(launch.LaunchPayload{LaunchID: e.launch.ID})
	if err := launch.LaunchJobHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.LaunchJob, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	live := func(kind jobs.Kind) int {
		var n int
		if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM jobs WHERE launch_id = $1 AND kind = $2 AND dead = false`, e.launch.ID, string(kind)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := live(jobs.CloseWindow); n != 1 {
		t.Fatalf("%d live close jobs after the retry, want 1", n)
	}
	if n := live(jobs.PollStatus); n != 1 {
		t.Fatalf("%d live poll jobs after the retry, want 1 (not duplicated)", n)
	}
}

// A LaunchJob retried after launch_at cannot schedule at a time in the past;
// Buffer refuses that. The captain's posts take the next queue slot instead,
// and the draft says so.
func TestLateLaunchJobQueuesCaptainPosts(t *testing.T) {
	e := setup(t)
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	late := e.launch.LaunchAt.Time.Add(30 * time.Minute)
	e.deps.Now = func() time.Time { return late }
	if err := e.drain(t); err != nil {
		t.Fatalf("late launch job: %v", err)
	}
	posts := e.createInputs()
	if len(posts) != 2 {
		t.Fatalf("%d captain posts, want 2", len(posts))
	}
	for _, in := range posts {
		if in["mode"] != "addToQueue" || in["dueAt"] != nil {
			t.Fatalf("late captain post scheduled in the past: %v", in)
		}
	}
	ds, _ := e.q.ListCaptainDrafts(e.ctx, e.launch.ID)
	for _, d := range ds {
		if d.Status != "queued" || d.BufferPostID.String != "post-1" || !strings.Contains(d.Note, "queue") {
			t.Fatalf("captain draft %s: %s %q note=%q", d.ChannelID, d.Status, d.BufferPostID.String, d.Note)
		}
	}
}

func TestCommentUsesTheCaptainsTemplate(t *testing.T) {
	e := setup(t)
	tmpl := "{captain} just cut {project} {version}, thanks for {credited}. Tap {link}"
	if _, err := launch.SaveSettings(e.ctx, e.deps, e.owner.ID, launch.SettingsPatch{CommentTemplate: &tmpl}); err != nil {
		t.Fatal(err)
	}
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	cs := e.gh.IssueComments(int(e.alice.PrNumber.Int32))
	if len(cs) != 1 {
		t.Fatalf("comments on alice's PR: %+v", cs)
	}
	body := cs[0].Body
	if !strings.HasPrefix(body, "captain just cut rocket v1.2.0, thanks for your PR") || !strings.Contains(body, "Tap https://lc.test/s/") {
		t.Fatalf("body does not follow the template:\n%s", body)
	}
	if !strings.Contains(body, "Nothing posts without your tap") || !strings.Contains(body, "Opt out or delete your data: https://lc.test/settings?token=") {
		t.Fatalf("body lost the consent footer:\n%s", body)
	}
}
