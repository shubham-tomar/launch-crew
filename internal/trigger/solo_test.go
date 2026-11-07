package trigger

import (
	"testing"

	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/github"
)

func TestIsSolo(t *testing.T) {
	me := []drafting.PR{{AuthorLogin: "Ada", AuthorType: "User"}}
	bot := drafting.PR{AuthorLogin: "dependabot[bot]", AuthorType: "Bot"}
	cases := []struct {
		name    string
		author  string
		prs     []drafting.PR
		commits []github.ReleaseCommit
		want    bool
	}{
		{"own PRs, any case", "ada", me, nil, true},
		{"bots do not count", "ada", append(me, bot), nil, true},
		{"commits without an account are theirs", "ada", nil, []github.ReleaseCommit{{Subject: "x"}}, true},
		{"another PR author", "ada", append(me, drafting.PR{AuthorLogin: "bob", AuthorType: "User"}), nil, false},
		{"another commit author", "ada", me, []github.ReleaseCommit{{AuthorLogin: "bob", AuthorType: "User"}}, false},
		{"nothing in the release", "ada", nil, nil, false},
		{"a bot released it", "release-bot[bot]", me, nil, false},
	}
	for _, c := range cases {
		if got := isSolo(c.author, "User", c.prs, c.commits); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestSoloOfSkipsMergesAndCounts(t *testing.T) {
	commits := []github.ReleaseCommit{{Subject: "first"}, {Subject: "Merge branch 'x'"}, {Subject: "second"}}
	s := soloOf(nil, commits, github.ReleaseHistory{PrevTag: "v1", Ordinal: 3})
	if s.Commits != 3 || s.SubjectCount != 2 || len(s.Subjects) != 2 || s.Subjects[0] != "second" || s.Ordinal != 3 {
		t.Fatalf("%+v", s)
	}
	s = soloOf([]drafting.PR{{Added: 5, Deleted: 2}, {Added: 1}}, commits, github.ReleaseHistory{})
	if s.Added != 6 || s.Deleted != 2 || len(s.Subjects) != 0 {
		t.Fatalf("with PRs %+v", s)
	}
}

func TestIsMajor(t *testing.T) {
	for tag, want := range map[string]bool{"v1.0.0": true, "2.0": true, "v3": true, "v0.1.0": false, "v1.2.0": false, "v1.0.1": false, "latest": false, "v2.0.0-rc1": false} {
		if isMajor(tag) != want {
			t.Errorf("%s: got %v", tag, !want)
		}
	}
}
