package launch_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/launch"
	"github.com/shubham-tomar/launchcrew/internal/mail"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

type sentMail struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (s *sentMail) Send(_ context.Context, m mail.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	return nil
}

func (e *env) notify(t *testing.T) {
	t.Helper()
	payload, _ := json.Marshal(trigger.NotifyPayload{LaunchID: e.launch.ID})
	if err := launch.NotifyCaptainHandler(e.deps)(e.ctx, jobs.Job{Kind: jobs.NotifyCaptain, Payload: payload}); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyMentionsTheCaptainByDefault(t *testing.T) {
	e := setup(t)
	e.notify(t)
	// PR 14 is the captain's own: the mention goes there, not on a contributor's PR.
	cs := e.gh.IssueComments(14)
	if len(cs) != 1 || !strings.HasPrefix(cs[0].Body, "@captain your Launch Crew draft for **rocket v1.2.0** is ready") ||
		!strings.Contains(cs[0].Body, "https://lc.test/captain/launches/"+e.launch.ID.String()) {
		t.Fatalf("comments on #14: %+v", cs)
	}
	if n := len(e.gh.IssueComments(12)); n != 0 {
		t.Fatalf("%d comments on a contributor's PR", n)
	}
	if r, _ := e.gh.Release(1001); strings.Contains(r.Body, github.DraftNoteStart) {
		t.Fatal("release notes edited without being chosen")
	}
	// A retried job mentions nobody twice.
	e.notify(t)
	if n := len(e.gh.IssueComments(14)); n != 1 {
		t.Fatalf("%d comments after a retry", n)
	}
	if l, _ := e.q.GetLaunch(e.ctx, e.launch.ID); len(l.CaptainNotified) != 1 || l.CaptainNotified[0] != launch.NotifyGitHub {
		t.Fatalf("notified %v", l.CaptainNotified)
	}
}

func TestNotifyByReleaseNotesAndEmail(t *testing.T) {
	e := setup(t)
	sent := &sentMail{}
	e.deps.Mail = sent
	if _, err := e.q.SetCaptainNotifyChannels(e.ctx, sqlc.SetCaptainNotifyChannelsParams{UserID: e.owner.ID, Github: false, ReleaseNotes: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(e.ctx, `UPDATE captain_notify SET email = true, email_address = 'cap@lc.test' WHERE user_id = $1`, e.owner.ID); err != nil {
		t.Fatal(err)
	}
	e.notify(t)
	e.notify(t)
	if n := len(e.gh.IssueComments(14)); n != 0 {
		t.Fatalf("GitHub mention sent while off: %d", n)
	}
	r, _ := e.gh.Release(1001)
	if !strings.Contains(r.Body, github.DraftNoteStart) || !strings.Contains(r.Body, "/captain/launches/"+e.launch.ID.String()) {
		t.Fatalf("release body %q", r.Body)
	}
	if len(sent.msgs) != 1 || sent.msgs[0].To != "cap@lc.test" || !strings.Contains(sent.msgs[0].Subject, "rocket v1.2.0") {
		t.Fatalf("mail %+v", sent.msgs)
	}
}

func TestNotifySkipsALaunchedDraft(t *testing.T) {
	e := setup(t)
	if err := e.q.UpdateLaunchStatus(e.ctx, sqlc.UpdateLaunchStatusParams{ID: e.launch.ID, Status: "launched"}); err != nil {
		t.Fatal(err)
	}
	e.notify(t)
	if n := len(e.gh.IssueComments(14)); n != 0 {
		t.Fatalf("%d comments for a launched draft", n)
	}
}

// A post marked sent before Buffer knew its link gets the link on the next page load.
func TestDetailFillsInMissingPostLinks(t *testing.T) {
	e := setup(t)
	if _, err := e.pool.Exec(e.ctx, `UPDATE launches SET status = 'launched', launch_at = now() - interval '1 hour' WHERE id = $1`, e.launch.ID); err != nil {
		t.Fatal(err)
	}
	u, _ := e.q.CreateUser(e.ctx)
	t.Cleanup(func() { _, _ = e.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID) })
	access, keyID, _ := e.kr.Encrypt([]byte("tok2"))
	account := "acct-" + uuid.NewString()
	if _, err := e.q.UpsertBufferConnection(e.ctx, sqlc.UpsertBufferConnectionParams{
		UserID: u.ID, BufferAccountID: account, OrgID: pgtype.Text{String: "org-2", Valid: true},
		AccessTokenEnc: access, KeyID: keyID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.UpdateCrewMemberConnection(e.ctx, sqlc.UpdateCrewMemberConnectionParams{
		ID: e.alice.ID, UserID: uuid.NullUUID{UUID: u.ID, Valid: true},
		BufferAccountID: pgtype.Text{String: account, Valid: true}, State: "posted",
	}); err != nil {
		t.Fatal(err)
	}
	dr, err := e.q.UpsertCrewDraft(e.ctx, sqlc.UpsertCrewDraftParams{CrewMemberID: e.alice.ID, ChannelID: "a-x", Network: "twitter", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.q.UpdateCrewDraftPosting(e.ctx, sqlc.UpdateCrewDraftPostingParams{ID: dr.ID, Status: "posted", BufferPostID: pgtype.Text{String: "p-sent", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.buf.Handle("GetPostsCreatedBetween", map[string]any{"posts": map[string]any{"edges": []any{
		map[string]any{"node": map[string]any{"id": "p-sent", "channelId": "a-x", "text": "hi", "status": "sent", "externalLink": "https://x.com/alice/status/9", "createdAt": time.Now().Format(time.RFC3339)}},
	}}})
	v, err := launch.Detail(e.ctx, e.deps, e.launch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Crew {
		if r.ID == e.alice.ID && (len(r.Posts) != 1 || r.Posts[0].ExternalLink != "https://x.com/alice/status/9") {
			t.Fatalf("alice's posts %+v", r.Posts)
		}
	}
}
