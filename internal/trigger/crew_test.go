package trigger

import (
	"testing"
	"time"

	"github.com/shubham-tomar/launchcrew/internal/drafting"
)

func pr(n int, author int64, login string, merged time.Time) drafting.PR {
	return drafting.PR{Number: n, Title: "PR " + login + " " + string(rune('A'+n%26)), AuthorID: author, AuthorLogin: login, AuthorType: "User", MergedAt: merged}
}

func TestSuggestCrewManyPRsOnePerson(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	prs := []drafting.PR{
		pr(12, 1, "ada", t0),
		pr(18, 1, "ada", t0.Add(2*time.Hour)),
		pr(15, 1, "ada", t0.Add(time.Hour)),
		pr(20, 2, "bob", t0),
	}
	heads := []drafting.Change{{PRNumber: 12}} // ada's headline is her oldest PR
	got := suggestCrew(prs, heads)
	if len(got) != 2 {
		t.Fatalf("crew = %d, want 2", len(got))
	}
	ada := got[0]
	if ada.Login != "ada" || ada.PRNumber != 18 || !ada.Ticked {
		t.Fatalf("ada = %+v, want comment on #18 and ticked", ada)
	}
	var nums []int
	for _, r := range ada.PRs {
		nums = append(nums, r.Number)
	}
	if len(nums) != 3 || nums[0] != 18 || nums[1] != 15 || nums[2] != 12 {
		t.Fatalf("ada PRs = %v, want [18 15 12]", nums)
	}
	if ada.PRTitle != ada.PRs[0].Title {
		t.Fatalf("PRTitle %q is not the newest PR's title", ada.PRTitle)
	}
	if bob := got[1]; bob.Ticked || bob.PRNumber != 20 || len(bob.PRs) != 1 {
		t.Fatalf("bob = %+v", bob)
	}
}

func TestSuggestCrewTieBrokenByHigherNumber(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	got := suggestCrew([]drafting.PR{pr(5, 1, "ada", t0), pr(9, 1, "ada", t0), pr(7, 1, "ada", t0)}, nil)
	if got[0].PRNumber != 9 || got[0].Ticked {
		t.Fatalf("got %+v, want #9 and not ticked", got[0])
	}
}

func TestSuggestCrewSkipsBotsAndKeepsSinglePR(t *testing.T) {
	t0 := time.Now()
	bot := pr(3, 7, "dependabot[bot]", t0)
	bot.AuthorType = "Bot"
	got := suggestCrew([]drafting.PR{bot, pr(4, 1, "ada", t0), pr(5, 1, "ada", t0.Add(-time.Hour))}[:2], []drafting.Change{{PRNumber: 4}})
	if len(got) != 1 || got[0].Login != "ada" || got[0].PRNumber != 4 || !got[0].Ticked || len(got[0].PRs) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestSuggestCrewKeepsLineCounts(t *testing.T) {
	p := pr(7, 1, "ada", time.Now())
	p.Added, p.Deleted = 412, 96
	got := suggestCrew([]drafting.PR{p}, nil)
	if len(got) != 1 || got[0].PRs[0].Added != 412 || got[0].PRs[0].Deleted != 96 {
		t.Fatalf("crew = %+v, want the PR's +412 -96 kept", got)
	}
}
