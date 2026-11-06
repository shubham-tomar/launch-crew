// Package comments writes Launch Crew's consent-first GitHub comments and
// posts them idempotently through the GitHub client.
package comments

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/github"
)

// ConsentSentence is the line every comment carries (design 5.7).
const ConsentSentence = "One link, your voice, your accounts. Nothing posts without your tap."

// maxTitleWords keeps Body under 60 words for any PR title.
const maxTitleWords = 20

// maxListed is how many PRs a comment names before "and N more"; each title
// is clipped shorter then, so the comment stays short.
const (
	maxListed      = 3
	maxListedWords = 8
)

// PR is one PR credited in a comment.
type PR struct {
	Number int
	Title  string
}

// DefaultTemplate is the per-person comment as shipped. A captain may
// replace it with their own wording; see Render.
const DefaultTemplate = "**@{captain}** launched **{project} {version}** and credited {credited}.\n\n" +
	"[Share your contribution]({link})\n\n" + ConsentSentence + "\n\nOpt out or delete your data: {settings}"

// Placeholders are the fields a template may use, in the order the editor lists them.
var Placeholders = []string{"{captain}", "{project}", "{version}", "{credited}", "{link}", "{settings}"}

// MaxTemplateLen bounds a captain's template, so a comment stays a comment.
const MaxTemplateLen = 1500

// Vars fill a template: the captain's login, the project and version, the
// credited PRs as a phrase ("your PR, *title*"), the invite link and the
// settings link.
type Vars struct {
	Captain, Project, Version, Credited, Link, Settings string
}

// Body is the per-person comment. It opens with the captain, not the bot.
// prs is newest first; one PR reads as a single title, several as a short list.
func Body(captainLogin, project, version string, prs []PR, link, settingsLink string) string {
	return Render(DefaultTemplate, Vars{Captain: captainLogin, Project: project, Version: version, Credited: credited(prs), Link: link, Settings: settingsLink})
}

// Credited phrases the PRs a comment credits, for a template's {credited}.
func Credited(prs []PR) string { return credited(prs) }

// Render fills a captain's template. Whatever the template says, the
// consent sentence and the opt-out line are kept: they are appended when
// the template leaves them out, so a comment never drops the person's way out.
func Render(tmpl string, v Vars) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultTemplate
	}
	body := strings.NewReplacer(
		"{captain}", v.Captain, "{project}", v.Project, "{version}", v.Version,
		"{credited}", v.Credited, "{link}", v.Link, "{settings}", v.Settings,
	).Replace(strings.TrimSpace(tmpl))
	if !strings.Contains(body, ConsentSentence) {
		body += "\n\n" + ConsentSentence
	}
	if v.Settings != "" && !strings.Contains(body, v.Settings) {
		body += "\n\nOpt out or delete your data: " + v.Settings
	}
	return body
}

// ValidateTemplate says what is wrong with a captain's template, or nil.
// An empty template means the default.
func ValidateTemplate(tmpl string) error {
	t := strings.TrimSpace(tmpl)
	switch {
	case t == "":
		return nil
	case len([]rune(t)) > MaxTemplateLen:
		return fmt.Errorf("keep the comment template under %d characters", MaxTemplateLen)
	case !strings.Contains(t, "{link}"):
		return fmt.Errorf("the comment template needs {link}, or the person has nothing to tap")
	}
	return nil
}

func credited(prs []PR) string {
	switch len(prs) {
	case 0:
		return "your work"
	case 1:
		return "your PR, *" + clip(prs[0].Title, maxTitleWords) + "*"
	}
	shown := prs
	if len(shown) > maxListed {
		shown = shown[:maxListed]
	}
	items := make([]string, len(shown))
	for i, p := range shown {
		items[i] = fmt.Sprintf("#%d %s", p.Number, clip(p.Title, maxListedWords))
	}
	list := strings.Join(items, ", ")
	if more := len(prs) - len(shown); more > 0 {
		list += fmt.Sprintf(" and %d more", more)
	}
	return fmt.Sprintf("your %d PRs: %s", len(prs), list)
}

func clip(title string, max int) string {
	w := strings.Fields(title)
	if len(w) <= max {
		return strings.Join(w, " ")
	}
	return strings.Join(w[:max], " ") + "…"
}

// Marker is the hidden tag that finds a launch's comment again. Callers that
// post append it on its own line.
func Marker(launchID uuid.UUID) string {
	return "<!-- launchcrew:" + launchID.String() + " -->"
}

// Entry is one crew member in a roll call.
type Entry struct {
	Login, Link string
}

// RollCallInput is everything a roll call needs.
type RollCallInput struct {
	Captain, Project, Version, SettingsLink string
	Entries                                 []Entry
}

// RollCall is the single comment used instead of per-PR comments for a large crew.
func RollCall(in RollCallInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s launched %s %s. Crew, your links:\n\n", in.Captain, in.Project, in.Version)
	for _, e := range in.Entries {
		fmt.Fprintf(&b, "- @%s: %s\n", e.Login, e.Link)
	}
	fmt.Fprintf(&b, "\n%s\n\nOpt out or delete your data: %s", ConsentSentence, in.SettingsLink)
	return b.String()
}

func withMarker(body, marker string) string {
	if strings.Contains(body, marker) {
		return body
	}
	return body + "\n" + marker
}

// Upsert edits the PR comment carrying marker, or posts a new one. The marker
// is appended to body on its own line.
func Upsert(ctx context.Context, gh *github.Client, installID int64, owner, repo string, pr int, marker, body string) (commentID int64, edited bool, err error) {
	body = withMarker(body, marker)
	id, found, err := gh.FindMarkedComment(ctx, installID, owner, repo, pr, marker)
	if err != nil {
		return 0, false, err
	}
	if found {
		return id, true, gh.EditComment(ctx, installID, owner, repo, id, body)
	}
	id, err = gh.CommentOnPR(ctx, installID, owner, repo, pr, body)
	return id, false, err
}

// PostRollCall puts the roll call under the release notes. When the release
// cannot be edited it upserts one comment on fallbackPR (the first ticked PR)
// instead, returning that comment's id.
func PostRollCall(ctx context.Context, gh *github.Client, installID int64, owner, repo string, releaseID int64, fallbackPR int, marker, body string) (onRelease bool, commentID int64, err error) {
	body = withMarker(body, marker)
	relErr := gh.CommentOnRelease(ctx, installID, owner, repo, releaseID, body)
	if relErr == nil {
		return true, 0, nil
	}
	id, _, err := Upsert(ctx, gh, installID, owner, repo, fallbackPR, marker, body)
	if err != nil {
		return false, 0, fmt.Errorf("%w; fallback comment: %w", relErr, err)
	}
	return false, id, nil
}
