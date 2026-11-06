package comments_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/github/comments"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/githubfake"
)

const install = 4242

func client(t *testing.T, f *githubfake.Server) *github.Client {
	t.Helper()
	c, err := github.New(1, githubfake.PrivateKeyPEM(), github.WithBaseURL(f.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var mdLink = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)

// words counts words with markdown markers stripped; a link counts its text and URL.
func words(s string) int {
	s = mdLink.ReplaceAllString(s, "$1 $2")
	s = strings.NewReplacer("**", "", "*", "").Replace(s)
	return len(strings.Fields(s))
}

func TestCommentBodyUnder60WordsWithConsentSentence(t *testing.T) {
	got := comments.Body("captain", "rocket", "v2.4.0", []comments.PR{{Number: 42, Title: "Split-pane resize with keyboard"}},
		"https://launchcrew.example/s/abc", "https://launchcrew.example/settings")
	want := "**@captain** launched **rocket v2.4.0** and credited your PR, *Split-pane resize with keyboard*.\n\n" +
		"[Share your contribution](https://launchcrew.example/s/abc)\n\n" +
		"One link, your voice, your accounts. Nothing posts without your tap.\n\n" +
		"Opt out or delete your data: https://launchcrew.example/settings"
	if got != want {
		t.Fatalf("body:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(got, comments.ConsentSentence) {
		t.Fatal("consent sentence missing")
	}
	if n := words(got); n >= 60 {
		t.Fatalf("%d words, want < 60", n)
	}
	long := comments.Body("captain", "rocket", "v2.4.0", []comments.PR{{Number: 1, Title: strings.Repeat("very ", 80) + "long title"}},
		"https://l.example/s/x", "https://l.example/settings")
	if n := words(long); n >= 60 {
		t.Fatalf("long title: %d words, want < 60", n)
	}
	if !strings.Contains(long, comments.ConsentSentence) {
		t.Fatal("long title: consent sentence missing")
	}
}

func TestCommentBodyManyPRs(t *testing.T) {
	prs := []comments.PR{
		{Number: 18, Title: "Add queue view"}, {Number: 15, Title: "Fix retry loop"}, {Number: 12, Title: "Speed up cards"},
		{Number: 9, Title: "Docs"}, {Number: 7, Title: strings.Repeat("long ", 40)},
	}
	for _, c := range []struct {
		n    int
		want string
	}{
		{3, "credited your 3 PRs: #18 Add queue view, #15 Fix retry loop, #12 Speed up cards."},
		{5, "credited your 5 PRs: #18 Add queue view, #15 Fix retry loop, #12 Speed up cards and 2 more."},
	} {
		got := comments.Body("captain", "rocket", "v2.4.0", prs[:c.n], "https://l.example/s/x", "https://l.example/settings")
		wantHead := "**@captain** launched **rocket v2.4.0** and " + c.want + "\n\n[Share your contribution](https://l.example/s/x)\n\n"
		if !strings.HasPrefix(got, wantHead) {
			t.Errorf("%d PRs:\n%s\nwant prefix:\n%s", c.n, got, wantHead)
		}
		if !strings.Contains(got, comments.ConsentSentence) || words(got) >= 70 {
			t.Errorf("%d PRs: consent missing or %d words", c.n, words(got))
		}
	}
	long := comments.Body("captain", "rocket", "v2.4.0", []comments.PR{prs[4], prs[4], prs[4]}, "https://l.example/s/x", "https://l.example/settings")
	if n := words(long); n >= 70 {
		t.Errorf("long titles: %d words", n)
	}
}

func TestSecondReleaseEditsExistingComment(t *testing.T) {
	f := githubfake.New("acme", "rocket")
	defer f.Close()
	launch := uuid.New()
	marker := comments.Marker(launch)
	if marker != "<!-- launchcrew:"+launch.String()+" -->" {
		t.Fatalf("marker %q", marker)
	}
	// 120 unrelated comments push the marked one onto the second page.
	f.Lock()
	for i := range 120 {
		f.Comments[101] = append(f.Comments[101], githubfake.Comment{ID: int64(i + 1), Body: fmt.Sprintf("lgtm %d", i)})
	}
	f.Comments[101] = append(f.Comments[101], githubfake.Comment{ID: 777, Body: "old body\n" + marker})
	f.Unlock()
	gh := client(t, f)
	ctx := context.Background()

	id, edited, err := comments.Upsert(ctx, gh, install, "acme", "rocket", 101, marker, "new body")
	if err != nil {
		t.Fatal(err)
	}
	if !edited || id != 777 {
		t.Fatalf("id=%d edited=%v, want 777 true", id, edited)
	}
	if n := len(f.Requests("POST", "/issues/101/comments")); n != 0 {
		t.Fatalf("%d CommentOnPR calls, want 0", n)
	}
	patches := f.Requests("PATCH", "/issues/comments/777")
	if len(patches) != 1 {
		t.Fatalf("%d edits, want 1", len(patches))
	}
	var sent struct{ Body string }
	_ = json.Unmarshal(patches[0].Body, &sent)
	if sent.Body != "new body\n"+marker {
		t.Fatalf("edited body %q", sent.Body)
	}

	// A PR with no marked comment gets a new one.
	id, edited, err = comments.Upsert(ctx, gh, install, "acme", "rocket", 102, marker, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if edited || id == 0 {
		t.Fatalf("id=%d edited=%v, want new comment", id, edited)
	}
	cs := f.IssueComments(102)
	if len(cs) != 1 || cs[0].Body != "hello\n"+marker {
		t.Fatalf("comments on 102: %+v", cs)
	}
}

func TestRollCallFallsBackToPRComment(t *testing.T) {
	f := githubfake.New("acme", "rocket")
	defer f.Close()
	f.Releases = []githubfake.Release{{ID: 1001, TagName: "v1.2.0", Body: "notes"}}
	gh := client(t, f)
	ctx := context.Background()
	marker := comments.Marker(uuid.New())
	body := comments.RollCall(comments.RollCallInput{
		Captain: "captain", Project: "rocket", Version: "v1.2.0", SettingsLink: "https://l.example/settings",
		Entries: []comments.Entry{{Login: "alice", Link: "https://l.example/s/a"}},
	})
	if !strings.Contains(body, "- @alice: https://l.example/s/a") || !strings.Contains(body, comments.ConsentSentence) {
		t.Fatalf("roll call:\n%s", body)
	}

	onRelease, _, err := comments.PostRollCall(ctx, gh, install, "acme", "rocket", 1001, 101, marker, body)
	if err != nil || !onRelease {
		t.Fatalf("onRelease=%v err=%v", onRelease, err)
	}
	// A rerun replaces the section rather than appending a second one.
	if _, _, err := comments.PostRollCall(ctx, gh, install, "acme", "rocket", 1001, 101, marker, body); err != nil {
		t.Fatal(err)
	}
	rel, _ := f.Release(1001)
	if !strings.HasPrefix(rel.Body, "notes") || strings.Count(rel.Body, "@alice") != 1 {
		t.Fatalf("release body:\n%s", rel.Body)
	}

	f.Lock()
	f.FailReleaseEdit = true
	f.Unlock()
	onRelease, id, err := comments.PostRollCall(ctx, gh, install, "acme", "rocket", 1001, 101, marker, body)
	if err != nil || onRelease || id == 0 {
		t.Fatalf("fallback: onRelease=%v id=%d err=%v", onRelease, id, err)
	}
	if cs := f.IssueComments(101); len(cs) != 1 || !strings.Contains(cs[0].Body, marker) {
		t.Fatalf("fallback comments: %+v", cs)
	}
}

func TestRenderTemplateKeepsConsentAndOptOut(t *testing.T) {
	v := comments.Vars{Captain: "captain", Project: "rocket", Version: "v2.4.0",
		Credited: comments.Credited([]comments.PR{{Number: 42, Title: "Split-pane resize"}}),
		Link:     "https://l.example/s/abc", Settings: "https://l.example/settings"}
	// The default template renders exactly as Body does.
	if got, want := comments.Render("", v), comments.Body("captain", "rocket", "v2.4.0", []comments.PR{{Number: 42, Title: "Split-pane resize"}}, v.Link, v.Settings); got != want {
		t.Fatalf("default render:\n%s\nwant:\n%s", got, want)
	}
	// A captain's own wording keeps the consent sentence and the opt-out line.
	got := comments.Render("Hey! {captain} just cut {project} {version}, thanks for {credited}. Tap here: {link}", v)
	want := "Hey! captain just cut rocket v2.4.0, thanks for your PR, *Split-pane resize*. Tap here: https://l.example/s/abc\n\n" +
		comments.ConsentSentence + "\n\nOpt out or delete your data: https://l.example/settings"
	if got != want {
		t.Fatalf("custom render:\n%s\nwant:\n%s", got, want)
	}
	// A template that already carries them is not doubled.
	again := comments.Render(got, v)
	if strings.Count(again, comments.ConsentSentence) != 1 || strings.Count(again, v.Settings) != 1 {
		t.Fatalf("footer doubled:\n%s", again)
	}
}

func TestValidateTemplate(t *testing.T) {
	if err := comments.ValidateTemplate(""); err != nil {
		t.Fatalf("empty means default: %v", err)
	}
	if err := comments.ValidateTemplate("Thanks for your PR, tap {link}"); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := comments.ValidateTemplate("Thanks for your PR"); err == nil || !strings.Contains(err.Error(), "{link}") {
		t.Fatalf("missing link: %v", err)
	}
	if err := comments.ValidateTemplate(strings.Repeat("x", comments.MaxTemplateLen+1) + "{link}"); err == nil {
		t.Fatal("too long accepted")
	}
}
