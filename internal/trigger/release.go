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
			ID        int64  `json:"id"`
			Login     string `json:"login"`
			Type      string `json:"type"`
			AvatarURL string `json:"avatar_url"`
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
	hist, err := g.GH.History(ctx, ev.Installation.ID, owner, repo, rel.TagName)
	if err != nil {
		return LaunchSpec{}, err
	}
	prs, commits, err := g.GH.ReleaseChanges(ctx, ev.Installation.ID, owner, repo, hist.PrevTag, rel.TagName)
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
	var solo *Solo
	if isSolo(rel.Author.Login, rel.Author.Type, prs, commits) {
		solo = soloOf(prs, commits, hist)
		solo.Login, solo.AvatarURL = rel.Author.Login, rel.Author.AvatarURL
		if name, err := g.GH.UserName(ctx, ev.Installation.ID, rel.Author.ID); err == nil {
			solo.Name = strings.TrimSpace(name)
		}
	}
	return LaunchSpec{
		Solo:         solo,
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

// isSolo reports whether the release author is the only person in the
// release: every human PR author and commit author is them. Commits whose
// author has no GitHub account count as theirs.
func isSolo(author, authorType string, prs []drafting.PR, commits []github.ReleaseCommit) bool {
	if author == "" || IsBot(author, authorType) || (len(prs) == 0 && len(commits) == 0) {
		return false
	}
	other := func(login, kind string) bool {
		return login != "" && !IsBot(login, kind) && !strings.EqualFold(login, author)
	}
	for _, p := range prs {
		if other(p.AuthorLogin, p.AuthorType) {
			return false
		}
	}
	for _, c := range commits {
		if other(c.AuthorLogin, c.AuthorType) {
			return false
		}
	}
	return true
}

// maxSubjects is how many commit subjects a solo spec keeps for its card.
const maxSubjects = 20

// soloOf counts the release's commits and lines. Lines come from the PRs;
// a release without PRs keeps its commit subjects instead, merges skipped.
func soloOf(prs []drafting.PR, commits []github.ReleaseCommit, h github.ReleaseHistory) *Solo {
	s := &Solo{Commits: len(commits), PrevTag: h.PrevTag, PrevAt: h.PrevAt, Ordinal: h.Ordinal}
	for _, p := range prs {
		s.Added += p.Added
		s.Deleted += p.Deleted
	}
	if len(prs) == 0 {
		for i := len(commits) - 1; i >= 0; i-- {
			sub := commits[i].Subject
			if sub == "" || strings.HasPrefix(sub, "Merge ") {
				continue
			}
			s.SubjectCount++
			if len(s.Subjects) < maxSubjects {
				s.Subjects = append(s.Subjects, sub)
			}
		}
	}
	return s
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
