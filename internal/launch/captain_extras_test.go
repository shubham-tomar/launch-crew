package launch_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/launch"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

func TestSkippedCaptainPostIsKeptAndNeverSent(t *testing.T) {
	e := setup(t)
	ds := captainDraftsByChannel(t, e)
	x, li := ds["ch-x"], ds["ch-li"]
	skip := func(id uuid.UUID, on bool) error {
		return launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, id, launch.DraftPatch{Skipped: ptr(on)})
	}

	// The character limit of the channel is enforced: a tweet is 280.
	long := strings.Repeat("a", 300)
	if err := launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, x.ID, launch.DraftPatch{Text: &long}); !errors.Is(err, launch.ErrInvalid) {
		t.Fatalf("300 characters on X: %v, want ErrInvalid", err)
	}
	if err := launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, x.ID, launch.DraftPatch{}); !errors.Is(err, launch.ErrInvalid) {
		t.Fatalf("empty patch: %v, want ErrInvalid", err)
	}

	if err := skip(x.ID, true); err != nil {
		t.Fatal(err)
	}
	ds = captainDraftsByChannel(t, e)
	if len(ds) != 2 || ds["ch-x"].Status != "skipped" {
		t.Fatalf("after skip: %+v", ds)
	}
	// A skipped post cannot be edited, and a change to the title that rebuilds unsent drafts leaves it alone.
	if err := launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, x.ID, launch.DraftPatch{Text: ptr("new words")}); !errors.Is(err, launch.ErrInvalid) {
		t.Fatalf("edit a skipped post: %v, want ErrInvalid", err)
	}
	if err := launch.Update(e.ctx, e.deps, e.launch.ID, launch.Patch{Title: ptr("Rocket 1.2!")}); err != nil {
		t.Fatal(err)
	}
	ds = captainDraftsByChannel(t, e)
	if len(ds) != 2 || ds["ch-x"].Status != "skipped" || ds["ch-li"].Status != "draft" || ds["ch-li"].ID == li.ID {
		t.Fatalf("after a title change: %+v (the LinkedIn draft should be rebuilt, the skipped X one kept)", ds)
	}
	// Preflight does not count or check the skipped post.
	if v, err := launch.Detail(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	} else {
		for _, c := range v.Checks {
			if !c.OK {
				t.Fatalf("red check with one post skipped: %+v", c)
			}
		}
	}

	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	if n := len(e.createInputs()); n != 1 {
		t.Fatalf("%d posts created, want only the LinkedIn one", n)
	}
	ds = captainDraftsByChannel(t, e)
	if ds["ch-x"].Status != "skipped" || ds["ch-x"].BufferPostID != "" || ds["ch-li"].Status != "queued" {
		t.Fatalf("after launch: %+v", ds)
	}
	if err := skip(ds["ch-li"].ID, true); !errors.Is(err, launch.ErrNotDraft) {
		t.Fatalf("skip after launch: %v, want ErrNotDraft", err)
	}
}

func TestSkipThenRestoreAndEditInOneStep(t *testing.T) {
	e := setup(t)
	x := captainDraftsByChannel(t, e)["ch-x"]
	patch := func(p launch.DraftPatch) error {
		return launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, x.ID, p)
	}
	if err := patch(launch.DraftPatch{Skipped: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	// Putting it back and rewording it in one call works.
	if err := patch(launch.DraftPatch{Skipped: ptr(false), Text: ptr("  Shipped, finally.  ")}); err != nil {
		t.Fatal(err)
	}
	if d := captainDraftsByChannel(t, e)["ch-x"]; d.Status != "draft" || d.Text != "Shipped, finally." {
		t.Fatalf("after restore: %+v", d)
	}
	// Skipping and rewording at once makes no sense.
	if err := patch(launch.DraftPatch{Skipped: ptr(true), Text: ptr("x")}); !errors.Is(err, launch.ErrInvalid) {
		t.Fatalf("skip with text: %v, want ErrInvalid", err)
	}
	if err := launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, uuid.New(), launch.DraftPatch{Skipped: ptr(true)}); !errors.Is(err, launch.ErrNotFound) {
		t.Fatalf("unknown post: %v, want ErrNotFound", err)
	}
}

func TestCaptainMaySkipEveryChannel(t *testing.T) {
	e := setup(t)
	for _, d := range captainDraftsByChannel(t, e) {
		if err := launch.PatchCaptainDraft(e.ctx, e.deps, e.launch.ID, d.ID, launch.DraftPatch{Skipped: ptr(true)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatalf("launch with every post skipped: %v", err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	if n := len(e.createInputs()); n != 0 {
		t.Fatalf("%d posts created, want none", n)
	}
	if a := e.member(t, e.alice.ID); a.State != "invited" {
		t.Fatalf("alice %s: the crew is still invited", a.State)
	}
}

func TestQRLogoChoice(t *testing.T) {
	e := setup(t)
	img := onePixelPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(img)
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		}
	}))
	defer srv.Close()
	e.deps.AllowLoopback = true // tests only: lets the happy path reach httptest
	before, _ := e.q.ListLaunchesByOwner(e.ctx, uuidNull(e.owner.ID))

	// A custom logo is fetched, stored as a launch asset and reported with its URL.
	l, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, trigger.ManualInput{Title: "Meetup", QRLogo: "custom", QRLogoURL: srv.URL + "/logo.png"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := launch.Detail(e.ctx, e.deps, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	asset, aerr := e.q.GetLatestLaunchAssetByKind(e.ctx, sqlc.GetLatestLaunchAssetByKindParams{LaunchID: l.ID, Kind: "qrlogo"})
	if aerr != nil || v.QRLogo != "custom" || v.QRLogoURL != asset.R2Url || !strings.HasPrefix(v.QRLogoURL, "https://cdn.example/cards/") {
		t.Fatalf("custom logo: detail %q %q, asset %+v (%v)", v.QRLogo, v.QRLogoURL, asset, aerr)
	}
	if got := crew.SlideLogo(e.ctx, crew.Deps{Q: e.q, Pool: e.pool}, l.ID); got.Mode != "custom" || got.URL != asset.R2Url {
		t.Fatalf("slide logo %+v", got)
	}

	// The default is the plane. It can be switched to none, back to the plane, and to a custom image later.
	l2, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, trigger.ManualInput{Title: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	expect := func(mode, url string) {
		t.Helper()
		v, err := launch.Detail(e.ctx, e.deps, l2.ID)
		if err != nil || v.QRLogo != mode || (url == "" && v.QRLogoURL != "") || (url != "" && v.QRLogoURL == "") {
			t.Fatalf("detail logo %q %q (%v), want %q (url %q)", v.QRLogo, v.QRLogoURL, err, mode, url)
		}
	}
	expect("plane", "")
	for _, step := range []struct{ mode, url string }{{"none", ""}, {"plane", ""}, {"custom", srv.URL + "/logo.png"}, {"none", ""}} {
		p := launch.Patch{QRLogo: ptr(step.mode)}
		if step.url != "" {
			p.QRLogoURL = ptr(step.url)
		}
		if err := launch.Update(e.ctx, e.deps, l2.ID, p); err != nil {
			t.Fatalf("patch %+v: %v", step, err)
		}
		expect(step.mode, step.url)
	}

	// Bad choices are refused, and a refused create leaves no launch behind.
	for _, p := range []launch.Patch{
		{QRLogo: ptr("sparkle")}, {QRLogo: ptr("custom")}, {QRLogo: ptr("custom"), QRLogoURL: ptr("ftp://x/y.png")},
	} {
		if err := launch.Update(e.ctx, e.deps, l2.ID, p); !errors.Is(err, launch.ErrInvalid) {
			t.Fatalf("patch %+v: %v, want ErrInvalid", p, err)
		}
	}
	if err := launch.Update(e.ctx, e.deps, l2.ID, launch.Patch{QRLogo: ptr("custom"), QRLogoURL: ptr(srv.URL + "/page")}); !errors.Is(err, launch.ErrLogoNotImage) {
		t.Fatalf("html as logo: %v, want ErrLogoNotImage", err)
	}
	n := len(before) + 2
	if _, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, trigger.ManualInput{Title: "Bad", QRLogo: "custom", QRLogoURL: srv.URL + "/page"}); !errors.Is(err, launch.ErrLogoNotImage) {
		t.Fatalf("create with html logo: %v, want ErrLogoNotImage", err)
	}
	if ls, _ := e.q.ListLaunchesByOwner(e.ctx, uuidNull(e.owner.ID)); len(ls) != n {
		t.Fatalf("%d launches after a refused logo, want %d", len(ls), n)
	}

	// Once launched the setting is fixed.
	if err := launch.Update(e.ctx, e.deps, e.launch.ID, launch.Patch{QRLogo: ptr("none")}); err != nil {
		t.Fatal(err)
	}
	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := launch.Update(e.ctx, e.deps, e.launch.ID, launch.Patch{QRLogo: ptr("plane")}); !errors.Is(err, launch.ErrNotDraft) {
		t.Fatalf("logo after launch: %v, want ErrNotDraft", err)
	}
}

func TestListSummaryCarriesRepoAndCrew(t *testing.T) {
	e := setup(t)
	find := func(id uuid.UUID) launch.Summary {
		t.Helper()
		ls, err := launch.List(e.ctx, e.deps, e.owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ls {
			if s.ID == id {
				return s
			}
		}
		t.Fatalf("launch %s not listed", id)
		return launch.Summary{}
	}
	setState := func(id uuid.UUID, state string) {
		t.Helper()
		if _, err := e.pool.Exec(e.ctx, `UPDATE crew_members SET state = $2 WHERE id = $1`, id, state); err != nil {
			t.Fatal(err)
		}
	}

	// Alice and Bob are ticked; Carol and the captain are suggested (not counted). Two headline PRs show, two are hidden.
	s := find(e.launch.ID)
	if s.Repo != "acme/rocket" || s.PRCount != 2 || s.CrewIn != 0 || s.CrewTotal != 2 || strings.Join(s.CrewInitials, ",") != "A,B" {
		t.Fatalf("summary %+v", s)
	}
	// Bob queues: he is in, and people who are in come first. The captain's own row is never crew, whatever its state.
	setState(e.bob.ID, "queued")
	if _, err := e.pool.Exec(e.ctx, `UPDATE crew_members SET state = 'invited' WHERE launch_id = $1 AND github_user_id = $2`, e.launch.ID, e.captain); err != nil {
		t.Fatal(err)
	}
	s = find(e.launch.ID)
	if s.CrewIn != 1 || s.CrewTotal != 2 || strings.Join(s.CrewInitials, ",") != "B,A" {
		t.Fatalf("after bob queued: %+v", s)
	}
	// The counts are the ones Detail shows.
	v, _ := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if v.Counts.In != s.CrewIn || v.Counts.Total != s.CrewTotal {
		t.Fatalf("detail counts %+v, summary %d of %d", v.Counts, s.CrewIn, s.CrewTotal)
	}
	if v.CaptainName != "captain" {
		t.Fatalf("captain name %q", v.CaptainName)
	}

	// A manual launch has no repo and no PRs, and an empty (not null) initials list.
	l, _, err := launch.CreateManual(e.ctx, e.deps, e.owner.ID, trigger.ManualInput{Title: "Meetup"})
	if err != nil {
		t.Fatal(err)
	}
	if m := find(l.ID); m.Repo != "" || m.PRCount != 0 || m.CrewTotal != 0 || m.CrewInitials == nil || len(m.CrewInitials) != 0 {
		t.Fatalf("manual summary %+v", m)
	}
}

func uuidNull(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: true} }

func TestRepoBrandingReachesTheContributorCard(t *testing.T) {
	e := setup(t)
	img := onePixelPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(img)
	}))
	defer srv.Close()
	e.deps.AllowLoopback = true

	str := func(s string) *string { return &s }
	// Bad and too-dark accents are refused with a reason; nothing is stored.
	for _, a := range []string{"blue", "#111111"} {
		if _, err := launch.UpdateRepo(e.ctx, e.deps, e.owner.ID, e.repo.ID, launch.RepoPatch{Accent: str(a)}); !errors.Is(err, launch.ErrInvalid) {
			t.Fatalf("accent %q: %v, want invalid", a, err)
		}
	}
	if _, err := launch.UpdateRepo(e.ctx, e.deps, e.owner.ID, e.repo.ID, launch.RepoPatch{LogoURL: str("ftp://x/y.png")}); !errors.Is(err, launch.ErrInvalid) {
		t.Fatalf("ftp logo: %v, want invalid", err)
	}
	// Someone else's repo is not found.
	if _, err := launch.UpdateRepo(e.ctx, e.deps, uuid.New(), e.repo.ID, launch.RepoPatch{Accent: str("#58D3AE")}); !errors.Is(err, launch.ErrNotFound) {
		t.Fatalf("stranger: %v", err)
	}

	r, err := launch.UpdateRepo(e.ctx, e.deps, e.owner.ID, e.repo.ID, launch.RepoPatch{LogoURL: str(srv.URL + "/logo.png"), Accent: str("#58d3ae")})
	if err != nil {
		t.Fatal(err)
	}
	if r.Accent != "#58D3AE" || r.LogoURL != srv.URL+"/logo.png" || !r.Enabled {
		t.Fatalf("repo after branding: %+v", r)
	}
	row, _ := e.q.GetRepo(e.ctx, e.repo.ID)
	if len(row.LogoPng) == 0 {
		t.Fatal("logo not stored")
	}

	if err := launch.Launch(e.ctx, e.deps, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.drain(t); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var got *cards.ContributorCard
	for _, c := range e.rendered {
		if cc, ok := c.(cards.ContributorCard); ok {
			got = &cc
			break
		}
	}
	if got == nil {
		t.Fatalf("no contributor card rendered: %v", e.rendered)
	}
	if got.Brand.Owner != "acme" || got.Brand.Accent != "#58D3AE" || got.Brand.Logo == nil || got.Brand.LogoURL != "" {
		t.Fatalf("brand %+v", got.Brand)
	}
	if got.Handle == "" || got.Name == "" || got.CrewCount == 0 || len(got.Release) == 0 || got.Date.IsZero() {
		t.Fatalf("card %+v", got)
	}

	// The launch page links the release card and each invited member's card.
	e.mu.Unlock()
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	e.mu.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.CardURL, "https://cdn.example/cards/") {
		t.Fatalf("release card url %q", v.CardURL)
	}
	withCard := 0
	for _, r := range v.Crew {
		if r.CardURL != "" {
			withCard++
		}
	}
	if withCard == 0 {
		t.Fatalf("no crew card urls: %+v", v.Crew)
	}

	// A preview draws unsaved branding and stores nothing; a bad accent is refused.
	e.rendered = nil
	e.mu.Unlock()
	png, err := launch.RepoCardPreview(e.ctx, e.deps, e.owner.ID, e.repo.ID, launch.RepoPatch{Accent: str("#D9A520")})
	_, badErr := launch.RepoCardPreview(e.ctx, e.deps, e.owner.ID, e.repo.ID, launch.RepoPatch{Accent: str("#000000")})
	_, strangerErr := launch.RepoCardPreview(e.ctx, e.deps, uuid.New(), e.repo.ID, launch.RepoPatch{})
	e.mu.Lock()
	if err != nil || len(png) == 0 {
		t.Fatalf("preview: %v", err)
	}
	if !errors.Is(badErr, launch.ErrInvalid) || !errors.Is(strangerErr, launch.ErrNotFound) {
		t.Fatalf("bad accent %v, stranger %v", badErr, strangerErr)
	}
	pc, ok := e.rendered[0].(cards.ContributorCard)
	if !ok || pc.Brand.Accent != "#D9A520" || pc.Brand.Logo == nil || pc.Project != "rocket" || len(pc.Release) == 0 || pc.Release[0].Title != "Faster boosters" {
		t.Fatalf("preview card %+v", e.rendered[0])
	}
	if row, _ := e.q.GetRepo(e.ctx, e.repo.ID); row.Accent != "#58D3AE" {
		t.Fatalf("preview saved the accent: %s", row.Accent)
	}

	// Background and text colours are stored upper-cased beside the accent;
	// text or an accent that cannot read on the background is refused.
	coloured, err := launch.UpdateRepo(e.ctx, e.deps, e.owner.ID, e.repo.ID, launch.RepoPatch{
		Background: str("#f4efe6"), Text: str("#4a45d1"), Accent: str("#B3326B"),
	})
	if err != nil || coloured.Background != "#F4EFE6" || coloured.Text != "#4A45D1" || coloured.Accent != "#B3326B" {
		t.Fatalf("colours: %+v %v", coloured, err)
	}
	for _, p := range []launch.RepoPatch{
		{Text: str("#FFFFFF")},
		{Accent: str("#ADA9FF")},
		{Background: str("blue")},
	} {
		if _, err := launch.UpdateRepo(e.ctx, e.deps, e.owner.ID, e.repo.ID, p); !errors.Is(err, launch.ErrInvalid) {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	if _, err := launch.UpdateRepo(e.ctx, e.deps, e.owner.ID, e.repo.ID, launch.RepoPatch{Background: str(""), Text: str(""), Accent: str("#58D3AE")}); err != nil {
		t.Fatal(err)
	}

	// Clearing the logo falls back to the owner's GitHub avatar.
	if _, err := launch.UpdateRepo(e.ctx, e.deps, e.owner.ID, e.repo.ID, launch.RepoPatch{LogoURL: str("")}); err != nil {
		t.Fatal(err)
	}
	row, _ = e.q.GetRepo(e.ctx, e.repo.ID)
	b := cards.RepoBrand(row.FullName, row.LogoPng, row.Accent, row.CardBg, row.CardText)
	if b.Logo != nil || b.LogoURL != "https://github.com/acme.png?size=208" {
		t.Fatalf("fallback brand %+v", b)
	}
}
