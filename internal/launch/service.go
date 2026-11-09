// Package launch is the captain's side: manual launches, edits, the detail
// view, the Launch action and the jobs it starts (captain posts, crew
// invites, PR comments, window close and status poll).
package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/mail"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

var (
	ErrNotFound        = errors.New("launch: not found")
	ErrAlreadyLaunched = errors.New("launch: already launched")
	ErrNotDraft        = errors.New("launch: only a draft launch can be edited")
	ErrInvalid         = errors.New("launch: invalid")
	ErrNoBuffer        = errors.New("launch: no Buffer connection")
)

const (
	statusDraft    = "draft"
	statusLaunched = "launched"
	statusClosed   = "closed"
	statusDeleted  = "deleted"

	// stateOptedOut marks a crew row whose person opted out; never commented on.
	stateOptedOut = "opted_out"

	maxLead       = 30 * 24 * time.Hour
	minWindow     = 5
	maxWindow     = 720
	maxCrewCap    = 1000
	defaultWindow = 90
	defaultCap    = 200
)

// Deps are what the launch service and its jobs need.
type Deps struct {
	Q      *sqlc.Queries
	Pool   *pgxpool.Pool
	Buffer *buffer.Client
	GitHub *github.Client
	Cards  trigger.CardStore
	// Images stores captain uploads; Cards when nil and it can.
	Images ImageStore
	// Render draws cards; cards.Render when nil.
	Render    func(c any, p cards.Preset) ([]byte, error)
	Preflight preflight.Deps
	Keyring   *crypto.Keyring
	BaseURL   string           // public origin for invite and open links
	Now       func() time.Time // time.Now when nil
	// TokenTTL is how long an invite token works; 30 days when zero.
	TokenTTL time.Duration
	// PostTimeout bounds one createPost; 15 s when zero.
	PostTimeout time.Duration
	// AllowLoopback lets the hero fetch reach loopback. Tests only.
	AllowLoopback bool
	// Mail sends the captain's "draft ready" email; nil sends none.
	Mail mail.Sender
}

// RefreshChannels re-reads the owner's Buffer channels, bypassing the cache,
// and returns how many there are.
func RefreshChannels(ctx context.Context, d Deps, id uuid.UUID) (int, error) {
	l, err := getLaunch(ctx, d.Q, id)
	if err != nil {
		return 0, err
	}
	conns, err := d.Q.ListConnectionsByUser(ctx, l.OwnerUserID.UUID)
	if err != nil {
		return 0, err
	}
	var conn *sqlc.BufferConnection
	for i := range conns {
		if conns[i].Status == "ok" {
			conn = &conns[i]
			break
		}
	}
	if conn == nil || d.Buffer == nil {
		return 0, ErrNoBuffer
	}
	chs, err := d.Buffer.RefreshChannels(ctx, conn.ID)
	if err != nil {
		return 0, err
	}
	return len(chs), nil
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) render() func(any, cards.Preset) ([]byte, error) {
	if d.Render != nil {
		return d.Render
	}
	return cards.Render
}

func (d Deps) tokenTTL() time.Duration {
	if d.TokenTTL > 0 {
		return d.TokenTTL
	}
	return 30 * 24 * time.Hour
}

func (d Deps) link(path string) string { return strings.TrimRight(d.BaseURL, "/") + path }

// invalidError is a validation failure with a sentence for the captain.
type invalidError struct{ msg string }

func (e invalidError) Error() string        { return "launch: invalid: " + e.msg }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, a ...any) error { return invalidError{fmt.Sprintf(format, a...)} }

// Message is the captain-facing sentence of an ErrInvalid error.
func Message(err error) string {
	var ie invalidError
	if errors.As(err, &ie) {
		return ie.msg
	}
	return ""
}

// Summary is one launch in the captain's list.
type Summary struct {
	ID            uuid.UUID  `json:"id"`
	Status        string     `json:"status"`
	Source        string     `json:"source"`
	Title         string     `json:"title"`
	ReleaseTag    string     `json:"releaseTag"`
	LaunchAt      *time.Time `json:"launchAt"`
	WindowMinutes int        `json:"windowMinutes"`
	OpenCount     int        `json:"openCount"`
	CreatedAt     time.Time  `json:"createdAt"`
	// Repo is the full repo name of a GitHub launch, empty for a manual one.
	Repo string `json:"repo"`
	// PRCount is the headline changes that are not hidden.
	PRCount int `json:"prCount"`
	// CrewIn and CrewTotal count the crew as Detail's counts do; the captain is not crew.
	CrewIn    int `json:"crewIn"`
	CrewTotal int `json:"crewTotal"`
	// CrewInitials is up to five crew members' initials, people who are in first.
	CrewInitials []string `json:"crewInitials"`
	// CaptainName is the captain's GitHub login, else the repo owner, else "".
	CaptainName string `json:"captainName"`
}

func summaryOf(l sqlc.Launch) Summary {
	return Summary{
		ID: l.ID, Status: l.Status, Source: l.Source, Title: l.Title, ReleaseTag: l.ReleaseTag.String,
		LaunchAt: timePtr(l.LaunchAt), WindowMinutes: int(l.WindowMinutes), OpenCount: int(l.OpenCount),
		CreatedAt: l.CreatedAt.Time.UTC(), CrewInitials: []string{},
	}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

// List returns the launches the user owns or whose repo they own (the
// same rule as Authorize), newest first.
func List(ctx context.Context, d Deps, userID uuid.UUID) ([]Summary, error) {
	ls, err := d.Q.ListLaunchesForUser(ctx, uuid.NullUUID{UUID: userID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("launch: list: %w", err)
	}
	out := make([]Summary, 0, len(ls))
	launches := make([]sqlc.Launch, len(ls))
	for i, r := range ls {
		launches[i] = r.Launch
		out = append(out, summaryOf(r.Launch))
	}
	if err := withCrew(ctx, d, launches, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Authorize returns the launch when userID owns it or owns its repo;
// otherwise ErrNotFound, so its existence is not revealed.
func Authorize(ctx context.Context, d Deps, id, userID uuid.UUID) (sqlc.Launch, error) {
	l, err := getLaunch(ctx, d.Q, id)
	if err != nil {
		return l, err
	}
	if userID != uuid.Nil && l.OwnerUserID.Valid && l.OwnerUserID.UUID == userID {
		return l, nil
	}
	if userID != uuid.Nil && l.RepoID.Valid {
		if r, err := d.Q.GetRepo(ctx, l.RepoID.UUID); err == nil && r.OwnerUserID.Valid && r.OwnerUserID.UUID == userID {
			return l, nil
		}
	}
	return sqlc.Launch{}, ErrNotFound
}

func getLaunch(ctx context.Context, q *sqlc.Queries, id uuid.UUID) (sqlc.Launch, error) {
	l, err := q.GetLaunch(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && l.Status == statusDeleted) {
		return sqlc.Launch{}, ErrNotFound
	}
	if err != nil {
		return l, fmt.Errorf("launch: load: %w", err)
	}
	return l, nil
}

// CreateManual saves a draft manual launch: the form is checked by
// trigger.Manual, the preset comes from the launch id, the release card is
// rendered from the title, and the open token is stored hashed and
// encrypted. A hero URL is fetched through the address guard, re-encoded
// and re-uploaded as a hero asset.
func CreateManual(ctx context.Context, d Deps, ownerID uuid.UUID, in trigger.ManualInput) (Summary, string, error) {
	hero := strings.TrimSpace(in.HeroURL)
	in.HeroURL = "" // fetched below, through the guard, never by trigger.Manual
	spec, err := trigger.Manual{}.Build(ctx, trigger.Input{Manual: &in})
	if err != nil {
		return Summary{}, "", err
	}
	var heroPNG []byte
	if hero != "" {
		if heroPNG, err = fetchHero(ctx, hero, d.AllowLoopback); err != nil {
			return Summary{}, "", err
		}
	}
	logo, err := prepareLogo(ctx, d, in.QRLogo, in.QRLogoURL)
	if err != nil {
		return Summary{}, "", err
	}
	tmpl, err := templateRef(ctx, d, ownerID, in.TemplateID)
	if err != nil {
		return Summary{}, "", err
	}
	roles, err := json.Marshal(spec.Roles)
	if err != nil {
		return Summary{}, "", err
	}
	tok, hash := crew.NewToken()
	enc, _, err := d.Keyring.Encrypt([]byte(tok))
	if err != nil {
		return Summary{}, "", fmt.Errorf("launch: encrypt open token: %w", err)
	}

	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return Summary{}, "", err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := sqlc.New(tx)
	l, err := q.CreateLaunch(ctx, sqlc.CreateLaunchParams{
		OwnerUserID: ownerID, Source: "manual", Title: spec.Title, Summary: spec.Summary,
		HeadlineChanges: []byte(`[]`), Roles: roles, WindowMinutes: defaultWindow, CrewCap: defaultCap,
		TemplateID: tmpl,
	})
	if err != nil {
		return Summary{}, "", fmt.Errorf("launch: create: %w", err)
	}
	preset := cards.PresetFor(l.ID)
	headline := spec.Summary
	if headline == "" {
		headline = spec.Title
	}
	card, err := d.render()(cards.ReleaseCard{Project: spec.Title, Headline: headline, Date: d.now()}, preset)
	if err != nil {
		return Summary{}, "", fmt.Errorf("launch: render release card: %w", err)
	}
	if _, err := putAsset(ctx, d, q, l.ID, "release", card); err != nil {
		return Summary{}, "", err
	}
	if heroPNG != nil {
		a, err := putAsset(ctx, d, q, l.ID, "hero", heroPNG)
		if err != nil {
			return Summary{}, "", err
		}
		if err := q.SetLaunchHeroAsset(ctx, sqlc.SetLaunchHeroAssetParams{ID: l.ID, HeroAssetID: uuid.NullUUID{UUID: a.ID, Valid: true}}); err != nil {
			return Summary{}, "", err
		}
	}
	if err := writeLogo(ctx, d, tx, q, l.ID, logo); err != nil {
		return Summary{}, "", err
	}
	idx := presetIndex(preset)
	if err := q.SetLaunchCardPreset(ctx, sqlc.SetLaunchCardPresetParams{ID: l.ID, CardPreset: pgtype.Int2{Int16: idx, Valid: true}}); err != nil {
		return Summary{}, "", err
	}
	if err := q.SetLaunchOpenTokens(ctx, sqlc.SetLaunchOpenTokensParams{ID: l.ID, OpenTokenHash: hash, OpenTokenEnc: enc}); err != nil {
		return Summary{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return Summary{}, "", err
	}
	l.CardPreset = pgtype.Int2{Int16: idx, Valid: true}
	return summaryOf(l), tok, nil
}

// putAsset stores a rendered PNG with its size and, when it is over
// Bluesky's limit, a small copy, so a hero or card posts to Bluesky too.
func putAsset(ctx context.Context, d Deps, q *sqlc.Queries, launchID uuid.UUID, kind string, png []byte) (sqlc.LaunchAsset, error) {
	st, err := d.Cards.PutCard(ctx, png)
	if err != nil {
		return sqlc.LaunchAsset{}, fmt.Errorf("launch: upload %s: %w", kind, err)
	}
	a, err := q.InsertLaunchAsset(ctx, sqlc.InsertLaunchAssetParams{
		LaunchID: launchID, Kind: kind, R2Url: st.URL, Sha: st.Sha, Bytes: st.Bytes, SmallUrl: st.Small,
	})
	if err != nil {
		return a, fmt.Errorf("launch: save %s asset: %w", kind, err)
	}
	return a, nil
}

func presetIndex(p cards.Preset) int16 {
	for i, q := range cards.Presets {
		if q == p {
			return int16(i)
		}
	}
	return 0
}

// LaunchPayload is the payload of the LaunchJob, CloseWindow and PollStatus jobs.
type LaunchPayload struct {
	LaunchID uuid.UUID
}

// Launch moves a draft launch to launched and enqueues its LaunchJob in one
// transaction. A second press finds no draft and returns ErrAlreadyLaunched
// without enqueuing anything.
//
// A draft whose preflight is red is refused with a PreflightError and stays
// a draft, so the captain can fix it; LaunchJob checks again before posting.
func Launch(ctx context.Context, d Deps, id uuid.UUID) error {
	if err := checkBeforeLaunch(ctx, d, id); err != nil {
		return err
	}
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := sqlc.New(tx)
	if _, err := q.MarkLaunched(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		if _, err := getLaunch(ctx, d.Q, id); err != nil {
			return err
		}
		return ErrAlreadyLaunched
	} else if err != nil {
		return fmt.Errorf("launch: mark launched: %w", err)
	}
	if _, err := jobs.Enqueue(ctx, q, jobs.LaunchJob, LaunchPayload{LaunchID: id}, time.Time{}, &id); err != nil {
		return fmt.Errorf("launch: enqueue: %w", err)
	}
	return tx.Commit(ctx)
}

// PreflightError lists the red checks that stopped a launch.
type PreflightError struct{ Red []string }

func (e *PreflightError) Error() string {
	return "preflight not green: " + strings.Join(e.Red, ", ")
}

func redNames(checks []preflight.Check) []string {
	var red []string
	for _, c := range checks {
		if !c.OK {
			red = append(red, c.Name)
		}
	}
	return red
}

func checkBeforeLaunch(ctx context.Context, d Deps, id uuid.UUID) error {
	s, err := load(ctx, d, id)
	if err != nil || s.l.Status != statusDraft {
		return err // a launched one gets ErrAlreadyLaunched below
	}
	s.withBuffer(ctx, d)
	if err := ensureCaptainDrafts(ctx, d, s); err != nil {
		return err
	}
	drafts, err := d.Q.ListCaptainDrafts(ctx, id)
	if err != nil {
		return fmt.Errorf("launch: list captain drafts: %w", err)
	}
	if checks, ok := runPreflight(ctx, d, s, drafts); !ok {
		return &PreflightError{Red: redNames(checks)}
	}
	return nil
}

// Retry revives a dead job of the launch; any other job is ErrNotFound.
func Retry(ctx context.Context, d Deps, launchID uuid.UUID, jobID int64) error {
	j, err := d.Q.GetJob(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!j.Dead || !j.LaunchID.Valid || j.LaunchID.UUID != launchID)) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("launch: load job: %w", err)
	}
	return jobs.Retry(ctx, d.Q, jobID)
}

// RateHeadroom is a preflight.Deps.RateHeadroom reading the connection's
// last seen rate budget; a connection with none, or past its reset, has the
// full window of 100 calls.
func RateHeadroom(q *sqlc.Queries, now func() time.Time) func(context.Context, uuid.UUID) (int, error) {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, connID uuid.UUID) (int, error) {
		c, err := q.GetBufferConnection(ctx, connID)
		if err != nil {
			return 0, err
		}
		if !c.RateRemaining.Valid || (c.RateResetAt.Valid && !c.RateResetAt.Time.After(now())) {
			return 100, nil
		}
		return int(c.RateRemaining.Int32), nil
	}
}
