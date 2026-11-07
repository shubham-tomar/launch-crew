package trigger_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/githubfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/r2fake"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
	"github.com/shubham-tomar/launchcrew/internal/webhook"
)

var prSuffixRe = regexp.MustCompile(`\s*\(#\d+\)\s*$`)

const rootSHA = "a000000000000000000000000000000000000001"

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// newFake serves acme/rocket with v1.1.0 and v1.2.0 released, unless first is set,
// in which case v1.2.0 is the only tag and release.
func newFake(t *testing.T, first bool) *githubfake.Server {
	t.Helper()
	var cmp struct {
		BaseCommit githubfake.Commit   `json:"base_commit"`
		Commits    []githubfake.Commit `json:"commits"`
	}
	readJSON(t, "compare.json", &cmp)
	f := githubfake.New("acme", "rocket")
	t.Cleanup(f.Close)
	readJSON(t, "prs.json", &f.PRs)
	f.Commits = append([]githubfake.Commit{githubfake.NewCommit(rootSHA, "Initial commit"), cmp.BaseCommit}, cmp.Commits...)
	head := cmp.Commits[len(cmp.Commits)-1].SHA
	f.Tags = []githubfake.Tag{{Name: "v1.2.0", SHA: head}}
	f.Releases = []githubfake.Release{{ID: 1001, TagName: "v1.2.0", Name: "Rocket 1.2"}}
	if !first {
		f.Tags = append(f.Tags, githubfake.Tag{Name: "v1.1.0", SHA: cmp.BaseCommit.SHA})
		f.Releases = append(f.Releases, githubfake.Release{ID: 1000, TagName: "v1.1.0", Name: "Rocket 1.1"})
	}
	return f
}

func ghClient(t *testing.T, f *githubfake.Server) *github.Client {
	t.Helper()
	c, err := github.New(1, githubfake.PrivateKeyPEM(), github.WithBaseURL(f.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// stubHeadlines picks #101 and #105 as headlines; everything else is a chore.
func stubHeadlines(_ context.Context, _ string, prs []drafting.PR) (h, c []drafting.Change, err error) {
	for _, p := range prs {
		ch := drafting.Change{PRNumber: p.Number, Title: p.Title, AuthorID: p.AuthorID, AuthorLogin: p.AuthorLogin}
		if p.Number == 101 || p.Number == 105 {
			h = append(h, ch)
		} else {
			c = append(c, ch)
		}
	}
	return h, c, nil
}

// releasePayload returns release.json with installation id and release flags changed.
func releasePayload(t *testing.T, installID int64, draft, prerelease bool) []byte {
	t.Helper()
	var m map[string]any
	readJSON(t, "release.json", &m)
	m["installation"].(map[string]any)["id"] = installID
	rel := m["release"].(map[string]any)
	rel["draft"], rel["prerelease"] = draft, prerelease
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFirstReleaseUsesFullHistory(t *testing.T) {
	f := newFake(t, true)
	tr := trigger.GitHubRelease{GH: ghClient(t, f), Headlines: stubHeadlines}
	spec, err := tr.Build(context.Background(), trigger.Input{ReleasePayload: releasePayload(t, 4242, false, false)})
	if err != nil {
		t.Fatal(err)
	}
	cmp := f.Requests("GET", "/compare/")
	if len(cmp) == 0 {
		t.Fatal("no compare call")
	}
	if want := "/repos/acme/rocket/compare/" + rootSHA + "...v1.2.0"; cmp[0].Path != want {
		t.Fatalf("compare %s, want %s", cmp[0].Path, want)
	}
	// The v1.1.0 base commit is part of the history too.
	if len(f.Requests("GET", "/commits/b000000000000000000000000000000000000110/pulls")) != 1 {
		t.Fatal("history before the last tag was not walked")
	}
	if spec.Title != "Rocket 1.2" || spec.ReleaseTag != "v1.2.0" || spec.RepoFullName != "acme/rocket" || spec.ReleaseID != 1001 {
		t.Fatalf("spec header: %+v", spec)
	}
	if len(spec.Headlines) != 2 || len(spec.Crew) != 4 { // alice, bob, carol, dave; bots dropped
		t.Fatalf("headlines=%d crew=%+v", len(spec.Headlines), spec.Crew)
	}
}

func TestOverTwentyTickedUsesRollCall(t *testing.T) {
	spec := trigger.LaunchSpec{Captain: "captain", RepoFullName: "acme/rocket", ReleaseTag: "v3.0.0"}
	links := map[int64]string{}
	for i := range 23 {
		id := int64(500 + i)
		spec.Crew = append(spec.Crew, trigger.CrewSuggestion{
			GitHubUserID: id, Login: fmt.Sprintf("dev%d", i), PRNumber: 1000 + i, PRTitle: "PR", Ticked: i < 21,
		})
		links[id] = fmt.Sprintf("https://l.example/s/%d", i)
	}
	plan := trigger.PlanComments(spec, links, "https://l.example/settings")
	if len(plan.PerPR) != 0 {
		t.Fatalf("%d per-PR comments, want 0", len(plan.PerPR))
	}
	if !strings.Contains(plan.RollCall, "captain launched rocket v3.0.0. Crew, your links:") {
		t.Fatalf("roll call intro:\n%s", plan.RollCall)
	}
	for i := range 23 {
		line := fmt.Sprintf("- @dev%d: https://l.example/s/%d\n", i, i)
		if got := strings.Contains(plan.RollCall, line); got != (i < 21) {
			t.Fatalf("dev%d in roll call = %v", i, got)
		}
	}

	spec.Crew = spec.Crew[:20]
	plan = trigger.PlanComments(spec, links, "https://l.example/settings")
	if plan.RollCall != "" || len(plan.PerPR) != 20 {
		t.Fatalf("20 ticked: rollcall=%q perPR=%d", plan.RollCall, len(plan.PerPR))
	}
	if !strings.Contains(plan.PerPR[1003], "https://l.example/s/3") {
		t.Fatalf("PR 1003 body: %s", plan.PerPR[1003])
	}
}

func TestManualBuildRejectsEmptyTitle(t *testing.T) {
	_, err := trigger.Manual{}.Build(context.Background(), trigger.Input{Manual: &trigger.ManualInput{Title: "  "}})
	if !errors.Is(err, trigger.ErrEmptyTitle) {
		t.Fatalf("err = %v, want ErrEmptyTitle", err)
	}
	spec, err := trigger.Manual{}.Build(context.Background(), trigger.Input{Manual: &trigger.ManualInput{Title: "Meetup", Summary: "Go night"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []trigger.Role{{"Attendee", "came along"}, {"Speaker", "spoke about"}, {"Organiser", "helped organise"}}
	if spec.Source != "manual" || spec.Title != "Meetup" || fmt.Sprint(spec.Roles) != fmt.Sprint(want) {
		t.Fatalf("spec: %+v", spec)
	}
	five := make([]trigger.Role, 5)
	for i := range five {
		five[i] = trigger.Role{Name: fmt.Sprint("r", i)}
	}
	if _, err := (trigger.Manual{}).Build(context.Background(), trigger.Input{Manual: &trigger.ManualInput{Title: "x", Roles: five}}); err == nil {
		t.Fatal("five roles accepted")
	}
}

func TestManualHeroMustBeImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hero.png":
			w.Header().Set("Content-Type", "image/png")
		case "/page":
			w.Header().Set("Content-Type", "text/html")
		case "/nohead.jpg": // answers images only to a ranged GET
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("Range") != "bytes=0-65535" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte{0xff, 0xd8})
		}
	}))
	defer srv.Close()
	m := trigger.Manual{HTTP: srv.Client()}
	build := func(hero string) (trigger.LaunchSpec, error) {
		return m.Build(context.Background(), trigger.Input{Manual: &trigger.ManualInput{Title: "Meetup", HeroURL: hero}})
	}
	if _, err := build(srv.URL + "/page"); !errors.Is(err, trigger.ErrHeroNotImage) {
		t.Fatalf("html hero: err = %v", err)
	}
	if _, err := build("ftp://example.com/a.png"); !errors.Is(err, trigger.ErrHeroNotImage) {
		t.Fatalf("ftp hero: err = %v", err)
	}
	for _, p := range []string{"/hero.png", "/nohead.jpg"} {
		spec, err := build(srv.URL + p)
		if err != nil || spec.HeroURL != srv.URL+p {
			t.Fatalf("%s: spec.HeroURL=%q err=%v", p, spec.HeroURL, err)
		}
	}
}

// --- release job (Postgres) ---

type env struct {
	pool    *pgxpool.Pool
	q       *sqlc.Queries
	gh      *githubfake.Server
	r2      *r2fake.Server
	handler jobs.Handler
	repo    sqlc.Repo
	install int64
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
	user, err := q.CreateUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	install := 7_000_000_000 + int64(uuid.New().ID())
	repo, err := q.UpsertRepo(ctx, sqlc.UpsertRepoParams{
		InstallationID: install, OwnerUserID: uuid.NullUUID{UUID: user.ID, Valid: true}, FullName: "acme/rocket",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM repos WHERE id = $1`, repo.ID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, user.ID) // cascades to launches
	})
	gh := newFake(t, false)
	r2 := r2fake.New("cards")
	t.Cleanup(r2.Close)
	e := &env{pool: pool, q: q, gh: gh, r2: r2, repo: repo, install: install}
	e.handler = trigger.ReleaseJob(trigger.ReleaseDeps{
		Pool:    pool,
		Trigger: trigger.GitHubRelease{GH: ghClient(t, gh), Headlines: stubHeadlines},
		Cards:   cards.NewUploader(r2.URL, "cards", "ak", "sk", "https://cdn.example"),
	})
	return e
}

// deliver stores a release delivery and runs the job for it.
func (e *env) deliver(t *testing.T, draft, prerelease bool) {
	t.Helper()
	ctx := context.Background()
	id := "test-" + uuid.NewString()
	if _, err := e.q.InsertWebhookDelivery(ctx, sqlc.InsertWebhookDeliveryParams{
		DeliveryID: id, Event: "release", Payload: releasePayload(t, e.install, draft, prerelease),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.q.DeleteWebhookDelivery(context.Background(), id) })
	payload, _ := json.Marshal(webhook.ProcessReleasePayload{DeliveryID: id})
	if err := e.handler(ctx, jobs.Job{Kind: jobs.ProcessRelease, Payload: payload}); err != nil {
		t.Fatalf("job: %v", err)
	}
}

func (e *env) launches(t *testing.T) []sqlc.Launch {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT id FROM launches WHERE repo_id = $1`, e.repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []sqlc.Launch
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		l, err := e.q.GetLaunch(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

func TestSkipsDraftRelease(t *testing.T) {
	e := setup(t)
	e.deliver(t, true, false)
	if n := len(e.launches(t)); n != 0 {
		t.Fatalf("%d launches for a draft release", n)
	}
	if n := len(e.gh.Requests("", "/repos/")); n != 0 {
		t.Fatalf("%d GitHub calls for a draft release", n)
	}
}

func TestSkipsPrereleaseUnlessAllowed(t *testing.T) {
	e := setup(t)
	e.deliver(t, false, true)
	if n := len(e.launches(t)); n != 0 {
		t.Fatalf("%d launches for a prerelease", n)
	}
	if _, err := e.q.UpdateRepoFlags(context.Background(), sqlc.UpdateRepoFlagsParams{ID: e.repo.ID, Enabled: true, AllowPrerelease: true}); err != nil {
		t.Fatal(err)
	}
	e.deliver(t, false, true)
	if n := len(e.launches(t)); n != 1 {
		t.Fatalf("%d launches with prereleases allowed, want 1", n)
	}
	// A disabled repo creates nothing, prerelease or not.
	if _, err := e.q.UpdateRepoFlags(context.Background(), sqlc.UpdateRepoFlagsParams{ID: e.repo.ID, Enabled: false, AllowPrerelease: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM launches WHERE repo_id = $1`, e.repo.ID); err != nil {
		t.Fatal(err)
	}
	e.deliver(t, false, false)
	if n := len(e.launches(t)); n != 0 {
		t.Fatalf("%d launches for a disabled repo", n)
	}
}

func TestDropsBotsAndOptedOut(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const carol = 91003
	if err := e.q.InsertOptOut(ctx, carol); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM opt_outs WHERE github_user_id = $1`, carol)
	})

	e.deliver(t, false, false)
	ls := e.launches(t)
	if len(ls) != 1 {
		t.Fatalf("%d launches, want 1", len(ls))
	}
	l := ls[0]
	if l.Status != "draft" || l.Source != "github" || l.Title != "Rocket 1.2" || l.ReleaseTag.String != "v1.2.0" ||
		l.ReleaseUrl.String != "https://github.com/acme/rocket/releases/tag/v1.2.0" || l.OwnerUserID != e.repo.OwnerUserID {
		t.Fatalf("launch: %+v", l)
	}
	if want := cards.PresetFor(l.ID); !l.CardPreset.Valid || cards.Presets[l.CardPreset.Int16] != want {
		t.Fatalf("card preset %v, want %s", l.CardPreset, want.Name)
	}
	// The captain hears about the draft.
	var notifyJobs int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind = 'notify_captain' AND launch_id = $1`, l.ID).Scan(&notifyJobs); err != nil || notifyJobs != 1 {
		t.Fatalf("notify jobs %d, %v", notifyJobs, err)
	}
	var changes []struct {
		PRNumber int  `json:"pr_number"`
		Hidden   bool `json:"hidden"`
	}
	if err := json.Unmarshal(l.HeadlineChanges, &changes); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 6 || changes[0].PRNumber != 101 || changes[1].PRNumber != 105 || changes[0].Hidden || !changes[2].Hidden {
		t.Fatalf("headline_changes: %s", l.HeadlineChanges)
	}
	if string(l.Roles) != "[]" {
		t.Fatalf("roles: %s", l.Roles)
	}
	var assetURL string
	if err := e.pool.QueryRow(ctx, `SELECT r2_url FROM launch_assets WHERE launch_id = $1 AND kind = 'release'`, l.ID).Scan(&assetURL); err != nil {
		t.Fatalf("release asset: %v", err)
	}
	if !strings.HasPrefix(assetURL, "https://cdn.example/cards/") || e.r2.Len() != 1 {
		t.Fatalf("asset %s, %d objects", assetURL, e.r2.Len())
	}

	crew, err := e.q.ListCrewByLaunch(ctx, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]sqlc.CrewMember{}
	for _, c := range crew {
		got[c.GithubUserID.Int64] = c
	}
	want := map[int64]struct {
		state string
		pr    int32
	}{91001: {"ticked", 101}, 91002: {"suggested", 102}, 91004: {"ticked", 105}}
	if len(got) != len(want) {
		t.Fatalf("crew %v, want alice, bob, dave", got)
	}
	for id, w := range want {
		c, ok := got[id]
		if !ok || c.State != w.state || c.PrNumber != (pgtype.Int4{Int32: w.pr, Valid: true}) || c.Role != "contributor" || c.PrTitle.String == "" {
			t.Fatalf("crew %d: %+v, want %+v", id, c, w)
		}
	}
}

func TestReleaseJobIgnoresDuplicateRelease(t *testing.T) {
	e := setup(t)
	e.deliver(t, false, false)
	e.deliver(t, false, false) // a second delivery for the same tag
	ls := e.launches(t)
	if len(ls) != 1 {
		t.Fatalf("%d launches, want 1", len(ls))
	}
	if ls[0].ReleaseID.Int64 != 1001 { // kept for the roll call
		t.Fatalf("release id %v, want 1001", ls[0].ReleaseID)
	}
	if n, _ := e.q.CountCrewByLaunch(context.Background(), ls[0].ID); n != 4 {
		t.Fatalf("%d crew, want 4", n)
	}
}

// soloEnv is setup with a renderer that records the card it was asked for.
func soloEnv(t *testing.T, f *githubfake.Server) (*env, *[]any) {
	t.Helper()
	e := setup(t)
	var drawn []any
	e.gh = f
	e.handler = trigger.ReleaseJob(trigger.ReleaseDeps{
		Pool:    e.pool,
		Trigger: trigger.GitHubRelease{GH: ghClient(t, f), Headlines: stubHeadlines},
		Cards:   cards.NewUploader(e.r2.URL, "cards", "ak", "sk", "https://cdn.example"),
		Render: func(c any, _ cards.Preset) ([]byte, error) {
			drawn = append(drawn, c)
			return []byte("\x89PNG"), nil
		},
	})
	return e, &drawn
}

func TestSoloReleaseGetsTheSoloCard(t *testing.T) {
	f := newFake(t, false)
	for i := range f.PRs {
		f.PRs[i].User = githubfake.User{ID: 910010, Login: "captain", Type: "User"}
	}
	f.Releases[1].PublishedAt = time.Now().Add(-10*24*time.Hour - time.Hour).UTC().Format(time.RFC3339)
	e, drawn := soloEnv(t, f)
	e.deliver(t, false, false)
	if len(*drawn) != 1 {
		t.Fatalf("%d cards drawn", len(*drawn))
	}
	c, ok := (*drawn)[0].(cards.SoloCard)
	if !ok {
		t.Fatalf("card %T, want SoloCard", (*drawn)[0])
	}
	if c.Handle != "captain" || c.PrevTag != "v1.1.0" || c.DaysSince != 10 || c.Ordinal != 2 || c.FirstRelease || c.Major ||
		c.Commits == 0 || c.Added == 0 || len(c.Release) == 0 || c.Release[0].Number == 0 {
		t.Fatalf("solo card %+v", c)
	}
	// Nobody else to invite: the captain's own row only, as before.
	if ls := e.launches(t); len(ls) != 1 {
		t.Fatalf("%d launches", len(ls))
	}
}

func TestReleaseWithOthersKeepsTheReleaseCard(t *testing.T) {
	e, drawn := soloEnv(t, newFake(t, false))
	e.deliver(t, false, false)
	if len(*drawn) != 1 {
		t.Fatalf("%d cards drawn", len(*drawn))
	}
	if _, ok := (*drawn)[0].(cards.ReleaseCard); !ok {
		t.Fatalf("card %T, want ReleaseCard", (*drawn)[0])
	}
}

// A first release pushed straight to main: no PRs, so the card lists commit subjects.
func TestSoloReleaseWithoutPRsListsCommits(t *testing.T) {
	f := newFake(t, true)
	f.PRs = nil
	for i := range f.Commits {
		f.Commits[i].Commit.Message = strings.TrimSpace(prSuffixRe.ReplaceAllString(f.Commits[i].Commit.Message, ""))
	}
	e, drawn := soloEnv(t, f)
	e.deliver(t, false, false)
	c, ok := (*drawn)[0].(cards.SoloCard)
	if !ok {
		t.Fatalf("card %T, want SoloCard", (*drawn)[0])
	}
	if !c.FirstRelease || c.DaysSince != -1 || len(c.Release) == 0 || c.Release[0].Number != 0 || c.Release[0].Title == "" {
		t.Fatalf("solo card %+v", c)
	}
}
