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
	// avatar), accent, background and text colours (empty: the defaults).
	LogoURL    string `json:"logoUrl"`
	Accent     string `json:"accent"`
	Background string `json:"background"`
	Text       string `json:"text"`
	// TemplateID is the template its release launches use, "" for the built-in default.
	TemplateID string `json:"templateId"`
}

// RepoPatch changes a repo's flags; nil fields are left alone.
type RepoPatch struct {
	Enabled         *bool `json:"enabled"`
	AllowPrerelease *bool `json:"allowPrerelease"`
	// LogoURL, Accent, Background and Text set the card branding; "" clears one.
	LogoURL    *string `json:"logoUrl"`
	Accent     *string `json:"accent"`
	Background *string `json:"background"`
	Text       *string `json:"text"`
	// TemplateID picks the template for its release launches; "" is the built-in default.
	TemplateID *string `json:"templateId"`
}

// brandingPatch reports whether p touches the card branding.
func (p RepoPatch) brandingPatch() bool {
	return p.LogoURL != nil || p.Accent != nil || p.Background != nil || p.Text != nil
}

func repoOf(r sqlc.Repo) Repo {
	out := Repo{ID: r.ID, FullName: r.FullName, Enabled: r.Enabled, AllowPrerelease: r.AllowPrerelease, LogoURL: r.LogoUrl, Accent: r.Accent, Background: r.CardBg, Text: r.CardText}
	if r.TemplateID.Valid {
		out.TemplateID = r.TemplateID.UUID.String()
	}
	return out
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
	if p.brandingPatch() {
		if r, err = updateBranding(ctx, d, r, p); err != nil {
			return Repo{}, err
		}
	}
	if p.TemplateID != nil {
		ref, err := templateRef(ctx, d, userID, *p.TemplateID)
		if err != nil {
			return Repo{}, err
		}
		if r, err = d.Q.SetRepoTemplate(ctx, sqlc.SetRepoTemplateParams{ID: r.ID, TemplateID: ref}); err != nil {
			return Repo{}, fmt.Errorf("launch: set repo template: %w", err)
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

// cardBranding is a repo's resolved card branding.
type cardBranding struct {
	logoURL                  string
	logoPNG                  []byte
	accent, background, text string
}

// hexField checks and upper-cases one colour of a patch; nil keeps cur.
func hexField(v *string, cur, what string) (string, error) {
	if v == nil {
		return cur, nil
	}
	h := strings.ToUpper(strings.TrimSpace(*v))
	if h != "" && !cards.ValidAccent(h) {
		return "", invalid("use a hex colour like #58D3AE for the %s", what)
	}
	return h, nil
}

// branding resolves a patch against the stored row: colours checked and
// upper-cased, the logo fetched through the QR logo guard and shrunk. An
// unchanged logo URL reuses the stored image instead of fetching it again.
// Contrast is left to readable, so a preview can draw any pick.
func branding(ctx context.Context, d Deps, r sqlc.Repo, p RepoPatch) (cardBranding, error) {
	b := cardBranding{logoURL: r.LogoUrl, logoPNG: r.LogoPng}
	var err error
	if b.accent, err = hexField(p.Accent, r.Accent, "accent"); err != nil {
		return cardBranding{}, err
	}
	if b.background, err = hexField(p.Background, r.CardBg, "background"); err != nil {
		return cardBranding{}, err
	}
	if b.text, err = hexField(p.Text, r.CardText, "text colour"); err != nil {
		return cardBranding{}, err
	}
	if p.LogoURL != nil {
		raw := strings.TrimSpace(*p.LogoURL)
		if raw == r.LogoUrl && raw != "" && len(r.LogoPng) > 0 {
			return b, nil
		}
		b.logoURL, b.logoPNG = raw, nil
		if raw != "" {
			if err := validateLogo(qrCustom, raw, d.AllowLoopback); err != nil {
				return cardBranding{}, invalid("use an https:// image URL for the repo logo")
			}
			img, err := fetchLogo(ctx, raw, d.AllowLoopback)
			if err != nil {
				return cardBranding{}, err
			}
			if b.logoPNG, err = cards.ShrinkLogo(img); err != nil {
				return cardBranding{}, fmt.Errorf("%w: %v", ErrLogoNotImage, err)
			}
		}
	}
	return b, nil
}

// readable checks that the text and accent read on the chosen background.
// Only saving asks: the preview draws whatever the captain is trying out.
func (b cardBranding) readable() error {
	field := cards.Brand{Background: b.background}.Apply(cards.PresetFor(uuid.Nil)).Field
	if b.text != "" && cards.Contrast(b.text, field) < 4.5 {
		return invalid("that text colour is hard to read on the background; pick one with more contrast")
	}
	if b.accent != "" && cards.Contrast(b.accent, field) < 4.5 {
		if b.background == "" {
			return invalid("that accent is too dark to read on the card; pick a lighter one")
		}
		return invalid("that accent is hard to read on the background; pick one with more contrast")
	}
	return nil
}

// updateBranding stores a repo's card branding. The logo is kept shrunk in
// the row, so rendering a card never fetches a captain's URL.
func updateBranding(ctx context.Context, d Deps, r sqlc.Repo, p RepoPatch) (sqlc.Repo, error) {
	b, err := branding(ctx, d, r, p)
	if err != nil {
		return r, err
	}
	if err := b.readable(); err != nil {
		return r, err
	}
	out, err := d.Q.UpdateRepoBranding(ctx, sqlc.UpdateRepoBrandingParams{
		ID: r.ID, LogoUrl: b.logoURL, LogoPng: b.logoPNG, Accent: b.accent, CardBg: b.background, CardText: b.text,
	})
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
	b, err := branding(ctx, d, r, p)
	if err != nil {
		return nil, err
	}
	_, name, _ := strings.Cut(r.FullName, "/")
	c := cards.ContributorCard{
		Name: "Your contributor", Handle: "contributor", Project: name, Version: "v1.0.0",
		PRCount: 3, Added: 412, Deleted: 96, Release: sampleRelease, ReleaseMore: 9, CrewCount: 9,
		Date: d.now(), Brand: cards.RepoBrand(r.FullName, b.logoPNG, b.accent, b.background, b.text),
	}
	c.Brand.Exact = true
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
