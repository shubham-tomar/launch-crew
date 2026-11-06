package launch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
)

// Repo is a repository the captain installed the App on.
type Repo struct {
	ID              uuid.UUID `json:"id"`
	FullName        string    `json:"fullName"`
	Enabled         bool      `json:"enabled"`
	AllowPrerelease bool      `json:"allowPrerelease"`
	// Card branding: the captain's logo URL (empty: the owner's GitHub
	// avatar) and accent colour (empty: the default).
	LogoURL string `json:"logoUrl"`
	Accent  string `json:"accent"`
}

// RepoPatch changes a repo's flags; nil fields are left alone.
type RepoPatch struct {
	Enabled         *bool `json:"enabled"`
	AllowPrerelease *bool `json:"allowPrerelease"`
	// LogoURL and Accent set the card branding; "" clears either.
	LogoURL *string `json:"logoUrl"`
	Accent  *string `json:"accent"`
}

func repoOf(r sqlc.Repo) Repo {
	return Repo{ID: r.ID, FullName: r.FullName, Enabled: r.Enabled, AllowPrerelease: r.AllowPrerelease, LogoURL: r.LogoUrl, Accent: r.Accent}
}

// Repos lists the captain's repos by name.
func Repos(ctx context.Context, d Deps, userID uuid.UUID) ([]Repo, error) {
	rs, err := d.Q.ListReposByOwner(ctx, uuid.NullUUID{UUID: userID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("launch: list repos: %w", err)
	}
	out := make([]Repo, 0, len(rs))
	for _, r := range rs {
		out = append(out, repoOf(r))
	}
	return out, nil
}

// UpdateRepo sets a repo's flags. A repo the user does not own is ErrNotFound.
func UpdateRepo(ctx context.Context, d Deps, userID, repoID uuid.UUID, p RepoPatch) (Repo, error) {
	r, err := ownRepo(ctx, d, userID, repoID)
	if err != nil {
		return Repo{}, err
	}
	if p.LogoURL != nil || p.Accent != nil {
		if r, err = updateBranding(ctx, d, r, p); err != nil {
			return Repo{}, err
		}
	}
	if p.Enabled == nil && p.AllowPrerelease == nil {
		return repoOf(r), nil
	}
	arg := sqlc.UpdateRepoFlagsParams{ID: r.ID, Enabled: r.Enabled, AllowPrerelease: r.AllowPrerelease}
	if p.Enabled != nil {
		arg.Enabled = *p.Enabled
	}
	if p.AllowPrerelease != nil {
		arg.AllowPrerelease = *p.AllowPrerelease
	}
	r, err = d.Q.UpdateRepoFlags(ctx, arg)
	if err != nil {
		return Repo{}, fmt.Errorf("launch: update repo: %w", err)
	}
	return repoOf(r), nil
}

// ownRepo loads a repo the user owns; anyone else's is ErrNotFound.
func ownRepo(ctx context.Context, d Deps, userID, repoID uuid.UUID) (sqlc.Repo, error) {
	r, err := d.Q.GetRepo(ctx, repoID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!r.OwnerUserID.Valid || r.OwnerUserID.UUID != userID)) {
		return sqlc.Repo{}, ErrNotFound
	}
	if err != nil {
		return sqlc.Repo{}, fmt.Errorf("launch: load repo: %w", err)
	}
	return r, nil
}

// branding resolves a patch against the stored row: the accent checked and
// upper-cased, the logo fetched through the QR logo guard and shrunk. An
// unchanged logo URL reuses the stored image instead of fetching it again.
func branding(ctx context.Context, d Deps, r sqlc.Repo, p RepoPatch) (logoURL string, logoPNG []byte, accent string, err error) {
	logoURL, logoPNG, accent = r.LogoUrl, r.LogoPng, r.Accent
	if p.Accent != nil {
		a := strings.ToUpper(strings.TrimSpace(*p.Accent))
		switch {
		case a == "":
		case !cards.ValidAccent(a):
			return "", nil, "", invalid("use a hex colour like #58D3AE for the accent")
		case cards.Contrast(a, cards.PresetFor(uuid.Nil).Field) < 4.5:
			return "", nil, "", invalid("that accent is too dark to read on the card; pick a lighter one")
		}
		accent = a
	}
	if p.LogoURL != nil {
		raw := strings.TrimSpace(*p.LogoURL)
		if raw == r.LogoUrl && raw != "" && len(r.LogoPng) > 0 {
			return raw, r.LogoPng, accent, nil
		}
		logoURL, logoPNG = raw, nil
		if raw != "" {
			if err := validateLogo(qrCustom, raw, d.AllowLoopback); err != nil {
				return "", nil, "", invalid("use an https:// image URL for the repo logo")
			}
			b, err := fetchLogo(ctx, raw, d.AllowLoopback)
			if err != nil {
				return "", nil, "", err
			}
			if logoPNG, err = cards.ShrinkLogo(b); err != nil {
				return "", nil, "", fmt.Errorf("%w: %v", ErrLogoNotImage, err)
			}
		}
	}
	return logoURL, logoPNG, accent, nil
}

// updateBranding stores a repo's card logo and accent. The logo is kept shrunk
// in the row, so rendering a card never fetches a captain's URL.
func updateBranding(ctx context.Context, d Deps, r sqlc.Repo, p RepoPatch) (sqlc.Repo, error) {
	logoURL, logoPNG, accent, err := branding(ctx, d, r, p)
	if err != nil {
		return r, err
	}
	out, err := d.Q.UpdateRepoBranding(ctx, sqlc.UpdateRepoBrandingParams{ID: r.ID, LogoUrl: logoURL, LogoPng: logoPNG, Accent: accent})
	if err != nil {
		return r, fmt.Errorf("launch: update repo branding: %w", err)
	}
	return out, nil
}

// sampleRelease fills a branding preview when the repo has no launch yet.
var sampleRelease = []cards.PRLine{
	{Number: 42, Title: "Split-pane resize with keyboard"},
	{Number: 41, Title: "Credit every PR a person has in a release"},
	{Number: 39, Title: "Remember workspace layout between sessions"},
	{Number: 37, Title: "Refresh channels button"},
	{Number: 35, Title: "Respect gaps when dragging to resize"},
}

// RepoCardPreview draws a contributor card with the given branding, unsaved,
// so the captain sees it before saving. The captain stands in as the
// contributor; the repo's latest launch supplies the changes when it has one.
func RepoCardPreview(ctx context.Context, d Deps, userID, repoID uuid.UUID, p RepoPatch) ([]byte, error) {
	r, err := ownRepo(ctx, d, userID, repoID)
	if err != nil {
		return nil, err
	}
	_, logoPNG, accent, err := branding(ctx, d, r, p)
	if err != nil {
		return nil, err
	}
	_, name, _ := strings.Cut(r.FullName, "/")
	c := cards.ContributorCard{
		Name: "Your contributor", Handle: "contributor", Project: name, Version: "v1.0.0",
		PRCount: 3, Added: 412, Deleted: 96, Release: sampleRelease, ReleaseMore: 9, CrewCount: 9,
		Date: d.now(), Brand: cards.RepoBrand(r.FullName, logoPNG, accent),
	}
	if gi, err := d.Q.GetGitHubIdentityByUser(ctx, userID); err == nil {
		c.Name, c.Handle = gi.Login, gi.Login
		c.AvatarURL = fmt.Sprintf("https://avatars.githubusercontent.com/u/%d?s=256", gi.GithubUserID)
	}
	if l, err := latestRepoLaunch(ctx, d, r.ID); err == nil {
		if changes, err := changesOf(l); err == nil {
			if lines, more := cardLines(changes); len(lines) > 0 {
				c.Release, c.ReleaseMore, c.CrewCount = lines, more, (&state{changes: changes}).crewSize()
			}
		}
		if l.ReleaseTag.String != "" {
			c.Version = l.ReleaseTag.String
		}
	}
	return d.render()(c, cards.PresetFor(uuid.Nil))
}

func latestRepoLaunch(ctx context.Context, d Deps, repoID uuid.UUID) (sqlc.Launch, error) {
	var id uuid.UUID
	if err := d.Pool.QueryRow(ctx, `SELECT id FROM launches WHERE repo_id = $1 AND status <> 'deleted' ORDER BY created_at DESC LIMIT 1`, repoID).Scan(&id); err != nil {
		return sqlc.Launch{}, err
	}
	return d.Q.GetLaunch(ctx, id)
}
