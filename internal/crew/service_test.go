package crew_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/anthropicfake"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/bufferfake"
)

type enqueued struct {
	kind    jobs.Kind
	payload any
}

// svc is a launched manual launch with an open link and a release card.
type svc struct {
	ctx       context.Context
	q         *sqlc.Queries
	pool      *pgxpool.Pool
	kr        *crypto.Keyring
	fake      *bufferfake.Fake
	ai        *anthropicfake.Fake
	deps      crew.Deps
	launch    sqlc.Launch
	openToken string
	now       time.Time

	mu   sync.Mutex
	jobs []enqueued
}

func newSvc(t *testing.T) *svc {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(pool)
	kr, err := crypto.ParseKeyring("k1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := q.CreateUser(ctx)
	launch, err := q.CreateLaunch(ctx, sqlc.CreateLaunchParams{
		OwnerUserID: owner.ID, Source: "manual", Title: "Gophercon Meetup", Summary: "Talks on Go tooling.",
		HeadlineChanges: []byte(`[]`),
		Roles:           []byte(`[{"name":"Speaker","hint":"spoke about"},{"name":"Attendee","hint":"came along"}]`),
		LaunchAt:        pgtype.Timestamptz{Time: time.Now().Add(time.Hour).UTC().Truncate(time.Minute), Valid: true},
		WindowMinutes:   90, CrewCap: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateLaunchStatus(ctx, sqlc.UpdateLaunchStatusParams{ID: launch.ID, Status: "launched"}); err != nil {
		t.Fatal(err)
	}
	launch.Status = "launched"
	openToken, openHash := crew.NewToken()
	if err := q.SetLaunchOpenToken(ctx, sqlc.SetLaunchOpenTokenParams{ID: launch.ID, OpenTokenHash: openHash}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertLaunchAsset(ctx, sqlc.InsertLaunchAssetParams{
		LaunchID: launch.ID, Kind: "release", R2Url: "https://cdn.test/cards/release.png", Sha: "abc",
	}); err != nil {
		t.Fatal(err)
	}

	fake := bufferfake.New(t, "unused", "org-1")
	fake.Handle("GetConfiguration", map[string]any{"configuration": map[string]any{"services": []any{}}})
	fake.Handle("GetSentPosts", map[string]any{"posts": map[string]any{"edges": []any{}}})
	ai := anthropicfake.New()
	t.Cleanup(ai.Close)
	buf := buffer.New(&http.Client{Timeout: 10 * time.Second}, q, kr, buffer.WithEndpoints(fake.APIURL(), fake.TokenURL()))

	s := &svc{ctx: ctx, q: q, pool: pool, kr: kr, fake: fake, ai: ai, launch: launch, openToken: openToken,
		now: time.Now().UTC().Truncate(time.Second)}
	s.deps = crew.Deps{
		Q: q, Pool: pool, Buffer: buf,
		Preflight: preflight.Deps{
			Fetch:  func(context.Context, string) (int, string, error) { return 200, "image/png", nil },
			Config: buf.Configuration,
		},
		Drafting: &drafting.Service{
			Model: drafting.NewClaude("test-key", option.WithBaseURL(ai.URL)), Budget: 5 * time.Second,
		},
		Enqueue: func(_ context.Context, kind jobs.Kind, payload any, _ time.Time, _ *uuid.UUID) (int64, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.jobs = append(s.jobs, enqueued{kind, payload})
			return int64(len(s.jobs)), nil
		},
		BaseURL: "https://lc.test",
		Keyring: kr,
		Now:     func() time.Time { return s.now },
	}
	return s
}

func (s *svc) enqueuedKinds() []jobs.Kind {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []jobs.Kind
	for _, j := range s.jobs {
		out = append(out, j.kind)
	}
	return out
}

// conn makes a Buffer connection for a fresh user and returns it.
func (s *svc) conn(t *testing.T) sqlc.BufferConnection {
	t.Helper()
	u, _ := s.q.CreateUser(s.ctx)
	access, keyID, _ := s.kr.Encrypt([]byte("tok"))
	c, err := s.q.UpsertBufferConnection(s.ctx, sqlc.UpsertBufferConnectionParams{
		UserID: u.ID, BufferAccountID: "acct-" + uuid.NewString(),
		OrgID:          pgtype.Text{String: "org-1", Valid: true},
		AccessTokenEnc: access, KeyID: keyID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (s *svc) channels(chs ...map[string]any) {
	s.fake.Handle("GetChannels", map[string]any{"channels": chs})
}

func ch(id, service string) map[string]any {
	return map[string]any{"id": id, "name": "@" + id, "service": service}
}

// member creates a crew row through the open link and role picker.
func (s *svc) member(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	id, tok, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Speaker", "", "Profiling in prod", net.ParseIP("10.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	return id, tok
}

// ghMember inserts a GitHub crew row with an invite token.
func (s *svc) ghMember(t *testing.T, state string, expires time.Time) (sqlc.CrewMember, string) {
	t.Helper()
	tok, hash := crew.NewToken()
	m, err := s.q.InsertCrewMember(s.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: s.launch.ID, GithubUserID: pgtype.Int8{Int64: 2_000_000_000 + int64(uuid.New().ID()), Valid: true},
		Role: "contributor", State: state, InviteTokenHash: hash,
		PrNumber: pgtype.Int4{Int32: 42, Valid: true}, PrTitle: pgtype.Text{String: "Split-pane resize with keyboard", Valid: true},
		TokenExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, tok
}

func (s *svc) row(t *testing.T, id uuid.UUID) sqlc.CrewMember {
	t.Helper()
	m, err := s.q.GetCrewMember(s.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOpenCreatesOpenedRow(t *testing.T) {
	s := newSvc(t)
	m, tok := s.ghMember(t, "ticked", s.now.Add(24*time.Hour))
	v, err := crew.Open(s.ctx, s.deps, tok, net.ParseIP("10.0.0.2"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Member.ID != m.ID || v.Member.State != "opened" || v.Verified || v.Preview == "" {
		t.Fatalf("view = %+v", v)
	}
	if v.Card == nil || v.Card.URL != "https://cdn.test/cards/release.png" {
		t.Fatalf("card = %+v, want the release card", v.Card)
	}
	got := s.row(t, m.ID)
	if got.State != "opened" || !got.OpenedAt.Valid || !got.OpenedAt.Time.Equal(s.now) {
		t.Fatalf("row = %s %v", got.State, got.OpenedAt)
	}
	// A later open keeps the first opened_at; a later state never regresses.
	s.now = s.now.Add(time.Hour)
	if _, err := crew.Open(s.ctx, s.deps, tok, nil); err != nil {
		t.Fatal(err)
	}
	if again := s.row(t, m.ID); !again.OpenedAt.Time.Equal(got.OpenedAt.Time) {
		t.Fatalf("opened_at moved to %v", again.OpenedAt.Time)
	}
	if err := s.q.UpdateCrewMemberState(s.ctx, sqlc.UpdateCrewMemberStateParams{ID: m.ID, State: "queued"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := crew.Open(s.ctx, s.deps, tok, nil); v.Member.State != "queued" {
		t.Fatalf("state regressed to %s", v.Member.State)
	}

	// The open link's role picker creates its row in opened.
	id, tok2 := s.member(t)
	r := s.row(t, id)
	if r.State != "opened" || !r.OpenedAt.Valid || r.Role != "Speaker" || r.RoleHint != "spoke about" ||
		r.TalkTitle != "Profiling in prod" || !r.TokenExpiresAt.Time.Equal(s.now.Add(30*24*time.Hour)) {
		t.Fatalf("role row = %+v", r)
	}
	if string(r.InviteTokenHash) != string(crew.HashToken(tok2)) || strings.Contains(string(r.InviteTokenHash), tok2) {
		t.Fatal("token not stored as its hash")
	}
}

func TestExpiredTokenErr(t *testing.T) {
	s := newSvc(t)
	_, tok := s.ghMember(t, "ticked", s.now.Add(-time.Minute))
	if _, err := crew.Open(s.ctx, s.deps, tok, nil); !errors.Is(err, crew.ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
	if _, err := crew.Open(s.ctx, s.deps, "no-such-token", nil); !errors.Is(err, crew.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSecondDeviceReturnsSameRow(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("x1", "twitter"), ch("li1", "linkedin"))
	c := s.conn(t)
	first, _ := s.member(t)
	a, err := crew.Connect(s.ctx, s.deps, first, c.ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.MemberID != first || len(a.Drafts) != 2 || s.row(t, first).State != "connected" {
		t.Fatalf("first connect = %+v", a)
	}
	// Another device opens the link again, gets a new row, connects the same account.
	second, _ := s.member(t)
	b, err := crew.Connect(s.ctx, s.deps, second, c.ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.MemberID != first {
		t.Fatalf("second device member = %s, want %s", b.MemberID, first)
	}
	ids := func(v crew.ApproveView) string {
		var out []string
		for _, d := range v.Drafts {
			out = append(out, d.ID.String())
		}
		return strings.Join(out, ",")
	}
	if ids(a) != ids(b) {
		t.Fatalf("draft ids differ: %s vs %s", ids(a), ids(b))
	}
	if r := s.row(t, second); r.State != "opened" || r.BufferAccountID.Valid {
		t.Fatalf("second row touched: %+v", r)
	}
	// Connecting the first row again changes nothing.
	again, err := crew.Connect(s.ctx, s.deps, first, c.ID, uuid.Nil)
	if err != nil || ids(again) != ids(a) {
		t.Fatalf("reconnect = %v, %s", err, ids(again))
	}
}

func TestGitHubRowUniquePerLaunch(t *testing.T) {
	s := newSvc(t)
	m, tok := s.ghMember(t, "ticked", s.now.Add(time.Hour))
	if _, err := s.q.InsertCrewMember(s.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: s.launch.ID, GithubUserID: m.GithubUserID, Role: "contributor", State: "ticked",
	}); err == nil {
		t.Fatal("second row for the same GitHub user in one launch was accepted")
	}
	for range 2 {
		if _, err := crew.Open(s.ctx, s.deps, tok, nil); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.q.CountCrewByLaunch(s.ctx, s.launch.ID)
	if err != nil || n != 1 {
		t.Fatalf("crew rows = %d, %v; want 1", n, err)
	}
}

func TestEleventhOpenRateLimited(t *testing.T) {
	s := newSvc(t)
	ip := net.ParseIP("203.0.113.9")
	for i := range 10 {
		v, err := crew.OpenLink(s.ctx, s.deps, s.openToken, ip)
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		if v.OpenCount != i+1 || v.ShortURL != "https://lc.test/l/"+s.openToken || len(v.Roles) != 2 || v.Card.URL == "" {
			t.Fatalf("open %d view = %+v", i+1, v)
		}
	}
	if _, err := crew.OpenLink(s.ctx, s.deps, s.openToken, ip); !errors.Is(err, crew.ErrRateLimited) {
		t.Fatalf("11th open err = %v, want ErrRateLimited", err)
	}
	if _, err := crew.OpenLink(s.ctx, s.deps, s.openToken, net.ParseIP("203.0.113.10")); err != nil {
		t.Fatalf("other ip: %v", err)
	}
	s.now = s.now.Add(time.Minute)
	if _, err := crew.OpenLink(s.ctx, s.deps, s.openToken, ip); err != nil {
		t.Fatalf("next minute: %v", err)
	}
	// A draft launch has no public link yet.
	if err := s.q.UpdateLaunchStatus(s.ctx, sqlc.UpdateLaunchStatusParams{ID: s.launch.ID, Status: "draft"}); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.OpenLink(s.ctx, s.deps, s.openToken, net.ParseIP("198.51.100.1")); !errors.Is(err, crew.ErrNotFound) {
		t.Fatalf("draft launch err = %v", err)
	}
}

func TestCrewCapStopsAt200AndCaptainCanRaise(t *testing.T) {
	s := newSvc(t)
	if _, err := s.pool.Exec(s.ctx, `INSERT INTO crew_members (launch_id, state, opened_at)
		SELECT $1, 'opened', now() FROM generate_series(1, 200)`, s.launch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.OpenLink(s.ctx, s.deps, s.openToken, net.ParseIP("10.1.0.1")); !errors.Is(err, crew.ErrCrewFull) {
		t.Fatalf("open at cap err = %v, want ErrCrewFull", err)
	}
	if _, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Speaker", "", "", nil); !errors.Is(err, crew.ErrCrewFull) {
		t.Fatalf("role at cap err = %v, want ErrCrewFull", err)
	}
	l := s.launch
	if _, err := s.q.UpdateLaunch(s.ctx, sqlc.UpdateLaunchParams{
		ID: l.ID, Title: l.Title, Summary: l.Summary, LaunchAt: l.LaunchAt, WindowMinutes: l.WindowMinutes,
		CrewCap: 201, Roles: l.Roles, HeadlineChanges: l.HeadlineChanges,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := crew.OpenLink(s.ctx, s.deps, s.openToken, net.ParseIP("10.1.0.2")); err != nil {
		t.Fatalf("open after raise: %v", err)
	}
	if _, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Attendee", "", "", nil); err != nil {
		t.Fatalf("role after raise: %v", err)
	}
	if _, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Attendee", "", "", nil); !errors.Is(err, crew.ErrCrewFull) {
		t.Fatalf("202nd err = %v, want ErrCrewFull", err)
	}
}

func TestSweepRemovesUntouchedOpened(t *testing.T) {
	s := newSvc(t)
	old, _ := s.member(t)
	fresh, _ := s.member(t)
	connected, _ := s.member(t)
	gh, _ := s.ghMember(t, "ticked", s.now.Add(time.Hour))
	if _, err := s.pool.Exec(s.ctx, `UPDATE crew_members SET opened_at = now() - interval '25 hours'
		WHERE id = ANY($1)`, []uuid.UUID{old, connected, gh.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(s.ctx, `UPDATE crew_members SET state = 'opened' WHERE id = $1`, gh.ID); err != nil {
		t.Fatal(err)
	}
	s.channels(ch("x1", "twitter"))
	if _, err := crew.Connect(s.ctx, s.deps, connected, s.conn(t).ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	s.now = time.Now()
	if err := crew.SweepOpenedHandler(s.deps)(s.ctx, jobs.Job{Kind: jobs.SweepOpened, Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.q.GetCrewMember(s.ctx, old); err == nil {
		t.Fatal("untouched opened row survived the sweep")
	}
	for _, id := range []uuid.UUID{fresh, connected, gh.ID} {
		if _, err := s.q.GetCrewMember(s.ctx, id); err != nil {
			t.Fatalf("row %s swept: %v", id, err)
		}
	}
}

func TestVoicedReplacesOnlyUnedited(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("x1", "twitter"), ch("bs1", "bluesky"), ch("li1", "linkedin"))
	id, _ := s.member(t)
	a, err := crew.Connect(s.ctx, s.deps, id, s.conn(t).ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Drafts) != 3 || a.VoiceLine != "neutral voice" || a.VoicedReady {
		t.Fatalf("approve view = %+v", a)
	}
	if k := s.enqueuedKinds(); len(k) != 1 || k[0] != jobs.VoicedDrafts {
		t.Fatalf("enqueued = %v", k)
	}
	byChan := map[string]crew.DraftView{}
	for _, d := range a.Drafts {
		if d.FromModel || d.Text == "" {
			t.Fatalf("template draft = %+v", d)
		}
		byChan[d.ChannelID] = d
	}
	if err := crew.SaveEdit(s.ctx, s.deps, byChan["x1"].ID, "my own words"); err != nil {
		t.Fatal(err)
	}
	// LinkedIn is already queued: never overwritten.
	if err := s.q.UpdateCrewDraftPosting(s.ctx, sqlc.UpdateCrewDraftPostingParams{
		ID: byChan["li1"].ID, Status: "queued", BufferPostID: pgtype.Text{String: "p1", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	s.ai.Script(anthropicfake.Response{Text: "Spoke at the Gophercon meetup tonight about profiling in prod. Slides are up."})

	payload, _ := json.Marshal(map[string]any{"MemberID": id})
	if err := crew.VoicedDraftsHandler(s.deps)(s.ctx, jobs.Job{Kind: jobs.VoicedDrafts, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	ds, ready, err := crew.Drafts(s.ctx, s.deps, id)
	if err != nil || !ready {
		t.Fatalf("drafts: ready=%v err=%v", ready, err)
	}
	if n := len(s.ai.Requests()); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
	for _, d := range ds {
		switch d.ChannelID {
		case "x1":
			if d.Text != "my own words" || !d.Edited || d.FromModel {
				t.Fatalf("edited draft replaced: %+v", d)
			}
		case "bs1":
			if !d.FromModel || !strings.HasPrefix(d.Text, "Spoke at the Gophercon") {
				t.Fatalf("unedited draft not voiced: %+v", d)
			}
		case "li1":
			if d.Text != byChan["li1"].Text || d.FromModel || d.Status != "queued" {
				t.Fatalf("queued draft touched: %+v", d)
			}
		}
	}
}

func TestNoEligibleChannelsView(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("yt1", "youtube"))
	id, _ := s.member(t)
	a, err := crew.Connect(s.ctx, s.deps, id, s.conn(t).ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Channels) != 1 || a.Channels[0].Eligible || a.Channels[0].Checked || a.Channels[0].Reason != "video only" {
		t.Fatalf("channels = %+v", a.Channels)
	}
	if len(a.Drafts) != 0 {
		t.Fatalf("drafts = %+v", a.Drafts)
	}
	if s.fake.Count("CreatePost") != 0 {
		t.Fatal("createPost called")
	}
}

func TestClosedLaunchHidesWindowTiming(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("x1", "twitter"))
	id, _ := s.member(t)
	c := s.conn(t)
	a, err := crew.Connect(s.ctx, s.deps, id, c.ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(a.Timing, ",") != "window,queue,draft" || a.Closed {
		t.Fatalf("open launch timing = %v closed=%v", a.Timing, a.Closed)
	}
	if err := s.q.UpdateLaunchStatus(s.ctx, sqlc.UpdateLaunchStatusParams{ID: s.launch.ID, Status: "closed"}); err != nil {
		t.Fatal(err)
	}
	a, err = crew.Connect(s.ctx, s.deps, id, c.ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(a.Timing, ",") != "queue,draft" || !a.Closed {
		t.Fatalf("closed launch timing = %v closed=%v", a.Timing, a.Closed)
	}
	// The open link still works on a closed launch.
	if _, err := crew.OpenLink(s.ctx, s.deps, s.openToken, net.ParseIP("10.2.0.1")); err != nil {
		t.Fatalf("open link on closed launch: %v", err)
	}
}

func TestChooseRoleRejectsUnknownRole(t *testing.T) {
	s := newSvc(t)
	if _, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Wizard", "", "", nil); !errors.Is(err, crew.ErrInvalidRole) {
		t.Fatalf("unknown role err = %v", err)
	}
	if _, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Other", strings.Repeat("é", 41), "", nil); !errors.Is(err, crew.ErrInvalidRole) {
		t.Fatalf("long hint err = %v", err)
	}
	if _, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Other", "", "", nil); !errors.Is(err, crew.ErrInvalidRole) {
		t.Fatalf("empty other hint err = %v", err)
	}
	id, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Other", strings.Repeat("é", 40), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.row(t, id); r.Role != "Other" || r.RoleHint != strings.Repeat("é", 40) {
		t.Fatalf("other row = %+v", r)
	}
	n, _ := s.q.CountCrewByLaunch(s.ctx, s.launch.ID)
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestSaveEditOnQueuedDraftRejected(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("x1", "twitter"))
	id, _ := s.member(t)
	a, err := crew.Connect(s.ctx, s.deps, id, s.conn(t).ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	d := a.Drafts[0]
	if err := s.q.UpdateCrewDraftPosting(s.ctx, sqlc.UpdateCrewDraftPostingParams{
		ID: d.ID, Status: "queued", BufferPostID: pgtype.Text{String: "p1", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := crew.SaveEdit(s.ctx, s.deps, d.ID, "late edit"); !errors.Is(err, crew.ErrAlreadyQueued) {
		t.Fatalf("err = %v, want ErrAlreadyQueued", err)
	}
	got, _ := s.q.GetCrewDraft(s.ctx, d.ID)
	if got.Text != d.Text || got.Edited {
		t.Fatalf("queued draft changed: %+v", got)
	}
}

func TestConnectBoundRowRejectsOtherAccount(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("x1", "twitter"))
	id, _ := s.member(t)
	owner := s.conn(t)
	if _, err := crew.Connect(s.ctx, s.deps, id, owner.ID, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	before := s.row(t, id)
	intruder := s.conn(t)
	for _, sessionUser := range []uuid.UUID{uuid.Nil, intruder.UserID} {
		if _, err := crew.Connect(s.ctx, s.deps, id, intruder.ID, sessionUser); !errors.Is(err, crew.ErrAlreadyClaimed) {
			t.Fatalf("intruder connect (session %s) err = %v, want ErrAlreadyClaimed", sessionUser, err)
		}
	}
	after := s.row(t, id)
	if after.UserID != before.UserID || after.BufferAccountID != before.BufferAccountID || after.State != before.State {
		t.Fatalf("bound row changed: %+v -> %+v", before, after)
	}
	// The owner reconnecting with their own session still works.
	if a, err := crew.Connect(s.ctx, s.deps, id, owner.ID, owner.UserID); err != nil || a.MemberID != id {
		t.Fatalf("owner reconnect: %v %+v", err, a)
	}
}

func TestConnectOwnerSessionMergesConnectionUser(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("x1", "twitter"))
	m, _ := s.ghMember(t, "opened", s.now.Add(time.Hour))
	ghUser, _ := s.q.CreateUser(s.ctx) // created by GitHub verify
	if err := s.q.UpdateCrewMemberUser(s.ctx, sqlc.UpdateCrewMemberUserParams{ID: m.ID, UserID: uuid.NullUUID{UUID: ghUser.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	fresh := s.conn(t) // a new Buffer sign-in made its own user
	elsewhere, err := s.q.InsertCrewMember(s.ctx, sqlc.InsertCrewMemberParams{
		LaunchID: s.launch.ID, UserID: uuid.NullUUID{UUID: fresh.UserID, Valid: true}, State: "opened",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crew.Connect(s.ctx, s.deps, m.ID, fresh.ID, uuid.Nil); !errors.Is(err, crew.ErrAlreadyClaimed) {
		t.Fatalf("without owner session err = %v", err)
	}
	a, err := crew.Connect(s.ctx, s.deps, m.ID, fresh.ID, ghUser.ID)
	if err != nil || a.MemberID != m.ID || len(a.Drafts) != 1 {
		t.Fatalf("owner session connect: %v %+v", err, a)
	}
	r := s.row(t, m.ID)
	if r.UserID.UUID != ghUser.ID || r.BufferAccountID.String != fresh.BufferAccountID || r.State != "connected" {
		t.Fatalf("row = %+v", r)
	}
	c, _ := s.q.GetBufferConnection(s.ctx, fresh.ID)
	if c.UserID != ghUser.ID {
		t.Fatalf("connection user = %s, want %s", c.UserID, ghUser.ID)
	}
	// The connection's user was merged in, not orphaned.
	if r := s.row(t, elsewhere.ID); r.UserID.UUID != ghUser.ID {
		t.Fatalf("other row user = %s, want %s", r.UserID.UUID, ghUser.ID)
	}
	if _, err := s.q.GetUser(s.ctx, fresh.UserID); err == nil {
		t.Fatal("merged user still exists")
	}
}

func TestConnectUnverifiedGitHubRowRefused(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("x1", "twitter"))
	m, _ := s.ghMember(t, "opened", s.now.Add(time.Hour))
	c := s.conn(t)
	if _, err := crew.Connect(s.ctx, s.deps, m.ID, c.ID, c.UserID); !errors.Is(err, crew.ErrNotVerified) {
		t.Fatalf("err = %v, want ErrNotVerified", err)
	}
	if r := s.row(t, m.ID); r.UserID.Valid || r.BufferAccountID.Valid || r.State != "opened" {
		t.Fatalf("row changed: %+v", r)
	}
}

func TestEleventhRolePickRateLimited(t *testing.T) {
	s := newSvc(t)
	ip := net.ParseIP("203.0.113.20")
	for i := range 10 {
		if _, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Attendee", "", "", ip); err != nil {
			t.Fatalf("pick %d: %v", i+1, err)
		}
	}
	if _, _, err := crew.ChooseRole(s.ctx, s.deps, s.launch.ID, "Attendee", "", "", ip); !errors.Is(err, crew.ErrRateLimited) {
		t.Fatalf("11th pick err = %v, want ErrRateLimited", err)
	}
	if n, _ := s.q.CountCrewByLaunch(s.ctx, s.launch.ID); n != 10 {
		t.Fatalf("rows = %d, want 10", n)
	}
	// Opens and picks share the budget.
	if _, err := crew.OpenLink(s.ctx, s.deps, s.openToken, ip); !errors.Is(err, crew.ErrRateLimited) {
		t.Fatalf("open after 10 picks err = %v", err)
	}
}

func TestManualLaunchBoardAndShortURL(t *testing.T) {
	s := newSvc(t)
	enc, _, _ := s.kr.Encrypt([]byte(s.openToken))
	if _, err := s.pool.Exec(s.ctx, `UPDATE launches SET open_token_enc = $2 WHERE id = $1`, s.launch.ID, enc); err != nil {
		t.Fatal(err)
	}
	id, tok := s.member(t)
	v, err := crew.Open(s.ctx, s.deps, tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Launch.ShortURL != "https://lc.test/l/"+s.openToken {
		t.Fatalf("shortUrl = %q", v.Launch.ShortURL)
	}
	b, err := crew.Board(s.ctx, s.deps, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 1 || b.Rows[0].Name != "Speaker" {
		t.Fatalf("board rows = %+v, want the role as name", b.Rows)
	}

	// A GitHub launch has no short link.
	gm, gtok := s.ghMember(t, "invited", s.now.Add(time.Hour))
	if _, err := s.pool.Exec(s.ctx, `UPDATE launches SET source = 'github' WHERE id = $1`, s.launch.ID); err != nil {
		t.Fatal(err)
	}
	if gv, err := crew.Open(s.ctx, s.deps, gtok, nil); err != nil || gv.Launch.ShortURL != "" || gm.ID != gv.Member.ID {
		t.Fatalf("github view shortUrl = %q (%v)", gv.Launch.ShortURL, err)
	}
}

func TestOptedOutRowStaysOptedOut(t *testing.T) {
	s := newSvc(t)
	m, _ := s.ghMember(t, "opted_out", s.now.Add(time.Hour))
	if err := s.q.MarkCrewMemberCopied(s.ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.q.MarkCrewMemberQueued(s.ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.row(t, m.ID); got.State != "opted_out" {
		t.Fatalf("state %s, want opted_out", got.State)
	}
}

func TestRefreshChannelsPicksUpANewChannel(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("ig1", "instagram"))
	id, _ := s.member(t)
	conn := s.conn(t)
	a, err := crew.Connect(s.ctx, s.deps, id, conn.ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Channels) != 1 || len(a.Drafts) != 1 {
		t.Fatalf("before: channels=%d drafts=%d, want 1 and 1", len(a.Channels), len(a.Drafts))
	}

	// X is added in Buffer. Within the hour Connect-time cache hides it.
	s.channels(ch("ig1", "instagram"), ch("x1", "x"))
	// Age the cache past the throttle so the refresh really goes to Buffer.
	if _, err := s.pool.Exec(s.ctx, `UPDATE buffer_connections SET channels_cached_at = now() - interval '1 minute' WHERE id = $1`, conn.ID); err != nil {
		t.Fatal(err)
	}
	b, err := crew.RefreshChannels(s.ctx, s.deps, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Channels) != 2 {
		t.Fatalf("after refresh: channels = %+v, want 2", b.Channels)
	}
	if len(b.Drafts) != 2 {
		t.Fatalf("after refresh: drafts = %d, want one per eligible channel (2)", len(b.Drafts))
	}
	byChannel := map[string]bool{}
	for _, d := range b.Drafts {
		byChannel[d.ChannelID] = true
	}
	if !byChannel["x1"] || !byChannel["ig1"] {
		t.Fatalf("drafts for %v, want ig1 and x1", byChannel)
	}
}

func TestRefreshChannelsKeepsEditedDrafts(t *testing.T) {
	s := newSvc(t)
	s.channels(ch("ig1", "instagram"))
	id, _ := s.member(t)
	conn := s.conn(t)
	a, err := crew.Connect(s.ctx, s.deps, id, conn.ID, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := crew.SaveEdit(s.ctx, s.deps, a.Drafts[0].ID, "my own words"); err != nil {
		t.Fatal(err)
	}
	s.channels(ch("ig1", "instagram"), ch("x1", "x"))
	if _, err := s.pool.Exec(s.ctx, `UPDATE buffer_connections SET channels_cached_at = now() - interval '1 minute' WHERE id = $1`, conn.ID); err != nil {
		t.Fatal(err)
	}
	b, err := crew.RefreshChannels(s.ctx, s.deps, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range b.Drafts {
		if d.ChannelID == "ig1" && d.Text != "my own words" {
			t.Fatalf("an edited draft was overwritten: %q", d.Text)
		}
	}
}

func TestRefreshChannelsNeedsAConnection(t *testing.T) {
	s := newSvc(t)
	id, _ := s.member(t) // chose a role but never connected Buffer
	if _, err := crew.RefreshChannels(s.ctx, s.deps, id); !errors.Is(err, crew.ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
}
