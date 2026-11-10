package launch

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/buffer"
	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/drafting"
)

// DraftPatch is a captain's change to one of their own posts. Nil fields are
// left alone. Skipped true removes the post from the launch for its channel
// (it is kept, so it is not rebuilt, and it is never sent); false puts it back.
type DraftPatch struct {
	Text    *string `json:"text"`
	Skipped *bool   `json:"skipped"`
	// Format is the channel's post format: "story" (Instagram, Facebook),
	// "document" (LinkedIn) or "" for a plain post. It is kept on the
	// launch by channel, so it outlives the draft being rebuilt.
	Format *string `json:"format"`
}

// PatchCaptainDraft edits one of the captain's own posts. Only a draft launch
// can be changed, and only a post that has not been sent. New text must be
// non-empty and fit the channel's character limit; a removed post cannot have
// its text edited unless the same patch puts it back.
func PatchCaptainDraft(ctx context.Context, d Deps, launchID, draftID uuid.UUID, p DraftPatch) error {
	if p.Text == nil && p.Skipped == nil && p.Format == nil {
		return invalid("nothing to change")
	}
	var text string
	if p.Text != nil {
		text = strings.TrimSpace(*p.Text)
		if text == "" {
			return invalid("a post cannot be empty")
		}
	}
	s, err := load(ctx, d, launchID)
	if err != nil {
		return err
	}
	if s.l.Status != statusDraft {
		return ErrNotDraft
	}
	all, err := d.Q.ListCaptainDrafts(ctx, launchID)
	if err != nil {
		return fmt.Errorf("launch: load captain drafts: %w", err)
	}
	i := slices.IndexFunc(all, func(c sqlc.CaptainDraft) bool { return c.ID == draftID })
	if i < 0 {
		return ErrNotFound
	}
	cd := all[i]
	if (cd.Status != draftDraft && cd.Status != captainSkipped) || cd.BufferPostID.Valid {
		return invalid("this post has already been sent to Buffer")
	}

	next := cd.Status
	if p.Skipped != nil {
		next = draftDraft
		if *p.Skipped {
			next = captainSkipped
		}
	}
	if p.Text != nil && next == captainSkipped {
		return invalid("this post is removed from the launch; add it back to edit it")
	}
	if p.Text != nil {
		s.withBuffer(ctx, d)
		if ch, ok := s.channels[cd.ChannelID]; ok {
			max := drafting.RulesFor(drafting.Network(ch.Service), s.cfg).MaxChars
			if n := utf8.RuneCountInString(text); max > 0 && n > max {
				return invalid("that post is %d characters and %s allows %d", n, ch.Service, max)
			}
		}
	}

	if p.Format != nil {
		s.withBuffer(ctx, d)
		ch, ok := s.channels[cd.ChannelID]
		if !ok || !buffer.FormatAllowed(ch.Service, *p.Format) {
			return invalid("that channel cannot post as %q", *p.Format)
		}
		if err := d.Q.SetLaunchPostFormat(ctx, sqlc.SetLaunchPostFormatParams{ID: launchID, ChannelID: cd.ChannelID, Format: *p.Format}); err != nil {
			return fmt.Errorf("launch: set post format: %w", err)
		}
		if p.Text == nil && p.Skipped == nil {
			return nil
		}
	}
	var newText *string
	if p.Text != nil {
		newText = &text
	}
	tag, err := d.Pool.Exec(ctx,
		`UPDATE captain_drafts SET text = COALESCE($3, text), status = $4
		 WHERE id = $2 AND launch_id = $1 AND status IN ('draft', 'skipped') AND buffer_post_id IS NULL
		   AND EXISTS (SELECT 1 FROM launches WHERE id = $1 AND status = 'draft')`,
		launchID, draftID, newText, next)
	if err != nil {
		return fmt.Errorf("launch: patch captain draft: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return invalid("this post can no longer be changed")
	}
	return nil
}
