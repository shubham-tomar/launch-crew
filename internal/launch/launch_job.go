package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crew"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

const (
	defaultPostTimeout = 15 * time.Second
	pollDelay          = 10 * time.Minute
	checkLaunchTime    = 6              // preflight's "Launch time"
	reconcileBack      = 24 * time.Hour // a stale send is looked for this far back
	lookupAhead        = 2 * time.Minute

	draftDraft    = "draft"
	draftSending  = "sending"
	draftQueued   = "queued"
	captainFailed = "failed"
	// captainSkipped is a post the captain removed from the launch for one
	// channel. It is kept, so it is not rebuilt, and it is never sent.
	captainSkipped = "skipped"

	// lateQueueNote is kept on a captain draft sent after launch_at had
	// passed: Buffer refuses a dueAt in the past, so it took the next slot.
	lateQueueNote = "the launch time had passed when this was sent, so it took the next free slot in your queue"
)

// CommentsPayload is the SendComments payload: one member's PR comment,
// or the launch's roll call.
type CommentsPayload struct {
	LaunchID uuid.UUID
	MemberID uuid.UUID
	RollCall bool
}

// LaunchJobHandler handles jobs.LaunchJob. Runs for one launch are
// serialised by a Postgres advisory lock; a run that finds the lock taken
// returns at once, since the holder does the same work. Every step is
// idempotent, so a retry or a duplicate job creates nothing twice.
func LaunchJobHandler(d Deps) jobs.Handler {
	return func(ctx context.Context, j jobs.Job) error {
		var p LaunchPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		conn, err := d.Pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()
		key := "launchjob:" + p.LaunchID.String()
		var got bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&got); err != nil {
			return err
		}
		if !got {
			log.Printf("launch %s: launch job already running elsewhere", p.LaunchID)
			return nil
		}
		defer func() {
			if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key); err != nil {
				log.Printf("launch %s: unlock: %v", p.LaunchID, err)
				conn.Conn().Close(context.WithoutCancel(ctx)) // closing the session drops the lock
			}
		}()
		return runLaunch(ctx, d, p.LaunchID)
	}
}

func runLaunch(ctx context.Context, d Deps, id uuid.UUID) error {
	s, err := load(ctx, d, id)
	if errors.Is(err, ErrNotFound) {
		log.Printf("launch %s: gone, skipping launch job", id)
		return nil
	}
	if err != nil {
		return err
	}
	if s.l.Status != statusLaunched && s.l.Status != statusClosed {
		log.Printf("launch %s: status %s, skipping launch job", id, s.l.Status)
		return nil
	}
	s.withBuffer(ctx, d)
	if err := ensureCaptainDrafts(ctx, d, s); err != nil {
		return err
	}
	drafts, err := d.Q.ListCaptainDrafts(ctx, id)
	if err != nil {
		return fmt.Errorf("list captain drafts: %w", err)
	}
	// The press already checked the launch time. A retry after launch_at
	// must still finish the launch, so that check does not block the job.
	checks, _ := runPreflight(ctx, d, s, drafts)
	if red := redNames(slices.DeleteFunc(checks, func(c preflight.Check) bool { return c.ID == checkLaunchTime })); len(red) > 0 {
		return &PreflightError{Red: red}
	}
	// A channel Buffer refuses is marked failed and the launch goes on; an
	// unclear error fails the job after the crew is invited, so it retries.
	var retry error
	for _, cd := range drafts {
		if err := postCaptainDraft(ctx, d, s, cd); err != nil && retry == nil {
			retry = fmt.Errorf("captain post on %s: %w", cd.ChannelID, err)
		}
	}
	if retry != nil {
		return retry
	}
	return inviteCrew(ctx, d, s)
}

// postCaptainDraft creates the captain's post for one channel, scheduled at
// launch_at, or added to the queue when launch_at has already passed (a
// retry). The draft moves draft -> sending -> queued, or to a Buffer draft
// (status draft with a post id) when the queue is full, or to failed when
// Buffer refuses it. A send whose answer was lost stays sending and is looked
// up in Buffer before any new create.
func postCaptainDraft(ctx context.Context, d Deps, s *state, cd sqlc.CaptainDraft) error {
	switch {
	case cd.Status == captainSkipped, cd.Status == draftQueued, cd.Status == captainFailed, cd.BufferPostID.Valid:
		return nil
	case cd.Status == draftSending:
		found, err := adoptCaptainPost(ctx, d, s, cd)
		if err != nil || found {
			return err
		}
	default:
		n, err := d.Q.ClaimCaptainDraft(ctx, cd.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil // claimed by another run since we listed it
		}
	}
	if s.conn == nil || !s.channelsOK {
		_ = setCaptainDraft(ctx, d, cd.ID, draftDraft, "", "")
		return errors.New("captain's Buffer channels are unavailable")
	}
	ch, ok := s.channels[cd.ChannelID]
	if !ok {
		return setCaptainDraft(ctx, d, cd.ID, captainFailed, "", "channel is no longer in your Buffer")
	}
	n := network(ch.Service)
	rules := drafting.RulesFor(n, s.cfg)
	at := s.l.LaunchAt.Time
	in := buffer.CreatePostInput{
		ChannelID: ch.ID, Text: cd.Text, Mode: "customScheduled", DueAt: &at,
		FirstComment: cd.FirstComment.String, Notification: buffer.RemindersOnly(ch),
	}
	note := ""
	if !at.After(d.now()) {
		in.Mode, in.DueAt = "addToQueue", nil
		note = lateQueueNote
	}
	if t := threadOf(cd.Thread); len(t) > 1 && rules.Threads {
		in.Text, in.Thread = t[0], t
	}
	if cd.AssetID.Valid && rules.ImageOK {
		if a, err := d.Q.GetLaunchAsset(ctx, cd.AssetID.UUID); err == nil {
			in.AssetURLs = []string{a.R2Url}
		}
	}
	timeout := d.PostTimeout
	if timeout <= 0 {
		timeout = defaultPostTimeout
	}
	cctx, cancel := context.WithTimeout(buffer.WithLaunch(ctx, s.l.ID), timeout)
	res, err := d.Buffer.CreatePost(cctx, s.conn.ID, in)
	cancel()
	wctx := context.WithoutCancel(ctx)
	switch {
	case err == nil && res.SavedAsDraft:
		return setCaptainDraft(wctx, d, cd.ID, draftDraft, res.PostID, res.Note)
	case err == nil:
		return setCaptainDraft(wctx, d, cd.ID, draftQueued, res.PostID, joinNotes(res.Note, note))
	case errors.Is(err, buffer.ErrNeedsReconnect):
		_ = setCaptainDraft(wctx, d, cd.ID, draftDraft, "", "")
		return err
	case definite(err):
		log.Printf("launch %s: captain post on %s refused: %v", s.l.ID, cd.ChannelID, err)
		return setCaptainDraft(wctx, d, cd.ID, captainFailed, "", refusalNote(err))
	}
	return err // unclear: stays sending, the retry looks it up first
}

// adoptCaptainPost looks for a post an earlier unclear send made.
func adoptCaptainPost(ctx context.Context, d Deps, s *state, cd sqlc.CaptainDraft) (bool, error) {
	if s.conn == nil {
		return false, errors.New("captain has no Buffer connection")
	}
	now := d.now()
	posts, err := d.Buffer.PostsCreatedBetween(buffer.WithLaunch(ctx, s.l.ID), s.conn.ID, cd.ChannelID, now.Add(-reconcileBack), now.Add(lookupAhead))
	if err != nil {
		return false, fmt.Errorf("look up earlier send: %w", err)
	}
	want := strings.TrimSpace(cd.Text)
	if t := threadOf(cd.Thread); len(t) > 1 {
		want = strings.TrimSpace(t[0])
	}
	for _, p := range posts {
		if strings.TrimSpace(p.Text) == want {
			return true, setCaptainDraft(ctx, d, cd.ID, draftQueued, p.ID, "")
		}
	}
	return false, nil
}

// joinNotes reads two captain-facing notes as one sentence pair.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

func setCaptainDraft(ctx context.Context, d Deps, id uuid.UUID, status, postID, note string) error {
	return d.Q.UpdateCaptainDraftStatus(ctx, sqlc.UpdateCaptainDraftStatusParams{
		ID: id, Status: status, BufferPostID: pgtype.Text{String: postID, Valid: postID != ""}, Note: note,
	})
}

// refusalNote is the captain-facing reason Buffer gave for refusing a post.
func refusalNote(err error) string {
	var me *buffer.MutationError
	var ge *buffer.GraphQLError
	var he *buffer.HTTPError
	switch {
	case errors.As(err, &he):
		return fmt.Sprintf("Buffer refused it (HTTP %d)", he.Status)
	case errors.As(err, &me) && me.Message != "":
		return "Buffer refused it: " + me.Message
	case errors.As(err, &ge):
		return "Buffer refused it: " + ge.Message
	}
	return "Buffer refused it"
}

// definite reports whether Buffer answered clearly that no post was made.
func definite(err error) bool {
	var me *buffer.MutationError
	var lre *buffer.LimitReachedError
	var ge *buffer.GraphQLError
	var he *buffer.HTTPError
	switch {
	case errors.Is(err, buffer.ErrNeedsReconnect), errors.As(err, &me), errors.As(err, &lre), errors.As(err, &ge):
		return true
	case errors.As(err, &he):
		return he.Status < 500
	}
	return false
}

// inviteCrew gives each ticked contributor a card and an invite token and
// moves them to invited, enqueuing their PR comment in the same transaction
// (or one roll call when more than 20 are invited). The window jobs are
// enqueued once.
func inviteCrew(ctx context.Context, d Deps, s *state) error {
	rows, err := d.Q.ListCrewByLaunch(ctx, s.l.ID)
	if err != nil {
		return fmt.Errorf("list crew: %w", err)
	}
	var ticked []sqlc.CrewMember
	invited := 0
	for _, m := range rows {
		if !m.GithubUserID.Valid || s.isCaptain(m) || m.State == stateOptedOut {
			continue
		}
		switch m.State {
		case trigger.StateTicked:
			if gone, err := optedOut(ctx, d, m); err != nil {
				return err
			} else if gone {
				if err := d.Q.MarkCrewMemberOptedOut(ctx, m.ID); err != nil {
					return err
				}
				continue
			}
			ticked = append(ticked, m)
		case trigger.StateSuggested:
		default:
			invited++
		}
	}
	for i, m := range ticked {
		if m.CardAssetID.Valid {
			continue
		}
		if ticked[i], err = contributorCard(ctx, d, s, m); err != nil {
			return err
		}
	}
	rollCall := invited+len(ticked) > trigger.RollCallThreshold

	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := sqlc.New(tx)
	id := s.l.ID
	newly := 0
	for _, m := range ticked {
		tok, hash := crew.NewToken()
		enc, _, err := d.Keyring.Encrypt([]byte(tok))
		if err != nil {
			return fmt.Errorf("encrypt invite token: %w", err)
		}
		n, err := q.InviteCrewMember(ctx, sqlc.InviteCrewMemberParams{
			ID: m.ID, InviteTokenHash: hash, InviteTokenEnc: enc,
			TokenExpiresAt: pgtype.Timestamptz{Time: d.now().Add(d.tokenTTL()), Valid: true},
		})
		if err != nil {
			return fmt.Errorf("invite %s: %w", m.ID, err)
		}
		if n == 0 {
			continue // unticked or invited meanwhile
		}
		newly++
		if !rollCall {
			if _, err := jobs.Enqueue(ctx, q, jobs.SendComments, CommentsPayload{LaunchID: id, MemberID: m.ID}, time.Time{}, &id); err != nil {
				return err
			}
		}
	}
	if rollCall && newly > 0 {
		if _, err := jobs.Enqueue(ctx, q, jobs.SendComments, CommentsPayload{LaunchID: id, RollCall: true}, time.Time{}, &id); err != nil {
			return err
		}
	}
	if s.l.LaunchAt.Valid {
		closeAt := s.l.LaunchAt.Time.Add(time.Duration(s.l.WindowMinutes) * time.Minute)
		for kind, at := range map[jobs.Kind]time.Time{jobs.CloseWindow: closeAt, jobs.PollStatus: closeAt.Add(pollDelay)} {
			c, err := q.CountLiveJobsOfKindForLaunch(ctx, sqlc.CountLiveJobsOfKindForLaunchParams{Kind: string(kind), LaunchID: uuid.NullUUID{UUID: id, Valid: true}})
			if err != nil {
				return err
			}
			if c == 0 {
				if _, err := jobs.Enqueue(ctx, q, kind, LaunchPayload{LaunchID: id}, at, &id); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit(ctx)
}

// contributorCard renders and uploads a member's card and links it.
func contributorCard(ctx context.Context, d Deps, s *state, m sqlc.CrewMember) (sqlc.CrewMember, error) {
	preset := cards.PresetFor(s.l.ID)
	prs := trigger.MemberPRs(ctx, d.Pool, m.ID, m.PrNumber.Int32, m.PrTitle.String)
	var first trigger.PRRef
	if len(prs) > 0 {
		first = prs[0]
	}
	png, err := d.render()(cards.ContributorCard{
		Name:      s.login(ctx, d, m.GithubUserID.Int64),
		AvatarURL: fmt.Sprintf("https://avatars.githubusercontent.com/u/%d?s=256", m.GithubUserID.Int64),
		Project:   s.project, Version: s.version(), PRTitle: first.Title, PRCount: len(prs),
	}, preset)
	if err != nil {
		return m, fmt.Errorf("render card for %s: %w", m.ID, err)
	}
	a, err := putAsset(ctx, d, d.Q, s.l.ID, "contributor", png)
	if err != nil {
		return m, err
	}
	if err := d.Q.UpdateCrewMemberCard(ctx, sqlc.UpdateCrewMemberCardParams{ID: m.ID, CardAssetID: uuid.NullUUID{UUID: a.ID, Valid: true}}); err != nil {
		return m, err
	}
	m.CardAssetID = uuid.NullUUID{UUID: a.ID, Valid: true}
	return m, nil
}

// titleCase upper-cases the first letter of a role word, as the card prints it.
func titleCase(role string) string {
	if role == "" {
		return ""
	}
	return strings.ToUpper(role[:1]) + role[1:]
}
