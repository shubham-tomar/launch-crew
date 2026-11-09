package crew_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/bufferfake"
)

func TestDueAtWithinWindow(t *testing.T) {
	launch := time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)
	rnd := rand.New(rand.NewPCG(1, 2))
	lo, hi := launch.Add(5*time.Minute), launch.Add(90*time.Minute)
	minSeen, maxSeen := hi, lo
	for range 1000 {
		d := crew.DueAt(launch, 90, rnd)
		if d.Before(lo) || d.After(hi) {
			t.Fatalf("due %v outside [%v, %v]", d, lo, hi)
		}
		if d.Before(minSeen) {
			minSeen = d
		}
		if d.After(maxSeen) {
			maxSeen = d
		}
	}
	// Uniform over 85 minutes: 1000 samples reach both ends.
	if minSeen.Sub(lo) > 5*time.Minute || hi.Sub(maxSeen) > 5*time.Minute {
		t.Fatalf("samples span [%v, %v], want close to the whole window", minSeen, maxSeen)
	}
	if d := crew.DueAt(launch, 3, rnd); !d.Equal(lo) {
		t.Fatalf("window 3: due %v, want %v", d, lo)
	}
}

type env struct {
	q        *sqlc.Queries
	fake     *bufferfake.Fake
	buf      *buffer.Client
	deps     crew.Deps
	pool     *pgxpool.Pool
	connID   uuid.UUID
	launch   sqlc.Launch
	memberID uuid.UUID
}

type channel struct{ id, service, name string }

// setup seeds a launched launch an hour out, a connected crew member and
// one crew draft per channel (text "draft for <id>").
func setup(t *testing.T, chs ...channel) *env {
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
	user, _ := q.CreateUser(ctx)
	access, keyID, _ := kr.Encrypt([]byte("tok"))
	conn, err := q.UpsertBufferConnection(ctx, sqlc.UpsertBufferConnectionParams{
		UserID: user.ID, BufferAccountID: "acct-" + uuid.NewString(),
		OrgID:          pgtype.Text{String: "org-1", Valid: true},
		AccessTokenEnc: access, KeyID: keyID,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := q.CreateLaunch(ctx, sqlc.CreateLaunchParams{
		OwnerUserID: owner.ID, Source: "manual", HeadlineChanges: []byte(`[]`), Roles: []byte(`[]`),
		LaunchAt:      pgtype.Timestamptz{Time: time.Now().Add(time.Hour).UTC().Truncate(time.Minute), Valid: true},
		WindowMinutes: 90, CrewCap: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateLaunchStatus(ctx, sqlc.UpdateLaunchStatusParams{ID: launch.ID, Status: "launched"}); err != nil {
		t.Fatal(err)
	}
	launch.Status = "launched"
	m, err := q.InsertCrewMember(ctx, sqlc.InsertCrewMemberParams{
		LaunchID: launch.ID, UserID: uuid.NullUUID{UUID: user.ID, Valid: true},
		BufferAccountID: pgtype.Text{String: conn.BufferAccountID, Valid: true}, State: "connected",
	})
	if err != nil {
		t.Fatal(err)
	}

	fake := bufferfake.New(t, conn.BufferAccountID, "org-1")
	var list []map[string]any
	for _, ch := range chs {
		list = append(list, map[string]any{"id": ch.id, "name": ch.name, "service": ch.service})
		if _, err := q.UpsertCrewDraft(ctx, sqlc.UpsertCrewDraftParams{
			CrewMemberID: m.ID, ChannelID: ch.id, Network: ch.service, Text: "draft for " + ch.id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	fake.Handle("GetChannels", map[string]any{"channels": list})
	fake.Handle("GetConfiguration", map[string]any{"configuration": map[string]any{"services": []any{}}})
	fake.Handle("GetPostsCreatedBetween", map[string]any{"posts": map[string]any{"edges": []any{}}})

	buf := buffer.New(&http.Client{Timeout: 10 * time.Second}, q, kr, buffer.WithEndpoints(fake.APIURL(), fake.TokenURL()))
	deps := crew.Deps{
		Q: q, Pool: pool, Buffer: buf,
		Preflight: preflight.Deps{
			Fetch:  func(context.Context, string) (int, string, error) { return 200, "image/png", nil },
			Config: buf.Configuration,
		},
		Rand:        rand.New(rand.NewPCG(7, 7)),
		PostTimeout: 300 * time.Millisecond,
	}
	return &env{q: q, pool: pool, fake: fake, buf: buf, deps: deps, connID: conn.ID, launch: launch, memberID: m.ID}
}

func success(id string) bufferfake.Response {
	return bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": id},
	}}}
}

func createInputs(f *bufferfake.Fake) []map[string]any {
	var out []map[string]any
	for _, r := range f.Requests() {
		if r.Op == "CreatePost" {
			out = append(out, r.Vars["input"].(map[string]any))
		}
	}
	return out
}

func (e *env) draft(t *testing.T, channelID string) sqlc.CrewDraft {
	t.Helper()
	ds, err := e.q.ListCrewDrafts(context.Background(), e.memberID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.ChannelID == channelID {
			return d
		}
	}
	t.Fatalf("no draft for %s", channelID)
	return sqlc.CrewDraft{}
}

func (e *env) attempt(t *testing.T, channelID string) sqlc.PostAttempt {
	t.Helper()
	a, err := e.q.GetPostAttempt(context.Background(), sqlc.GetPostAttemptParams{CrewMemberID: e.memberID, ChannelID: channelID})
	if err != nil {
		t.Fatalf("attempt %s: %v", channelID, err)
	}
	return a
}

func sameOutcomes(t *testing.T, a, b []crew.Outcome) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("outcomes %d vs %d", len(a), len(b))
	}
	for i := range a {
		x, y := a[i], b[i]
		whenEq := (x.When == nil && y.When == nil) || (x.When != nil && y.When != nil && x.When.Equal(*y.When))
		if x.ChannelID != y.ChannelID || x.Service != y.Service || x.Status != y.Status || x.Note != y.Note || !whenEq {
			t.Fatalf("outcome %d: %+v vs %+v", i, x, y)
		}
	}
}

func TestDoubleTapCreatesOnePost(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"}, channel{"bs1", "bluesky", "ana.bsky"}, channel{"th1", "threads", "ana"})
	ctx := context.Background()
	e.fake.Script("CreatePost", success("p-x1"), success("p-bs1"))
	sel := []crew.Selection{
		{ChannelID: "x1", Text: "shipping v2 today", Timing: crew.TimingWindow},
		{ChannelID: "bs1", Text: "v2 is out", Timing: crew.TimingWindow},
	}
	first, err := crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil {
		t.Fatal(err)
	}
	second, err := crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil {
		t.Fatal(err)
	}
	sameOutcomes(t, first, second)

	lo := e.launch.LaunchAt.Time.Add(5 * time.Minute)
	hi := e.launch.LaunchAt.Time.Add(90 * time.Minute)
	for i, o := range first {
		if o.Status != "queued" || o.ChannelID != sel[i].ChannelID || o.When == nil || o.When.Before(lo) || o.When.After(hi) {
			t.Fatalf("outcome %d = %+v", i, o)
		}
	}
	if first[0].Service != "twitter" || first[1].Service != "bluesky" {
		t.Fatalf("services = %q, %q", first[0].Service, first[1].Service)
	}

	ins := createInputs(e.fake)
	if len(ins) != 2 || ins[0]["channelId"] != "x1" || ins[1]["channelId"] != "bs1" {
		t.Fatalf("createPost inputs = %v, want one per channel", ins)
	}
	for i, in := range ins {
		if in["mode"] != "customScheduled" || in["text"] != sel[i].Text || in["dueAt"] != first[i].When.UTC().Format(time.RFC3339) {
			t.Fatalf("input %d = %v, outcome when %v", i, in, first[i].When)
		}
	}
	for i, id := range []string{"x1", "bs1"} {
		d := e.draft(t, id)
		if d.Status != "queued" || d.BufferPostID.String != "p-"+id || !d.DueAt.Time.Equal(*first[i].When) {
			t.Fatalf("draft %s = status %q post %q due %v", id, d.Status, d.BufferPostID.String, d.DueAt.Time)
		}
		if a := e.attempt(t, id); a.Status != "queued" || a.BufferPostID.String != "p-"+id {
			t.Fatalf("attempt %s = %+v", id, a)
		}
	}

	// Two taps at once: one create, the other waits or replays.
	e.fake.Script("CreatePost", success("p-th1"))
	e.fake.OnRequest(func(r bufferfake.Request) {
		if r.Op == "CreatePost" {
			time.Sleep(100 * time.Millisecond)
		}
	})
	both := make(chan crew.Outcome, 2)
	for range 2 {
		go func() {
			out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "th1", Text: "thread time", Timing: crew.TimingQueue}})
			if err != nil || len(out) != 1 {
				t.Errorf("concurrent tap: %+v, %v", out, err)
				both <- crew.Outcome{}
				return
			}
			both <- out[0]
		}()
	}
	queued := 0
	for range 2 {
		o := <-both
		switch {
		case o.Status == "queued":
			queued++
		case o.Status == "failed" && o.Note == "still working on the last tap, try again in a moment":
		default:
			t.Fatalf("concurrent outcome = %+v", o)
		}
	}
	if n := e.fake.Count("CreatePost"); n != 3 || queued == 0 {
		t.Fatalf("createPost calls = %d (want 3), queued outcomes = %d", n, queued)
	}
}

func TestTimeoutRecoveryFindsPost(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	var creates atomic.Int32
	e.fake.OnRequest(func(r bufferfake.Request) {
		if r.Op == "CreatePost" && creates.Add(1) == 1 {
			time.Sleep(700 * time.Millisecond) // past the 300 ms post timeout
		}
	})
	e.fake.Script("CreatePost", bufferfake.Response{Status: http.StatusGatewayTimeout})
	text := "shipping v2 today"
	e.fake.Script("GetPostsCreatedBetween", bufferfake.Response{Data: map[string]any{"posts": map[string]any{"edges": []any{
		map[string]any{"node": map[string]any{"id": "other", "channelId": "x1", "text": "an older post", "status": "scheduled", "createdAt": time.Now().Format(time.RFC3339)}},
		map[string]any{"node": map[string]any{"id": "p-found", "channelId": "x1", "text": text, "status": "scheduled", "createdAt": time.Now().Format(time.RFC3339)}},
	}}}})

	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	start := time.Now()
	sel := []crew.Selection{{ChannelID: "x1", Text: text, Timing: crew.TimingQueue}}
	out, err := crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("took %v", el)
	}
	if len(out) != 1 || out[0].Status != "queued" || out[0].When != nil {
		t.Fatalf("outcomes = %+v", out)
	}
	if !strings.Contains(logs.String(), "unclear, looking it up") {
		t.Fatalf("log = %q", logs.String())
	}
	if n := e.fake.Count("GetPostsCreatedBetween"); n != 1 {
		t.Fatalf("posts queries = %d, want 1", n)
	}
	for _, r := range e.fake.Requests() {
		if r.Op == "GetPostsCreatedBetween" && r.Vars["channelId"] != "x1" {
			t.Fatalf("posts query vars = %v", r.Vars)
		}
	}
	if a := e.attempt(t, "x1"); a.Status != "queued" || a.BufferPostID.String != "p-found" {
		t.Fatalf("attempt = %+v", a)
	}
	if d := e.draft(t, "x1"); d.Status != "queued" || d.BufferPostID.String != "p-found" || d.DueAt.Valid {
		t.Fatalf("draft = %+v", d)
	}

	// The retry tap returns the adopted post; Buffer still saw one create.
	again, err := crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil {
		t.Fatal(err)
	}
	sameOutcomes(t, out, again)
	if n := e.fake.Count("CreatePost"); n != 1 {
		t.Fatalf("createPost calls = %d, want 1", n)
	}
}

func TestMixedOutcomesReported(t *testing.T) {
	e := setup(t,
		channel{"a", "twitter", "@ana"}, channel{"b", "bluesky", "ana.bsky"},
		channel{"c", "twitter", "@ana_alt"}, channel{"d", "linkedin", "Ana"})
	ctx := context.Background()
	e.fake.Script("CreatePost",
		success("p-a"),
		bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
			"__typename": "LimitReachedError", "message": "You've reached your queue limit",
		}}},
		success("p-b"),
		bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
			"__typename": "InvalidInputError", "message": "Text exceeds the maximum length",
		}}},
	)
	long := strings.Repeat("a", 300)
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{
		{ChannelID: "a", Text: "v2 is out", Timing: crew.TimingQueue},
		{ChannelID: "b", Text: "v2 is out", Timing: crew.TimingQueue},
		{ChannelID: "c", Text: long, Timing: crew.TimingQueue},
		{ChannelID: "d", Text: "v2 is out, here is what changed", Timing: crew.TimingQueue},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("outcomes = %+v", out)
	}
	if out[0].Status != "queued" || out[0].Note != "" {
		t.Fatalf("a = %+v", out[0])
	}
	if out[1].Status != "draft" || out[1].Note != buffer.DraftNote {
		t.Fatalf("b = %+v", out[1])
	}
	if out[2].Status != "failed" || !strings.Contains(out[2].Note, "over 280 characters") {
		t.Fatalf("c = %+v", out[2])
	}
	if out[3].Status != "failed" || !strings.Contains(out[3].Note, "over 3000 characters") {
		t.Fatalf("d = %+v", out[3])
	}
	for i, want := range []string{"p-a", "p-b", "", ""} {
		if out[i].PostID != want {
			t.Fatalf("outcome %d postId = %q, want %q", i, out[i].PostID, want)
		}
	}
	ins := createInputs(e.fake)
	if len(ins) != 4 || ins[0]["channelId"] != "a" || ins[1]["channelId"] != "b" || ins[2]["channelId"] != "b" || ins[3]["channelId"] != "d" {
		t.Fatalf("createPost inputs = %v (c must never reach Buffer)", ins)
	}
	for id, want := range map[string]string{"a": "queued", "b": "draft", "c": "failed", "d": "failed"} {
		if a := e.attempt(t, id); a.Status != want {
			t.Fatalf("attempt %s = %q, want %q", id, a.Status, want)
		}
	}
	if d := e.draft(t, "b"); d.Status != "draft" || d.BufferPostID.String != "p-b" {
		t.Fatalf("draft b = %+v", d)
	}
	if d := e.draft(t, "c"); d.Status != "draft" || d.BufferPostID.Valid {
		t.Fatalf("draft c = %+v, want untouched", d)
	}

	// A replay keeps the note it was given, whatever the second tap's timing.
	again, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "b", Text: "v2 is out", Timing: crew.TimingDraft}})
	if err != nil || again[0].Status != "draft" || again[0].Note != buffer.DraftNote {
		t.Fatalf("replay b = %+v, %v", again, err)
	}

	// A failed channel can be tapped again once fixed.
	e.fake.Script("CreatePost", success("p-c"))
	retry, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "c", Text: "fixed the long one: release notes inside", Timing: crew.TimingQueue}})
	if err != nil {
		t.Fatal(err)
	}
	if retry[0].Status != "queued" {
		t.Fatalf("retry = %+v", retry[0])
	}
	if a := e.attempt(t, "c"); a.Status != "queued" || a.BufferPostID.String != "p-c" {
		t.Fatalf("attempt c = %+v", a)
	}
}

func TestClosedLaunchRejectsWindowTiming(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"}, channel{"bs1", "bluesky", "ana.bsky"})
	ctx := context.Background()
	if err := e.q.UpdateLaunchStatus(ctx, sqlc.UpdateLaunchStatusParams{ID: e.launch.ID, Status: "closed"}); err != nil {
		t.Fatal(err)
	}
	_, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{
		{ChannelID: "bs1", Text: "v2 is out", Timing: crew.TimingQueue},
		{ChannelID: "x1", Text: "v2 is out", Timing: crew.TimingWindow},
	})
	if !errors.Is(err, crew.ErrWindowClosed) {
		t.Fatalf("err = %v, want ErrWindowClosed", err)
	}
	if n := e.fake.Count("CreatePost"); n != 0 {
		t.Fatalf("createPost calls = %d, want 0", n)
	}
	if _, err := e.q.GetPostAttempt(ctx, sqlc.GetPostAttemptParams{CrewMemberID: e.memberID, ChannelID: "bs1"}); err == nil {
		t.Fatal("an attempt was recorded before the window check")
	}

	// Next free slot still works after the window closes.
	e.fake.Script("CreatePost", success("p-bs1"))
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "bs1", Text: "v2 is out", Timing: crew.TimingQueue}})
	if err != nil || out[0].Status != "queued" {
		t.Fatalf("queue timing on closed launch: %+v, %v", out, err)
	}
}

func TestNeedsReconnectOutcome(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	// Warm the caches, then lose the connection.
	if _, err := e.buf.Channels(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.buf.Configuration(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpdateConnectionStatus(ctx, sqlc.UpdateConnectionStatusParams{ID: e.connID, Status: "needs_reconnect"}); err != nil {
		t.Fatal(err)
	}
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: "v2 is out", Timing: crew.TimingQueue}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Status != "failed" || out[0].Note != "reconnect Buffer" {
		t.Fatalf("outcomes = %+v", out)
	}
	if n := e.fake.Count("CreatePost"); n != 0 {
		t.Fatalf("createPost calls = %d, want 0", n)
	}
	if a := e.attempt(t, "x1"); a.Status != "failed" {
		t.Fatalf("attempt = %+v", a)
	}
}

func TestPendingAttemptIsNotRetried(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	if _, err := e.q.InsertPostAttempt(ctx, sqlc.InsertPostAttemptParams{CrewMemberID: e.memberID, ChannelID: "x1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: "v2 is out", Timing: crew.TimingQueue}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Status != "failed" || out[0].Note != "still working on the last tap, try again in a moment" {
		t.Fatalf("outcomes = %+v", out)
	}
	if n := e.fake.Count("CreatePost"); n != 0 {
		t.Fatalf("createPost calls = %d, want 0", n)
	}
	if a := e.attempt(t, "x1"); a.Status != "pending" {
		t.Fatalf("attempt = %+v", a)
	}
}

func quiet(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &logs
}

func postsOf(nodes ...map[string]any) bufferfake.Response {
	edges := []any{}
	for _, n := range nodes {
		edges = append(edges, map[string]any{"node": n})
	}
	return bufferfake.Response{Data: map[string]any{"posts": map[string]any{"edges": edges}}}
}

func node(id, channel, text string) map[string]any {
	return map[string]any{"id": id, "channelId": channel, "text": text, "status": "scheduled", "createdAt": time.Now().Format(time.RFC3339)}
}

// lastLookup returns the window of the latest posts lookup.
func lastLookup(t *testing.T, f *bufferfake.Fake) (start, end time.Time) {
	t.Helper()
	var vars map[string]any
	for _, r := range f.Requests() {
		if r.Op == "GetPostsCreatedBetween" {
			vars = r.Vars
		}
	}
	if vars == nil {
		t.Fatal("no posts lookup")
	}
	start, err1 := time.Parse(time.RFC3339Nano, vars["start"].(string))
	end, err2 := time.Parse(time.RFC3339Nano, vars["end"].(string))
	if err1 != nil || err2 != nil {
		t.Fatalf("lookup vars %v", vars)
	}
	return start, end
}

func TestRetryAfterUnclearReconcilesBeforeCreate(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	quiet(t)
	var creates atomic.Int32
	e.fake.OnRequest(func(r bufferfake.Request) {
		if r.Op == "CreatePost" && creates.Add(1) == 1 {
			time.Sleep(700 * time.Millisecond)
		}
	})
	e.fake.Script("CreatePost", bufferfake.Response{Status: http.StatusGatewayTimeout})
	text := "shipping v2 today"
	sel := []crew.Selection{{ChannelID: "x1", Text: text, Timing: crew.TimingQueue}}

	// The create is lost and the lookup finds nothing (Buffer is slow).
	out, err := crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil || out[0].Status != "failed" || out[0].Note != "Buffer is not answering, try again" {
		t.Fatalf("first tap = %+v, %v", out, err)
	}
	if _, end := lastLookup(t, e.fake); end.Before(time.Now().Add(time.Minute)) {
		t.Fatalf("immediate lookup ends %v, want about now+2m", end)
	}
	first := e.attempt(t, "x1")
	if first.Status != "failed" || !first.Unclear || first.SentText.String != text || !first.SentAt.Valid {
		t.Fatalf("attempt after first tap = %+v", first)
	}

	// The lookup itself fails: still no second create.
	e.fake.Script("GetPostsCreatedBetween", bufferfake.Response{Status: http.StatusInternalServerError})
	out, err = crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil || out[0].Status != "failed" || out[0].Note != "Buffer is not answering, try again" {
		t.Fatalf("second tap = %+v, %v", out, err)
	}
	if a := e.attempt(t, "x1"); a.Status != "failed" || !a.Unclear || a.SentText.String != text {
		t.Fatalf("attempt after second tap = %+v", a)
	}

	// Simulate a gap: the send was 5 minutes ago, the last claim just now.
	if _, err := e.pool.Exec(ctx, `UPDATE post_attempts SET sent_at = sent_at - interval '5 minutes', claimed_at = now()
		WHERE crew_member_id = $1 AND channel_id = $2`, e.memberID, "x1"); err != nil {
		t.Fatal(err)
	}
	second := e.attempt(t, "x1")

	// The post has surfaced: it is adopted, not created again.
	e.fake.Script("GetPostsCreatedBetween", postsOf(node("p-late", "x1", text)))
	out, err = crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil || out[0].Status != "queued" {
		t.Fatalf("third tap = %+v, %v", out, err)
	}
	if n := e.fake.Count("CreatePost"); n != 1 {
		t.Fatalf("createPost calls = %d, want 1", n)
	}
	if start, _ := lastLookup(t, e.fake); !start.Equal(second.SentAt.Time.Add(-time.Minute)) {
		t.Fatalf("reconcile lookup starts %v, want sent_at-1m = %v", start, second.SentAt.Time.Add(-time.Minute))
	}
	if a := e.attempt(t, "x1"); a.Status != "queued" || a.BufferPostID.String != "p-late" || a.Unclear {
		t.Fatalf("attempt = %+v", a)
	}
	if d := e.draft(t, "x1"); d.Status != "queued" || d.BufferPostID.String != "p-late" {
		t.Fatalf("draft = %+v", d)
	}
}

func TestStalePendingIsReclaimedAndReconciled(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	quiet(t)
	if _, err := e.q.InsertPostAttempt(ctx, sqlc.InsertPostAttemptParams{CrewMemberID: e.memberID, ChannelID: "x1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE post_attempts SET claimed_at = now() - interval '5 minutes'
		WHERE crew_member_id = $1 AND channel_id = $2`, e.memberID, "x1"); err != nil {
		t.Fatal(err)
	}
	stale := e.attempt(t, "x1")
	text := "v2 is out"
	e.fake.Script("GetPostsCreatedBetween", postsOf(node("p-stale", "x1", text)))
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: text, Timing: crew.TimingQueue}})
	if err != nil || len(out) != 1 || out[0].Status != "queued" {
		t.Fatalf("outcomes = %+v, %v", out, err)
	}
	if n := e.fake.Count("CreatePost"); n != 0 {
		t.Fatalf("createPost calls = %d, want 0", n)
	}
	if start, _ := lastLookup(t, e.fake); !start.Equal(stale.ClaimedAt.Time.Add(-time.Minute)) {
		t.Fatalf("lookup starts %v, want %v", start, stale.ClaimedAt.Time.Add(-time.Minute))
	}
	if a := e.attempt(t, "x1"); a.Status != "queued" || a.BufferPostID.String != "p-stale" {
		t.Fatalf("attempt = %+v", a)
	}
}

func TestFreshPendingStillInFlight(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	if _, err := e.q.InsertPostAttempt(ctx, sqlc.InsertPostAttemptParams{CrewMemberID: e.memberID, ChannelID: "x1", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE post_attempts SET claimed_at = now() - interval '90 seconds'
		WHERE crew_member_id = $1 AND channel_id = $2`, e.memberID, "x1"); err != nil {
		t.Fatal(err)
	}
	before := e.attempt(t, "x1")
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: "v2 is out", Timing: crew.TimingQueue}})
	if err != nil || out[0].Status != "failed" || out[0].Note != "still working on the last tap, try again in a moment" {
		t.Fatalf("outcomes = %+v, %v", out, err)
	}
	if n := e.fake.Count("CreatePost") + e.fake.Count("GetPostsCreatedBetween"); n != 0 {
		t.Fatalf("buffer post calls = %d, want 0", n)
	}
	if a := e.attempt(t, "x1"); a.Status != "pending" || !a.ClaimedAt.Time.Equal(before.ClaimedAt.Time) {
		t.Fatalf("attempt = %+v, want untouched", a)
	}
}

func TestUneditedLongDraftThreadsOnX(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	long := strings.TrimSpace(strings.Repeat("Shipping v2 with faster builds and a new cache. ", 7))
	if n := len([]rune(long)); n <= 280 {
		t.Fatalf("draft is %d chars, want over 280", n)
	}
	if _, err := e.q.UpsertCrewDraft(ctx, sqlc.UpsertCrewDraftParams{CrewMemberID: e.memberID, ChannelID: "x1", Network: "twitter", Text: long}); err != nil {
		t.Fatal(err)
	}
	e.fake.Script("CreatePost", success("p-thread"))
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: long, Timing: crew.TimingQueue}})
	if err != nil || out[0].Status != "queued" {
		t.Fatalf("outcomes = %+v, %v", out, err)
	}
	ins := createInputs(e.fake)
	if len(ins) != 1 {
		t.Fatalf("createPost inputs = %v", ins)
	}
	md, _ := ins[0]["metadata"].(map[string]any)
	tw, _ := md["twitter"].(map[string]any)
	items, _ := tw["thread"].([]any)
	if len(items) < 2 {
		t.Fatalf("input = %v, want a thread", ins[0])
	}
	var joined []string
	for i, it := range items {
		txt := it.(map[string]any)["text"].(string)
		if len([]rune(txt)) > 280 {
			t.Fatalf("thread item %d is %d chars", i, len([]rune(txt)))
		}
		joined = append(joined, txt)
	}
	if ins[0]["text"] != joined[0] || strings.Join(joined, " ") != long {
		t.Fatalf("text %q, thread %q", ins[0]["text"], joined)
	}
	if a := e.attempt(t, "x1"); a.SentText.String != joined[0] {
		t.Fatalf("sent_text = %q, want the fitted root", a.SentText.String)
	}
	if d := e.draft(t, "x1"); d.Text != long || d.Edited {
		t.Fatalf("draft text changed: %+v", d)
	}
}

func TestReconcileAnchorSurvivesFailedReclaim(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	quiet(t)
	var creates atomic.Int32
	e.fake.OnRequest(func(r bufferfake.Request) {
		if r.Op == "CreatePost" && creates.Add(1) == 1 {
			time.Sleep(700 * time.Millisecond)
		}
	})
	e.fake.Script("CreatePost", bufferfake.Response{Status: http.StatusGatewayTimeout})
	text := "shipping v2 today"
	sel := []crew.Selection{{ChannelID: "x1", Text: text, Timing: crew.TimingQueue}}

	// Tap 1 at T1: unclear send, nothing found.
	if out, _ := crew.PostAll(ctx, e.deps, e.memberID, sel); out[0].Status != "failed" {
		t.Fatalf("tap 1 = %+v", out)
	}
	t1 := e.attempt(t, "x1")
	if !t1.SentAt.Valid {
		t.Fatalf("attempt after tap 1 = %+v, want sent_at", t1)
	}

	// Tap 2 reclaims, its lookup errors: no send, so sent_at stays put.
	e.fake.Script("GetPostsCreatedBetween", bufferfake.Response{Status: http.StatusInternalServerError})
	if out, _ := crew.PostAll(ctx, e.deps, e.memberID, sel); out[0].Status != "failed" || out[0].Note != "Buffer is not answering, try again" {
		t.Fatalf("tap 2 = %+v", out)
	}
	t2 := e.attempt(t, "x1")
	if !t2.SentAt.Time.Equal(t1.SentAt.Time) || !t2.ClaimedAt.Time.After(t1.ClaimedAt.Time) {
		t.Fatalf("after tap 2: sent_at %v (was %v), claimed_at %v (was %v)", t2.SentAt.Time, t1.SentAt.Time, t2.ClaimedAt.Time, t1.ClaimedAt.Time)
	}

	// Tap 3 still looks back to T1 and adopts the post made then.
	e.fake.Script("GetPostsCreatedBetween", postsOf(node("p-t1", "x1", text)))
	out, err := crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil || out[0].Status != "queued" {
		t.Fatalf("tap 3 = %+v, %v", out, err)
	}
	if start, _ := lastLookup(t, e.fake); !start.Equal(t1.SentAt.Time.Add(-time.Minute)) {
		t.Fatalf("lookup starts %v, want T1-1m = %v", start, t1.SentAt.Time.Add(-time.Minute))
	}
	if n := e.fake.Count("CreatePost"); n != 1 {
		t.Fatalf("createPost calls = %d, want 1", n)
	}
	if a := e.attempt(t, "x1"); a.Status != "queued" || a.BufferPostID.String != "p-t1" {
		t.Fatalf("attempt = %+v", a)
	}
}

func TestLateTapNeverSchedulesInThePast(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"}, channel{"bs1", "bluesky", "ana.bsky"}, channel{"th1", "threads", "ana"})
	ctx := context.Background()
	now := e.launch.LaunchAt.Time.Add(60 * time.Minute)
	e.deps.Now = func() time.Time { return now }
	e.fake.Script("CreatePost", success("p-1"), success("p-2"), success("p-3"))
	sel := []crew.Selection{
		{ChannelID: "x1", Text: "shipping v2 today", Timing: crew.TimingWindow},
		{ChannelID: "bs1", Text: "v2 is out", Timing: crew.TimingWindow},
		{ChannelID: "th1", Text: "v2 shipped", Timing: crew.TimingWindow},
	}
	out, err := crew.PostAll(ctx, e.deps, e.memberID, sel)
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := now.Add(2*time.Minute), e.launch.LaunchAt.Time.Add(90*time.Minute)
	for i, o := range out {
		if o.Status != "queued" || o.When == nil || o.When.Before(lo) || o.When.After(hi) {
			t.Fatalf("outcome %d = %+v, want due in [%v, %v]", i, o, lo, hi)
		}
	}
}

func TestTapAfterWindowEndIsNotScheduled(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	now := e.launch.LaunchAt.Time.Add(89 * time.Minute) // under two minutes left
	e.deps.Now = func() time.Time { return now }
	out, err := crew.PostAll(context.Background(), e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: "v2", Timing: crew.TimingWindow}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != "failed" || !strings.Contains(out[0].Note, "next free slot") || len(createInputs(e.fake)) != 0 {
		t.Fatalf("outcome %+v, %d creates", out[0], len(createInputs(e.fake)))
	}
}

// A selection that asks for an image gets the member's own card as the
// Buffer asset, whatever URL the caller sent; asking for none sends none.
func TestPostAttachesMemberCard(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"}, channel{"x2", "twitter", "@ana2"})
	ctx := context.Background()
	const card = "https://r2.example/cards/ana.png"
	a, err := e.q.InsertLaunchAsset(ctx, sqlc.InsertLaunchAssetParams{LaunchID: e.launch.ID, Kind: "contributor", R2Url: card, Sha: "sha-ana"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpdateCrewMemberCard(ctx, sqlc.UpdateCrewMemberCardParams{ID: e.memberID, CardAssetID: uuid.NullUUID{UUID: a.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.fake.Script("CreatePost", success("p-1"), success("p-2"))
	out, err := crew.Post(ctx, e.deps, e.memberID, []crew.Selection{
		{ChannelID: "x1", Text: "shipped it", Timing: crew.TimingQueue, Attach: true, Card: crew.Image{URL: "https://evil.example/x.png"}},
		{ChannelID: "x2", Text: "shipped it too", Timing: crew.TimingQueue},
	})
	if err != nil || len(out) != 2 {
		t.Fatalf("post = %+v, %v", out, err)
	}
	byText := map[string]map[string]any{}
	for _, in := range createInputs(e.fake) {
		byText[in["text"].(string)] = in
	}
	assets, _ := byText["shipped it"]["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("assets = %v, want the card", byText["shipped it"]["assets"])
	}
	img, _ := assets[0].(map[string]any)["image"].(map[string]any)
	if img["url"] != card {
		t.Fatalf("asset = %v, want %s", assets[0], card)
	}
	if _, has := byText["shipped it too"]["assets"]; has {
		t.Fatalf("empty assetUrl still sent assets: %v", byText["shipped it too"]["assets"])
	}
}

// A created post must never be known to the attempt but not the draft: the
// board and the status poll read crew_drafts. When the draft write keeps
// failing, the attempt still closes and its note says the record is missing.
func TestDraftWriteFailureIsRecordedOnTheAttempt(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	// Make the posting write to crew_drafts fail for this post id only.
	if _, err := e.pool.Exec(ctx, `ALTER TABLE crew_drafts ADD CONSTRAINT lc_test_posting_write CHECK (buffer_post_id IS DISTINCT FROM 'p-unwritable')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `ALTER TABLE crew_drafts DROP CONSTRAINT IF EXISTS lc_test_posting_write`)
	})
	e.fake.Script("CreatePost", success("p-unwritable"))
	start := time.Now()
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: "draft for x1", Timing: crew.TimingQueue}})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("gave up after %v; want three tries 100 ms apart", d)
	}
	if out[0].Status != "queued" || out[0].PostID != "p-unwritable" || !strings.Contains(out[0].Note, "record") {
		t.Fatalf("outcome = %+v, want queued with a note about the missing record", out[0])
	}
	a := e.attempt(t, "x1")
	if a.Status != "queued" || a.BufferPostID.String != "p-unwritable" || !strings.Contains(a.Note.String, "record") {
		t.Fatalf("attempt = %+v, want queued with the note", a)
	}
	if d := e.draft(t, "x1"); d.BufferPostID.Valid {
		t.Fatalf("draft = %+v; the constraint should have refused the write", d)
	}
}

func (e *env) addImage(t *testing.T, url string) sqlc.LaunchAsset {
	return e.addSizedImage(t, url, 400_000, "")
}

func (e *env) addSizedImage(t *testing.T, url string, bytes int64, small string) sqlc.LaunchAsset {
	t.Helper()
	a, err := e.q.InsertLaunchImage(context.Background(), sqlc.InsertLaunchImageParams{LaunchID: e.launch.ID, R2Url: url, Sha: url, Bytes: bytes, SmallUrl: small})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (e *env) giveCard(t *testing.T, url string) {
	t.Helper()
	ctx := context.Background()
	a, err := e.q.InsertLaunchAsset(ctx, sqlc.InsertLaunchAssetParams{LaunchID: e.launch.ID, Kind: "contributor", R2Url: url, Sha: "sha-" + url})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpdateCrewMemberCard(ctx, sqlc.UpdateCrewMemberCardParams{ID: e.memberID, CardAssetID: uuid.NullUUID{UUID: a.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
}

func assetURLs(in map[string]any) []string {
	var out []string
	assets, _ := in["assets"].([]any)
	for _, a := range assets {
		img, _ := a.(map[string]any)["image"].(map[string]any)
		out = append(out, img["url"].(string))
	}
	return out
}

// The images are read when the post is sent, so one the captain removed
// after the crew member approved is not attached.
func TestCrewPostUsesCurrentImages(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"}, channel{"x2", "twitter", "@ana2"})
	ctx := context.Background()
	const card = "https://r2.example/cards/ana.png"
	e.giveCard(t, card)
	i1 := e.addImage(t, "https://r2.example/uploads/one.jpg")
	e.addImage(t, "https://r2.example/uploads/two.jpg")
	e.fake.Script("CreatePost", success("p-1"), success("p-2"))

	out, err := crew.Post(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: "first", Timing: crew.TimingQueue, Attach: true}})
	if err != nil || out[0].Status != "queued" {
		t.Fatalf("post 1: %+v %v", out, err)
	}
	if _, err := e.q.DeleteLaunchImage(ctx, sqlc.DeleteLaunchImageParams{ID: i1.ID, LaunchID: e.launch.ID}); err != nil {
		t.Fatal(err)
	}
	out, err = crew.Post(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x2", Text: "second", Timing: crew.TimingQueue, Attach: true}})
	if err != nil || out[0].Status != "queued" {
		t.Fatalf("post 2: %+v %v", out, err)
	}
	ins := createInputs(e.fake)
	if len(ins) != 2 {
		t.Fatalf("%d creates", len(ins))
	}
	want1 := []string{card, "https://r2.example/uploads/one.jpg", "https://r2.example/uploads/two.jpg"}
	if got := assetURLs(ins[0]); !reflect.DeepEqual(got, want1) {
		t.Errorf("first post assets = %v, want %v", got, want1)
	}
	want2 := []string{card, "https://r2.example/uploads/two.jpg"}
	if got := assetURLs(ins[1]); !reflect.DeepEqual(got, want2) {
		t.Errorf("second post assets = %v, want %v (removed image gone)", got, want2)
	}
}

func TestCrewPostCapsImagesPerNetwork(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"}, channel{"li", "linkedin", "Ana"})
	ctx := context.Background()
	const card = "https://r2.example/cards/ana.png"
	e.giveCard(t, card)
	var imgs []string
	for i := 0; i < 5; i++ {
		u := fmt.Sprintf("https://r2.example/uploads/%d.jpg", i)
		e.addImage(t, u)
		imgs = append(imgs, u)
	}
	e.fake.Script("CreatePost", success("p-1"), success("p-2"))
	out, err := crew.Post(ctx, e.deps, e.memberID, []crew.Selection{
		{ChannelID: "x1", Text: "x post", Timing: crew.TimingQueue, Attach: true},
		{ChannelID: "li", Text: "li post", Timing: crew.TimingQueue, Attach: true},
	})
	if err != nil || out[0].Status != "queued" || out[1].Status != "queued" {
		t.Fatalf("post: %+v %v", out, err)
	}
	byText := map[string][]string{}
	for _, in := range createInputs(e.fake) {
		byText[in["text"].(string)] = assetURLs(in)
	}
	if want := append([]string{card}, imgs[:3]...); !reflect.DeepEqual(byText["x post"], want) {
		t.Errorf("X assets = %v, want %v (4 max)", byText["x post"], want)
	}
	if want := append([]string{card}, imgs...); !reflect.DeepEqual(byText["li post"], want) {
		t.Errorf("LinkedIn assets = %v, want %v", byText["li post"], want)
	}
}

// Card switch off and no images: nothing is attached, and a network that
// takes no images is not told the post carries one.
func TestNoCardNoImages(t *testing.T) {
	e := setup(t, channel{"yt", "youtube", "Ana"}, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	const card = "https://r2.example/cards/ana.png"
	e.giveCard(t, card)
	if err := e.q.SetLaunchIncludeCard(ctx, sqlc.SetLaunchIncludeCardParams{ID: e.launch.ID, IncludeCard: false}); err != nil {
		t.Fatal(err)
	}
	e.fake.Script("CreatePost", success("p-1"), success("p-2"))
	out, err := crew.Post(ctx, e.deps, e.memberID, []crew.Selection{
		{ChannelID: "yt", Text: "yt post", Timing: crew.TimingQueue, Attach: true},
		{ChannelID: "x1", Text: "x post", Timing: crew.TimingQueue, Attach: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range out {
		if o.Status != "queued" {
			t.Errorf("%s: %+v, want queued (no image to object to)", o.ChannelID, o)
		}
	}
	for _, in := range createInputs(e.fake) {
		if _, has := in["assets"]; has {
			t.Errorf("%v sent assets %v with the card off and no images", in["text"], in["assets"])
		}
	}
}

// Images attach even when the member has no card of their own, and the
// selection only asks: what is attached is what the launch has now, so an
// image added after the person approved goes out too.
func TestCrewPostImagesWithoutCard(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	e.addImage(t, "https://r2.example/uploads/only.jpg")
	e.fake.Script("CreatePost", success("p-1"))
	out, err := crew.Post(context.Background(), e.deps, e.memberID, []crew.Selection{
		{ChannelID: "x1", Text: "x post", Timing: crew.TimingQueue, Attach: true},
	})
	if err != nil || out[0].Status != "queued" {
		t.Fatalf("post: %+v %v", out, err)
	}
	if got := assetURLs(createInputs(e.fake)[0]); !reflect.DeepEqual(got, []string{"https://r2.example/uploads/only.jpg"}) {
		t.Errorf("assets = %v, want the image alone", got)
	}
}

// Bluesky takes images up to 1 MB and Buffer cannot shrink them: an image over
// it goes as its small copy, one without a copy is left out of that post only.
func TestBlueskyGetsSmallCopies(t *testing.T) {
	e := setup(t, channel{"bs", "bluesky", "ana.bsky"}, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	const card = "https://r2.example/cards/ana.png"
	e.giveCard(t, card)
	e.addSizedImage(t, "https://r2.example/uploads/small.png", 500_000, "")
	e.addSizedImage(t, "https://r2.example/uploads/big.png", 3_000_000, "https://r2.example/uploads/big-small.jpg")
	e.addSizedImage(t, "https://r2.example/uploads/huge.png", 9_000_000, "")
	e.fake.Script("CreatePost", success("p-1"), success("p-2"))
	out, err := crew.Post(ctx, e.deps, e.memberID, []crew.Selection{
		{ChannelID: "bs", Text: "bluesky post", Timing: crew.TimingQueue, Attach: true},
		{ChannelID: "x1", Text: "x post", Timing: crew.TimingQueue, Attach: true},
	})
	if err != nil || out[0].Status != "queued" || out[1].Status != "queued" {
		t.Fatalf("post: %+v %v", out, err)
	}
	byText := map[string][]string{}
	for _, in := range createInputs(e.fake) {
		byText[in["text"].(string)] = assetURLs(in)
	}
	want := []string{card, "https://r2.example/uploads/small.png", "https://r2.example/uploads/big-small.jpg"}
	if !reflect.DeepEqual(byText["bluesky post"], want) {
		t.Errorf("Bluesky assets = %v, want %v", byText["bluesky post"], want)
	}
	want = []string{card, "https://r2.example/uploads/small.png", "https://r2.example/uploads/big.png", "https://r2.example/uploads/huge.png"}
	if !reflect.DeepEqual(byText["x post"], want) {
		t.Errorf("X assets = %v, want the originals %v", byText["x post"], want)
	}
}

// When the images cannot be read the post is not sent half-dressed: the
// attempt fails with a retryable note instead.
func TestCrewPostFailsWhenImagesCannotBeRead(t *testing.T) {
	e := setup(t, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	e.giveCard(t, "https://r2.example/cards/ana.png")
	e.addImage(t, "https://r2.example/uploads/one.png")
	// Break the image listing for this test only.
	if _, err := e.pool.Exec(ctx, `ALTER TABLE launch_assets RENAME COLUMN position TO lc_test_hidden`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `ALTER TABLE launch_assets RENAME COLUMN lc_test_hidden TO position`)
	})
	e.fake.Script("CreatePost", success("p-1"))
	// PostAll takes the resolved selection, as Post hands it over once it has
	// picked the card; the rename above also hides the card from Post itself.
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "x1", Text: "x post", Timing: crew.TimingQueue, Attach: true, Card: crew.Image{URL: "https://r2.example/cards/ana.png"}}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != "failed" || !strings.Contains(out[0].Note, "try again") {
		t.Errorf("outcome = %+v, want failed with a retry note", out[0])
	}
	if n := len(createInputs(e.fake)); n != 0 {
		t.Errorf("%d posts reached Buffer without their images", n)
	}
}

// A member's card over Bluesky's limit goes to Bluesky as its small copy and
// to X as it is.
func TestBlueskyGetsTheSmallCardCopy(t *testing.T) {
	e := setup(t, channel{"bs", "bluesky", "ana.bsky"}, channel{"x1", "twitter", "@ana"})
	ctx := context.Background()
	card := crew.Image{URL: "https://r2.example/cards/hero.png", Bytes: 2_000_000, Small: "https://r2.example/uploads/hero-small.jpg"}
	e.addImage(t, "https://r2.example/uploads/one.png")
	e.fake.Script("CreatePost", success("p-1"), success("p-2"))
	out, err := crew.PostAll(ctx, e.deps, e.memberID, []crew.Selection{
		{ChannelID: "bs", Text: "bluesky post", Timing: crew.TimingQueue, Attach: true, Card: card},
		{ChannelID: "x1", Text: "x post", Timing: crew.TimingQueue, Attach: true, Card: card},
	})
	if err != nil || out[0].Status != "queued" || out[1].Status != "queued" {
		t.Fatalf("post: %+v %v", out, err)
	}
	byText := map[string][]string{}
	for _, in := range createInputs(e.fake) {
		byText[in["text"].(string)] = assetURLs(in)
	}
	if want := []string{card.Small, "https://r2.example/uploads/one.png"}; !reflect.DeepEqual(byText["bluesky post"], want) {
		t.Errorf("Bluesky assets = %v, want %v", byText["bluesky post"], want)
	}
	if want := []string{card.URL, "https://r2.example/uploads/one.png"}; !reflect.DeepEqual(byText["x post"], want) {
		t.Errorf("X assets = %v, want %v", byText["x post"], want)
	}
}

// Post fills the card slot with the member's own card, size and copy
// included, so the posting job can pick the right file per network.
func TestPostCarriesTheCardWithItsSmallCopy(t *testing.T) {
	e := setup(t, channel{"bs", "bluesky", "ana.bsky"})
	ctx := context.Background()
	a, err := e.q.InsertLaunchAsset(ctx, sqlc.InsertLaunchAssetParams{
		LaunchID: e.launch.ID, Kind: "contributor", R2Url: "https://r2.example/cards/big.png", Sha: "big",
		Bytes: 2_000_000, SmallUrl: "https://r2.example/uploads/big-small.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpdateCrewMemberCard(ctx, sqlc.UpdateCrewMemberCardParams{ID: e.memberID, CardAssetID: uuid.NullUUID{UUID: a.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.fake.Script("CreatePost", success("p-1"))
	out, err := crew.Post(ctx, e.deps, e.memberID, []crew.Selection{{ChannelID: "bs", Text: "bluesky post", Timing: crew.TimingQueue, Attach: true}})
	if err != nil || out[0].Status != "queued" {
		t.Fatalf("post: %+v %v", out, err)
	}
	if got := assetURLs(createInputs(e.fake)[0]); !reflect.DeepEqual(got, []string{"https://r2.example/uploads/big-small.jpg"}) {
		t.Errorf("Bluesky assets = %v, want the small copy of the card", got)
	}
}
