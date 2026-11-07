package github

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/shubham-tomar/launchcrew/internal/drafting"
)

const (
	maxCommits = 500 // newest commits walked per release
	maxPages   = 10
)

// prSuffix matches the "(#123)" GitHub appends to squash-merge subjects.
var prSuffix = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// PRsBetween lists the merged PRs whose commits are in base...head, oldest first.
//
// A commit whose subject ends in "(#N)" (squash and merge-commit subjects)
// names its PR without a request. Only the rest (rebase merges, hand-written
// subjects) are mapped with the "pulls associated with a commit" endpoint,
// which keeps a 500-commit release to a handful of calls instead of one per
// commit. Each PR is then fetched once; a number that is not a merged PR
// (an issue, another repository's PR) is skipped. An empty base means a
// first release: the compare runs from the repository's root commit, so the
// whole history up to head counts (newest maxCommits).
func (c *Client) PRsBetween(ctx context.Context, installID int64, owner, repo, base, head string) ([]drafting.PR, error) {
	prs, _, err := c.ReleaseChanges(ctx, installID, owner, repo, base, head)
	return prs, err
}

// ReleaseCommit is one commit in a release: its subject line and author.
type ReleaseCommit struct {
	SHA, Subject, AuthorLogin, AuthorType string
}

// ReleaseChanges is PRsBetween plus the commits it walked, newest last.
func (c *Client) ReleaseChanges(ctx context.Context, installID int64, owner, repo, base, head string) ([]drafting.PR, []ReleaseCommit, error) {
	cl, err := c.installation(installID)
	if err != nil {
		return nil, nil, err
	}
	var shas []commitRef
	if base == "" {
		root, err := rootCommit(ctx, cl, owner, repo, head)
		if err != nil {
			return nil, nil, err
		}
		shas = append(shas, root)
		base = root.sha
	}
	between, err := compare(ctx, cl, owner, repo, base, head)
	if err != nil {
		return nil, nil, err
	}
	shas = append(shas, between...)
	if len(shas) > maxCommits {
		shas = shas[len(shas)-maxCommits:]
	}

	var numbers []int
	seen := map[int]bool{}
	add := func(n int) {
		if !seen[n] {
			seen[n] = true
			numbers = append(numbers, n)
		}
	}
	for _, cm := range shas {
		if n, ok := subjectPR(cm.message); ok {
			add(n)
			continue
		}
		prs, _, err := cl.PullRequests.ListPullRequestsWithCommit(ctx, owner, repo, cm.sha, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("pulls for %s: %w", cm.sha, err)
		}
		for _, p := range prs {
			if p.MergedAt != nil && p.Number != nil {
				add(*p.Number)
			}
		}
	}

	out := make([]drafting.PR, 0, len(numbers))
	for _, n := range numbers {
		p, resp, err := cl.PullRequests.Get(ctx, owner, repo, n)
		if resp != nil && resp.StatusCode == 404 {
			continue // "(#N)" pointed at an issue or another repo's PR
		}
		if err != nil {
			return nil, nil, fmt.Errorf("get PR #%d: %w", n, err)
		}
		if p.MergedAt == nil {
			continue
		}
		out = append(out, toPR(p))
	}
	commits := make([]ReleaseCommit, 0, len(shas))
	for _, cm := range shas {
		subject, _, _ := strings.Cut(cm.message, "\n")
		commits = append(commits, ReleaseCommit{SHA: cm.sha, Subject: strings.TrimSpace(subject), AuthorLogin: cm.author, AuthorType: cm.authorType})
	}
	return out, commits, nil
}

// subjectPR reads the "(#N)" GitHub appends to a squash or merge commit subject.
func subjectPR(message string) (int, bool) {
	subject, _, _ := strings.Cut(message, "\n")
	m := prSuffix.FindStringSubmatch(subject)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n > 0
}

func toPR(p *gh.PullRequest) drafting.PR {
	pr := drafting.PR{
		Number:  p.GetNumber(),
		Title:   p.GetTitle(),
		Body:    p.GetBody(),
		Added:   p.GetAdditions(),
		Deleted: p.GetDeletions(),
	}
	if t := p.GetMergedAt(); !t.IsZero() {
		pr.MergedAt = t.Time
	}
	for _, l := range p.Labels {
		if l != nil {
			pr.Labels = append(pr.Labels, l.Name)
		}
	}
	if u := p.User; u != nil {
		pr.AuthorID, pr.AuthorLogin = u.GetID(), u.GetLogin()
		pr.AuthorType, pr.AuthorAvatarURL = u.GetType(), u.GetAvatarURL()
	}
	return pr
}

type commitRef struct{ sha, message, author, authorType string }

func refOf(rc *gh.RepositoryCommit) commitRef {
	r := commitRef{sha: rc.GetSHA()}
	if rc.Commit != nil {
		r.message = rc.Commit.GetMessage()
	}
	if rc.Author != nil {
		r.author, r.authorType = rc.Author.GetLogin(), rc.Author.GetType()
	}
	return r
}

// compare returns the commits in base...head, oldest first.
func compare(ctx context.Context, cl *gh.Client, owner, repo, base, head string) ([]commitRef, error) {
	var out []commitRef
	opts := &gh.ListOptions{PerPage: 100}
	for range maxPages {
		cmp, resp, err := cl.Repositories.CompareCommits(ctx, owner, repo, base, head, opts)
		if err != nil {
			return nil, fmt.Errorf("compare %s...%s: %w", base, head, err)
		}
		for _, rc := range cmp.Commits {
			out = append(out, refOf(rc))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// rootCommit finds the oldest commit reachable from head: the last entry of
// the last page of the commit list.
func rootCommit(ctx context.Context, cl *gh.Client, owner, repo, head string) (commitRef, error) {
	opts := &gh.CommitsListOptions{SHA: head, ListOptions: gh.ListOptions{PerPage: 1}}
	list, resp, err := cl.Repositories.ListCommits(ctx, owner, repo, opts)
	if err != nil {
		return commitRef{}, fmt.Errorf("list commits: %w", err)
	}
	if resp.LastPage > 1 {
		opts.Page = resp.LastPage
		if list, _, err = cl.Repositories.ListCommits(ctx, owner, repo, opts); err != nil {
			return commitRef{}, fmt.Errorf("list commits: %w", err)
		}
	}
	if len(list) == 0 {
		return commitRef{}, fmt.Errorf("no commits reachable from %s", head)
	}
	return refOf(list[len(list)-1]), nil
}

// PreviousTag returns the tag released before head, or "" when head is the
// first (the caller then uses the full history).
//
// Releases are checked first, newest first, skipping drafts, and skipping
// prereleases when head is a full release. When head has no older release the
// tag list (newest first) decides.
func (c *Client) PreviousTag(ctx context.Context, installID int64, owner, repo, head string) (string, error) {
	h, err := c.History(ctx, installID, owner, repo, head)
	return h.PrevTag, err
}

// ReleaseHistory is where a release sits among the repo's releases.
type ReleaseHistory struct {
	// PrevTag is the tag released before head, "" for the first.
	PrevTag string
	// PrevAt is when the previous release was published; zero when it came
	// from the tag list or is unknown.
	PrevAt time.Time
	// Ordinal counts head among the repo's published releases, oldest first
	// (prereleases count only when head is one); 0 when head is not a release.
	Ordinal int
}

// History is PreviousTag with the previous release's date and head's ordinal.
func (c *Client) History(ctx context.Context, installID int64, owner, repo, head string) (ReleaseHistory, error) {
	var h ReleaseHistory
	cl, err := c.installation(installID)
	if err != nil {
		return h, err
	}
	var rels []*gh.RepositoryRelease
	opts := &gh.ListOptions{PerPage: 100}
	for range maxPages {
		page, resp, err := cl.Repositories.ListReleases(ctx, owner, repo, opts)
		if err != nil {
			return h, fmt.Errorf("list releases: %w", err)
		}
		rels = append(rels, page...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	for i, r := range rels {
		if r.GetTagName() != head {
			continue
		}
		h.Ordinal = 1
		for _, older := range rels[i+1:] {
			if older.GetDraft() || (older.GetPrerelease() && !r.GetPrerelease()) {
				continue
			}
			h.Ordinal++
			if h.PrevTag == "" {
				h.PrevTag = older.GetTagName()
				if t := older.GetPublishedAt(); !t.IsZero() {
					h.PrevAt = t.Time
				}
			}
		}
		if h.PrevTag != "" {
			return h, nil
		}
		break
	}

	opts = &gh.ListOptions{PerPage: 100}
	var tags []string
	for range maxPages {
		page, resp, err := cl.Repositories.ListTags(ctx, owner, repo, opts)
		if err != nil {
			return h, fmt.Errorf("list tags: %w", err)
		}
		for _, t := range page {
			tags = append(tags, t.GetName())
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	for i, t := range tags {
		if t == head && i+1 < len(tags) {
			h.PrevTag = tags[i+1]
			return h, nil
		}
	}
	return h, nil
}
