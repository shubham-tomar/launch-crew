package preflight

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shubham-tomar/launchcrew/internal/buffer"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

type fixture struct {
	deps Deps
	l    Launch
}

func good() fixture {
	return fixture{
		deps: Deps{
			Fetch: func(ctx context.Context, url string) (int, string, error) { return 200, "image/png", nil },
			Limits: func(ctx context.Context, id uuid.UUID) (map[string]buffer.Limit, error) {
				return map[string]buffer.Limit{"ch1": {}, "ch2": {}}, nil
			},
			Config: func(ctx context.Context, id uuid.UUID) (buffer.Config, error) {
				return buffer.Config{Services: map[string]buffer.ServiceRules{
					"twitter":  {MaxChars: 280, ImageOK: true},
					"linkedin": {MaxChars: 3000, ImageOK: true, FirstComment: true},
				}}, nil
			},
			RateHeadroom: func(ctx context.Context, id uuid.UUID) (int, error) { return 100, nil },
			Now:          func() time.Time { return now },
		},
		l: Launch{
			ID:            uuid.New(),
			ConnectionID:  uuid.New(),
			LaunchAt:      now.Add(2 * time.Hour),
			Assets:        []Asset{{URL: "https://cdn.example.com/cards/avatar.png", Label: "Lena"}},
			ExpectedCalls: 20,
			CaptainDrafts: []Draft{
				{ChannelID: "ch1", Name: "Acme X", Service: "twitter", Network: "twitter", Text: "Shipped v2 today.", HasAsset: true},
				{ChannelID: "ch2", Service: "linkedin", Network: "linkedin", Text: "Shipped v2.", FirstComment: "https://example.com", HasAsset: true},
			},
		},
	}
}

func failedIDs(cs []Check) []int {
	var ids []int
	for _, c := range cs {
		if !c.OK {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func mutations() map[int]func(*fixture) {
	return map[int]func(*fixture){
		1: func(f *fixture) {
			f.deps.Fetch = func(ctx context.Context, url string) (int, string, error) { return 404, "text/html", nil }
		},
		2: func(f *fixture) {
			f.deps.Limits = func(ctx context.Context, id uuid.UUID) (map[string]buffer.Limit, error) {
				return map[string]buffer.Limit{"ch1": {IsAtLimit: true}, "ch2": {}}, nil
			}
		},
		3: func(f *fixture) { f.l.CaptainDrafts[0].Text = strings.Repeat("a", 290) },
		4: func(f *fixture) { f.l.CaptainDrafts[1].Text = "Shipped v2 https://example.com/x" },
		6: func(f *fixture) { f.l.LaunchAt = now.Add(-time.Hour) },
		7: func(f *fixture) {
			f.deps.RateHeadroom = func(ctx context.Context, id uuid.UUID) (int, error) { return 25, nil }
		},
	}
}

func TestEachRuleFailsOnOwnFixture(t *testing.T) {
	ctx := context.Background()
	g := good()
	cs, ok := Run(ctx, g.deps, g.l)
	if !ok || len(failedIDs(cs)) != 0 || len(cs) != 7 {
		t.Fatalf("good fixture: ok=%v checks=%+v", ok, cs)
	}
	for i := 1; i <= 7; i++ {
		f := good()
		mut, has := mutations()[i]
		if has {
			mut(&f)
		}
		cs, ok := Run(ctx, f.deps, f.l)
		if len(cs) != 7 {
			t.Fatalf("check %d: got %d checks", i, len(cs))
		}
		for j, c := range cs {
			if c.ID != j+1 || c.Name == "" {
				t.Fatalf("check %d: bad check at %d: %+v", i, j, c)
			}
		}
		want := []int{i}
		if i == 5 {
			want = nil
		}
		got := failedIDs(cs)
		if len(got) != len(want) || (len(got) == 1 && got[0] != want[0]) || ok != (len(want) == 0) {
			t.Fatalf("fixture %d: failed %v, want %v", i, got, want)
		}
	}

	// Check 5 fails through ForCrewDraft.
	g = good()
	cd := CrewDraft{Draft: Draft{Network: "twitter", Text: "Shipped v2 today with a new queue."}, Threshold: 0.35}
	cs, ok = ForCrewDraft(ctx, g.deps, cd, []string{"Shipped v2 today with a new queue."})
	if ok || len(cs) != 4 || len(failedIDs(cs)) != 1 || failedIDs(cs)[0] != 5 {
		t.Fatalf("crew overlap: ok=%v checks=%+v", ok, cs)
	}
	if cs[3].Fix != "This draft reads like one already queued. Edit a line or two." {
		t.Fatalf("fix = %q", cs[3].Fix)
	}
	cs, ok = ForCrewDraft(ctx, g.deps, cd, []string{"Completely unrelated sentence about lunch."})
	if !ok {
		t.Fatalf("distinct draft should pass: %+v", cs)
	}
}

func TestFixNamesAnAction(t *testing.T) {
	ctx := context.Background()
	wantFix := map[int]string{
		1: "Card image for Lena could not be fetched (HTTP 404). Untick Lena, or create the launch again.",
		2: "Acme X has hit today's posting limit. Pick a launch time tomorrow or untick it.",
		3: "twitter post is 10 characters over 280. Shorten it.",
		4: "LinkedIn draft carries a link and an image together. Move the link to the first comment.",
		6: "Launch time is in the past.",
		7: "Buffer has 25 calls left this window; this launch needs about 20. Wait for the window to reset.",
	}
	for i, mut := range mutations() {
		f := good()
		mut(&f)
		cs, _ := Run(ctx, f.deps, f.l)
		c := cs[i-1]
		if c.OK || c.Fix == "" || !strings.HasSuffix(c.Fix, ".") {
			t.Fatalf("check %d: %+v", i, c)
		}
		if !strings.HasPrefix(c.Fix, wantFix[i]) {
			t.Fatalf("check %d fix = %q, want prefix %q", i, c.Fix, wantFix[i])
		}
	}
	f := good()
	f.l.LaunchAt = now.Add(31 * 24 * time.Hour)
	cs, _ := Run(ctx, f.deps, f.l)
	if cs[5].Fix != "Launch time is more than 30 days away. Pick a closer time." {
		t.Fatalf("fix = %q", cs[5].Fix)
	}
	for _, c := range cs {
		if c.OK && c.Fix != "" {
			t.Fatalf("OK check has fix: %+v", c)
		}
	}
}

// Buffer's "today" follows the channel's timezone, not UTC, so the limit is
// checked for any launch within 36 hours and skipped for later ones.
func TestLimitAppliesWithinThirtySixHours(t *testing.T) {
	ctx := context.Background()
	atLimit := func(f *fixture) {
		f.deps.Limits = func(ctx context.Context, id uuid.UUID) (map[string]buffer.Limit, error) {
			return map[string]buffer.Limit{"ch1": {IsAtLimit: true}}, nil
		}
	}
	for _, tc := range []struct {
		lead time.Duration
		fail bool
	}{
		{2 * time.Hour, true},
		{30 * time.Hour, true},  // tomorrow in UTC, still today somewhere west
		{48 * time.Hour, false}, // a later day everywhere
	} {
		f := good()
		atLimit(&f)
		f.l.LaunchAt = now.Add(tc.lead)
		cs, _ := Run(ctx, f.deps, f.l)
		if cs[1].OK == tc.fail {
			t.Fatalf("launch in %v at limit: %+v", tc.lead, cs[1])
		}
	}
	f := good()
	cs, _ := Run(ctx, f.deps, f.l)
	if !cs[1].OK {
		t.Fatalf("same day under limit should pass")
	}
}

func TestEdgesPass(t *testing.T) {
	ctx := context.Background()
	f := good()
	f.l.LaunchAt = now.Add(30 * 24 * time.Hour)
	f.deps.RateHeadroom = func(ctx context.Context, id uuid.UUID) (int, error) { return f.l.ExpectedCalls + 10, nil }
	f.l.CaptainDrafts[1].Text = "Shipped https://example.com/x"
	f.l.CaptainDrafts[1].HasAsset = false
	if cs, ok := Run(ctx, f.deps, f.l); !ok {
		t.Fatalf("edges should pass: %+v", cs)
	}
	f = good()
	f.l.CaptainDrafts[1].FirstComment = "https://example.com/x"
	if cs, ok := Run(ctx, f.deps, f.l); !ok {
		t.Fatalf("first-comment link should pass: %+v", cs)
	}
	f.deps.Now = nil
	f.l.LaunchAt = time.Now().Add(time.Hour)
	f.deps.RateHeadroom = func(ctx context.Context, id uuid.UUID) (int, error) { return 100, nil }
	if cs, _ := Run(ctx, f.deps, f.l); !cs[5].OK {
		t.Fatalf("nil Now should default: %+v", cs[5])
	}
}

func TestCrewDraftPassesConnectionID(t *testing.T) {
	g := good()
	var seen uuid.UUID
	g.deps.Config = func(ctx context.Context, id uuid.UUID) (buffer.Config, error) {
		seen = id
		return buffer.Config{}, nil
	}
	conn := uuid.New()
	cd := CrewDraft{Draft: Draft{Network: "twitter", Text: "hi"}, ConnectionID: conn}
	ForCrewDraft(context.Background(), g.deps, cd, nil)
	if seen != conn {
		t.Fatalf("config called with %v, want %v", seen, conn)
	}
}

func TestRulesInstagramNeedsAnImage(t *testing.T) {
	f := good()
	f.l.CaptainDrafts = []Draft{{ChannelID: "ig", Service: "instagram", Network: "instagram", Text: "Shipped.", HasAsset: false}}
	cs, _ := Run(context.Background(), f.deps, f.l)
	var rules Check
	for _, c := range cs {
		if c.ID == 3 {
			rules = c
		}
	}
	if rules.OK || rules.Fix != "Instagram post has no image. Turn the card on or add an image." {
		t.Errorf("check 3 = %+v, want red for an Instagram post without an image", rules)
	}
	f.l.CaptainDrafts[0].HasAsset = true
	cs, _ = Run(context.Background(), f.deps, f.l)
	for _, c := range cs {
		if c.ID == 3 && !c.OK {
			t.Errorf("check 3 red with an image: %s", c.Fix)
		}
	}
}

// Every image a crew post carries is fetched, not just the first: an
// expired later one fails check 1 before anything is sent.
func TestForCrewDraftChecksEveryImage(t *testing.T) {
	g := good()
	g.deps.Fetch = func(ctx context.Context, url string) (int, string, error) {
		if url == "https://cdn.example/uploads/second.png" {
			return 404, "", nil
		}
		return 200, "image/png", nil
	}
	cd := CrewDraft{
		Draft:     Draft{Network: "twitter", Text: "hi", HasAsset: true},
		AssetURLs: []string{"https://cdn.example/cards/card.png", "https://cdn.example/uploads/second.png"},
	}
	cs, ok := ForCrewDraft(context.Background(), g.deps, cd, nil)
	if ok || len(failedIDs(cs)) != 1 || failedIDs(cs)[0] != 1 {
		t.Fatalf("ok=%v failed=%v, want check 1 to fail", ok, failedIDs(cs))
	}
	if !strings.Contains(cs[0].Fix, "second.png") || !strings.Contains(cs[0].Fix, "HTTP 404") {
		t.Errorf("fix = %q", cs[0].Fix)
	}
}

// A story or a swipeable LinkedIn post with nothing to show is red, with a
// fix that names the way out.
func TestRulesStoriesAndDocumentsNeedAnImage(t *testing.T) {
	for _, c := range []struct {
		d   Draft
		fix string
	}{
		{Draft{ChannelID: "fb", Service: "facebook", Network: "facebook", Text: "Shipped.", Format: "story"},
			"A facebook story needs an image. Turn the card on, add an image, or post it normally."},
		{Draft{ChannelID: "li", Service: "linkedin", Network: "linkedin", Text: "Shipped.", Format: "document"},
			"A swipeable LinkedIn post needs an image. Turn the card on, add an image, or turn Swipeable off."},
	} {
		f := good()
		f.l.CaptainDrafts = []Draft{c.d}
		cs, _ := Run(context.Background(), f.deps, f.l)
		for _, ch := range cs {
			if ch.ID == 3 && (ch.OK || ch.Fix != c.fix) {
				t.Errorf("%s: check 3 = %+v, want %q", c.d.Network, ch, c.fix)
			}
		}
		f.l.CaptainDrafts[0].HasAsset = true
		cs, _ = Run(context.Background(), f.deps, f.l)
		for _, ch := range cs {
			if ch.ID == 3 && !ch.OK {
				t.Errorf("%s with an image: check 3 red: %s", c.d.Network, ch.Fix)
			}
		}
	}
}
