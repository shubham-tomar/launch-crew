package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/webhook"
)

// CardStore uploads a rendered card; *cards.Uploader is one.
type CardStore interface {
	Put(ctx context.Context, png []byte) (url, sha string, err error)
}

// ReleaseDeps are what ReleaseJob needs. Render defaults to cards.Render.
type ReleaseDeps struct {
	Pool    *pgxpool.Pool
	Trigger Trigger
	Cards   CardStore
	Render  func(c any, p cards.Preset) ([]byte, error)
}

// Crew states written by ReleaseJob. Ticked members are pre-selected.
const (
	StateSuggested = "suggested"
	StateTicked    = "ticked"
)

type changeJSON struct {
	PRNumber    int    `json:"pr_number"`
	Title       string `json:"title"`
	AuthorID    int64  `json:"author_id"`
	AuthorLogin string `json:"author_login"`
	Hidden      bool   `json:"hidden"`
}

// ReleaseJob handles jobs.ProcessRelease: it turns a stored release delivery
// into a draft launch with its release card and suggested crew. Releases that
// should not launch (unknown or disabled repo, draft, prerelease when not
// allowed) and repeats of an already launched tag are skipped without error.
func ReleaseJob(d ReleaseDeps) jobs.Handler {
	render := d.Render
	if render == nil {
		render = cards.Render
	}
	return func(ctx context.Context, j jobs.Job) error {
		var p webhook.ProcessReleasePayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return fmt.Errorf("decode job payload: %w", err)
		}
		q := sqlc.New(d.Pool)
		del, err := q.GetWebhookDelivery(ctx, p.DeliveryID)
		if errors.Is(err, pgx.ErrNoRows) {
			log.Printf("release job: delivery %s gone, skipping", p.DeliveryID)
			return nil
		}
		if err != nil {
			return err
		}
		ev, err := DecodeRelease(del.Payload)
		if err != nil {
			log.Printf("release job: delivery %s: %v, skipping", p.DeliveryID, err)
			return nil
		}
		repo, err := q.GetRepoByInstallationAndName(ctx, sqlc.GetRepoByInstallationAndNameParams{
			InstallationID: ev.Installation.ID, FullName: ev.Repository.FullName,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			log.Printf("release job: %s on installation %d not enabled, skipping", ev.Repository.FullName, ev.Installation.ID)
			return nil
		}
		if err != nil {
			return err
		}
		if reason := skipReason(repo, ev); reason != "" {
			log.Printf("release job: %s %s: %s, skipping", ev.Repository.FullName, ev.Release.TagName, reason)
			return nil
		}
		key := sqlc.GetLaunchByRepoAndTagParams{
			RepoID:     uuid.NullUUID{UUID: repo.ID, Valid: true},
			ReleaseTag: pgtype.Text{String: ev.Release.TagName, Valid: true},
		}
		if _, err := q.GetLaunchByRepoAndTag(ctx, key); err == nil {
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		spec, err := d.Trigger.Build(ctx, Input{ReleasePayload: del.Payload})
		if err != nil {
			return fmt.Errorf("build launch: %w", err)
		}
		if spec.Crew, err = dropOptedOut(ctx, q, spec.Crew); err != nil {
			return err
		}
		return d.save(ctx, render, repo, key, spec)
	}
}

func skipReason(repo sqlc.Repo, ev ReleaseEvent) string {
	switch {
	case !repo.Enabled:
		return "repo not enabled"
	case !repo.OwnerUserID.Valid:
		return "repo has no owner"
	case ev.Release.Draft:
		return "draft release"
	case ev.Release.Prerelease && !repo.AllowPrerelease:
		return "prerelease not allowed"
	}
	return ""
}

func dropOptedOut(ctx context.Context, q *sqlc.Queries, crew []CrewSuggestion) ([]CrewSuggestion, error) {
	if len(crew) == 0 {
		return crew, nil
	}
	ids := make([]int64, len(crew))
	for i, c := range crew {
		ids[i] = c.GitHubUserID
	}
	out, err := q.ListOptOutsIn(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("opt-outs: %w", err)
	}
	gone := map[int64]bool{}
	for _, id := range out {
		gone[id] = true
	}
	kept := crew[:0:0]
	for _, c := range crew {
		if !gone[c.GitHubUserID] {
			kept = append(kept, c)
		}
	}
	return kept, nil
}

// save writes the launch, its release card and crew in one transaction, under
// an advisory lock on repo+tag so concurrent deliveries create one launch.
func (d ReleaseDeps) save(ctx context.Context, render func(any, cards.Preset) ([]byte, error), repo sqlc.Repo, key sqlc.GetLaunchByRepoAndTagParams, spec LaunchSpec) error {
	changes := make([]changeJSON, 0, len(spec.Headlines)+len(spec.Chores))
	add := func(cs []drafting.Change, hidden bool) {
		for _, c := range cs {
			changes = append(changes, changeJSON{c.PRNumber, c.Title, c.AuthorID, c.AuthorLogin, hidden})
		}
	}
	add(spec.Headlines, false)
	add(spec.Chores, true)
	changesJSON, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	roles := spec.Roles
	if roles == nil {
		roles = []Role{}
	}
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		return err
	}

	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"launch:"+repo.ID.String()+":"+key.ReleaseTag.String); err != nil {
		return err
	}
	q := sqlc.New(tx)
	if _, err := q.GetLaunchByRepoAndTag(ctx, key); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	l, err := q.CreateLaunch(ctx, sqlc.CreateLaunchParams{
		OwnerUserID:     repo.OwnerUserID.UUID,
		RepoID:          key.RepoID,
		Source:          "github",
		Title:           spec.Title,
		Summary:         spec.Summary,
		ReleaseTag:      key.ReleaseTag,
		ReleaseUrl:      pgtype.Text{String: spec.ReleaseURL, Valid: spec.ReleaseURL != ""},
		HeadlineChanges: changesJSON,
		Roles:           rolesJSON,
		WindowMinutes:   90,
		CrewCap:         200,
	})
	if err != nil {
		return fmt.Errorf("create launch: %w", err)
	}
	if spec.ReleaseID != 0 {
		if err := q.SetLaunchReleaseID(ctx, sqlc.SetLaunchReleaseIDParams{ID: l.ID, ReleaseID: pgtype.Int8{Int64: spec.ReleaseID, Valid: true}}); err != nil {
			return err
		}
	}

	preset := cards.PresetFor(l.ID)
	headline := spec.Title
	if len(spec.Headlines) > 0 {
		headline = spec.Headlines[0].Title
	}
	var lines []cards.PRLine
	more := 0
	for _, h := range spec.Headlines {
		if h.Hidden || strings.TrimSpace(h.Title) == "" {
			continue
		}
		if len(lines) == 5 {
			more++
			continue
		}
		lines = append(lines, cards.PRLine{Number: h.PRNumber, Title: h.Title})
	}
	brand := cards.RepoBrand(repo.FullName, repo.LogoPng, repo.Accent, repo.CardBg, repo.CardText)
	var card any = cards.ReleaseCard{
		Project: spec.Project(), Version: spec.ReleaseTag, Headline: headline,
		Release: lines, ReleaseMore: more, CrewCount: len(spec.Crew), Date: time.Now(),
		Brand: brand,
	}
	if spec.Solo != nil {
		card = soloCard(spec, lines, more, brand, time.Now())
	}
	png, err := render(card, preset)
	if err != nil {
		return fmt.Errorf("render release card: %w", err)
	}
	url, sha, err := d.Cards.Put(ctx, png)
	if err != nil {
		return fmt.Errorf("upload release card: %w", err)
	}
	if _, err := q.InsertLaunchAsset(ctx, sqlc.InsertLaunchAssetParams{LaunchID: l.ID, Kind: "release", R2Url: url, Sha: sha}); err != nil {
		return err
	}
	if err := q.SetLaunchCardPreset(ctx, sqlc.SetLaunchCardPresetParams{ID: l.ID, CardPreset: pgtype.Int2{Int16: presetIndex(preset), Valid: true}}); err != nil {
		return err
	}
	for _, c := range spec.Crew {
		state := StateSuggested
		if c.Ticked {
			state = StateTicked
		}
		cm, err := q.InsertCrewMember(ctx, sqlc.InsertCrewMemberParams{
			LaunchID:     l.ID,
			GithubUserID: pgtype.Int8{Int64: c.GitHubUserID, Valid: true},
			Role:         "contributor",
			State:        state,
			PrNumber:     pgtype.Int4{Int32: int32(c.PRNumber), Valid: c.PRNumber != 0},
			PrTitle:      pgtype.Text{String: c.PRTitle, Valid: c.PRTitle != ""},
		})
		if err != nil {
			return fmt.Errorf("insert crew %s: %w", c.Login, err)
		}
		if len(c.PRs) > 1 {
			raw, err := json.Marshal(c.PRs)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE crew_members SET prs = $2 WHERE id = $1`, cm.ID, raw); err != nil {
				return fmt.Errorf("save prs of %s: %w", c.Login, err)
			}
		}
	}
	// Tell the captain the draft is waiting, the ways they chose in settings.
	if _, err := jobs.Enqueue(ctx, q, jobs.NotifyCaptain, NotifyPayload{LaunchID: l.ID}, time.Time{}, &l.ID); err != nil {
		return fmt.Errorf("enqueue notify: %w", err)
	}
	return tx.Commit(ctx)
}

// NotifyPayload is the payload of jobs.NotifyCaptain.
type NotifyPayload struct {
	LaunchID uuid.UUID `json:"launch_id"`
}

func presetIndex(p cards.Preset) int16 {
	for i, q := range cards.Presets {
		if q == p {
			return int16(i)
		}
	}
	return 0
}

// soloCard is the release card for one person's release: their PRs as the
// lines, else their commit subjects.
func soloCard(spec LaunchSpec, lines []cards.PRLine, more int, brand cards.Brand, now time.Time) cards.SoloCard {
	so := spec.Solo
	if len(lines) == 0 {
		for _, sub := range so.Subjects {
			if len(lines) == 5 {
				break
			}
			lines = append(lines, cards.PRLine{Title: sub})
		}
		more = max(so.SubjectCount-len(lines), 0)
	}
	days := -1
	if !so.PrevAt.IsZero() {
		days = int(now.Sub(so.PrevAt).Hours() / 24)
	}
	return cards.SoloCard{
		Name: so.Name, Handle: so.Login, AvatarURL: so.AvatarURL,
		Project: spec.Project(), Version: spec.ReleaseTag,
		Commits: so.Commits, Added: so.Added, Deleted: so.Deleted,
		Release: lines, ReleaseMore: more, Date: now,
		PrevTag: so.PrevTag, DaysSince: days, Ordinal: so.Ordinal,
		FirstRelease: so.PrevTag == "", Major: isMajor(spec.ReleaseTag),
		Brand: brand,
	}
}

// isMajor reports a vN.0.0 tag with N of at least 1 ("v2.0.0", "2.0", "v1").
func isMajor(tag string) bool {
	t := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(tag), "v"), "V")
	parts := strings.Split(t, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return false
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 {
		return false
	}
	for _, p := range parts[1:] {
		if p != "0" {
			return false
		}
	}
	return true
}
