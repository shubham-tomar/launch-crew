package launch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

// Change is one PR in the release, kept (Hidden false) or hidden.
type Change struct {
	PRNumber    int    `json:"prNumber"`
	Title       string `json:"title"`
	AuthorLogin string `json:"authorLogin"`
	Hidden      bool   `json:"hidden"`
}

// CaptainDraftView is the captain's post for one channel.
type CaptainDraftView struct {
	ID           uuid.UUID `json:"id"`
	ChannelID    string    `json:"channelId"`
	ChannelName  string    `json:"channelName"`
	Service      string    `json:"service"`
	Text         string    `json:"text"`
	Status       string    `json:"status"` // draft | sending | queued | failed
	BufferPostID string    `json:"bufferPostId"`
	Note         string    `json:"note"` // why it became a Buffer draft or failed
}

// CrewRow is one crew member. InviteURL is set once the member was invited.
type CrewRow struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Login         string    `json:"login"`
	Role          string    `json:"role"`
	State         string    `json:"state"`
	CommentFailed bool      `json:"commentFailed"`
	// RetriesLeft is how many more times the failed comment can be retried.
	RetriesLeft int    `json:"commentRetriesLeft"`
	InviteURL   string `json:"inviteUrl,omitempty"`
}

// CheckView is one preflight check.
type CheckView struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Fix  string `json:"fix"`
}

// DeadLetter is a job that gave up; Retry takes its JobID.
type DeadLetter struct {
	JobID     int64  `json:"jobId"`
	Kind      string `json:"kind"`
	LastError string `json:"lastError"`
	Attempts  int    `json:"attempts"`
}

// Counts: In is crew who queued, posted or shared another way, out of
// Total invited (suggested rows are not counted).
type Counts struct {
	In    int `json:"in"`
	Total int `json:"total"`
}

// DetailView is the captain's launch page. The open, show and PNG links are
// set for launched or closed manual launches only. Checks run for draft launches only.
type DetailView struct {
	ID              uuid.UUID          `json:"id"`
	Status          string             `json:"status"`
	Source          string             `json:"source"`
	Title           string             `json:"title"`
	Summary         string             `json:"summary"`
	ReleaseTag      string             `json:"releaseTag"`
	ReleaseURL      string             `json:"releaseUrl"`
	LaunchAt        *time.Time         `json:"launchAt"`
	WindowMinutes   int                `json:"windowMinutes"`
	CrewCap         int                `json:"crewCap"`
	OpenCount       int                `json:"openCount"`
	OpenURL         string             `json:"openUrl"`
	ShowURL         string             `json:"showUrl"`
	PNGURL          string             `json:"pngUrl"`
	Roles           []trigger.Role     `json:"roles"`
	BufferConnected bool               `json:"bufferConnected"`
	Headlines       []Change           `json:"headlines"`
	Chores          []Change           `json:"chores"`
	CaptainDrafts   []CaptainDraftView `json:"captainDrafts"`
	Crew            []CrewRow          `json:"crew"`
	Checks          []CheckView        `json:"checks"`
	DeadLetters     []DeadLetter       `json:"deadLetters"`
	Counts          Counts             `json:"counts"`
	// CaptainName is who the captain is on the page: their GitHub login, else
	// the name of their first Buffer channel, else "You".
	CaptainName string `json:"captainName"`
	// QRLogo is what sits over the centre of the QR code: plane, none or
	// custom; QRLogoURL is the stored image for custom.
	QRLogo    string `json:"qrLogo"`
	QRLogoURL string `json:"qrLogoUrl"`
}

// Detail builds the launch page. For a draft it first adds any missing
// captain drafts, then runs preflight.
func Detail(ctx context.Context, d Deps, id uuid.UUID) (DetailView, error) {
	s, err := load(ctx, d, id)
	if err != nil {
		return DetailView{}, err
	}
	s.withBuffer(ctx, d)
	l := s.l
	v := DetailView{
		ID: l.ID, Status: l.Status, Source: l.Source, Title: l.Title, Summary: l.Summary,
		ReleaseTag: l.ReleaseTag.String, ReleaseURL: l.ReleaseUrl.String, LaunchAt: timePtr(l.LaunchAt),
		WindowMinutes: int(l.WindowMinutes), CrewCap: int(l.CrewCap), OpenCount: int(l.OpenCount),
		Roles: rolesOf(l), BufferConnected: s.conn != nil,
		Headlines: []Change{}, Chores: []Change{}, CaptainDrafts: []CaptainDraftView{},
		Crew: []CrewRow{}, Checks: []CheckView{}, DeadLetters: []DeadLetter{},
	}
	v.CaptainName = captainName(s)
	v.QRLogo, v.QRLogoURL = qrLogoOf(ctx, d, l.ID)
	if l.Source == "manual" && len(l.OpenTokenEnc) > 0 && (l.Status == statusLaunched || l.Status == statusClosed) {
		if tok, err := d.Keyring.Decrypt(l.OpenTokenEnc); err == nil {
			v.OpenURL = d.link("/l/" + string(tok))
			v.ShowURL = d.link("/l/" + string(tok) + "/show")
			v.PNGURL = d.link("/api/l/" + string(tok) + "/show.png")
		} else {
			log.Printf("launch %s: open token: %v", l.ID, err)
		}
	}
	for _, c := range s.changes {
		cv := Change{PRNumber: c.PRNumber, Title: c.Title, AuthorLogin: c.AuthorLogin, Hidden: c.Hidden}
		if c.Hidden {
			v.Chores = append(v.Chores, cv)
		} else {
			v.Headlines = append(v.Headlines, cv)
		}
	}

	if l.Status == statusDraft {
		if err := ensureCaptainDrafts(ctx, d, s); err != nil {
			return DetailView{}, err
		}
	}
	drafts, err := d.Q.ListCaptainDrafts(ctx, l.ID)
	if err != nil {
		return DetailView{}, fmt.Errorf("launch: list captain drafts: %w", err)
	}
	for _, cd := range drafts {
		ch := s.channels[cd.ChannelID]
		v.CaptainDrafts = append(v.CaptainDrafts, CaptainDraftView{
			ID: cd.ID, ChannelID: cd.ChannelID, ChannelName: ch.Name, Service: ch.Service,
			Text: cd.Text, Status: cd.Status, BufferPostID: cd.BufferPostID.String, Note: cd.Note,
		})
	}
	if l.Status == statusDraft {
		checks, _ := runPreflight(ctx, d, s, drafts)
		for _, c := range checks {
			v.Checks = append(v.Checks, CheckView{ID: c.ID, Name: c.Name, OK: c.OK, Fix: c.Fix})
		}
	}

	if v.Crew, v.Counts, err = crewRows(ctx, d, s); err != nil {
		return DetailView{}, err
	}
	dead, err := jobs.DeadLetters(ctx, d.Q, &l.ID)
	if err != nil {
		return DetailView{}, fmt.Errorf("launch: dead letters: %w", err)
	}
	for _, j := range dead {
		v.DeadLetters = append(v.DeadLetters, DeadLetter{JobID: j.ID, Kind: string(j.Kind), LastError: j.LastError, Attempts: j.Attempts})
	}
	return v, nil
}

func crewRows(ctx context.Context, d Deps, s *state) ([]CrewRow, Counts, error) {
	rows, err := d.Q.ListCrewByLaunch(ctx, s.l.ID)
	if err != nil {
		return nil, Counts{}, fmt.Errorf("launch: list crew: %w", err)
	}
	left, err := commentRetriesLeft(ctx, d, s.l.ID)
	if err != nil {
		return nil, Counts{}, err
	}
	out := []CrewRow{}
	var n Counts
	for _, m := range rows {
		if s.isCaptain(m) {
			continue // the captain is not their own crew
		}
		r := CrewRow{ID: m.ID, Role: m.Role, State: m.State, CommentFailed: m.CommentFailed, RetriesLeft: left[m.ID]}
		if m.GithubUserID.Valid {
			r.Login = s.login(ctx, d, m.GithubUserID.Int64)
		}
		r.Name = firstNonEmpty(r.Login, m.Role)
		if len(m.InviteTokenEnc) > 0 {
			if tok, err := d.Keyring.Decrypt(m.InviteTokenEnc); err == nil {
				r.InviteURL = d.link("/s/" + string(tok))
			} else {
				log.Printf("launch: invite token for %s: %v", m.ID, err)
			}
		}
		switch m.State {
		case trigger.StateSuggested:
		case "queued", "posted", "copied":
			n.In++
			n.Total++
		default:
			n.Total++
		}
		out = append(out, r)
	}
	return out, n, nil
}

func rolesOf(l sqlc.Launch) []trigger.Role {
	roles := []trigger.Role{}
	if len(l.Roles) > 0 {
		if err := json.Unmarshal(l.Roles, &roles); err != nil {
			log.Printf("launch %s: roles: %v", l.ID, err)
		}
	}
	return roles
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// captainName is the login the launch knows for its captain, else the first
// Buffer channel's name, else "You".
func captainName(s *state) string {
	if s.captain != "" {
		return s.captain
	}
	if len(s.order) > 0 && s.order[0].Name != "" {
		return s.order[0].Name
	}
	return "You"
}
