package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
)

// Patch is a captain's edit. Nil fields are left alone. HiddenPRs, when
// non-nil, is the full set of hidden PR numbers; TickedCrew, when non-nil,
// is the full set of ticked crew rows (the rest of the not-yet-invited rows
// become suggested).
type Patch struct {
	Title         *string     `json:"title"`
	Summary       *string     `json:"summary"`
	LaunchAt      *time.Time  `json:"launchAt"`
	WindowMinutes *int        `json:"windowMinutes"`
	HiddenPRs     []int       `json:"hiddenPrs"`
	TickedCrew    []uuid.UUID `json:"tickedCrew"`
	CrewCap       *int        `json:"crewCap"`
	// QRLogo is plane, none or custom; QRLogoURL is the https image for custom.
	QRLogo    *string `json:"qrLogo"`
	QRLogoURL *string `json:"qrLogoUrl"`
	// IncludeCard says whether the launch's posts lead with the card slot.
	IncludeCard *bool `json:"includeCard"`
}

// Validate checks the patch against now.
func (p Patch) Validate(now time.Time) error {
	if p.Title != nil && strings.TrimSpace(*p.Title) == "" {
		return invalid("the title cannot be empty")
	}
	if p.LaunchAt != nil {
		if !p.LaunchAt.After(now) {
			return invalid("pick a launch time in the future")
		}
		if p.LaunchAt.After(now.Add(maxLead)) {
			return invalid("pick a launch time within 30 days")
		}
	}
	if p.WindowMinutes != nil && (*p.WindowMinutes < minWindow || *p.WindowMinutes > maxWindow) {
		return invalid("the window is 5 to 720 minutes")
	}
	if p.CrewCap != nil && (*p.CrewCap < 1 || *p.CrewCap > maxCrewCap) {
		return invalid("the crew cap is 1 to 1000")
	}
	return nil
}

// change mirrors the stored headline_changes entries (trigger's changeJSON).
type change struct {
	PRNumber    int    `json:"pr_number"`
	Title       string `json:"title"`
	AuthorID    int64  `json:"author_id"`
	AuthorLogin string `json:"author_login"`
	Hidden      bool   `json:"hidden"`
}

func changesOf(l sqlc.Launch) ([]change, error) {
	var cs []change
	if len(l.HeadlineChanges) == 0 {
		return cs, nil
	}
	if err := json.Unmarshal(l.HeadlineChanges, &cs); err != nil {
		return nil, fmt.Errorf("launch: headline changes: %w", err)
	}
	return cs, nil
}

// Update applies a captain's edit to a draft launch.
func Update(ctx context.Context, d Deps, id uuid.UUID, p Patch) error {
	if err := p.Validate(d.now()); err != nil {
		return err
	}
	var (
		logo *preparedLogo
		err  error
	)
	if p.QRLogo != nil {
		raw := ""
		if p.QRLogoURL != nil {
			raw = *p.QRLogoURL
		}
		if logo, err = prepareLogo(ctx, d, *p.QRLogo, raw); err != nil {
			return err
		}
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Lock the row so a concurrent Launch cannot slip between check and write.
	if err := lockDraft(ctx, tx, id, ErrNotDraft); err != nil {
		return err
	}
	q := sqlc.New(tx)
	l, err := q.GetLaunch(ctx, id)
	if err != nil {
		return err
	}
	arg := sqlc.UpdateLaunchParams{
		ID: id, Title: l.Title, Summary: l.Summary, LaunchAt: l.LaunchAt, WindowMinutes: l.WindowMinutes,
		CrewCap: l.CrewCap, Roles: l.Roles, HeadlineChanges: l.HeadlineChanges,
	}
	if p.Title != nil {
		arg.Title = strings.TrimSpace(*p.Title)
	}
	if p.Summary != nil {
		arg.Summary = strings.TrimSpace(*p.Summary)
	}
	if p.LaunchAt != nil {
		arg.LaunchAt = pgtype.Timestamptz{Time: p.LaunchAt.UTC(), Valid: true}
	}
	if p.WindowMinutes != nil {
		arg.WindowMinutes = int32(*p.WindowMinutes)
	}
	if p.CrewCap != nil {
		arg.CrewCap = int32(*p.CrewCap)
	}
	if p.HiddenPRs != nil {
		cs, err := changesOf(l)
		if err != nil {
			return err
		}
		hide := map[int]bool{}
		for _, n := range p.HiddenPRs {
			hide[n] = true
		}
		for i := range cs {
			cs[i].Hidden = hide[cs[i].PRNumber]
		}
		if arg.HeadlineChanges, err = json.Marshal(cs); err != nil {
			return err
		}
	}
	if _, err := q.UpdateLaunch(ctx, arg); err != nil {
		return fmt.Errorf("launch: update: %w", err)
	}
	if err := writeLogo(ctx, d, tx, q, id, logo); err != nil {
		return err
	}
	if p.IncludeCard != nil {
		if err := q.SetLaunchIncludeCard(ctx, sqlc.SetLaunchIncludeCardParams{ID: id, IncludeCard: *p.IncludeCard}); err != nil {
			return fmt.Errorf("launch: set include card: %w", err)
		}
	}
	// Captain drafts are built from the title, summary and headlines; the
	// ones not yet sent are dropped so Detail or LaunchJob rebuilds them.
	if p.Title != nil || p.Summary != nil || p.HiddenPRs != nil {
		if err := q.DeleteUnsentCaptainDrafts(ctx, id); err != nil {
			return fmt.Errorf("launch: reset captain drafts: %w", err)
		}
	}
	if p.TickedCrew != nil {
		if err := q.SetCrewTicked(ctx, sqlc.SetCrewTickedParams{LaunchID: id, Ticked: p.TickedCrew}); err != nil {
			return fmt.Errorf("launch: tick crew: %w", err)
		}
	}
	return tx.Commit(ctx)
}
