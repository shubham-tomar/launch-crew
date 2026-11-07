package github

import (
	"context"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"
)

// CommentOnPR posts body as a new comment on PR number.
func (c *Client) CommentOnPR(ctx context.Context, installID int64, owner, repo string, number int, body string) (int64, error) {
	cl, err := c.installation(installID)
	if err != nil {
		return 0, err
	}
	cm, _, err := cl.Issues.CreateComment(ctx, owner, repo, number, gh.IssueCommentRequest{Body: body})
	if err != nil {
		return 0, fmt.Errorf("comment on #%d: %w", number, err)
	}
	return cm.GetID(), nil
}

// FindMarkedComment returns the first comment on PR number containing marker.
func (c *Client) FindMarkedComment(ctx context.Context, installID int64, owner, repo string, number int, marker string) (int64, bool, error) {
	cl, err := c.installation(installID)
	if err != nil {
		return 0, false, err
	}
	opts := &gh.IssueListCommentsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		page, resp, err := cl.Issues.ListComments(ctx, owner, repo, number, opts)
		if err != nil {
			return 0, false, fmt.Errorf("list comments on #%d: %w", number, err)
		}
		for _, cm := range page {
			if strings.Contains(cm.GetBody(), marker) {
				return cm.GetID(), true, nil
			}
		}
		if resp.NextPage == 0 {
			return 0, false, nil
		}
		opts.Page = resp.NextPage
	}
}

// EditComment replaces the body of issue comment id. Edits send no notification.
func (c *Client) EditComment(ctx context.Context, installID int64, owner, repo string, id int64, body string) error {
	cl, err := c.installation(installID)
	if err != nil {
		return err
	}
	if _, _, err := cl.Issues.UpdateComment(ctx, owner, repo, id, gh.IssueCommentRequest{Body: body}); err != nil {
		return fmt.Errorf("edit comment %d: %w", id, err)
	}
	return nil
}

// ReleaseSection opens the Launch Crew section of a release body.
const ReleaseSection = "<!-- launchcrew:roll-call -->"

// CommentOnRelease puts body under the release notes. GitHub has no release
// comments, so the release body is edited: anything from an earlier
// ReleaseSection on is replaced, the rest is kept. Callers fall back to a PR
// comment when this fails (see comments.PostRollCall).
func (c *Client) CommentOnRelease(ctx context.Context, installID int64, owner, repo string, releaseID int64, body string) error {
	cl, err := c.installation(installID)
	if err != nil {
		return err
	}
	rel, _, err := cl.Repositories.GetRelease(ctx, owner, repo, releaseID)
	if err != nil {
		return fmt.Errorf("get release %d: %w", releaseID, err)
	}
	// The roll call replaces the "draft is ready" note: the launch is out.
	notes, _, _ := strings.Cut(WithDraftNote(rel.GetBody(), ""), ReleaseSection)
	notes = strings.TrimRight(notes, "\n")
	if notes != "" {
		notes += "\n\n"
	}
	updated := notes + ReleaseSection + "\n" + body
	if _, _, err := cl.Repositories.UpdateRelease(ctx, owner, repo, releaseID, gh.UpdateReleaseRequest{Body: &updated}); err != nil {
		return fmt.Errorf("edit release %d: %w", releaseID, err)
	}
	return nil
}

// Draft note markers fence the line that tells the captain a launch draft is
// waiting. The roll call removes it.
const (
	DraftNoteStart = "<!-- launchcrew:draft -->"
	DraftNoteEnd   = "<!-- /launchcrew:draft -->"
)

// WithDraftNote returns body with its draft note replaced by note, or
// removed when note is empty. A new note goes at the end, above any roll call.
func WithDraftNote(body, note string) string {
	if i := strings.Index(body, DraftNoteStart); i >= 0 {
		rest := body[i:]
		if j := strings.Index(rest, DraftNoteEnd); j >= 0 {
			body = strings.TrimRight(body[:i], "\n") + rest[j+len(DraftNoteEnd):]
		}
	}
	if note == "" {
		return body
	}
	block := DraftNoteStart + "\n" + note + "\n" + DraftNoteEnd
	notes, roll, hasRoll := strings.Cut(body, ReleaseSection)
	notes = strings.TrimRight(notes, "\n")
	if notes != "" {
		notes += "\n\n"
	}
	out := notes + block
	if hasRoll {
		out += "\n\n" + ReleaseSection + roll
	}
	return out
}

// SetReleaseDraftNote writes note into the release body between the draft
// note markers, keeping everything else.
func (c *Client) SetReleaseDraftNote(ctx context.Context, installID int64, owner, repo string, releaseID int64, note string) error {
	cl, err := c.installation(installID)
	if err != nil {
		return err
	}
	rel, _, err := cl.Repositories.GetRelease(ctx, owner, repo, releaseID)
	if err != nil {
		return fmt.Errorf("get release %d: %w", releaseID, err)
	}
	updated := WithDraftNote(rel.GetBody(), note)
	if updated == rel.GetBody() {
		return nil
	}
	if _, _, err := cl.Repositories.UpdateRelease(ctx, owner, repo, releaseID, gh.UpdateReleaseRequest{Body: &updated}); err != nil {
		return fmt.Errorf("edit release %d: %w", releaseID, err)
	}
	return nil
}
