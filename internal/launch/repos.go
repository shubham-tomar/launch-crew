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
	r, err := d.Q.GetRepo(ctx, repoID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!r.OwnerUserID.Valid || r.OwnerUserID.UUID != userID)) {
		return Repo{}, ErrNotFound
	}
	if err != nil {
		return Repo{}, fmt.Errorf("launch: load repo: %w", err)
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

// updateBranding validates and stores a repo's card logo and accent. A logo is
// fetched once, through the same guard as the QR logo, and kept shrunk in the
// row, so rendering a card never fetches a captain's URL.
func updateBranding(ctx context.Context, d Deps, r sqlc.Repo, p RepoPatch) (sqlc.Repo, error) {
	arg := sqlc.UpdateRepoBrandingParams{ID: r.ID, LogoUrl: r.LogoUrl, LogoPng: r.LogoPng, Accent: r.Accent}
	if p.Accent != nil {
		a := strings.ToUpper(strings.TrimSpace(*p.Accent))
		switch {
		case a == "":
		case !cards.ValidAccent(a):
			return r, invalid("use a hex colour like #58D3AE for the accent")
		case cards.Contrast(a, cards.PresetFor(uuid.Nil).Field) < 4.5:
			return r, invalid("that accent is too dark to read on the card; pick a lighter one")
		}
		arg.Accent = a
	}
	if p.LogoURL != nil {
		raw := strings.TrimSpace(*p.LogoURL)
		arg.LogoUrl, arg.LogoPng = raw, nil
		if raw != "" {
			if err := validateLogo(qrCustom, raw, d.AllowLoopback); err != nil {
				return r, invalid("use an https:// image URL for the repo logo")
			}
			b, err := fetchLogo(ctx, raw, d.AllowLoopback)
			if err != nil {
				return r, err
			}
			if arg.LogoPng, err = cards.ShrinkLogo(b); err != nil {
				return r, fmt.Errorf("%w: %v", ErrLogoNotImage, err)
			}
		}
	}
	out, err := d.Q.UpdateRepoBranding(ctx, arg)
	if err != nil {
		return r, fmt.Errorf("launch: update repo branding: %w", err)
	}
	return out, nil
}
