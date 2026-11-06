package launch_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/launch"
	"github.com/shubham-tomar/launchcrew/internal/testfakes/bufferfake"
)

// queuedCaptain makes the launch launched, with both captain drafts queued in
// Buffer as p-x and p-li, and a clock one minute past launch_at.
func queuedCaptain(t *testing.T, e *env) {
	t.Helper()
	if _, err := launch.Detail(e.ctx, e.deps, e.launch.ID); err != nil { // creates the drafts
		t.Fatal(err)
	}
	if err := e.q.UpdateLaunchStatus(e.ctx, sqlc.UpdateLaunchStatusParams{ID: e.launch.ID, Status: "launched"}); err != nil {
		t.Fatal(err)
	}
	for ch, post := range map[string]string{"ch-x": "p-x", "ch-li": "p-li"} {
		if _, err := e.pool.Exec(e.ctx, `UPDATE captain_drafts SET status = 'queued', buffer_post_id = $3 WHERE launch_id = $1 AND channel_id = $2`, e.launch.ID, ch, post); err != nil {
			t.Fatal(err)
		}
	}
	now := e.launch.LaunchAt.Time.Add(time.Minute)
	e.deps.Now = func() time.Time { return now }
}

func advance(e *env, d time.Duration) {
	now := e.deps.Now().Add(d)
	e.deps.Now = func() time.Time { return now }
}

func postsAre(e *env, statuses map[string]string) {
	var edges []any
	for id, st := range statuses {
		ch := map[string]string{"p-x": "ch-x", "p-li": "ch-li"}[id]
		edges = append(edges, map[string]any{"node": map[string]any{"id": id, "channelId": ch, "text": "t", "status": st, "createdAt": time.Now().Format(time.RFC3339)}})
	}
	e.buf.Handle("GetPostsCreatedBetween", map[string]any{"posts": map[string]any{"edges": edges}})
}

func captainStatuses(t *testing.T, e *env) map[string]string {
	t.Helper()
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, cd := range v.CaptainDrafts {
		got[cd.ChannelID] = cd.Status
	}
	return got
}

func TestDetailRefreshesCaptainPosts(t *testing.T) {
	e := setup(t)
	queuedCaptain(t, e)
	postsAre(e, map[string]string{"p-x": "sent", "p-li": "scheduled"})
	got := captainStatuses(t, e)
	if got["ch-x"] != "posted" || got["ch-li"] != "queued" {
		t.Fatalf("statuses %v, want x posted, li queued", got)
	}
}

func TestDetailMarksErroredCaptainPostFailed(t *testing.T) {
	e := setup(t)
	queuedCaptain(t, e)
	postsAre(e, map[string]string{"p-x": "sent", "p-li": "error"})
	got := captainStatuses(t, e)
	if got["ch-x"] != "posted" || got["ch-li"] != "failed" {
		t.Fatalf("statuses %v, want x posted, li failed", got)
	}
}

func TestDetailCaptainRefreshIsThrottled(t *testing.T) {
	e := setup(t)
	queuedCaptain(t, e)
	postsAre(e, map[string]string{"p-x": "scheduled", "p-li": "scheduled"})
	captainStatuses(t, e)
	n := e.buf.Count("GetPostsCreatedBetween")
	if n == 0 {
		t.Fatal("first load did not ask Buffer")
	}
	advance(e, 59*time.Second)
	captainStatuses(t, e)
	if got := e.buf.Count("GetPostsCreatedBetween"); got != n {
		t.Fatalf("%d Buffer lookups within 60s, want %d", got, n)
	}
	advance(e, 2*time.Second)
	postsAre(e, map[string]string{"p-x": "sent", "p-li": "scheduled"})
	if got := captainStatuses(t, e); got["ch-x"] != "posted" {
		t.Fatalf("statuses %v after 60s, want x posted", got)
	}
	if got := e.buf.Count("GetPostsCreatedBetween"); got <= n {
		t.Fatal("no lookup after 60s")
	}
}

func TestDetailSurvivesBufferFailingOnRefresh(t *testing.T) {
	e := setup(t)
	queuedCaptain(t, e)
	e.buf.HandleFunc("GetPostsCreatedBetween", func(bufferfake.Request) bufferfake.Response {
		return bufferfake.Response{Status: http.StatusInternalServerError, Errors: []map[string]any{{"message": "boom"}}}
	})
	got := captainStatuses(t, e) // fails the test if Detail errors
	if got["ch-x"] != "queued" || got["ch-li"] != "queued" {
		t.Fatalf("statuses %v, want both still queued", got)
	}
}

func TestDetailDoesNotRefreshBeforeLaunchAt(t *testing.T) {
	e := setup(t)
	queuedCaptain(t, e)
	early := e.launch.LaunchAt.Time.Add(-time.Minute)
	e.deps.Now = func() time.Time { return early }
	postsAre(e, map[string]string{"p-x": "sent", "p-li": "sent"})
	captainStatuses(t, e)
	if n := e.buf.Count("GetPostsCreatedBetween"); n != 0 {
		t.Fatalf("%d lookups before launch_at", n)
	}
}

func TestPollStatusMarksCaptainPosts(t *testing.T) {
	e := setup(t)
	queuedCaptain(t, e)
	postsAre(e, map[string]string{"p-x": "sent", "p-li": "error"})
	payload, _ := json.Marshal(launch.LaunchPayload{LaunchID: e.launch.ID})
	if err := launch.PollStatusHandler(e.deps)(context.Background(), jobs.Job{Kind: jobs.PollStatus, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	ds, err := e.q.ListCaptainDrafts(e.ctx, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range ds {
		got[d.ChannelID] = d.Status
	}
	if got["ch-x"] != "posted" || got["ch-li"] != "failed" {
		t.Fatalf("statuses %v, want x posted, li failed", got)
	}
}

func TestPollKeepsTheSentPostsLink(t *testing.T) {
	e := setup(t)
	queuedCaptain(t, e)
	e.buf.Handle("GetPostsCreatedBetween", map[string]any{"posts": map[string]any{"edges": []any{
		map[string]any{"node": map[string]any{"id": "p-x", "channelId": "ch-x", "text": "t", "status": "sent",
			"externalLink": "https://x.com/acme/status/1", "createdAt": time.Now().Format(time.RFC3339)}},
		map[string]any{"node": map[string]any{"id": "p-li", "channelId": "ch-li", "text": "t", "status": "scheduled",
			"createdAt": time.Now().Format(time.RFC3339)}},
	}}})
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]string{}
	for _, cd := range v.CaptainDrafts {
		links[cd.ChannelID] = cd.ExternalLink
	}
	if links["ch-x"] != "https://x.com/acme/status/1" || links["ch-li"] != "" {
		t.Fatalf("links %v", links)
	}
}

func TestListNamesTheCaptain(t *testing.T) {
	e := setup(t)
	ls, err := launch.List(e.ctx, e.deps, e.owner.ID)
	if err != nil || len(ls) != 1 {
		t.Fatalf("list: %v %+v", err, ls)
	}
	if ls[0].CaptainName != "captain" {
		t.Fatalf("captain name %q, want the GitHub login", ls[0].CaptainName)
	}
}
