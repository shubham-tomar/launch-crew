package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/cards"
	"github.com/shubham-tomar/launchcrew/internal/crypto"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/preflight"
)

// ErrWindowClosed means the launch is closed, so "land in the window" is no longer offered.
var ErrWindowClosed = errors.New("crew: the launch window is closed")

// Deps are what the crew service needs.
type Deps struct {
	Q         *sqlc.Queries
	Pool      *pgxpool.Pool
	Buffer    *buffer.Client
	Preflight preflight.Deps
	Now       func() time.Time // defaults to time.Now
	Rand      *rand.Rand       // window jitter; the global source when nil
	// PostTimeout bounds one createPost; 15 s when zero.
	PostTimeout time.Duration

	Drafting *drafting.Service
	Cards    *cards.Uploader
	// Docs builds the PDF a LinkedIn post sends as a swipeable document.
	Docs DocumentStore
	// Enqueue adds a job; jobs.Enqueue bound to Q in production.
	Enqueue func(ctx context.Context, kind jobs.Kind, payload any, runAt time.Time, launchID *uuid.UUID) (int64, error)
	BaseURL string // public origin, for open links
	// RenderSlide draws the QR slide PNG; cards.RenderQRSlideLogo when nil.
	RenderSlide func(title, shortURL string, logo cards.SlideLogo) ([]byte, error)
	Keyring     *crypto.Keyring
	// TokenTTL is how long an invite token works; 30 days when zero.
	TokenTTL time.Duration
}

// Selection is one channel the member ticked.
type Selection struct {
	ChannelID string `json:"channelId"`
	Text      string `json:"text"`
	Timing    Timing `json:"timing"`
	// Attach asks for the launch's images: the member's card, then the
	// captain's uploads. What goes out is resolved when the post is made,
	// never from what the page showed when the person approved.
	Attach bool `json:"attach"`
	// Card is the member's card slot, filled in by Post; a caller cannot set it.
	Card Image `json:"-"`
	// Format is "story" (Instagram, Facebook), "document" (LinkedIn, the
	// images as one swipeable PDF) or "" for a plain post. One the channel
	// cannot take is sent as a plain post.
	Format string `json:"format"`
}

// Outcome is what happened on one channel.
type Outcome struct {
	ChannelID string     `json:"channelId"`
	Service   string     `json:"service"`
	Status    string     `json:"status"` // queued | draft | failed
	When      *time.Time `json:"when"`
	Note      string     `json:"note"`
	PostID    string     `json:"postId"` // Buffer post id; empty when failed
}

const (
	statusPending = "pending"
	statusQueued  = "queued"
	statusDraft   = "draft"
	statusFailed  = "failed"

	defaultPostTimeout = 15 * time.Second
	lookupTimeout      = 10 * time.Second
	lookupBack         = 2 * time.Minute // immediate lookup reaches back this far
	lookupAhead        = 2 * time.Minute // and past now (clock skew, slow indexing)
	reconcileBack      = time.Minute     // before the earlier send
	staleAfter         = 2 * time.Minute // a pending claim older than this is abandoned
	attemptWrites      = 3
	attemptWriteGap    = 100 * time.Millisecond
	recentLimit        = 30

	noteReconnect  = "reconnect Buffer"
	noteInFlight   = "still working on the last tap, try again in a moment"
	noteNoAnswer   = "Buffer is not answering, try again"
	noteOurs       = "something went wrong on our side, try again"
	noteNotOffered = "this channel is not one of your drafts"
	noteBadTiming  = "pick when the post should go out"
	noteNoTime     = "the launch has no time set yet, pick next free slot"
	noteWindowOver = "the launch window is over, pick next free slot"
	noteEmpty      = "the post is empty, add some text"
	noteRefused    = "Buffer did not take the post, try again"
	// noteRecordMissing is set when the post exists in Buffer but the
	// crew_drafts write failed for good; the attempt still records the post.
	noteRecordMissing = "posted to Buffer, but we could not save the record; the board may show it late"
)

var randMu sync.Mutex // *rand.Rand is not safe for concurrent use

// PostAll posts each selection to the member's Buffer, in order. One
// channel's failure never stops the rest. The error is non-nil only for
// ErrWindowClosed or when the member or launch cannot be loaded.
//
// Each channel is guarded by a post_attempts row: pending while createPost
// runs, then queued, draft or failed. A tap that finds queued or draft
// returns the stored outcome without calling Buffer; one that finds a
// fresh pending reports it is still working. A failed attempt whose send
// was unclear, or a pending one older than two minutes, is reclaimed and
// first looked up in Buffer by its sent text; only if no post is found is
// a new one created.
func PostAll(ctx context.Context, deps Deps, memberID uuid.UUID, selections []Selection) ([]Outcome, error) {
	m, err := deps.Q.GetCrewMember(ctx, memberID)
	if err != nil {
		return nil, fmt.Errorf("crew: load member: %w", err)
	}
	l, err := deps.Q.GetLaunch(ctx, m.LaunchID)
	if err != nil {
		return nil, fmt.Errorf("crew: load launch: %w", err)
	}
	if l.Status == "closed" {
		for _, s := range selections {
			if s.Timing == TimingWindow {
				return nil, ErrWindowClosed
			}
		}
	}
	p := &poster{deps: deps, member: m, launch: l}
	p.load(ctx)
	out := make([]Outcome, 0, len(selections))
	for _, s := range selections {
		if ctx.Err() != nil {
			out = append(out, Outcome{ChannelID: s.ChannelID, Service: p.service(s.ChannelID), Status: statusFailed, Note: noteOurs})
			continue
		}
		// Once an attempt is claimed it must reach a final state, so the
		// rest runs even if the caller goes away.
		out = append(out, p.post(context.WithoutCancel(ctx), s))
	}
	return out, nil
}

type poster struct {
	deps   Deps
	member sqlc.CrewMember
	launch sqlc.Launch

	connID    uuid.UUID
	drafts    map[string]sqlc.CrewDraft
	channels  map[string]buffer.Channel
	cfg       *buffer.Config
	setupNote string // set when every channel must fail
}

// load reads the member's drafts, connection, channels and rules.
func (p *poster) load(ctx context.Context) {
	p.drafts = map[string]sqlc.CrewDraft{}
	ds, err := p.deps.Q.ListCrewDrafts(ctx, p.member.ID)
	if err != nil {
		log.Printf("crew: list drafts for %s: %v", p.member.ID, err)
		p.setupNote = noteOurs
		return
	}
	for _, d := range ds {
		p.drafts[d.ChannelID] = d
	}
	if !p.member.BufferAccountID.Valid {
		p.setupNote = noteReconnect
		return
	}
	conn, err := p.deps.Q.GetBufferConnectionByAccount(ctx, p.member.BufferAccountID.String)
	if err != nil || (p.member.UserID.Valid && conn.UserID != p.member.UserID.UUID) {
		p.setupNote = noteReconnect
		return
	}
	p.connID = conn.ID
	chs, err := p.deps.Buffer.Channels(ctx, conn.ID)
	if err != nil {
		p.setupNote = noteNoAnswer
		if errors.Is(err, buffer.ErrNeedsReconnect) {
			p.setupNote = noteReconnect
		}
		return
	}
	p.channels = map[string]buffer.Channel{}
	for _, ch := range chs {
		p.channels[ch.ID] = ch
	}
	if cfg, err := p.deps.Buffer.Configuration(ctx, conn.ID); err == nil {
		p.cfg = &cfg
	}
}

// service names the channel's network: Buffer's service, else the draft's.
func (p *poster) service(channelID string) string {
	if ch, ok := p.channels[channelID]; ok {
		return ch.Service
	}
	return p.drafts[channelID].Network
}

func (p *poster) now() time.Time {
	if p.deps.Now != nil {
		return p.deps.Now()
	}
	return time.Now()
}

// job is one selection on its way to Buffer.
type job struct {
	sel     Selection
	draft   sqlc.CrewDraft
	channel buffer.Channel
	network string
	rules   drafting.Rules
	out     Outcome

	// What goes to Buffer.
	text         string
	thread       []string
	firstComment string
	due          *time.Time

	// Attempt state carried into the final write.
	unclear   bool               // a send may have reached Buffer unseen
	sentText  pgtype.Text        // text of that send, the lookup key
	sentDue   pgtype.Timestamptz // dueAt of that send
	reconcile *time.Time         // when the earlier send went out (or was claimed)
}

func (j *job) fail(note string) Outcome {
	j.out.Status, j.out.Note = statusFailed, note
	return j.out
}

func (p *poster) post(ctx context.Context, s Selection) Outcome {
	j := &job{sel: s, out: Outcome{ChannelID: s.ChannelID, Service: p.service(s.ChannelID)}}
	d, ok := p.drafts[s.ChannelID]
	if !ok {
		return j.fail(firstNonEmpty(p.setupNote, noteNotOffered))
	}
	j.draft, j.network = d, d.Network
	switch s.Timing {
	case TimingWindow, TimingQueue, TimingDraft:
	default:
		return j.fail(noteBadTiming)
	}

	if replay, done := p.claim(ctx, j); done {
		return replay
	}
	// From here the attempt is pending and every exit records an outcome.
	o := p.attempt(ctx, j)
	if o.Status == statusFailed {
		p.setAttempt(ctx, j, statusFailed, "")
	}
	return o
}

// claimRow is the attempt as it was before a reclaim.
type claimRow = sqlc.ClaimFailedPostAttemptRow

// claim takes the channel's attempt row. done means the returned outcome
// stands and Buffer must not be called.
func (p *poster) claim(ctx context.Context, j *job) (Outcome, bool) {
	key := sqlc.GetPostAttemptParams{CrewMemberID: p.member.ID, ChannelID: j.sel.ChannelID}
	_, err := p.deps.Q.InsertPostAttempt(ctx, sqlc.InsertPostAttemptParams{
		CrewMemberID: key.CrewMemberID, ChannelID: key.ChannelID, Status: statusPending,
	})
	if err == nil {
		return Outcome{}, false
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("crew: claim %s/%s: %v", key.CrewMemberID, key.ChannelID, err)
		return j.fail(noteOurs), true
	}
	a, err := p.deps.Q.GetPostAttempt(ctx, key)
	if err != nil {
		log.Printf("crew: read attempt %s/%s: %v", key.CrewMemberID, key.ChannelID, err)
		return j.fail(noteOurs), true
	}
	var prev claimRow
	switch {
	case a.Status == statusQueued || a.Status == statusDraft:
		return p.replay(ctx, j, a), true
	case a.Status == statusFailed:
		prev, err = p.deps.Q.ClaimFailedPostAttempt(ctx, sqlc.ClaimFailedPostAttemptParams(key))
	case a.Status == statusPending && a.ClaimedAt.Time.Before(p.now().Add(-staleAfter)):
		var r sqlc.ClaimStalePostAttemptRow
		r, err = p.deps.Q.ClaimStalePostAttempt(ctx, sqlc.ClaimStalePostAttemptParams{
			CrewMemberID: key.CrewMemberID, ChannelID: key.ChannelID,
			OlderThan: pgtype.Timestamptz{Time: p.now().Add(-staleAfter), Valid: true},
		})
		// Whatever the stale tap did is unknown: treat it as an unclear send.
		prev, prev.Unclear = claimRow(r), true
	default:
		return j.fail(noteInFlight), true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return j.fail(noteInFlight), true // another tap took it first
	}
	if err != nil {
		log.Printf("crew: reclaim %s/%s: %v", key.CrewMemberID, key.ChannelID, err)
		return j.fail(noteOurs), true
	}
	j.unclear, j.sentText, j.sentDue = prev.Unclear, prev.SentText, prev.SentDueAt
	if prev.Unclear {
		// Anchor on the send itself: claimed_at moves on every reclaim,
		// sent_at only when something is sent. A stale pending that died
		// before sending has no sent_at; its pre-claim claimed_at stands in.
		t := prev.ClaimedAt.Time
		if prev.SentAt.Valid {
			t = prev.SentAt.Time
		}
		j.reconcile = &t
	}
	return Outcome{}, false
}

// replay rebuilds a finished outcome from the attempt and the crew_drafts
// row, which is written before the attempt is marked done.
func (p *poster) replay(ctx context.Context, j *job, a sqlc.PostAttempt) Outcome {
	d, err := p.deps.Q.GetCrewDraft(ctx, j.draft.ID)
	if err != nil {
		log.Printf("crew: reread draft %s: %v", j.draft.ID, err)
		d = j.draft
	}
	j.out.Status = a.Status
	j.out.Note = a.Note.String
	if !a.Note.Valid {
		j.out.Note = draftNote(a.Status, j.sel.Timing)
	}
	if d.DueAt.Valid {
		t := d.DueAt.Time.UTC()
		j.out.When = &t
	}
	return j.out
}

// attempt reconciles any unclear earlier send, then runs the checks and
// createPost for a claimed channel.
func (p *poster) attempt(ctx context.Context, j *job) Outcome {
	if p.setupNote != "" {
		return j.fail(p.setupNote)
	}
	ch, ok := p.channels[j.sel.ChannelID]
	if !ok {
		return j.fail("this channel is no longer in your Buffer")
	}
	j.channel, j.out.Service = ch, ch.Service
	if j.network == "" {
		j.network = ch.Service
	}
	j.rules = drafting.RulesFor(drafting.Network(j.network), p.cfg)
	edited := p.fit(j)

	if j.reconcile != nil {
		if o, done := p.reconcile(ctx, j); done {
			return o
		}
	}

	if strings.TrimSpace(j.text) == "" {
		return j.fail(noteEmpty)
	}
	if edited {
		for _, t := range append([]string{j.text}, tail(j.thread)...) {
			if len([]rune(t)) > j.rules.MaxChars {
				return j.fail(lengthNote(j))
			}
		}
	}
	if j.sel.Timing == TimingWindow {
		if !p.launch.LaunchAt.Valid {
			return j.fail(noteNoTime)
		}
		t, ok := p.dueAt()
		if !ok {
			return j.fail(noteWindowOver)
		}
		t = t.UTC()
		j.due = &t
	}

	recent, err := p.deps.Q.ListRecentQueuedTexts(ctx, sqlc.ListRecentQueuedTextsParams{
		LaunchID: p.launch.ID, Network: j.network, Limit: recentLimit,
	})
	if err != nil {
		log.Printf("crew: recent texts for %s: %v", p.launch.ID, err)
		return j.fail(noteOurs)
	}
	// Resolved now, not when the person approved: an image the captain
	// removed since is not sent, one they added since is.
	assets, pages, err := p.attachments(ctx, j)
	if err != nil {
		log.Printf("crew: images for %s: %v", p.launch.ID, err)
		return j.fail(noteOurs)
	}
	checks, ok := preflight.ForCrewDraft(ctx, p.deps.Preflight, preflight.CrewDraft{
		Draft: preflight.Draft{
			ChannelID: ch.ID, Service: ch.Service, Network: j.network, Name: ch.Name,
			Text: j.text, Thread: j.thread, FirstComment: j.firstComment, HasAsset: len(assets) > 0, Format: j.format(),
		},
		ConnectionID: p.connID, AssetURLs: assets, LaunchAt: p.launch.LaunchAt.Time,
	}, recent)
	if !ok {
		for _, c := range checks {
			if !c.OK {
				return j.fail(c.Fix)
			}
		}
	}

	in := buffer.CreatePostInput{
		ChannelID: ch.ID, Text: j.text, Mode: "addToQueue",
		FirstComment: j.firstComment, Thread: j.thread, Notification: buffer.RemindersOnly(ch),
	}
	switch j.sel.Timing {
	case TimingWindow:
		in.Mode, in.DueAt = "customScheduled", j.due
	case TimingDraft:
		in.SaveToDraft = true
	}
	in.AssetURLs = assets
	if err := ApplyFormat(ctx, p.deps.Docs, &in, j.channel.Service, j.format(), p.launch.Title, pages); err != nil {
		log.Printf("crew: document for %s: %v", p.launch.ID, err)
		return j.fail(noteOurs)
	}

	// Record what is about to be sent, so a later tap can look for it.
	j.sentText = pgtype.Text{String: j.text, Valid: true}
	j.sentDue = pgtype.Timestamptz{}
	if j.due != nil {
		j.sentDue = pgtype.Timestamptz{Time: *j.due, Valid: true}
	}
	if err := p.deps.Q.SetPostAttemptSending(ctx, sqlc.SetPostAttemptSendingParams{
		CrewMemberID: p.member.ID, ChannelID: ch.ID, SentText: j.sentText, SentDueAt: j.sentDue,
	}); err != nil {
		log.Printf("crew: record send %s/%s: %v", p.member.ID, ch.ID, err)
		return j.fail(noteOurs)
	}

	timeout := p.deps.PostTimeout
	if timeout <= 0 {
		timeout = defaultPostTimeout
	}
	cctx, cancel := context.WithTimeout(buffer.WithLaunch(ctx, p.launch.ID), timeout)
	res, err := p.deps.Buffer.CreatePost(cctx, p.connID, in)
	cancel()
	switch {
	case err == nil:
		return p.succeed(ctx, j, res.PostID, res.SavedAsDraft, res.Note, j.due)
	case errors.Is(err, buffer.ErrNeedsReconnect):
		j.unclear = false
		return j.fail(noteReconnect)
	case definite(err):
		j.unclear = false
		return j.fail(rejectNote(j, err))
	}
	// Buffer may have created the post before the answer was lost.
	log.Printf("crew: createPost %s/%s unclear, looking it up: %v", p.member.ID, ch.ID, err)
	return p.recoverPost(ctx, j)
}

// attachments is what this post carries: nothing when the selection asked
// for no image or the network takes none; else the member's card (when the
// launch includes the card slot) and the launch's current images, cut to
// the network's limit. sel.AssetURL carries the intent: Post sets it to the
// card, or to an image when the member has no card.
// pages are the same files for a LinkedIn document, uncut.
func (p *poster) attachments(ctx context.Context, j *job) (assets, pages []string, err error) {
	if !j.sel.Attach || !j.rules.ImageOK {
		return nil, nil, nil
	}
	rows, err := p.deps.Q.ListLaunchImages(ctx, p.launch.ID)
	if err != nil {
		return nil, nil, err
	}
	imgs := ImagesOf(rows)
	return Attachments(j.sel.Card, p.launch.IncludeCard, imgs, j.rules.MaxImages, j.rules.MaxImageBytes),
		DocumentPages(j.sel.Card, p.launch.IncludeCard, imgs), nil
}

// format is the selection's post format when the channel can take it, else "".
func (j *job) format() string {
	if buffer.FormatAllowed(j.channel.Service, j.sel.Format) {
		return j.sel.Format
	}
	return ""
}

// fit sets the text, thread and first comment to send and reports whether
// the person edited the text. An unedited draft goes out fitted to the
// rules (a long X draft becomes a thread). An edited text is sent as
// written: it replaces the thread's root and keeps the fitted tail.
func (p *poster) fit(j *job) (edited bool) {
	var thread []string
	if len(j.draft.Thread) > 0 {
		if err := json.Unmarshal(j.draft.Thread, &thread); err != nil {
			log.Printf("crew: draft %s thread: %v", j.draft.ID, err)
			thread = nil
		}
	}
	f := drafting.FitToRules(drafting.Draft{
		Text: j.draft.Text, Thread: thread, FirstComment: j.draft.FirstComment.String,
	}, j.rules)
	j.firstComment = f.FirstComment
	edited = j.sel.Text != j.draft.Text
	switch {
	case !edited && j.rules.Threads && len(f.Thread) > 1:
		j.text, j.thread = f.Thread[0], f.Thread
	case !edited:
		j.text = f.Text
	case j.rules.Threads && len(f.Thread) > 1:
		j.text, j.thread = j.sel.Text, append([]string{j.sel.Text}, f.Thread[1:]...)
	default:
		j.text = j.sel.Text
	}
	return edited
}

func (p *poster) dueAt() (time.Time, bool) {
	at := p.launch.LaunchAt.Time
	// Buffer schedules to the second; round up so jitter stays in the window.
	if t := at.Truncate(time.Second); t.Before(at) {
		at = t.Add(time.Second)
	}
	w := int(p.launch.WindowMinutes)
	now := p.now()
	if p.deps.Rand == nil {
		return DueAtFrom(at, w, now, rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
	}
	randMu.Lock()
	defer randMu.Unlock()
	return DueAtFrom(at, w, now, p.deps.Rand)
}

// reconcile looks for the post an earlier unclear send may have made
// before anything new is created. done means the outcome stands: the post
// was adopted, or the lookup failed and creating would risk a second post.
func (p *poster) reconcile(ctx context.Context, j *job) (Outcome, bool) {
	text := j.text // a stale pending may never have recorded its text
	if j.sentText.Valid {
		text = j.sentText.String
	}
	post, found, err := p.lookup(ctx, j, text, j.reconcile.Add(-reconcileBack))
	switch {
	case err != nil:
		return j.fail(noteNoAnswer), true
	case found:
		return p.succeed(ctx, j, post.ID, post.Status == statusDraft, "", timePtr(j.sentDue)), true
	}
	j.unclear = false
	return Outcome{}, false
}

// recoverPost adopts the post an unclear createPost may have made. With
// none found the attempt fails and stays unclear, so the next tap
// reconciles again before creating.
func (p *poster) recoverPost(ctx context.Context, j *job) Outcome {
	post, found, err := p.lookup(ctx, j, j.text, p.now().Add(-lookupBack))
	if err == nil && found {
		return p.succeed(ctx, j, post.ID, post.Status == statusDraft, "", j.due)
	}
	j.unclear = true
	return j.fail(noteNoAnswer)
}

// lookup finds the first post on the channel created between from and
// now+2m whose text equals text.
func (p *poster) lookup(ctx context.Context, j *job, text string, from time.Time) (buffer.Post, bool, error) {
	lctx, cancel := context.WithTimeout(buffer.WithLaunch(ctx, p.launch.ID), lookupTimeout)
	defer cancel()
	posts, err := p.deps.Buffer.PostsCreatedBetween(lctx, p.connID, j.channel.ID, from, p.now().Add(lookupAhead))
	if err != nil {
		log.Printf("crew: post lookup %s/%s: %v", p.member.ID, j.channel.ID, err)
		return buffer.Post{}, false, err
	}
	want := strings.TrimSpace(text)
	for _, post := range posts {
		if strings.TrimSpace(post.Text) == want {
			return post, true, nil
		}
	}
	return buffer.Post{}, false, nil
}

// succeed records a created post: crew_drafts first, then the attempt,
// so a tap that sees the attempt done can read the draft row. Both writes
// are retried; if the draft write still fails the attempt says so, rather
// than recording a post the board cannot see.
func (p *poster) succeed(ctx context.Context, j *job, postID string, asDraft bool, note string, due *time.Time) Outcome {
	status := statusQueued
	if asDraft || j.sel.Timing == TimingDraft {
		status = statusDraft
	}
	var dueAt pgtype.Timestamptz
	if status == statusQueued && due != nil {
		dueAt = pgtype.Timestamptz{Time: *due, Valid: true}
		j.out.When = due
	}
	if j.sel.Text != j.draft.Text {
		// Later overlap checks compare against what was actually posted.
		if err := p.deps.Q.UpdateCrewDraftText(ctx, sqlc.UpdateCrewDraftTextParams{ID: j.draft.ID, Text: j.sel.Text, Edited: true}); err != nil {
			log.Printf("crew: save posted text for draft %s: %v", j.draft.ID, err)
		}
	}
	j.unclear = false
	j.out.Status = status
	j.out.PostID = postID
	j.out.Note = firstNonEmpty(note, draftNote(status, j.sel.Timing))
	if err := retryWrite(func() error {
		return p.deps.Q.UpdateCrewDraftPosting(ctx, sqlc.UpdateCrewDraftPostingParams{
			ID: j.draft.ID, Status: status, BufferPostID: pgtype.Text{String: postID, Valid: true}, DueAt: dueAt,
		})
	}); err != nil {
		log.Printf("ERROR crew: save posting for draft %s (post %s) failed after %d tries: %v", j.draft.ID, postID, attemptWrites, err)
		j.out.Note = noteRecordMissing
	}
	p.setAttempt(ctx, j, status, postID)
	return j.out
}

// setAttempt writes the attempt's final state, retrying because a lost
// write after a created post would leave the channel pending.
func (p *poster) setAttempt(ctx context.Context, j *job, status, postID string) {
	arg := sqlc.UpdatePostAttemptParams{
		CrewMemberID: p.member.ID, ChannelID: j.sel.ChannelID, Status: status,
		BufferPostID: pgtype.Text{String: postID, Valid: postID != ""},
		Unclear:      j.unclear, SentText: j.sentText, SentDueAt: j.sentDue,
		Note: pgtype.Text{String: j.out.Note, Valid: true},
	}
	if err := retryWrite(func() error { return p.deps.Q.UpdatePostAttempt(ctx, arg) }); err != nil {
		log.Printf("ERROR crew: set attempt %s/%s to %s (post %q) failed after %d tries: %v",
			p.member.ID, j.sel.ChannelID, status, postID, attemptWrites, err)
	}
}

// retryWrite runs write up to attemptWrites times, attemptWriteGap apart,
// and returns the last error.
func retryWrite(write func() error) error {
	var err error
	for i := range attemptWrites {
		if i > 0 {
			time.Sleep(attemptWriteGap)
		}
		if err = write(); err == nil {
			return nil
		}
	}
	return err
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

// definite reports whether Buffer answered clearly that no post was made.
func definite(err error) bool {
	var me *buffer.MutationError
	var lre *buffer.LimitReachedError
	var ge *buffer.GraphQLError
	var he *buffer.HTTPError
	switch {
	case errors.As(err, &me), errors.As(err, &lre), errors.As(err, &ge):
		return true
	case errors.As(err, &he):
		return he.Status < 500
	}
	return false
}

func rejectNote(j *job, err error) string {
	var me *buffer.MutationError
	var lre *buffer.LimitReachedError
	switch {
	case errors.As(err, &me):
		msg := strings.ToLower(me.Message)
		for _, w := range []string{"length", "too long", "character", "exceed"} {
			if strings.Contains(msg, w) {
				return lengthNote(j)
			}
		}
		return fmt.Sprintf("%s rejected: %s", label(j), strings.TrimSuffix(me.Message, "."))
	case errors.As(err, &lre):
		return fmt.Sprintf("%s queue and drafts are full, make room in Buffer", label(j))
	}
	return noteRefused
}

func lengthNote(j *job) string {
	return fmt.Sprintf("%s rejected: over %d characters, edit", label(j), j.rules.MaxChars)
}

var labels = map[string]string{
	"x": "X", "twitter": "X", "bluesky": "Bluesky", "threads": "Threads", "mastodon": "Mastodon",
	"linkedin": "LinkedIn", "facebook": "Facebook", "instagram": "Instagram",
}

func label(j *job) string {
	if l, ok := labels[strings.ToLower(j.network)]; ok {
		return l
	}
	return firstNonEmpty(j.channel.Name, j.network)
}

func draftNote(status string, t Timing) string {
	if status == statusDraft && t != TimingDraft {
		return buffer.DraftNote
	}
	return ""
}

func tail(s []string) []string {
	if len(s) < 2 {
		return nil
	}
	return s[1:]
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
