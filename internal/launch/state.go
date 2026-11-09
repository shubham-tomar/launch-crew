package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
)

// state is a launch with what the detail view and the jobs read around it.
type state struct {
	l       sqlc.Launch
	repo    *sqlc.Repo
	project string
	changes []change
	// The captain's GitHub login and id, when they verified one; the
	// login falls back to the repo owner.
	captain   string
	captainGH int64

	conn       *sqlc.BufferConnection // the captain's Buffer, nil if none
	channels   map[string]buffer.Channel
	channelsOK bool // channels came back from Buffer this run
	order      []buffer.Channel
	cfg        *buffer.Config
}

func load(ctx context.Context, d Deps, id uuid.UUID) (*state, error) {
	l, err := getLaunch(ctx, d.Q, id)
	if err != nil {
		return nil, err
	}
	s := &state{l: l, project: l.Title}
	if s.changes, err = changesOf(l); err != nil {
		log.Printf("launch %s: %v", l.ID, err)
	}
	if l.RepoID.Valid {
		r, err := d.Q.GetRepo(ctx, l.RepoID.UUID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("launch: load repo: %w", err)
		}
		if err == nil {
			s.repo = &r
			owner, name, _ := strings.Cut(r.FullName, "/")
			s.project, s.captain = name, owner
		}
	}
	if gi, err := d.Q.GetGitHubIdentityByUser(ctx, l.OwnerUserID.UUID); err == nil {
		s.captain, s.captainGH = gi.Login, gi.GithubUserID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("launch: captain identity: %w", err)
	}
	return s, nil
}

// ownerConnection is the owner's first working Buffer connection, or nil.
func ownerConnection(ctx context.Context, d Deps, userID uuid.UUID) (*sqlc.BufferConnection, error) {
	conns, err := d.Q.ListConnectionsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range conns {
		if conns[i].Status == "ok" {
			return &conns[i], nil
		}
	}
	return nil, nil
}

// withBuffer loads the captain's connection, channels and rules. A missing
// connection or an unanswering Buffer leaves them empty; preflight reports it.
func (s *state) withBuffer(ctx context.Context, d Deps) {
	s.channels = map[string]buffer.Channel{}
	conn, err := ownerConnection(ctx, d, s.l.OwnerUserID.UUID)
	if err != nil {
		log.Printf("launch %s: captain connections: %v", s.l.ID, err)
		return
	}
	s.conn = conn
	if s.conn == nil || d.Buffer == nil {
		return
	}
	chs, err := d.Buffer.Channels(ctx, s.conn.ID)
	if err != nil {
		log.Printf("launch %s: captain channels: %v", s.l.ID, err)
		return
	}
	cfg, err := d.Buffer.Configuration(ctx, s.conn.ID)
	if err != nil {
		log.Printf("launch %s: configuration: %v", s.l.ID, err)
	} else {
		s.cfg = &cfg
	}
	s.order, s.channelsOK = chs, true
	for _, ch := range chs {
		s.channels[ch.ID] = ch
	}
}

func (s *state) version() string { return s.l.ReleaseTag.String }

// crewSize is how many different people shipped the changes in this launch.
func (s *state) crewSize() int {
	seen := map[int64]bool{}
	for _, c := range s.changes {
		if c.AuthorID != 0 {
			seen[c.AuthorID] = true
		}
	}
	return len(seen)
}

func (s *state) headline() string {
	for _, c := range s.changes {
		if !c.Hidden && c.Title != "" {
			return c.Title
		}
	}
	return s.l.Summary
}

// login is a contributor's GitHub login from the release, else their verified identity.
func (s *state) login(ctx context.Context, d Deps, ghID int64) string {
	for _, c := range s.changes {
		if c.AuthorID == ghID && c.AuthorLogin != "" {
			return c.AuthorLogin
		}
	}
	if gi, err := d.Q.GetGitHubIdentity(ctx, ghID); err == nil {
		return gi.Login
	}
	return ""
}

// displayName is a contributor's GitHub profile name, cached on the crew row
// the first time it is fetched; the login when they set none or GitHub is
// unreachable.
func (s *state) displayName(ctx context.Context, d Deps, m sqlc.CrewMember, login string) string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	if d.GitHub == nil || s.repo == nil || !m.GithubUserID.Valid {
		return login
	}
	name, err := d.GitHub.UserName(ctx, s.repo.InstallationID, m.GithubUserID.Int64)
	if err != nil {
		log.Printf("launch %s: profile name of %d: %v", s.l.ID, m.GithubUserID.Int64, err)
		return login
	}
	if name = strings.TrimSpace(name); name == "" {
		return login
	}
	if err := d.Q.SetCrewDisplayName(ctx, sqlc.SetCrewDisplayNameParams{ID: m.ID, DisplayName: name}); err != nil {
		log.Printf("launch %s: cache profile name: %v", s.l.ID, err)
	}
	return name
}

// releaseLines is the release's top changes for a card, captain-hidden ones
// left out, and how many more there are.
func (s *state) releaseLines() ([]cards.PRLine, int) {
	return cardLines(s.changes)
}

// cardLines keeps the first five visible changes and counts the rest.
func cardLines(changes []change) ([]cards.PRLine, int) {
	var out []cards.PRLine
	more := 0
	for _, c := range changes {
		if c.Hidden || strings.TrimSpace(c.Title) == "" {
			continue
		}
		if len(out) == 5 {
			more++
			continue
		}
		out = append(out, cards.PRLine{Number: c.PRNumber, Title: c.Title})
	}
	return out, more
}

// brand is the repo's card branding; none for a manual launch.
func (s *state) brand() cards.Brand {
	if s.repo == nil {
		return cards.Brand{}
	}
	return cards.RepoBrand(s.repo.FullName, s.repo.LogoPng, s.repo.Accent, s.repo.CardBg, s.repo.CardText)
}

// isCaptain reports a crew row that is the captain themselves.
func (s *state) isCaptain(m sqlc.CrewMember) bool {
	return s.captainGH != 0 && m.GithubUserID.Valid && m.GithubUserID.Int64 == s.captainGH
}

// cardAsset is the image captain posts carry: the hero, else the release card.
func (s *state) cardAsset(ctx context.Context, d Deps) (sqlc.LaunchAsset, bool) {
	if s.l.HeroAssetID.Valid {
		if a, err := d.Q.GetLaunchAsset(ctx, s.l.HeroAssetID.UUID); err == nil {
			return a, true
		}
	}
	a, err := d.Q.GetLatestLaunchAssetByKind(ctx, sqlc.GetLatestLaunchAssetByKindParams{LaunchID: s.l.ID, Kind: "release"})
	return a, err == nil
}

// network maps a Buffer service to the drafting network.
func network(service string) drafting.Network {
	if s := strings.ToLower(service); s != "x" {
		return drafting.Network(s)
	}
	return drafting.Twitter
}

// ensureCaptainDrafts adds a template draft for each eligible captain
// channel that has none. Existing drafts are never touched.
func ensureCaptainDrafts(ctx context.Context, d Deps, s *state) error {
	if s.conn == nil || len(s.order) == 0 {
		return nil
	}
	existing, err := d.Q.ListCaptainDrafts(ctx, s.l.ID)
	if err != nil {
		return fmt.Errorf("launch: list captain drafts: %w", err)
	}
	have := map[string]bool{}
	for _, cd := range existing {
		have[cd.ChannelID] = true
	}
	asset, hasAsset := s.cardAsset(ctx, d)
	cfg := buffer.Config{}
	if s.cfg != nil {
		cfg = *s.cfg
	}
	var hooks []drafting.HookType
	for _, ch := range s.order {
		if ok, _ := buffer.Eligible(ch, cfg, hasAsset); !ok || have[ch.ID] {
			continue
		}
		n := network(ch.Service)
		hook := drafting.AssignHookFrom(drafting.AnnouncementHooks, hooks)
		hooks = append(hooks, hook)
		req := drafting.DraftRequest{
			Subject: drafting.Subject{Project: s.project, Version: s.version(), URL: s.l.ReleaseUrl.String, Headline: s.headline()},
			Hook:    hook, Network: n, Rules: drafting.RulesFor(n, s.cfg),
		}
		td, err := drafting.Template{}.Draft(ctx, req)
		if err != nil {
			return fmt.Errorf("launch: captain draft: %w", err)
		}
		f := drafting.FitToRules(td, req.Rules)
		var thread []byte
		if len(f.Thread) > 0 {
			thread, _ = json.Marshal(f.Thread)
		}
		arg := sqlc.InsertCaptainDraftIfAbsentParams{
			LaunchID: s.l.ID, ChannelID: ch.ID, Text: f.Text, Thread: thread,
			FirstComment: pgtype.Text{String: f.FirstComment, Valid: f.FirstComment != ""},
		}
		if hasAsset {
			arg.AssetID = uuid.NullUUID{UUID: asset.ID, Valid: true}
		}
		if _, err := d.Q.InsertCaptainDraftIfAbsent(ctx, arg); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("launch: save captain draft: %w", err)
		}
	}
	return nil
}

func threadOf(raw []byte) []string {
	var t []string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &t); err != nil {
			log.Printf("launch: captain draft thread: %v", err)
		}
	}
	return t
}

func (d Deps) preflightDeps() preflight.Deps {
	p := d.Preflight
	if p.Fetch == nil {
		p.Fetch = preflight.HTTPFetch
	}
	if p.Limits == nil && d.Buffer != nil {
		p.Limits = d.Buffer.DailyPostingLimits
	}
	if p.Config == nil && d.Buffer != nil {
		p.Config = d.Buffer.Configuration
	}
	if p.RateHeadroom == nil {
		p.RateHeadroom = RateHeadroom(d.Q, d.now)
	}
	if p.Now == nil {
		p.Now = d.Now
	}
	return p
}

// runPreflight maps the launch into preflight: the release card and hero as
// assets, one draft per captain channel, and the calls Launch will make
// (one per captain post, plus two).
func runPreflight(ctx context.Context, d Deps, s *state, drafts []sqlc.CaptainDraft) ([]preflight.Check, bool) {
	// A post the captain removed for a channel is not sent, so it is not checked or counted.
	drafts = slices.DeleteFunc(slices.Clone(drafts), func(cd sqlc.CaptainDraft) bool { return cd.Status == captainSkipped })
	pl := preflight.Launch{ID: s.l.ID, LaunchAt: s.l.LaunchAt.Time, ExpectedCalls: len(drafts) + 2}
	if s.conn != nil {
		pl.ConnectionID = s.conn.ID
	}
	if a, err := d.Q.GetLatestLaunchAssetByKind(ctx, sqlc.GetLatestLaunchAssetByKindParams{LaunchID: s.l.ID, Kind: "release"}); err == nil {
		pl.Assets = append(pl.Assets, preflight.Asset{URL: a.R2Url, Label: "Release card"})
	}
	if s.l.HeroAssetID.Valid {
		if a, err := d.Q.GetLaunchAsset(ctx, s.l.HeroAssetID.UUID); err == nil {
			pl.Assets = append(pl.Assets, preflight.Asset{URL: a.R2Url, Label: "Hero image"})
		}
	}
	for _, cd := range drafts {
		ch := s.channels[cd.ChannelID]
		n := network(ch.Service)
		pl.CaptainDrafts = append(pl.CaptainDrafts, preflight.Draft{
			ChannelID: cd.ChannelID, Service: ch.Service, Network: string(n), Name: ch.Name,
			Text: cd.Text, Thread: threadOf(cd.Thread), FirstComment: cd.FirstComment.String,
			HasAsset: cd.AssetID.Valid && drafting.RulesFor(n, s.cfg).ImageOK,
		})
	}
	return preflight.Run(ctx, d.preflightDeps(), pl)
}
