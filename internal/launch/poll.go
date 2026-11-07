package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
)

const (
	draftPosted = "posted"
	draftFailed = "failed_in_buffer"
	pageMax     = 100 // PostsCreatedBetween returns at most this many
)

// pollOutcome is what Buffer's answer for a channel says about one queued
// post: draftPosted (sent, with the post's link on the network when Buffer
// knows it), draftFailed (errored, or gone) or "" to leave it queued (still
// scheduled, or possibly beyond the page).
func pollOutcome(posts []buffer.Post, postID string) (status, link string) {
	for _, p := range posts {
		if p.ID != postID {
			continue
		}
		switch p.Status {
		case "sent":
			return draftPosted, p.ExternalLink
		case "error":
			return draftFailed, ""
		}
		return "", ""
	}
	if len(posts) < pageMax {
		return draftFailed, "" // deleted in Buffer
	}
	return "", ""
}

// PollStatusHandler handles jobs.PollStatus, once after the window: each
// queued crew draft is looked up in its member's Buffer and marked posted
// (sent) or failed_in_buffer (errored, or gone). Drafts still scheduled
// stay queued. The captain's own queued posts are looked up the same way.
// A member with a posted draft becomes posted.
func PollStatusHandler(d Deps) jobs.Handler {
	return func(ctx context.Context, j jobs.Job) error {
		var p LaunchPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		l, err := getLaunch(ctx, d.Q, p.LaunchID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		rows, err := d.Q.ListQueuedCrewDraftsByLaunch(ctx, l.ID)
		if err != nil {
			return fmt.Errorf("list queued drafts: %w", err)
		}
		type key struct{ account, channel string }
		groups := map[key][]sqlc.ListQueuedCrewDraftsByLaunchRow{}
		var order []key
		for _, r := range rows {
			k := key{r.BufferAccountID.String, r.ChannelID}
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], r)
		}
		var errs []error
		posted := map[uuid.UUID]bool{}
		from, to := l.CreatedAt.Time, d.now().Add(lookupAhead)
		for _, k := range order {
			conn, err := d.Q.GetBufferConnectionByAccount(ctx, k.account)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // disconnected since; nothing to ask
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			posts, err := d.Buffer.PostsCreatedBetween(buffer.WithLaunch(ctx, l.ID), conn.ID, k.channel, from, to)
			if err != nil {
				errs = append(errs, fmt.Errorf("posts for %s: %w", k.channel, err))
				continue
			}
			byID := map[string]buffer.Post{}
			for _, p := range posts {
				byID[p.ID] = p
			}
			for _, r := range groups[k] {
				status, link := pollOutcome(posts, r.BufferPostID.String)
				if status == "" {
					continue // still scheduled, or beyond the page
				}
				if err := d.Q.SetCrewDraftPosted(ctx, sqlc.SetCrewDraftPostedParams{ID: r.ID, Status: status, ExternalLink: link}); err != nil {
					errs = append(errs, err)
					continue
				}
				if status == draftPosted {
					posted[r.CrewMemberID] = true
				}
			}
		}
		if err := refreshCaptainDrafts(ctx, d, l, nil); err != nil {
			errs = append(errs, err)
		}
		if err := backfillLinks(ctx, d, l); err != nil {
			log.Printf("launch %s: poll: links: %v", l.ID, err) // the page asks again
		}
		for id := range posted {
			if err := d.Q.MarkCrewMemberPosted(ctx, id); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			log.Printf("launch %s: poll: %d errors", l.ID, len(errs))
		}
		return errors.Join(errs...)
	}
}

// captainRefreshEvery is the least time between lazy refreshes of one
// launch's captain posts, so page loads do not spend Buffer's rate budget.
const captainRefreshEvery = 60 * time.Second

var (
	captainRefreshMu   sync.Mutex
	captainRefreshLast = map[uuid.UUID]time.Time{}
)

// throttleCaptainRefresh reports whether launch id may refresh now, and if so
// records the attempt.
func throttleCaptainRefresh(id uuid.UUID, now time.Time) bool {
	captainRefreshMu.Lock()
	defer captainRefreshMu.Unlock()
	if last, ok := captainRefreshLast[id]; ok && now.Sub(last) < captainRefreshEvery {
		return false
	}
	captainRefreshLast[id] = now
	return true
}

// refreshCaptainDrafts asks the owner's Buffer about the captain's queued
// posts and marks each posted (sent) or failed (errored, or gone). Posts
// still scheduled stay queued. conn is the owner's connection, found when nil.
func refreshCaptainDrafts(ctx context.Context, d Deps, l sqlc.Launch, conn *sqlc.BufferConnection) error {
	if d.Buffer == nil || d.Pool == nil {
		return nil
	}
	drafts, err := d.Q.ListCaptainDrafts(ctx, l.ID)
	if err != nil {
		return fmt.Errorf("list captain drafts: %w", err)
	}
	var queued []sqlc.CaptainDraft
	for _, cd := range drafts {
		if cd.Status == draftQueued && cd.BufferPostID.Valid {
			queued = append(queued, cd)
		}
	}
	if len(queued) == 0 {
		return nil
	}
	if conn == nil {
		if conn, err = ownerConnection(ctx, d, l.OwnerUserID.UUID); err != nil {
			return err
		}
		if conn == nil {
			return nil
		}
	}
	from, to := l.CreatedAt.Time, d.now().Add(lookupAhead)
	byChannel := map[string][]buffer.Post{}
	asked := map[string]bool{}
	var errs []error
	for _, cd := range queued {
		if !asked[cd.ChannelID] {
			asked[cd.ChannelID] = true
			posts, err := d.Buffer.PostsCreatedBetween(buffer.WithLaunch(ctx, l.ID), conn.ID, cd.ChannelID, from, to)
			if err != nil {
				errs = append(errs, fmt.Errorf("posts for %s: %w", cd.ChannelID, err))
				continue
			}
			byChannel[cd.ChannelID] = posts
		}
		posts, ok := byChannel[cd.ChannelID]
		if !ok {
			continue
		}
		status, link := pollOutcome(posts, cd.BufferPostID.String)
		if status == draftFailed {
			status = captainFailed
		}
		if status == "" {
			continue
		}
		if _, err := d.Q.MarkCaptainDraftFromBuffer(ctx, sqlc.MarkCaptainDraftFromBufferParams{ID: cd.ID, Status: status, ExternalLink: link}); err != nil {
			errs = append(errs, fmt.Errorf("mark captain draft: %w", err))
		}
	}
	return errors.Join(errs...)
}

// linkLookupFor is how long after the launch a sent post without its link is
// asked about again: Buffer can learn the link after the post was marked
// sent, and posts marked before links were stored have none.
const linkLookupFor = 14 * 24 * time.Hour

// backfillLinks asks Buffer for the network links of the launch's sent posts
// that have none yet, crew and captain alike. A post Buffer still has no link
// for is left for the next look.
func backfillLinks(ctx context.Context, d Deps, l sqlc.Launch) error {
	if d.Buffer == nil || !l.LaunchAt.Valid || d.now().Sub(l.LaunchAt.Time) > linkLookupFor {
		return nil
	}
	from, to := l.CreatedAt.Time, d.now().Add(lookupAhead)
	lctx := buffer.WithLaunch(ctx, l.ID)
	var errs []error
	linkOf := func(connID uuid.UUID, channel string, cache map[string]map[string]string) (map[string]string, bool) {
		if m, ok := cache[channel]; ok {
			return m, true
		}
		posts, err := d.Buffer.PostsCreatedBetween(lctx, connID, channel, from, to)
		if err != nil {
			errs = append(errs, fmt.Errorf("links for %s: %w", channel, err))
			cache[channel] = nil
			return nil, false
		}
		m := map[string]string{}
		for _, p := range posts {
			if p.ExternalLink != "" {
				m[p.ID] = p.ExternalLink
			}
		}
		cache[channel] = m
		return m, true
	}

	crewRows, err := d.Q.ListUnlinkedCrewPostsByLaunch(ctx, l.ID)
	if err != nil {
		return fmt.Errorf("list unlinked crew posts: %w", err)
	}
	byAccount := map[string]map[string]map[string]string{}
	conns := map[string]*sqlc.BufferConnection{}
	for _, r := range crewRows {
		acct := r.BufferAccountID.String
		if _, ok := conns[acct]; !ok {
			conn, err := d.Q.GetBufferConnectionByAccount(ctx, acct)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				conns[acct] = nil
			case err != nil:
				errs = append(errs, err)
				conns[acct] = nil
			default:
				conns[acct] = &conn
			}
			byAccount[acct] = map[string]map[string]string{}
		}
		conn := conns[acct]
		if conn == nil || conn.Status == "needs_reconnect" {
			continue
		}
		links, ok := linkOf(conn.ID, r.ChannelID, byAccount[acct])
		if !ok || links[r.BufferPostID.String] == "" {
			continue
		}
		if err := d.Q.SetCrewDraftLink(ctx, sqlc.SetCrewDraftLinkParams{ID: r.ID, ExternalLink: links[r.BufferPostID.String]}); err != nil {
			errs = append(errs, err)
		}
	}

	drafts, err := d.Q.ListCaptainDrafts(ctx, l.ID)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("list captain drafts: %w", err))...)
	}
	var owner *sqlc.BufferConnection
	captainCache := map[string]map[string]string{}
	for _, cd := range drafts {
		if cd.Status != draftPosted || cd.ExternalLink != "" || !cd.BufferPostID.Valid {
			continue
		}
		if owner == nil {
			if owner, err = ownerConnection(ctx, d, l.OwnerUserID.UUID); err != nil || owner == nil {
				if err != nil {
					errs = append(errs, err)
				}
				break
			}
		}
		links, ok := linkOf(owner.ID, cd.ChannelID, captainCache)
		if !ok || links[cd.BufferPostID.String] == "" {
			continue
		}
		if err := d.Q.SetCaptainDraftLink(ctx, sqlc.SetCaptainDraftLinkParams{ID: cd.ID, ExternalLink: links[cd.BufferPostID.String]}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
