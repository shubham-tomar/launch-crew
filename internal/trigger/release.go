package trigger

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/github"
)

const summaryRunes = 280

// ReleaseEvent is the part of a release webhook payload Launch Crew reads.
type ReleaseEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Release struct {
		ID         int64  `json:"id"`
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Name       string `json:"name"`
		Body       string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Author     struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"author"`
	} `json:"release"`
}

// DecodeRelease parses and sanity-checks a release payload.
func DecodeRelease(raw []byte) (ReleaseEvent, error) {
	var ev ReleaseEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return ev, fmt.Errorf("decode release payload: %w", err)
	}
	if ev.Installation.ID == 0 || ev.Release.TagName == "" || !strings.Contains(ev.Repository.FullName, "/") {
		return ev, errors.New("release payload lacks installation, tag or repository")
	}
	return ev, nil
}

// GitHubRelease builds a LaunchSpec from a published release.
type GitHubRelease struct {
	GH        *github.Client
	Headlines func(ctx context.Context, notes string, prs []drafting.PR) (headlines, chores []drafting.Change, err error)
}

// Build reads the release's PRs since the previous tag, picks headlines and
// suggests the crew: distinct human PR authors, ticked when they wrote a
// headline PR. Opt-outs are dropped by ReleaseJob, which holds the database.
func (g GitHubRelease) Build(ctx context.Context, in Input) (LaunchSpec, error) {
	ev, err := DecodeRelease(in.ReleasePayload)
	if err != nil {
		return LaunchSpec{}, err
	}
	owner, repo, _ := strings.Cut(ev.Repository.FullName, "/")
	rel := ev.Release
	prev, err := g.GH.PreviousTag(ctx, ev.Installation.ID, owner, repo, rel.TagName)
	if err != nil {
		return LaunchSpec{}, err
	}
	prs, err := g.GH.PRsBetween(ctx, ev.Installation.ID, owner, repo, prev, rel.TagName)
	if err != nil {
		return LaunchSpec{}, err
	}
	heads, chores, err := g.pick(ctx, rel.Body, prs)
	if err != nil {
		return LaunchSpec{}, err
	}
	title := strings.TrimSpace(rel.Name)
	if title == "" {
		title = rel.TagName
	}
	return LaunchSpec{
		Title:        title,
		Summary:      firstRunes(strings.TrimSpace(rel.Body), summaryRunes),
		Source:       "github",
		ReleaseTag:   rel.TagName,
		ReleaseURL:   rel.HTMLURL,
		Headlines:    heads,
		Chores:       chores,
		Crew:         suggestCrew(prs, heads),
		Roles:        []Role{},
		RepoFullName: ev.Repository.FullName,
		ReleaseID:    rel.ID,
		Captain:      rel.Author.Login,
	}, nil
}

// pick asks Headlines for 0..3 headlines and tops them up to 2 with the
// largest PRs it left unclassified; if it fails, the two largest PRs lead.
// Every other PR becomes a hidden chore.
func (g GitHubRelease) pick(ctx context.Context, notes string, prs []drafting.PR) (heads, chores []drafting.Change, err error) {
	if len(prs) == 0 {
		return nil, nil, nil
	}
	if g.Headlines != nil {
		heads, chores, err = g.Headlines(ctx, notes, prs)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			log.Printf("trigger: headlines failed, picking by size: %v", err)
			heads, chores = nil, nil
		}
	}
	if len(heads) > 3 {
		chores = append(chores, heads[3:]...)
		heads = heads[:3]
	}
	used := map[int]bool{}
	for _, c := range slices.Concat(heads, chores) {
		used[c.PRNumber] = true
	}
	if len(heads) < 2 {
		rest := slices.DeleteFunc(slices.Clone(prs), func(p drafting.PR) bool { return used[p.Number] })
		slices.SortStableFunc(rest, func(a, b drafting.PR) int { return cmp.Compare(b.Added+b.Deleted, a.Added+a.Deleted) })
		for _, p := range rest {
			if len(heads) >= 2 {
				break
			}
			heads = append(heads, change(p))
			used[p.Number] = true
		}
	}
	for _, p := range prs {
		if !used[p.Number] {
			chores = append(chores, change(p))
		}
	}
	for i := range heads {
		heads[i].Hidden = false
	}
	for i := range chores {
		chores[i].Hidden = true
	}
	return heads, chores, nil
}

func change(p drafting.PR) drafting.Change {
	return drafting.Change{PRNumber: p.Number, Title: p.Title, AuthorID: p.AuthorID, AuthorLogin: p.AuthorLogin}
}

// IsBot reports whether a GitHub account is a bot.
func IsBot(login, accountType string) bool {
	return accountType == "Bot" || strings.HasSuffix(login, "[bot]")
}

// suggestCrew lists each human PR author once, with all of their PRs newest
// first (most recently merged, then higher number). PRNumber and PRTitle are
// the newest, where the comment goes. Ticked when any of their PRs is a headline.
func suggestCrew(prs []drafting.PR, heads []drafting.Change) []CrewSuggestion {
	isHead := map[int]bool{}
	for _, h := range heads {
		isHead[h.PRNumber] = true
	}
	var out []CrewSuggestion
	idx := map[int64]int{}
	merged := map[int64][]drafting.PR{}
	for _, p := range prs {
		if p.AuthorID == 0 || IsBot(p.AuthorLogin, p.AuthorType) {
			continue
		}
		i, ok := idx[p.AuthorID]
		if !ok {
			i = len(out)
			idx[p.AuthorID] = i
			out = append(out, CrewSuggestion{GitHubUserID: p.AuthorID, Login: p.AuthorLogin, AvatarURL: p.AuthorAvatarURL})
		}
		merged[p.AuthorID] = append(merged[p.AuthorID], p)
		if isHead[p.Number] {
			out[i].Ticked = true
		}
	}
	for i := range out {
		own := merged[out[i].GitHubUserID]
		slices.SortStableFunc(own, func(a, b drafting.PR) int {
			if c := b.MergedAt.Compare(a.MergedAt); c != 0 {
				return c
			}
			return cmp.Compare(b.Number, a.Number)
		})
		for _, p := range own {
			out[i].PRs = append(out[i].PRs, PRRef{Number: p.Number, Title: p.Title, Added: p.Added, Deleted: p.Deleted})
		}
		out[i].PRNumber, out[i].PRTitle = own[0].Number, own[0].Title
	}
	return out
}

func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
