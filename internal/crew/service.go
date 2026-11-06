package crew

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
	"github.com/shubham-tomar/launchcrew/internal/users"
)

var (
	ErrNotFound       = errors.New("crew: not found")
	ErrExpired        = errors.New("crew: invite link expired")
	ErrRateLimited    = errors.New("crew: too many opens from this address, wait a minute")
	ErrCrewFull       = errors.New("crew: this launch's crew is full")
	ErrInvalidRole    = errors.New("crew: pick one of the roles, or Other with a short hint")
	ErrAlreadyQueued  = errors.New("crew: this draft is already in Buffer")
	ErrNotConnected   = errors.New("crew: no Buffer connection")
	ErrBuffer         = errors.New("crew: Buffer is not answering")
	ErrAlreadyClaimed = errors.New("crew: this invite was already connected by its owner")
	ErrInvalidVoice   = errors.New("crew: invalid voice")
	ErrNotVerified    = errors.New("crew: verify it's you with GitHub first, then connect Buffer")
)

// Crew member states. invited, suggested and ticked come before the first open.
const (
	stateInvited   = "invited"
	stateSuggested = "suggested"
	stateTicked    = "ticked"
	stateOpened    = "opened"
	stateConnected = "connected"
	stateCopied    = "copied"
	stateQueued    = "queued"
	statePosted    = "posted"

	launchLaunched = "launched"
	launchClosed   = "closed"

	maxHintRunes  = 40
	maxTalkRunes  = 120
	avoidInPrompt = 5
	voiceFresh    = 30 * 24 * time.Hour
	neutralLine   = "neutral voice"
)

// AssetView is an image with its alt text.
type AssetView struct {
	URL string `json:"url"`
	Alt string `json:"alt"`
}

// LaunchSummary is the launch as the invite page shows it.
type LaunchSummary struct {
	ID            uuid.UUID  `json:"id"`
	Title         string     `json:"title"`
	Summary       string     `json:"summary"`
	Project       string     `json:"project"`
	Version       string     `json:"version"`
	ReleaseURL    string     `json:"releaseUrl"`
	Status        string     `json:"status"`
	LaunchAt      *time.Time `json:"launchAt"`
	WindowMinutes int        `json:"windowMinutes"`
	Source        string     `json:"source"`
	ShortURL      string     `json:"shortUrl"` // the /l/ link of a manual launch, else ""
}

// MemberSummary is the crew member as the invite page shows them.
type MemberSummary struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	RoleHint string    `json:"roleHint"`
	State    string    `json:"state"`
	Verified bool      `json:"verified"`
}

// View is the invite page.
type View struct {
	Launch   LaunchSummary `json:"launch"`
	Member   MemberSummary `json:"member"`
	Card     *AssetView    `json:"card"`
	Preview  string        `json:"preview"`
	Verified bool          `json:"verified"`
	Foreign  bool          `json:"foreign"` // set by the API from ?foreign=1
	CopyText string        `json:"copyText"`
	// NeedsVerify: a GitHub invite nobody has verified yet; the page leads
	// with "Verify it's you".
	NeedsVerify bool `json:"needsVerify"`
	// Connected: the caller's session already holds a working Buffer
	// connection that belongs to this member; the page offers Continue instead
	// of sending them through Buffer's consent again. Set by the API.
	Connected bool `json:"connected"`
}

// LaunchView is the public open-link page.
type LaunchView struct {
	Title     string         `json:"title"`
	Summary   string         `json:"summary"`
	Roles     []trigger.Role `json:"roles"`
	Card      AssetView      `json:"card"`
	ShortURL  string         `json:"shortUrl"`
	OpenCount int            `json:"openCount"`
}

// ChannelView is one Buffer channel on the approve screen.
type ChannelView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Service  string `json:"service"`
	Avatar   string `json:"avatar"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
	Checked  bool   `json:"checked"`
}

// DraftView is one per-channel draft.
type DraftView struct {
	ID           uuid.UUID `json:"id"`
	ChannelID    string    `json:"channelId"`
	Network      string    `json:"network"`
	Text         string    `json:"text"`
	Thread       []string  `json:"thread"`
	FirstComment string    `json:"firstComment"`
	FromModel    bool      `json:"fromModel"`
	Edited       bool      `json:"edited"`
	Flagged      bool      `json:"flagged"`
	Status       string    `json:"status"`
	BufferPostID string    `json:"bufferPostId"`
}

// ApproveView is the approve screen after a Buffer connect. MemberID can
// differ from the row that connected: a second device gets the first row.
type ApproveView struct {
	MemberID       uuid.UUID        `json:"memberId"`
	Channels       []ChannelView    `json:"channels"`
	Drafts         []DraftView      `json:"drafts"`
	Voice          drafting.Profile `json:"voice"`
	VoiceLine      string           `json:"voiceLine"`
	Timing         []string         `json:"timing"`
	Closed         bool             `json:"closed"`
	NeedsReconnect bool             `json:"needsReconnect"`
	VoicedReady    bool             `json:"voicedReady"`
}

// BoardRow is one person on the crew board.
type BoardRow struct {
	Name  string     `json:"name"`
	Role  string     `json:"role"`
	State string     `json:"state"`
	When  *time.Time `json:"when"`
}

// BoardView is the launch's crew board. In counts queued, posted and copied.
type BoardView struct {
	Rows  []BoardRow `json:"rows"`
	In    int        `json:"in"`
	Total int        `json:"total"`
}

// VoicedPayload is the VoicedDrafts job payload.
type VoicedPayload struct {
	MemberID uuid.UUID
	Force    bool
}

// change mirrors the stored headline_changes entries.
type change struct {
	PRNumber    int    `json:"pr_number"`
	Title       string `json:"title"`
	AuthorID    int64  `json:"author_id"`
	AuthorLogin string `json:"author_login"`
	Hidden      bool   `json:"hidden"`
}

type launchInfo struct {
	l       sqlc.Launch
	project string
	changes []change
}

func (i launchInfo) version() string { return i.l.ReleaseTag.String }

func (i launchInfo) headline() string {
	for _, c := range i.changes {
		if !c.Hidden && c.Title != "" {
			return c.Title
		}
	}
	return i.l.Summary
}

func (i launchInfo) live() bool { return i.l.Status == launchLaunched || i.l.Status == launchClosed }

func loadLaunch(ctx context.Context, d Deps, id uuid.UUID) (launchInfo, error) {
	l, err := d.Q.GetLaunch(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return launchInfo{}, ErrNotFound
	}
	if err != nil {
		return launchInfo{}, fmt.Errorf("crew: load launch: %w", err)
	}
	return infoOf(ctx, d, l), nil
}

func infoOf(ctx context.Context, d Deps, l sqlc.Launch) launchInfo {
	info := launchInfo{l: l, project: l.Title}
	if l.RepoID.Valid {
		if r, err := d.Q.GetRepo(ctx, l.RepoID.UUID); err == nil {
			if _, name, ok := strings.Cut(r.FullName, "/"); ok {
				info.project = name
			}
		}
	}
	if len(l.HeadlineChanges) > 0 {
		if err := json.Unmarshal(l.HeadlineChanges, &info.changes); err != nil {
			log.Printf("crew: launch %s headline changes: %v", l.ID, err)
		}
	}
	return info
}

func getMember(ctx context.Context, d Deps, id uuid.UUID) (sqlc.CrewMember, error) {
	m, err := d.Q.GetCrewMember(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, fmt.Errorf("crew: load member: %w", err)
	}
	return m, nil
}

func rolesOf(l sqlc.Launch) []trigger.Role {
	var roles []trigger.Role
	if len(l.Roles) > 0 {
		if err := json.Unmarshal(l.Roles, &roles); err != nil {
			log.Printf("crew: launch %s roles: %v", l.ID, err)
		}
	}
	if len(roles) == 0 {
		return trigger.DefaultRoles
	}
	return roles
}

func (d Deps) drafter() *drafting.Service {
	if d.Drafting != nil {
		return d.Drafting
	}
	return &drafting.Service{}
}

// memberPRs is every PR credited to the member, newest first. A member from
// before several PRs were stored has just pr_number and pr_title.
func memberPRs(ctx context.Context, d Deps, m sqlc.CrewMember) []trigger.PRRef {
	if d.Pool == nil {
		return trigger.MemberPRs(ctx, nil, m.ID, m.PrNumber.Int32, m.PrTitle.String)
	}
	return trigger.MemberPRs(ctx, d.Pool, m.ID, m.PrNumber.Int32, m.PrTitle.String)
}

func subject(ctx context.Context, d Deps, info launchInfo, m sqlc.CrewMember) drafting.Subject {
	s := drafting.Subject{
		Project: info.project, Version: info.version(), URL: info.l.ReleaseUrl.String,
		Headline: info.headline(), PRTitle: m.PrTitle.String,
		Role: m.Role, RoleHint: m.RoleHint, TalkTitle: m.TalkTitle,
	}
	if prs := memberPRs(ctx, d, m); len(prs) > 1 {
		for _, p := range prs {
			s.PRTitles = append(s.PRTitles, p.Title)
		}
	}
	return s
}

// memberName is the contributor's GitHub login when known, else the role.
func memberName(ctx context.Context, d Deps, info launchInfo, m sqlc.CrewMember, lookup bool) string {
	if !m.GithubUserID.Valid {
		return m.Role
	}
	for _, c := range info.changes {
		if c.AuthorID == m.GithubUserID.Int64 && c.AuthorLogin != "" {
			return c.AuthorLogin
		}
	}
	if lookup {
		if id, err := d.Q.GetGitHubIdentity(ctx, m.GithubUserID.Int64); err == nil {
			return id.Login
		}
	}
	return m.Role
}

func summaryOf(d Deps, info launchInfo) LaunchSummary {
	s := LaunchSummary{
		ID: info.l.ID, Title: info.l.Title, Summary: info.l.Summary, Project: info.project,
		Version: info.version(), ReleaseURL: info.l.ReleaseUrl.String, Status: info.l.Status,
		WindowMinutes: int(info.l.WindowMinutes), Source: info.l.Source,
	}
	if info.l.LaunchAt.Valid {
		t := info.l.LaunchAt.Time.UTC()
		s.LaunchAt = &t
	}
	if info.l.Source == "manual" && len(info.l.OpenTokenEnc) > 0 && d.Keyring != nil {
		if tok, err := d.Keyring.Decrypt(info.l.OpenTokenEnc); err == nil {
			s.ShortURL = strings.TrimRight(d.BaseURL, "/") + "/l/" + string(tok)
		} else {
			log.Printf("crew: open token for %s: %v", info.l.ID, err)
		}
	}
	return s
}

// releaseCard is the launch's release card, if one was rendered.
func releaseCard(ctx context.Context, d Deps, info launchInfo) *AssetView {
	a, err := d.Q.GetLatestLaunchAssetByKind(ctx, sqlc.GetLatestLaunchAssetByKindParams{LaunchID: info.l.ID, Kind: "release"})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("crew: release card for %s: %v", info.l.ID, err)
		}
		return nil
	}
	alt := joinNonEmpty(" ", info.project, info.version())
	if h := info.headline(); h != "" {
		alt += ": " + h
	}
	return &AssetView{URL: a.R2Url, Alt: alt}
}

// cardFor is the contributor card for a verified member, else the release card.
func cardFor(ctx context.Context, d Deps, info launchInfo, m sqlc.CrewMember, name string) *AssetView {
	if m.UserID.Valid && m.CardAssetID.Valid {
		if a, err := d.Q.GetLaunchAsset(ctx, m.CardAssetID.UUID); err == nil {
			alt := joinNonEmpty(", shipped in ", firstNonEmpty(name, "A contributor"), joinNonEmpty(" ", info.project, info.version()))
			if prs := memberPRs(ctx, d, m); len(prs) > 1 {
				alt += fmt.Sprintf(": %d PRs, including %s", len(prs), prs[0].Title)
			} else if m.PrTitle.String != "" {
				alt += ": " + m.PrTitle.String
			}
			return &AssetView{URL: a.R2Url, Alt: alt}
		}
	}
	return releaseCard(ctx, d, info)
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// neutralRules is the neutral target: X's length, one post, no thread.
func neutralRules() drafting.Rules {
	r := drafting.RulesFor(drafting.Twitter, nil)
	r.Threads = false
	return r
}

// templatePreview is the neutral preview before one has been generated.
func templatePreview(ctx context.Context, d Deps, info launchInfo, m sqlc.CrewMember) string {
	req := drafting.DraftRequest{Subject: subject(ctx, d, info, m), Hook: drafting.AssignHook(nil), Network: drafting.Twitter, Rules: neutralRules()}
	td, err := drafting.Template{}.Draft(ctx, req)
	if err != nil {
		return ""
	}
	return drafting.FitToRules(td, req.Rules).Text
}

// Open handles an invite link. The first open moves an invited, suggested
// or ticked row to opened; later states are never moved back.
func Open(ctx context.Context, d Deps, token string, _ net.IP) (View, error) {
	m, err := Resolve(ctx, d, token)
	if err != nil {
		return View{}, err
	}
	info, err := loadLaunch(ctx, d, m.LaunchID)
	if err != nil {
		return View{}, err
	}
	if !info.live() {
		return View{}, ErrNotFound
	}
	switch m.State {
	case stateInvited, stateSuggested, stateTicked:
		if err := d.Q.MarkCrewMemberOpened(ctx, sqlc.MarkCrewMemberOpenedParams{
			ID: m.ID, OpenedAt: pgtype.Timestamptz{Time: d.now(), Valid: true},
		}); err != nil {
			return View{}, fmt.Errorf("crew: mark opened: %w", err)
		}
		if m, err = getMember(ctx, d, m.ID); err != nil {
			return View{}, err
		}
	}
	return inviteView(ctx, d, info, m), nil
}

// ConnectedFor reports whether connectionID is a healthy Buffer connection
// that is this member's own: same Buffer account the row is bound to, and the
// same user when the row has one. It reveals nothing beyond the boolean.
func ConnectedFor(ctx context.Context, d Deps, memberID, connectionID uuid.UUID) bool {
	if connectionID == uuid.Nil {
		return false
	}
	m, err := getMember(ctx, d, memberID)
	if err != nil || !m.BufferAccountID.Valid || m.BufferAccountID.String == "" {
		return false
	}
	conn, err := d.Q.GetBufferConnection(ctx, connectionID)
	if err != nil || conn.Status == "needs_reconnect" {
		return false
	}
	if conn.BufferAccountID != m.BufferAccountID.String {
		return false
	}
	return !m.UserID.Valid || m.UserID.UUID == conn.UserID
}

func inviteView(ctx context.Context, d Deps, info launchInfo, m sqlc.CrewMember) View {
	name := memberName(ctx, d, info, m, true)
	preview := m.NeutralDraft.String
	if strings.TrimSpace(preview) == "" {
		preview = templatePreview(ctx, d, info, m)
	}
	verified := m.UserID.Valid
	return View{
		Launch: summaryOf(d, info),
		Member: MemberSummary{ID: m.ID, Name: name, Role: m.Role, RoleHint: m.RoleHint, State: m.State, Verified: verified},
		Card:   cardFor(ctx, d, info, m, name), Preview: preview, Verified: verified, CopyText: preview,
		NeedsVerify: unverified(m),
	}
}

// unverified is a GitHub invite row no one has proved yet. Only GitHub
// verification gives it a user: the link is public in the PR comment.
func unverified(m sqlc.CrewMember) bool {
	return m.GithubUserID.Valid && !m.UserID.Valid
}

// OpenLink handles a launch's public link: 10 opens per IP per minute,
// and no more opens once the crew is at its cap.
func OpenLink(ctx context.Context, d Deps, openToken string, ip net.IP) (LaunchView, error) {
	if openToken == "" {
		return LaunchView{}, ErrNotFound
	}
	l, err := d.Q.GetLaunchByOpenTokenHash(ctx, HashToken(openToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return LaunchView{}, ErrNotFound
	}
	if err != nil {
		return LaunchView{}, fmt.Errorf("crew: load launch: %w", err)
	}
	info := infoOf(ctx, d, l)
	if !info.live() {
		return LaunchView{}, ErrNotFound
	}
	if err := rateLimit(ctx, d, l.ID, ip); err != nil {
		return LaunchView{}, err
	}
	if err := checkCap(ctx, d, l); err != nil {
		return LaunchView{}, err
	}
	n, err := d.Q.IncrementLaunchOpenCount(ctx, l.ID)
	if err != nil {
		return LaunchView{}, fmt.Errorf("crew: count open: %w", err)
	}
	v := LaunchView{
		Title: l.Title, Summary: l.Summary, Roles: rolesOf(l),
		ShortURL: strings.TrimRight(d.BaseURL, "/") + "/l/" + openToken, OpenCount: int(n),
	}
	if c := releaseCard(ctx, d, info); c != nil {
		v.Card = *c
	}
	return v, nil
}

// SlideFor resolves an open token for the QR slide PNG and spends the same
// per-IP open budget as OpenLink. It does not count an open or check the cap.
func SlideFor(ctx context.Context, d Deps, openToken string, ip net.IP) (launchID uuid.UUID, title, shortURL string, err error) {
	if openToken == "" {
		return uuid.Nil, "", "", ErrNotFound
	}
	l, err := d.Q.GetLaunchByOpenTokenHash(ctx, HashToken(openToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", "", ErrNotFound
	}
	if err != nil {
		return uuid.Nil, "", "", fmt.Errorf("crew: load launch: %w", err)
	}
	if !infoOf(ctx, d, l).live() {
		return uuid.Nil, "", "", ErrNotFound
	}
	if err := rateLimit(ctx, d, l.ID, ip); err != nil {
		return uuid.Nil, "", "", err
	}
	return l.ID, l.Title, strings.TrimRight(d.BaseURL, "/") + "/l/" + openToken, nil
}

// SlideLogo is what sits over the centre of the launch's QR code: a mode
// (plane, none or custom) and, for custom, the URL of the stored image. A
// launch whose setting cannot be read gets the plane.
func SlideLogo(ctx context.Context, d Deps, launchID uuid.UUID) cards.SlideLogo {
	logo := cards.SlideLogo{Mode: cards.LogoPlane}
	if d.Pool == nil {
		return logo
	}
	var mode string
	if err := d.Pool.QueryRow(ctx, `SELECT qr_logo FROM launches WHERE id = $1`, launchID).Scan(&mode); err != nil {
		return logo
	}
	switch mode {
	case cards.LogoNone:
		logo.Mode = cards.LogoNone
	case cards.LogoCustom:
		if a, err := d.Q.GetLatestLaunchAssetByKind(ctx, sqlc.GetLatestLaunchAssetByKindParams{LaunchID: launchID, Kind: "qrlogo"}); err == nil {
			logo = cards.SlideLogo{Mode: cards.LogoCustom, URL: a.R2Url}
		}
	}
	return logo
}

// ChooseRole creates the crew row for someone who came in through the open
// link, in opened, with a fresh invite token. role must be one of the
// launch's roles, or Other with a hint of at most 40 characters. It shares
// OpenLink's 10 per IP per minute.
func ChooseRole(ctx context.Context, d Deps, launchID uuid.UUID, role, hint, talkTitle string, ip net.IP) (uuid.UUID, string, error) {
	info, err := loadLaunch(ctx, d, launchID)
	if err != nil {
		return uuid.Nil, "", err
	}
	if !info.live() {
		return uuid.Nil, "", ErrNotFound
	}
	// Each pick spends the same per-IP budget as an open.
	if err := rateLimit(ctx, d, launchID, ip); err != nil {
		return uuid.Nil, "", err
	}
	role, hint = strings.TrimSpace(role), strings.TrimSpace(hint)
	var name, roleHint string
	if strings.EqualFold(role, "Other") {
		if hint == "" || utf8.RuneCountInString(hint) > maxHintRunes {
			return uuid.Nil, "", ErrInvalidRole
		}
		name, roleHint = "Other", hint
	} else {
		for _, r := range rolesOf(info.l) {
			if role != "" && strings.EqualFold(r.Name, role) {
				name, roleHint = r.Name, r.Hint
				break
			}
		}
		if name == "" {
			return uuid.Nil, "", ErrInvalidRole
		}
	}
	talkTitle = strings.TrimSpace(talkTitle)
	if r := []rune(talkTitle); len(r) > maxTalkRunes {
		talkTitle = string(r[:maxTalkRunes])
	}
	if err := checkCap(ctx, d, info.l); err != nil {
		return uuid.Nil, "", err
	}
	tok, hash := NewToken()
	now := d.now()
	m, err := d.Q.InsertCrewMember(ctx, sqlc.InsertCrewMemberParams{
		LaunchID: launchID, Role: name, RoleHint: roleHint, TalkTitle: talkTitle,
		InviteTokenHash: hash, State: stateOpened,
		TokenExpiresAt: pgtype.Timestamptz{Time: now.Add(d.tokenTTL()), Valid: true},
		OpenedAt:       pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("crew: insert member: %w", err)
	}
	return m.ID, tok, nil
}

// NeutralDraft returns the member's neutral-voice draft, generating and
// storing it on the first call.
func NeutralDraft(ctx context.Context, d Deps, memberID uuid.UUID) (string, error) {
	m, err := getMember(ctx, d, memberID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(m.NeutralDraft.String) != "" {
		return m.NeutralDraft.String, nil
	}
	info, err := loadLaunch(ctx, d, m.LaunchID)
	if err != nil {
		return "", err
	}
	recent, err := d.Q.ListRecentQueuedTexts(ctx, sqlc.ListRecentQueuedTextsParams{
		LaunchID: info.l.ID, Network: string(drafting.Twitter), Limit: recentLimit,
	})
	if err != nil {
		return "", fmt.Errorf("crew: recent texts: %w", err)
	}
	voice := drafting.Neutral()
	res, err := d.drafter().DraftFor(ctx, drafting.DraftRequest{
		Subject: subject(ctx, d, info, m), Voice: &voice, Hook: drafting.AssignHook(nil),
		Network: drafting.Twitter, Rules: neutralRules(), AllowModel: true, Avoid: head(recent, avoidInPrompt),
	}, recent)
	if err != nil {
		return "", fmt.Errorf("crew: neutral draft: %w", err)
	}
	text := res.Draft.Text
	if err := d.Q.UpdateCrewMemberNeutralDraft(ctx, sqlc.UpdateCrewMemberNeutralDraftParams{
		ID: m.ID, NeutralDraft: pgtype.Text{String: text, Valid: true},
	}); err != nil {
		return "", fmt.Errorf("crew: save neutral draft: %w", err)
	}
	return text, nil
}

// Connect binds the member to a Buffer connection. If another row in the
// launch already has that Buffer account (a second device), that row's
// view is returned unchanged and this row is left for the sweep. Otherwise
// the row moves to connected, gets a template draft per eligible channel
// and a VoicedDrafts job.
//
// An unverified GitHub row cannot be connected (ErrNotVerified). A row
// already bound to a user is connected only by that user: the connection
// must belong to them, or sessionUser must be them, in which case the
// connection's user is merged into them. Anyone else gets ErrAlreadyClaimed.
func Connect(ctx context.Context, d Deps, memberID, connectionID, sessionUser uuid.UUID) (ApproveView, error) {
	m, err := getMember(ctx, d, memberID)
	if err != nil {
		return ApproveView{}, err
	}
	if unverified(m) {
		return ApproveView{}, ErrNotVerified
	}
	conn, err := d.Q.GetBufferConnection(ctx, connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApproveView{}, ErrNotConnected
	}
	if err != nil {
		return ApproveView{}, fmt.Errorf("crew: load connection: %w", err)
	}
	info, err := loadLaunch(ctx, d, m.LaunchID)
	if err != nil {
		return ApproveView{}, err
	}
	if !info.live() {
		return ApproveView{}, ErrNotFound
	}
	if other, ok, err := rowForAccount(ctx, d, info.l.ID, conn.BufferAccountID); err != nil {
		return ApproveView{}, err
	} else if ok && other.ID != m.ID {
		return approve(ctx, d, info, other, conn, false)
	}

	if m.UserID.Valid && m.UserID.UUID != conn.UserID {
		if sessionUser == uuid.Nil || sessionUser != m.UserID.UUID {
			return ApproveView{}, ErrAlreadyClaimed
		}
		// The owner proved both identities in one session: everything the
		// connection's user has moves to them.
		if err := users.Merge(ctx, d.Pool, conn.UserID, m.UserID.UUID); err != nil {
			return ApproveView{}, fmt.Errorf("crew: %w", err)
		}
		conn.UserID = m.UserID.UUID
	}

	state := stateConnected
	switch m.State {
	case stateQueued, statePosted, stateCopied:
		state = m.State
	}
	n, err := d.Q.UpdateCrewMemberConnection(ctx, sqlc.UpdateCrewMemberConnectionParams{
		ID: m.ID, UserID: uuid.NullUUID{UUID: conn.UserID, Valid: true},
		BufferAccountID: pgtype.Text{String: conn.BufferAccountID, Valid: true}, State: state,
	})
	if err == nil && n == 0 {
		if _, err := getMember(ctx, d, m.ID); err != nil {
			return ApproveView{}, err // ErrNotFound when swept meanwhile
		}
		return ApproveView{}, ErrAlreadyClaimed // bound by someone else meanwhile
	}
	if isUnique(err) {
		// Another device won the race for this account.
		other, ok, err := rowForAccount(ctx, d, info.l.ID, conn.BufferAccountID)
		if err != nil || !ok {
			return ApproveView{}, fmt.Errorf("crew: reload account row: %v", err)
		}
		return approve(ctx, d, info, other, conn, false)
	}
	if err != nil {
		return ApproveView{}, fmt.Errorf("crew: save connection: %w", err)
	}
	m.UserID = uuid.NullUUID{UUID: conn.UserID, Valid: true}
	m.BufferAccountID = pgtype.Text{String: conn.BufferAccountID, Valid: true}
	m.State = state
	return approve(ctx, d, info, m, conn, true)
}

// RefreshChannels re-reads the member's Buffer channels, bypassing the
// one-hour cache, and returns the approve screen again: a channel added in
// Buffer since the member connected appears, with a template draft. Calls
// within a few seconds of each other reuse the cache (see buffer.RefreshChannels).
// A member who has not connected, or whose connection is another user's, is
// ErrNotConnected.
func RefreshChannels(ctx context.Context, d Deps, memberID uuid.UUID) (ApproveView, error) {
	m, err := getMember(ctx, d, memberID)
	if err != nil {
		return ApproveView{}, err
	}
	if !m.BufferAccountID.Valid {
		return ApproveView{}, ErrNotConnected
	}
	conn, err := d.Q.GetBufferConnectionByAccount(ctx, m.BufferAccountID.String)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && m.UserID.Valid && conn.UserID != m.UserID.UUID) {
		return ApproveView{}, ErrNotConnected
	}
	if err != nil {
		return ApproveView{}, fmt.Errorf("crew: load connection: %w", err)
	}
	info, err := loadLaunch(ctx, d, m.LaunchID)
	if err != nil {
		return ApproveView{}, err
	}
	if !info.live() {
		return ApproveView{}, ErrNotFound
	}
	if conn.Status != "needs_reconnect" {
		if _, err := d.Buffer.RefreshChannels(ctx, conn.ID); err != nil && !errors.Is(err, buffer.ErrNeedsReconnect) {
			log.Printf("crew: refresh channels for %s: %v", m.ID, err)
			return ApproveView{}, ErrBuffer
		}
	}
	return approve(ctx, d, info, m, conn, true)
}

func rowForAccount(ctx context.Context, d Deps, launchID uuid.UUID, account string) (sqlc.CrewMember, bool, error) {
	m, err := d.Q.GetCrewMemberByAccount(ctx, sqlc.GetCrewMemberByAccountParams{
		LaunchID: launchID, BufferAccountID: pgtype.Text{String: account, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, fmt.Errorf("crew: member by account: %w", err)
	}
	return m, true, nil
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// approve builds the approve screen. With write it also creates missing
// template drafts and enqueues voiced drafts.
func approve(ctx context.Context, d Deps, info launchInfo, m sqlc.CrewMember, conn sqlc.BufferConnection, write bool) (ApproveView, error) {
	v := ApproveView{
		MemberID: m.ID, Closed: info.l.Status == launchClosed, Timing: []string{"window", "queue", "draft"},
		NeedsReconnect: conn.Status == "needs_reconnect", VoicedReady: m.VoicedAt.Valid,
		Voice: drafting.Neutral(), VoiceLine: neutralLine, Channels: []ChannelView{}, Drafts: []DraftView{},
	}
	if v.Closed {
		v.Timing = []string{"queue", "draft"}
	}
	if m.VoicedAt.Valid {
		if p, ok := storedVoice(ctx, d, conn.BufferAccountID); ok {
			v.Voice, v.VoiceLine = p, drafting.Describe(p)
		}
	}
	existing, err := d.Q.ListCrewDrafts(ctx, m.ID)
	if err != nil {
		return v, fmt.Errorf("crew: list drafts: %w", err)
	}
	have := map[string]bool{}
	var hooks []drafting.HookType
	for _, dr := range existing {
		have[dr.ChannelID] = true
		if dr.HookType.Valid {
			hooks = append(hooks, drafting.HookType(dr.HookType.String))
		}
	}

	if !v.NeedsReconnect {
		chs, err := d.Buffer.Channels(ctx, conn.ID)
		switch {
		case errors.Is(err, buffer.ErrNeedsReconnect):
			v.NeedsReconnect = true
		case err != nil:
			log.Printf("crew: channels for %s: %v", m.ID, err)
			return v, ErrBuffer
		default:
			cfg, err := d.Buffer.Configuration(ctx, conn.ID)
			if err != nil {
				log.Printf("crew: configuration for %s: %v", m.ID, err)
			}
			hasImage := cardFor(ctx, d, info, m, "") != nil
			inserted := 0
			for _, ch := range chs {
				ok, reason := buffer.Eligible(ch, cfg, hasImage)
				v.Channels = append(v.Channels, ChannelView{
					ID: ch.ID, Name: ch.Name, Service: ch.Service, Avatar: ch.Avatar,
					Eligible: ok, Reason: reason, Checked: ok,
				})
				if !ok || !write || have[ch.ID] {
					continue
				}
				hook := drafting.AssignHook(hooks)
				hooks = append(hooks, hook)
				if err := templateDraft(ctx, d, info, m, ch, &cfg, hook); err != nil {
					return v, err
				}
				have[ch.ID] = true
				inserted++
			}
			if write && (inserted > 0 || !m.VoicedAt.Valid) && d.Enqueue != nil && !voicedPending(ctx, d, m.ID) {
				lid := info.l.ID
				if _, err := d.Enqueue(ctx, jobs.VoicedDrafts, VoicedPayload{MemberID: m.ID}, time.Time{}, &lid); err != nil {
					log.Printf("crew: enqueue voiced drafts for %s: %v", m.ID, err)
				}
			}
		}
	}

	ds, err := d.Q.ListCrewDrafts(ctx, m.ID)
	if err != nil {
		return v, fmt.Errorf("crew: list drafts: %w", err)
	}
	v.Drafts = draftViews(ds)
	return v, nil
}

// voicedPending reports a live VoicedDrafts job for the member.
func voicedPending(ctx context.Context, d Deps, memberID uuid.UUID) bool {
	n, err := d.Q.CountLiveMemberJobs(ctx, sqlc.CountLiveMemberJobsParams{Kind: string(jobs.VoicedDrafts), MemberID: memberID.String()})
	return err == nil && n > 0
}

// network maps a Buffer service to the drafting network.
func network(service string) drafting.Network {
	s := strings.ToLower(service)
	if s == "x" {
		return drafting.Twitter
	}
	return drafting.Network(s)
}

func templateDraft(ctx context.Context, d Deps, info launchInfo, m sqlc.CrewMember, ch buffer.Channel, cfg *buffer.Config, hook drafting.HookType) error {
	n := network(ch.Service)
	req := drafting.DraftRequest{Subject: subject(ctx, d, info, m), Hook: hook, Network: n, Rules: drafting.RulesFor(n, cfg)}
	td, err := drafting.Template{}.Draft(ctx, req)
	if err != nil {
		return fmt.Errorf("crew: template draft: %w", err)
	}
	f := drafting.FitToRules(td, req.Rules)
	_, err = d.Q.InsertCrewDraftIfAbsent(ctx, sqlc.InsertCrewDraftIfAbsentParams{
		CrewMemberID: m.ID, ChannelID: ch.ID, Network: string(n), Text: f.Text,
		Thread: threadJSON(f.Thread), FirstComment: pgtype.Text{String: f.FirstComment, Valid: f.FirstComment != ""},
		HookType: pgtype.Text{String: string(hook), Valid: true},
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) { // no rows: another request made it first
		return fmt.Errorf("crew: save template draft: %w", err)
	}
	return nil
}

func threadJSON(t []string) []byte {
	if len(t) == 0 {
		return nil
	}
	b, _ := json.Marshal(t)
	return b
}

func draftViews(ds []sqlc.CrewDraft) []DraftView {
	out := make([]DraftView, 0, len(ds))
	for _, dr := range ds {
		v := DraftView{
			ID: dr.ID, ChannelID: dr.ChannelID, Network: dr.Network, Text: dr.Text,
			FirstComment: dr.FirstComment.String, FromModel: dr.FromModel, Edited: dr.Edited,
			Flagged: dr.Flagged, Status: dr.Status, BufferPostID: dr.BufferPostID.String,
		}
		if len(dr.Thread) > 0 {
			if err := json.Unmarshal(dr.Thread, &v.Thread); err != nil {
				log.Printf("crew: draft %s thread: %v", dr.ID, err)
			}
		}
		if v.Thread == nil {
			v.Thread = []string{} // the page reads thread[0]; never send null
		}
		out = append(out, v)
	}
	return out
}

func storedVoice(ctx context.Context, d Deps, account string) (drafting.Profile, bool) {
	vp, err := d.Q.GetVoiceProfile(ctx, account)
	if err != nil {
		return drafting.Profile{}, false
	}
	var p drafting.Profile
	if err := json.Unmarshal(vp.Profile, &p); err != nil {
		return drafting.Profile{}, false
	}
	return p, true
}

// Drafts returns the member's drafts and whether the voiced pass has run.
func Drafts(ctx context.Context, d Deps, memberID uuid.UUID) ([]DraftView, bool, error) {
	m, err := getMember(ctx, d, memberID)
	if err != nil {
		return nil, false, err
	}
	ds, err := d.Q.ListCrewDrafts(ctx, m.ID)
	if err != nil {
		return nil, false, fmt.Errorf("crew: list drafts: %w", err)
	}
	return draftViews(ds), m.VoicedAt.Valid, nil
}

// SaveEdit stores the member's own text for a draft. A draft already
// handed to Buffer cannot be edited.
func SaveEdit(ctx context.Context, d Deps, draftID uuid.UUID, text string) error {
	if _, err := d.Q.GetCrewDraft(ctx, draftID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("crew: load draft: %w", err)
	}
	n, err := d.Q.SaveCrewDraftEdit(ctx, sqlc.SaveCrewDraftEditParams{ID: draftID, Text: text})
	if err != nil {
		return fmt.Errorf("crew: save edit: %w", err)
	}
	if n == 0 {
		return ErrAlreadyQueued
	}
	return nil
}

// Disconnect drops the stored Buffer tokens and returns the member to
// opened. Drafts are kept, so reconnecting picks up where they left off.
func Disconnect(ctx context.Context, d Deps, memberID uuid.UUID) error {
	m, err := getMember(ctx, d, memberID)
	if err != nil {
		return err
	}
	if m.BufferAccountID.Valid {
		conn, err := d.Q.GetBufferConnectionByAccount(ctx, m.BufferAccountID.String)
		switch {
		case err == nil:
			if err := d.Q.ClearConnectionTokens(ctx, conn.ID); err != nil {
				return fmt.Errorf("crew: clear tokens: %w", err)
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("crew: load connection: %w", err)
		}
	}
	if err := d.Q.ReopenCrewMember(ctx, m.ID); err != nil {
		return fmt.Errorf("crew: reopen: %w", err)
	}
	return nil
}

// Share returns the neutral text for the copy and composer buttons and
// records the share. A stranger copying a public GitHub invite's text must
// not mark the contributor as copied, so unverified rows only get the text.
func Share(ctx context.Context, d Deps, token string) (string, error) {
	m, err := Resolve(ctx, d, token)
	if err != nil {
		return "", err
	}
	text, err := NeutralDraft(ctx, d, m.ID)
	if err != nil {
		return "", err
	}
	if unverified(m) {
		return text, nil
	}
	return text, MarkCopied(ctx, d, m.ID)
}

// MarkCopied records a share outside Buffer, unless the member already queued.
func MarkCopied(ctx context.Context, d Deps, memberID uuid.UUID) error {
	if err := d.Q.MarkCrewMemberCopied(ctx, memberID); err != nil {
		return fmt.Errorf("crew: mark copied: %w", err)
	}
	return nil
}

// Post runs PostAll and moves the member to queued when a post was queued.
// A selection asking for an image gets the member's card, never a URL
// from the caller.
func Post(ctx context.Context, d Deps, memberID uuid.UUID, sels []Selection) ([]Outcome, error) {
	m, err := getMember(ctx, d, memberID)
	if err != nil {
		return nil, err
	}
	info, err := loadLaunch(ctx, d, m.LaunchID)
	if err != nil {
		return nil, err
	}
	card := ""
	if c := cardFor(ctx, d, info, m, ""); c != nil {
		card = c.URL
	}
	sels = append([]Selection(nil), sels...)
	for i := range sels {
		if sels[i].AssetURL != "" {
			sels[i].AssetURL = card
		}
	}
	out, err := PostAll(ctx, d, memberID, sels)
	if err != nil {
		return nil, err
	}
	for _, o := range out {
		if o.Status == statusQueued {
			if err := d.Q.MarkCrewMemberQueued(context.WithoutCancel(ctx), memberID); err != nil {
				log.Printf("crew: mark queued %s: %v", memberID, err)
			}
			break
		}
	}
	return out, nil
}

// Board lists the crew of the member's launch.
func Board(ctx context.Context, d Deps, memberID uuid.UUID) (BoardView, error) {
	m, err := getMember(ctx, d, memberID)
	if err != nil {
		return BoardView{}, err
	}
	info, err := loadLaunch(ctx, d, m.LaunchID)
	if err != nil {
		return BoardView{}, err
	}
	rows, err := d.Q.ListCrewByLaunch(ctx, info.l.ID)
	if err != nil {
		return BoardView{}, fmt.Errorf("crew: list crew: %w", err)
	}
	dues, err := d.Q.ListCrewDueByLaunch(ctx, info.l.ID)
	if err != nil {
		return BoardView{}, fmt.Errorf("crew: list due: %w", err)
	}
	due := map[uuid.UUID]time.Time{}
	for _, r := range dues {
		due[r.CrewMemberID] = r.DueAt.Time.UTC()
	}
	v := BoardView{Rows: []BoardRow{}}
	for _, r := range rows {
		if r.State == stateSuggested {
			continue // never invited
		}
		row := BoardRow{Name: memberName(ctx, d, info, r, false), Role: r.Role, State: r.State}
		if t, ok := due[r.ID]; ok {
			row.When = &t
		}
		switch r.State {
		case stateQueued, statePosted, stateCopied:
			v.In++
		}
		v.Rows = append(v.Rows, row)
	}
	v.Total = len(v.Rows)
	return v, nil
}

// Resolve returns the crew row an invite token belongs to.
func Resolve(ctx context.Context, d Deps, token string) (sqlc.CrewMember, error) {
	if token == "" {
		return sqlc.CrewMember{}, ErrNotFound
	}
	m, err := d.Q.GetCrewMemberByTokenHash(ctx, HashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, fmt.Errorf("crew: load member: %w", err)
	}
	if m.TokenExpiresAt.Valid && !d.now().Before(m.TokenExpiresAt.Time) {
		return m, ErrExpired
	}
	return m, nil
}

// Authorize loads the member when the caller holds its invite token or is
// signed in as its user; otherwise ErrNotFound, so existence is not revealed.
// Once the row is bound to a user, mutating calls need that user's session:
// the token alone only reads.
func Authorize(ctx context.Context, d Deps, memberID uuid.UUID, token string, sessionUser uuid.UUID, mutating bool) (sqlc.CrewMember, error) {
	m, err := getMember(ctx, d, memberID)
	if err != nil {
		return m, err
	}
	if sessionUser != uuid.Nil && m.UserID.Valid && m.UserID.UUID == sessionUser {
		return m, nil
	}
	if mutating && m.UserID.Valid {
		return sqlc.CrewMember{}, ErrNotFound
	}
	if token == "" || len(m.InviteTokenHash) == 0 || subtle.ConstantTimeCompare(HashToken(token), m.InviteTokenHash) != 1 {
		return sqlc.CrewMember{}, ErrNotFound
	}
	if m.TokenExpiresAt.Valid && !d.now().Before(m.TokenExpiresAt.Time) {
		return sqlc.CrewMember{}, ErrExpired
	}
	return m, nil
}

// LaunchForOpenLink returns the live launch behind an open token.
func LaunchForOpenLink(ctx context.Context, d Deps, openToken string) (uuid.UUID, error) {
	if openToken == "" {
		return uuid.Nil, ErrNotFound
	}
	l, err := d.Q.GetLaunchByOpenTokenHash(ctx, HashToken(openToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("crew: load launch: %w", err)
	}
	if l.Status != launchLaunched && l.Status != launchClosed {
		return uuid.Nil, ErrNotFound
	}
	return l.ID, nil
}

func head(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
