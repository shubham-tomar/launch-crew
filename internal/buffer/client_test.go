package buffer_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/bufferfake"
)

// clock is a fake clock whose Sleep advances time instantly.
type clock struct {
	mu    sync.Mutex
	t     time.Time
	slept []time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	c.slept = append(c.slept, d)
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *clock) Slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

type env struct {
	c      *buffer.Client
	q      *sqlc.Queries
	pool   *pgxpool.Pool
	kr     *crypto.Keyring
	fake   *bufferfake.Fake
	clock  *clock
	connID uuid.UUID
	userID uuid.UUID
}

// setup seeds a connection holding access token "old" and refresh token
// "refresh-0" that expires at now+expiresIn.
func setup(t *testing.T, expiresIn time.Duration) *env {
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
	kr, err := crypto.ParseKeyring("k1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(pool)
	clk := &clock{t: time.Now().UTC().Truncate(time.Microsecond)}
	user, err := q.CreateUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	access, keyID, _ := kr.Encrypt([]byte("old"))
	refresh, _, _ := kr.Encrypt([]byte("refresh-0"))
	conn, err := q.UpsertBufferConnection(ctx, sqlc.UpsertBufferConnectionParams{
		UserID: user.ID, BufferAccountID: "acct-" + uuid.NewString(),
		OrgID:          pgtype.Text{String: "org-1", Valid: true},
		AccessTokenEnc: access, RefreshTokenEnc: refresh, KeyID: keyID,
		ExpiresAt: pgtype.Timestamptz{Time: clk.Now().Add(expiresIn), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := bufferfake.New(t, conn.BufferAccountID, "org-1")
	fake.Handle("Ping", map[string]any{"account": map[string]any{"id": "a"}})
	c := buffer.New(&http.Client{Timeout: 5 * time.Second}, q, kr,
		buffer.WithEndpoints(fake.APIURL(), fake.TokenURL()),
		buffer.WithCredentials("cid", "secret"),
		buffer.WithClock(clk.Now, clk.Sleep),
	)
	return &env{c, q, pool, kr, fake, clk, conn.ID, user.ID}
}

const ping = `query Ping { account { id } }`

func (e *env) conn(t *testing.T) sqlc.BufferConnection {
	t.Helper()
	conn, err := e.q.GetBufferConnection(context.Background(), e.connID)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func (e *env) decrypt(t *testing.T, b []byte) string {
	t.Helper()
	p, err := e.kr.Decrypt(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(p)
}

var channelsData = map[string]any{"channels": []map[string]any{{
	"id": "ch1", "name": "octo", "displayName": "Octo Cat", "service": "bluesky",
	"avatar": "https://img/a.png", "isLocked": false, "isQueuePaused": true, "isDisconnected": false,
}}}

func TestRetryAfterSleepsOnce(t *testing.T) {
	e := setup(t, time.Hour)
	ctx := context.Background()
	e.fake.Script("GetChannels",
		bufferfake.Response{Status: 429, Header: map[string]string{"Retry-After": "1"}},
		bufferfake.Response{Data: channelsData},
	)
	start := e.clock.Now()
	chs, err := e.c.Channels(ctx, e.connID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 1 {
		t.Fatalf("channels = %v", chs)
	}
	if n := e.fake.Count("GetChannels"); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	if el := e.clock.Now().Sub(start); el < time.Second {
		t.Fatalf("elapsed %v, want >= 1s", el)
	}
	if s := e.clock.Slept(); len(s) != 1 || s[0] != time.Second {
		t.Fatalf("slept %v, want [1s]", s)
	}

	// A second 429 after the wait gives up and names the wait.
	e.fake.Script("Ping",
		bufferfake.Response{Status: 429, Header: map[string]string{"Retry-After": "7"}},
		bufferfake.Response{Status: 429, Header: map[string]string{"Retry-After": "7"}},
	)
	err = e.c.Do(ctx, e.connID, ping, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "7s") {
		t.Fatalf("err = %v, want one naming 7s", err)
	}
	if n := e.fake.Count("Ping"); n != 2 {
		t.Fatalf("ping requests = %d, want 2", n)
	}

	// A Retry-After beyond the 15-minute cap fails at once without sleeping.
	e.fake.Script("Ping", bufferfake.Response{Status: 429, Header: map[string]string{"Retry-After": "3600"}})
	err = e.c.Do(ctx, e.connID, ping, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "1h0m0s") {
		t.Fatalf("err = %v, want one naming 1h0m0s", err)
	}
	if n := e.fake.Count("Ping"); n != 3 {
		t.Fatalf("ping requests = %d, want 3", n)
	}
	if s := e.clock.Slept(); len(s) != 2 {
		t.Fatalf("slept %v, want no new wait", s)
	}
}

func TestInvalidGrantMarksNeedsReconnect(t *testing.T) {
	e := setup(t, -time.Minute)
	before := e.conn(t)
	e.fake.SetRefresh(bufferfake.RefreshInvalidGrant)
	err := e.c.Do(context.Background(), e.connID, ping, nil, nil)
	if !errors.Is(err, buffer.ErrNeedsReconnect) {
		t.Fatalf("err = %v, want ErrNeedsReconnect", err)
	}
	after := e.conn(t)
	if after.Status != "needs_reconnect" {
		t.Fatalf("status = %q", after.Status)
	}
	if string(after.AccessTokenEnc) != string(before.AccessTokenEnc) || string(after.RefreshTokenEnc) != string(before.RefreshTokenEnc) {
		t.Fatal("tokens were overwritten")
	}
	if n := len(e.fake.Requests()); n != 0 {
		t.Fatalf("graphql requests = %d, want 0", n)
	}
	// Later calls fail fast without touching Buffer.
	if err := e.c.Do(context.Background(), e.connID, ping, nil, nil); !errors.Is(err, buffer.ErrNeedsReconnect) {
		t.Fatalf("second err = %v", err)
	}
	if n := len(e.fake.RefreshForms()); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
}

func TestConcurrentInvalidGrantRefreshesOnce(t *testing.T) {
	e := setup(t, -time.Minute)
	before := e.conn(t)
	e.fake.SetRefresh(bufferfake.RefreshInvalidGrant)
	e.fake.OnToken(func() { time.Sleep(100 * time.Millisecond) })
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = e.c.Do(context.Background(), e.connID, ping, nil, nil)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if !errors.Is(err, buffer.ErrNeedsReconnect) {
			t.Fatalf("call %d err = %v, want ErrNeedsReconnect", i, err)
		}
	}
	if n := len(e.fake.RefreshForms()); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
	if n := len(e.fake.Requests()); n != 0 {
		t.Fatalf("graphql requests = %d, want 0", n)
	}
	after := e.conn(t)
	if after.Status != "needs_reconnect" {
		t.Fatalf("status = %q", after.Status)
	}
	if string(after.AccessTokenEnc) != string(before.AccessTokenEnc) || string(after.RefreshTokenEnc) != string(before.RefreshTokenEnc) {
		t.Fatal("tokens were overwritten")
	}
}

func TestTransportErrorKeepsStatus(t *testing.T) {
	e := setup(t, -time.Minute)
	e.fake.SetRefresh(bufferfake.RefreshDrop)
	err := e.c.Do(context.Background(), e.connID, ping, nil, nil)
	if err == nil || errors.Is(err, buffer.ErrNeedsReconnect) {
		t.Fatalf("err = %v, want a transport error", err)
	}
	conn := e.conn(t)
	if conn.Status != "ok" {
		t.Fatalf("status = %q, want ok", conn.Status)
	}
	if got := e.decrypt(t, conn.RefreshTokenEnc); got != "refresh-0" {
		t.Fatalf("refresh token = %q", got)
	}
}

func TestConcurrentRefreshOnce(t *testing.T) {
	e := setup(t, -time.Minute)
	e.fake.OnToken(func() { time.Sleep(100 * time.Millisecond) })
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = e.c.Do(context.Background(), e.connID, ping, nil, nil)
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.fake.RefreshForms()); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
	reqs := e.fake.Requests()
	if len(reqs) != 2 {
		t.Fatalf("graphql requests = %d, want 2", len(reqs))
	}
	for _, r := range reqs {
		if r.Auth != "Bearer access-1" {
			t.Fatalf("auth = %q, want the refreshed token", r.Auth)
		}
	}
	if got := e.decrypt(t, e.conn(t).AccessTokenEnc); got != "access-1" {
		t.Fatalf("stored access = %q", got)
	}
}

func TestNewRefreshPersistedBeforeNextCall(t *testing.T) {
	e := setup(t, time.Hour)
	e.fake.RejectToken("old")
	var mu sync.Mutex
	var seen []string
	e.fake.OnRequest(func(r bufferfake.Request) {
		// Runs on the fake's goroutine: record, don't t.Fatal.
		conn, err := e.q.GetBufferConnection(context.Background(), e.connID)
		access, _ := e.kr.Decrypt(conn.AccessTokenEnc)
		refresh, _ := e.kr.Decrypt(conn.RefreshTokenEnc)
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, fmt.Sprintf("%s db=%s,%s err=%v", r.Auth, access, refresh, err))
	})
	if err := e.c.Do(context.Background(), e.connID, ping, nil, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"Bearer old db=old,refresh-0 err=<nil>",
		"Bearer access-1 db=access-1,refresh-1 err=<nil>",
	}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("requests saw %q, want %q", seen, want)
	}
	if f := e.fake.RefreshForms(); len(f) != 1 || f[0].Get("refresh_token") != "refresh-0" || f[0].Get("client_secret") != "secret" {
		t.Fatalf("refresh forms = %v", f)
	}
	if exp := e.conn(t).ExpiresAt.Time; !exp.Equal(e.clock.Now().Add(time.Hour)) {
		t.Fatalf("expires_at = %v, want now+1h", exp)
	}
}

func TestBudgetQueuesAtEightyPercent(t *testing.T) {
	e := setup(t, time.Hour)
	ctx := context.Background()
	var mu sync.Mutex
	var at []time.Time
	e.fake.OnRequest(func(bufferfake.Request) {
		mu.Lock()
		defer mu.Unlock()
		at = append(at, e.clock.Now())
	})
	times := func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Time(nil), at...)
	}
	e.fake.Script("Ping", bufferfake.Response{
		Data: map[string]any{"account": map[string]any{"id": "a"}},
		Header: map[string]string{
			"RateLimit":        `"100-in-15min";r=19;t=600, "500-in-1day";r=400;t=80000`,
			"RateLimit-Policy": `"100-in-15min";q=100;w=900;pk=:abc:, "500-in-1day";q=500;w=86400;pk=:abc:`,
		},
	})
	if err := e.c.Do(ctx, e.connID, ping, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := e.clock.Slept(); len(s) != 0 {
		t.Fatalf("first call slept %v", s)
	}
	conn := e.conn(t)
	if conn.RateRemaining.Int32 != 19 || !conn.RateResetAt.Time.Equal(times()[0].Add(600*time.Second)) {
		t.Fatalf("rate = %v reset %v", conn.RateRemaining, conn.RateResetAt.Time)
	}
	if err := e.c.Do(ctx, e.connID, ping, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := e.clock.Slept(); len(s) != 1 || s[0] != 600*time.Second {
		t.Fatalf("slept %v, want [10m0s]", s)
	}
	if at := times(); len(at) != 2 || at[1].Before(at[0].Add(600*time.Second)) {
		t.Fatalf("second call at %v, want after reset", at)
	}

	// A reset further out than the 15-minute cap fails instead of sleeping.
	e.fake.Script("Ping", bufferfake.Response{
		Data:   map[string]any{"account": map[string]any{"id": "a"}},
		Header: map[string]string{"RateLimit": `"100-in-15min";r=5;t=3600`, "RateLimit-Policy": `"100-in-15min";q=100;w=900`},
	})
	if err := e.c.Do(ctx, e.connID, ping, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Do(ctx, e.connID, ping, nil, nil); err == nil || !strings.Contains(err.Error(), "1h0m0s") {
		t.Fatalf("err = %v, want one naming 1h0m0s", err)
	}
	if n := e.fake.Count("Ping"); n != 3 {
		t.Fatalf("ping requests = %d, want 3", n)
	}
	e.clock.Advance(time.Hour)

	// 20 of 100 is not below the 20% line, so no wait.
	e.fake.Script("Ping", bufferfake.Response{
		Data:   map[string]any{"account": map[string]any{"id": "a"}},
		Header: map[string]string{"RateLimit": `"100-in-15min";r=20;t=600`, "RateLimit-Policy": `"100-in-15min";q=100;w=900`},
	})
	for range 2 {
		if err := e.c.Do(ctx, e.connID, ping, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if s := e.clock.Slept(); len(s) != 1 {
		t.Fatalf("slept %v, want no new wait", s)
	}
}

func TestLimitReachedRetriesAsDraft(t *testing.T) {
	e := setup(t, time.Hour)
	ctx := context.Background()
	limit := bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "LimitReachedError", "message": "You've reached your queue limit",
	}}}
	e.fake.Script("CreatePost", limit, bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "p1", "status": "draft"},
	}}})
	res, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ch1", Text: "hi", Mode: "addToQueue"})
	if err != nil {
		t.Fatal(err)
	}
	if res.PostID != "p1" || !res.SavedAsDraft || res.Note != "your queue is full, saved as a draft instead" {
		t.Fatalf("result = %+v", res)
	}
	// Only the posts count; the channel lookup before them is not under test.
	var reqs []bufferfake.Request
	for _, r := range e.fake.Requests() {
		if r.Op == "CreatePost" {
			reqs = append(reqs, r)
		}
	}
	if len(reqs) != 2 {
		t.Fatalf("CreatePost requests = %d, want 2", len(reqs))
	}
	first := reqs[0].Vars["input"].(map[string]any)
	second := reqs[1].Vars["input"].(map[string]any)
	if first["saveToDraft"] == true || second["saveToDraft"] != true {
		t.Fatalf("saveToDraft first=%v second=%v", first["saveToDraft"], second["saveToDraft"])
	}
	if second["text"] != "hi" || second["channelId"] != "ch1" || second["mode"] != "addToQueue" || second["schedulingType"] != "automatic" {
		t.Fatalf("second input = %v", second)
	}

	// If the draft is refused too, the typed error comes back.
	e.fake.Script("CreatePost", limit, limit)
	_, err = e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ch1", Text: "hi", Mode: "addToQueue"})
	var lre *buffer.LimitReachedError
	if !errors.As(err, &lre) || lre.Message != "You've reached your queue limit" {
		t.Fatalf("err = %v, want LimitReachedError", err)
	}
}

func TestChannelsCachedForOneHour(t *testing.T) {
	e := setup(t, 3*time.Hour)
	ctx := context.Background()
	e.fake.Handle("GetChannels", channelsData)
	want := buffer.Channel{ID: "ch1", Name: "Octo Cat", Service: "bluesky", Avatar: "https://img/a.png", Paused: true, Connected: true}
	for range 2 {
		chs, err := e.c.Channels(ctx, e.connID)
		if err != nil {
			t.Fatal(err)
		}
		if len(chs) != 1 || chs[0] != want {
			t.Fatalf("channels = %+v", chs)
		}
	}
	if n := e.fake.Count("GetChannels"); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	if v := e.fake.Requests()[0].Vars["organizationId"]; v != "org-1" {
		t.Fatalf("organizationId = %v", v)
	}
	e.clock.Advance(61 * time.Minute)
	if _, err := e.c.Channels(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.Count("GetChannels"); n != 2 {
		t.Fatalf("requests after 61m = %d, want 2", n)
	}
}

func TestRefreshChannelsRereadsWithinTheHour(t *testing.T) {
	e := setup(t, 3*time.Hour)
	ctx := context.Background()
	e.fake.Handle("GetChannels", channelsData)
	if _, err := e.c.Channels(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	chs, err := e.c.RefreshChannels(ctx, e.connID)
	if err != nil || len(chs) != 1 {
		t.Fatalf("refresh = %+v, %v", chs, err)
	}
	if n := e.fake.Count("GetChannels"); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

func TestRefreshChannelsThrottled(t *testing.T) {
	e := setup(t, 3*time.Hour)
	ctx := context.Background()
	e.fake.Handle("GetChannels", channelsData)
	for range 2 {
		if _, err := e.c.RefreshChannels(ctx, e.connID); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(5 * time.Second)
	}
	if n := e.fake.Count("GetChannels"); n != 1 {
		t.Fatalf("requests within 15s = %d, want 1", n)
	}
	e.clock.Advance(20 * time.Second)
	if _, err := e.c.RefreshChannels(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.Count("GetChannels"); n != 2 {
		t.Fatalf("requests after throttle = %d, want 2", n)
	}
}

func TestAuditRowPerCall(t *testing.T) {
	e := setup(t, time.Hour)
	ctx := context.Background()
	since := pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	count := func() int64 {
		n, err := e.q.CountBufferCallsSince(ctx, sqlc.CountBufferCallsSinceParams{ConnectionID: e.connID, At: since})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("start count = %d", n)
	}
	launch, err := e.q.CreateLaunch(ctx, sqlc.CreateLaunchParams{
		OwnerUserID: e.userID, Source: "manual", HeadlineChanges: []byte("[]"), Roles: []byte("[]"),
		WindowMinutes: 90, CrewCap: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.fake.Script("Ping", bufferfake.Response{
		Data:   map[string]any{"account": map[string]any{"id": "a"}},
		Header: map[string]string{"RateLimit": `"100-in-15min";r=88;t=600`},
	})
	if err := e.c.Do(buffer.WithLaunch(ctx, launch.ID), e.connID, ping, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	if err := e.c.Do(ctx, e.connID, ping, nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
	rows, err := e.pool.Query(ctx, `SELECT launch_id, op, status, request_id, rate_remaining FROM buffer_calls WHERE connection_id = $1 ORDER BY id`, e.connID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var c sqlc.BufferCall
		if err := rows.Scan(&c.LaunchID, &c.Op, &c.Status, &c.RequestID, &c.RateRemaining); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{
			map[bool]string{true: "launch", false: "-"}[c.LaunchID.Valid && c.LaunchID.UUID == launch.ID],
			c.Op, http.StatusText(int(c.Status)), c.RequestID.String,
			map[bool]string{true: "88", false: "-"}[c.RateRemaining.Valid && c.RateRemaining.Int32 == 88],
		}, " "))
	}
	want := []string{"launch Ping OK req-1 88", "- Ping OK req-2 -"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}

func TestConfigurationFallbackIsCached(t *testing.T) {
	e := setup(t, 3*time.Hour)
	ctx := context.Background()
	gqlErr := bufferfake.Response{Errors: []map[string]any{{
		"message": "configuration is not available", "extensions": map[string]any{"code": "INTERNAL_SERVER_ERROR"},
	}}}
	e.fake.Script("GetConfiguration", gqlErr, gqlErr)
	for range 2 {
		cfg, err := e.c.Configuration(ctx, e.connID)
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Fallback || cfg.Services["twitter"].MaxChars != 280 {
			t.Fatalf("config = %+v, want the built-in fallback", cfg)
		}
	}
	if n := e.fake.Count("GetConfiguration"); n != 1 {
		t.Fatalf("requests = %d, want 1 (fallback cached)", n)
	}
	// The fallback is retried sooner than a real answer would be.
	e.clock.Advance(16 * time.Minute)
	if _, err := e.c.Configuration(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.Count("GetConfiguration"); n != 2 {
		t.Fatalf("requests after 16m = %d, want 2", n)
	}
	// A real answer replaces it and is kept for the full hour.
	e.fake.Handle("GetConfiguration", map[string]any{"configuration": map[string]any{"services": []any{
		map[string]any{"service": "twitter", "content": []any{map[string]any{
			"configurationContentTypes": []string{"post"}, "supportedProperties": []string{"thread", "image"},
			"rules": []any{map[string]any{"__typename": "LengthRule", "property": "text", "maxLength": 4000}},
		}}},
	}}})
	e.clock.Advance(16 * time.Minute)
	cfg, err := e.c.Configuration(ctx, e.connID)
	if err != nil || cfg.Fallback || cfg.Services["twitter"].MaxChars != 4000 {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	e.clock.Advance(30 * time.Minute)
	if _, err := e.c.Configuration(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.Count("GetConfiguration"); n != 3 {
		t.Fatalf("requests 30m after a real answer = %d, want 3", n)
	}
	e.clock.Advance(31 * time.Minute)
	if _, err := e.c.Configuration(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.Count("GetConfiguration"); n != 4 {
		t.Fatalf("requests after the hour = %d, want 4", n)
	}
}

func TestDailyPostingLimitsCachedForAMinute(t *testing.T) {
	e := setup(t, 3*time.Hour)
	ctx := context.Background()
	e.fake.Handle("GetChannels", channelsData)
	e.fake.Handle("GetDailyPostingLimits", map[string]any{"dailyPostingLimits": []any{
		map[string]any{"channelId": "ch1", "isAtLimit": true, "limit": 10, "scheduled": 7, "sent": 3},
	}})
	for range 3 {
		limits, err := e.c.DailyPostingLimits(ctx, e.connID)
		if err != nil {
			t.Fatal(err)
		}
		l, ok := limits["ch1"]
		if !ok || !l.IsAtLimit || l.Limit == nil || *l.Limit != 10 || l.Scheduled != 7 || l.Sent != 3 {
			t.Fatalf("limits = %+v", limits)
		}
	}
	if n := e.fake.Count("GetDailyPostingLimits"); n != 1 {
		t.Fatalf("requests = %d, want 1 (cached)", n)
	}
	e.clock.Advance(61 * time.Second)
	if _, err := e.c.DailyPostingLimits(ctx, e.connID); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.Count("GetDailyPostingLimits"); n != 2 {
		t.Fatalf("requests after 61s = %d, want 2", n)
	}
}

// Two machines overlap during a deploy: the other one refreshes first and
// stores new tokens, so this one's refresh token is spent and Buffer answers
// invalid_grant. The connection must not be marked needs_reconnect, since
// the stored tokens are good; the call goes on with them.
func TestInvalidGrantAfterForeignRefreshKeepsConnection(t *testing.T) {
	e := setup(t, -time.Minute)
	e.fake.SetRefresh(bufferfake.RefreshInvalidGrant)
	e.fake.OnToken(func() {
		access, keyID, _ := e.kr.Encrypt([]byte("access-other"))
		refresh, _, _ := e.kr.Encrypt([]byte("refresh-other"))
		_ = e.q.UpdateConnectionTokens(context.Background(), sqlc.UpdateConnectionTokensParams{
			ID: e.connID, AccessTokenEnc: access, RefreshTokenEnc: refresh, KeyID: keyID,
			ExpiresAt: pgtype.Timestamptz{Time: e.clock.Now().Add(time.Hour), Valid: true},
		})
	})
	if err := e.c.Do(context.Background(), e.connID, ping, nil, nil); err != nil {
		t.Fatalf("err = %v, want the call to go on with the other machine's token", err)
	}
	conn := e.conn(t)
	if conn.Status != "ok" {
		t.Fatalf("status = %q, want ok", conn.Status)
	}
	if got := e.decrypt(t, conn.AccessTokenEnc); got != "access-other" {
		t.Fatalf("stored access = %q, want the other machine's", got)
	}
	reqs := e.fake.Requests()
	if len(reqs) != 1 || reqs[0].Auth != "Bearer access-other" {
		t.Fatalf("requests = %+v, want one with the other machine's token", reqs)
	}
	if n := len(e.fake.RefreshForms()); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
}

func postInputOf(t *testing.T, e *env) map[string]any {
	t.Helper()
	reqs := e.fake.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].Op == "CreatePost" {
			return reqs[i].Vars["input"].(map[string]any)
		}
	}
	t.Fatal("no CreatePost request")
	return nil
}

func TestThreadCarriesTheCardOnItsRootItem(t *testing.T) {
	e := setup(t, time.Hour)
	ctx := context.Background()
	e.fake.Handle("GetChannels", channelsData)
	e.fake.Handle("CreatePost", map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "p1", "status": "scheduled"},
	}})
	_, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{
		ChannelID: "ch1", Mode: "addToQueue", Thread: []string{"one", "two"},
		AssetURLs: []string{"https://cards.example/card.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := postInputOf(t, e)
	if _, ok := input["assets"]; ok {
		t.Fatalf("top-level assets sent with a thread: %v", input)
	}
	items := input["metadata"].(map[string]any)["bluesky"].(map[string]any)["thread"].([]any)
	if len(items) != 2 {
		t.Fatalf("thread = %v", items)
	}
	root := items[0].(map[string]any)
	assets, ok := root["assets"].([]any)
	if !ok || len(assets) != 1 || assets[0].(map[string]any)["image"].(map[string]any)["url"] != "https://cards.example/card.png" {
		t.Fatalf("root item = %v", root)
	}
	if _, ok := items[1].(map[string]any)["assets"]; ok {
		t.Fatalf("reply carries assets: %v", items[1])
	}
	if input["text"] != "one" {
		t.Fatalf("text = %v, want the root", input["text"])
	}

	// Without a thread the card stays at the top level.
	if _, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{
		ChannelID: "ch1", Text: "solo", Mode: "addToQueue", AssetURLs: []string{"https://cards.example/card.png"},
	}); err != nil {
		t.Fatal(err)
	}
	if input := postInputOf(t, e); input["assets"] == nil || input["metadata"] != nil {
		t.Fatalf("single post input = %v", input)
	}
}

func TestInstagramProfileNeedsReminders(t *testing.T) {
	ig := buffer.Channel{ID: "ig", Service: "instagram", Type: "profile"}
	if !buffer.RemindersOnly(ig) {
		t.Fatal("a personal Instagram profile should post by reminder")
	}
	for _, ch := range []buffer.Channel{
		{Service: "instagram", Type: "business"}, {Service: "instagram"}, {Service: "linkedin", Type: "profile"},
	} {
		if buffer.RemindersOnly(ch) {
			t.Fatalf("%+v should publish automatically", ch)
		}
	}

	e := setup(t, time.Hour)
	ctx := context.Background()
	e.fake.Handle("CreatePost", map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "p1", "status": "scheduled"},
	}})
	res, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ig", Text: "hi", Mode: "addToQueue", Notification: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Notification || res.Note != buffer.NotifyNote || res.PostID != "p1" {
		t.Fatalf("result = %+v", res)
	}
	if input := postInputOf(t, e); input["schedulingType"] != "notification" {
		t.Fatalf("schedulingType = %v", input["schedulingType"])
	}
}

func TestReminderRefusalRetriesAsNotification(t *testing.T) {
	e := setup(t, time.Hour)
	ctx := context.Background()
	refused := bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "InvalidInputError", "message": "This channel only supports notification publishing. Use a reminder instead.",
	}}}
	e.fake.Script("CreatePost", refused, bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "p2", "status": "scheduled"},
	}}})
	res, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ig", Text: "hi", Mode: "addToQueue"})
	if err != nil {
		t.Fatal(err)
	}
	if res.PostID != "p2" || !res.Notification || res.Note != buffer.NotifyNote {
		t.Fatalf("result = %+v", res)
	}
	// Only the posts count; the channel lookup before them is not under test.
	var reqs []bufferfake.Request
	for _, r := range e.fake.Requests() {
		if r.Op == "CreatePost" {
			reqs = append(reqs, r)
		}
	}
	if len(reqs) != 2 {
		t.Fatalf("CreatePost requests = %d, want 2", len(reqs))
	}
	first := reqs[0].Vars["input"].(map[string]any)
	second := reqs[1].Vars["input"].(map[string]any)
	if first["schedulingType"] != "automatic" || second["schedulingType"] != "notification" {
		t.Fatalf("schedulingType first=%v second=%v", first["schedulingType"], second["schedulingType"])
	}

	// Any other refusal is not retried.
	e.fake.Script("CreatePost", bufferfake.Response{Data: map[string]any{"createPost": map[string]any{
		"__typename": "InvalidInputError", "message": "Text too long",
	}}})
	_, err = e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ig", Text: "hi", Mode: "addToQueue"})
	var me *buffer.MutationError
	if !errors.As(err, &me) || e.fake.Count("CreatePost") != 3 {
		t.Fatalf("err = %v, CreatePost calls = %d", err, e.fake.Count("CreatePost"))
	}
}

// Buffer refuses an Instagram post without a type ("Instagram posts require a
// type (post, story, or reel)"), so every Instagram post says it is a feed
// post, several images included (Buffer's composer makes that a carousel).
func TestInstagramPostsSayTheirType(t *testing.T) {
	e := setup(t, time.Hour)
	ctx := context.Background()
	e.fake.Handle("GetChannels", map[string]any{"channels": []map[string]any{
		{"id": "ig", "name": "rocket", "service": "instagram", "type": "business"},
		{"id": "ch1", "name": "octo", "service": "bluesky"},
	}})
	e.fake.Handle("CreatePost", map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "p1", "status": "scheduled"},
	}})
	igOf := func(input map[string]any) map[string]any {
		t.Helper()
		md, _ := input["metadata"].(map[string]any)
		ig, _ := md["instagram"].(map[string]any)
		if ig == nil {
			t.Fatalf("no instagram metadata in %v", input)
		}
		return ig
	}

	two := []string{"https://cards.example/a.png", "https://cards.example/b.jpg"}
	if _, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ig", Text: "hi", Mode: "addToQueue", AssetURLs: two}); err != nil {
		t.Fatal(err)
	}
	input := postInputOf(t, e)
	if ig := igOf(input); ig["type"] != "post" || ig["shouldShareToFeed"] != true {
		t.Fatalf("instagram metadata = %v, want type post shared to feed", ig)
	}
	if assets, _ := input["assets"].([]any); len(assets) != 2 {
		t.Fatalf("assets = %v, want both images", input["assets"])
	}

	// A first comment rides in the same metadata, next to the type.
	if _, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ig", Text: "hi", Mode: "addToQueue", AssetURLs: two[:1], FirstComment: "link"}); err != nil {
		t.Fatal(err)
	}
	if ig := igOf(postInputOf(t, e)); ig["type"] != "post" || ig["firstComment"] != "link" {
		t.Fatalf("instagram metadata = %v", ig)
	}

	// Other networks are untouched.
	if _, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ch1", Text: "solo", Mode: "addToQueue"}); err != nil {
		t.Fatal(err)
	}
	if input := postInputOf(t, e); input["metadata"] != nil {
		t.Fatalf("bluesky input carries metadata: %v", input)
	}
}

// A story says so in the network's metadata; a LinkedIn document goes as the
// one asset, in place of the images.
func TestStoriesAndDocuments(t *testing.T) {
	e := setup(t, time.Hour)
	ctx := context.Background()
	e.fake.Handle("GetChannels", map[string]any{"channels": []map[string]any{
		{"id": "ig", "name": "rocket", "service": "instagram", "type": "business"},
		{"id": "fb", "name": "rocket", "service": "facebook"},
		{"id": "li", "name": "rocket", "service": "linkedin"},
	}})
	e.fake.Handle("CreatePost", map[string]any{"createPost": map[string]any{
		"__typename": "PostActionSuccess", "post": map[string]any{"id": "p1", "status": "scheduled"},
	}})
	imgs := []string{"https://cards.example/a.png", "https://cards.example/b.jpg"}
	mdOf := func(input map[string]any, key string) map[string]any {
		t.Helper()
		md, _ := input["metadata"].(map[string]any)
		m, _ := md[key].(map[string]any)
		if m == nil {
			t.Fatalf("no %s metadata in %v", key, input)
		}
		return m
	}

	if _, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "ig", Text: "hi", Mode: "addToQueue", AssetURLs: imgs, Story: true}); err != nil {
		t.Fatal(err)
	}
	if ig := mdOf(postInputOf(t, e), "instagram"); ig["type"] != "story" || ig["shouldShareToFeed"] != false {
		t.Fatalf("instagram story metadata = %v", ig)
	}

	if _, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "fb", Text: "hi", Mode: "addToQueue", AssetURLs: imgs, Story: true}); err != nil {
		t.Fatal(err)
	}
	if fb := mdOf(postInputOf(t, e), "facebook"); fb["type"] != "story" {
		t.Fatalf("facebook story metadata = %v", fb)
	}
	// A plain Facebook post still sends no metadata.
	if _, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "fb", Text: "hi", Mode: "addToQueue", AssetURLs: imgs}); err != nil {
		t.Fatal(err)
	}
	if input := postInputOf(t, e); input["metadata"] != nil {
		t.Fatalf("plain facebook post carries metadata: %v", input)
	}

	doc := &buffer.Document{URL: "https://cards.example/docs/x.pdf", Title: "Rocket 1.2"}
	if _, err := e.c.CreatePost(ctx, e.connID, buffer.CreatePostInput{ChannelID: "li", Text: "hi", Mode: "addToQueue", AssetURLs: imgs, Document: doc}); err != nil {
		t.Fatal(err)
	}
	assets, _ := postInputOf(t, e)["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("assets = %v, want just the document", assets)
	}
	d, _ := assets[0].(map[string]any)["document"].(map[string]any)
	if d["url"] != doc.URL || d["title"] != doc.Title {
		t.Fatalf("document asset = %v", assets[0])
	}

	for _, c := range []struct {
		service, format string
		ok              bool
	}{
		{"instagram", "story", true}, {"facebook", "story", true}, {"linkedin", "document", true},
		{"twitter", "story", false}, {"linkedin", "story", false}, {"instagram", "document", false},
		{"bluesky", "", true}, {"instagram", "reel", false},
	} {
		if got := buffer.FormatAllowed(c.service, c.format); got != c.ok {
			t.Errorf("FormatAllowed(%q, %q) = %v, want %v", c.service, c.format, got, c.ok)
		}
	}
}
