package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github/comments"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/mail"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

// The ways a captain hears about a new draft, as stored in launches.captain_notified.
const (
	NotifyGitHub       = "github"
	NotifyReleaseNotes = "release_notes"
	NotifyEmail        = "email"
)

// NotifyPrefs is a captain's choice of channels. Without a stored row it is
// DefaultNotify: an @mention on GitHub only.
type NotifyPrefs struct {
	GitHub, ReleaseNotes, Email bool
	EmailAddress                string
}

// DefaultNotify is what a captain gets before they choose.
var DefaultNotify = NotifyPrefs{GitHub: true}

// CaptainNotify loads the user's notification choices, or DefaultNotify.
func CaptainNotify(ctx context.Context, q *sqlc.Queries, userID uuid.UUID) (NotifyPrefs, error) {
	n, err := q.GetCaptainNotify(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultNotify, nil
	}
	if err != nil {
		return NotifyPrefs{}, err
	}
	return NotifyPrefs{GitHub: n.Github, ReleaseNotes: n.ReleaseNotes, Email: n.Email && n.EmailAddress != "", EmailAddress: n.EmailAddress}, nil
}

// notifyMarker keeps one "draft ready" comment per launch.
func notifyMarker(id uuid.UUID) string { return "<!-- launchcrew:draft-ready:" + id.String() + " -->" }

// notifyPR is where the @mention goes: the captain's own latest PR in the
// release when they have one, else the latest PR. Zero when there are none.
func notifyPR(s *state) int {
	best, mine := 0, 0
	for _, c := range s.changes {
		best = max(best, c.PRNumber)
		if s.captain != "" && strings.EqualFold(c.AuthorLogin, s.captain) {
			mine = max(mine, c.PRNumber)
		}
	}
	if mine > 0 {
		return mine
	}
	return best
}

// NotifyCaptainHandler handles jobs.NotifyCaptain: a release made a draft,
// and the captain hears about it the ways they chose. Each channel is marked
// once it worked (or GitHub refused it for good), so a retry repeats only
// what failed. A draft launched or deleted meanwhile needs no notice.
func NotifyCaptainHandler(d Deps) jobs.Handler {
	return func(ctx context.Context, j jobs.Job) error {
		var p trigger.NotifyPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		s, err := load(ctx, d, p.LaunchID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if s.l.Status != statusDraft || !s.l.OwnerUserID.Valid {
			return nil
		}
		prefs, err := CaptainNotify(ctx, d.Q, s.l.OwnerUserID.UUID)
		if err != nil {
			return fmt.Errorf("notify prefs: %w", err)
		}
		link := d.link("/captain/launches/" + s.l.ID.String())
		title := strings.TrimSpace(s.project + " " + s.version())
		var errs []error
		try := func(channel string, on bool, send func() error) {
			if !on || slices.Contains(s.l.CaptainNotified, channel) {
				return
			}
			err := send()
			if denied(err) {
				logRefusal(ctx, d, s, "notify via "+channel, err)
				err = nil
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", channel, err))
				return
			}
			if err := d.Q.MarkCaptainNotified(context.WithoutCancel(ctx), sqlc.MarkCaptainNotifiedParams{ID: s.l.ID, Channel: channel}); err != nil {
				errs = append(errs, err)
			}
		}

		try(NotifyGitHub, prefs.GitHub && s.repo != nil, func() error {
			pr := notifyPR(s)
			if pr == 0 || s.captain == "" {
				log.Printf("launch %s: no PR to mention the captain on", s.l.ID)
				return nil
			}
			owner, name := repoParts(s.repo)
			body := fmt.Sprintf("@%s your Launch Crew draft for **%s** is ready. Review the crew and the posts, then launch: %s\n\n"+
				"Only you can open the draft. Choose how you hear about drafts: %s",
				s.captain, neutralise(title), link, d.link("/settings"))
			_, _, err := comments.Upsert(ctx, d.GitHub, s.repo.InstallationID, owner, name, pr, notifyMarker(s.l.ID), body)
			return err
		})
		try(NotifyReleaseNotes, prefs.ReleaseNotes && s.repo != nil && s.l.ReleaseID.Valid, func() error {
			owner, name := repoParts(s.repo)
			note := fmt.Sprintf("**Launch Crew:** [share this release with its contributors →](%s)", link)
			return d.GitHub.SetReleaseDraftNote(ctx, s.repo.InstallationID, owner, name, s.l.ReleaseID.Int64, note)
		})
		try(NotifyEmail, prefs.Email && mail.Available(d.Mail), func() error {
			return d.Mail.Send(ctx, mail.Message{
				To:      prefs.EmailAddress,
				Subject: "Your launch draft for " + title + " is ready",
				Text: fmt.Sprintf("A new release made a Launch Crew draft for %s.\n\nReview the crew and the posts, then launch:\n%s\n\n"+
					"Change how you hear about drafts: %s\n", title, link, d.link("/settings")),
			})
		})
		return errors.Join(errs...)
	}
}
