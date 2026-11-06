package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/jackc/pgx/v5"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/github/comments"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

// SendCommentsHandler handles jobs.SendComments: it posts or edits the
// member's marked PR comment (or the roll call) and stores the comment id.
// When GitHub refuses for lack of permission (403 or 404) the member is
// marked comment_failed and the job succeeds: the captain copies the link
// from the detail page instead.
func SendCommentsHandler(d Deps) jobs.Handler {
	return func(ctx context.Context, j jobs.Job) error {
		var p CommentsPayload
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
		if s.repo == nil {
			log.Printf("launch %s: no repo, no comments", s.l.ID)
			return nil
		}
		if p.RollCall {
			return sendRollCall(ctx, d, s)
		}
		return sendComment(ctx, d, s, p)
	}
}

func repoParts(r *sqlc.Repo) (owner, name string) {
	owner, name, _ = strings.Cut(r.FullName, "/")
	return owner, name
}

// neutralise keeps a PR title, project or tag from mentioning anyone: a zero-width space
// after each @ stops GitHub from linking or notifying.
func neutralise(s string) string { return strings.ReplaceAll(s, "@", "@​") }

// optedOut reports whether the member must not be commented on: their row
// is opted_out or their GitHub id is in opt_outs. Checked at send time, so an
// opt-out after the invite still stops the comment.
func optedOut(ctx context.Context, d Deps, m sqlc.CrewMember) (bool, error) {
	if m.State == stateOptedOut {
		return true, nil
	}
	if !m.GithubUserID.Valid {
		return false, nil
	}
	out, err := d.Q.IsOptedOut(ctx, m.GithubUserID.Int64)
	if err != nil {
		return false, fmt.Errorf("opt-out check: %w", err)
	}
	return out, nil
}

func (d Deps) inviteToken(m sqlc.CrewMember) (string, error) {
	if len(m.InviteTokenEnc) == 0 {
		return "", errors.New("member has no invite token")
	}
	tok, err := d.Keyring.Decrypt(m.InviteTokenEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt invite token: %w", err)
	}
	return string(tok), nil
}

func (d Deps) inviteLink(m sqlc.CrewMember) (string, error) {
	tok, err := d.inviteToken(m)
	if err != nil {
		return "", err
	}
	return d.link("/s/" + tok), nil
}

func sendComment(ctx context.Context, d Deps, s *state, p CommentsPayload) error {
	m, err := d.Q.GetCrewMember(ctx, p.MemberID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted by its owner since the job was queued
	}
	if err != nil {
		return fmt.Errorf("load member: %w", err)
	}
	if m.LaunchID != s.l.ID || m.PrCommentID.Valid || !m.PrNumber.Valid {
		return nil
	}
	if gone, err := optedOut(ctx, d, m); err != nil || gone {
		return err
	}
	tok, err := d.inviteToken(m)
	if err != nil {
		return err
	}
	owner, name := repoParts(s.repo)
	// The token in the footer lets the contributor opt out without signing in.
	var prs []comments.PR
	for _, r := range trigger.MemberPRs(ctx, d.Pool, m.ID, m.PrNumber.Int32, m.PrTitle.String) {
		prs = append(prs, comments.PR{Number: r.Number, Title: neutralise(r.Title)})
	}
	body := comments.Body(s.captain, neutralise(s.project), neutralise(s.version()), prs,
		d.link("/s/"+tok), d.link("/settings?token="+tok))
	id, _, err := comments.Upsert(ctx, d.GitHub, s.repo.InstallationID, owner, name, int(m.PrNumber.Int32), comments.Marker(s.l.ID), body)
	if denied(err) {
		logRefusal(ctx, d, s, fmt.Sprintf("comment on #%d", m.PrNumber.Int32), err)
		return d.Q.SetCrewCommentFailed(context.WithoutCancel(ctx), m.ID)
	}
	if err != nil {
		return err
	}
	return d.Q.SetCrewComment(context.WithoutCancel(ctx), sqlc.SetCrewCommentParams{ID: m.ID, PrCommentID: pgInt8(id)})
}

// sendRollCall puts one roll call under the release notes (or on the first
// PR when the release cannot be edited) for every invited contributor.
func sendRollCall(ctx context.Context, d Deps, s *state) error {
	rows, err := d.Q.ListCrewByLaunch(ctx, s.l.ID)
	if err != nil {
		return fmt.Errorf("list crew: %w", err)
	}
	in := comments.RollCallInput{Captain: s.captain, Project: neutralise(s.project), Version: neutralise(s.version()), SettingsLink: d.link("/settings")}
	var members []sqlc.CrewMember
	fallbackPR := 0
	for _, m := range rows {
		if !m.GithubUserID.Valid || s.isCaptain(m) || len(m.InviteTokenEnc) == 0 {
			continue
		}
		switch m.State {
		case trigger.StateSuggested, trigger.StateTicked:
			continue
		}
		if gone, err := optedOut(ctx, d, m); err != nil {
			return err
		} else if gone {
			continue
		}
		link, err := d.inviteLink(m)
		if err != nil {
			return err
		}
		login := s.login(ctx, d, m.GithubUserID.Int64)
		if login == "" {
			continue
		}
		in.Entries = append(in.Entries, comments.Entry{Login: login, Link: link})
		members = append(members, m)
		if fallbackPR == 0 && m.PrNumber.Valid {
			fallbackPR = int(m.PrNumber.Int32)
		}
	}
	if len(members) == 0 {
		return nil
	}
	owner, name := repoParts(s.repo)
	marker, body := comments.Marker(s.l.ID), comments.RollCall(in)
	var commentID int64
	if s.l.ReleaseID.Valid {
		_, commentID, err = comments.PostRollCall(ctx, d.GitHub, s.repo.InstallationID, owner, name, s.l.ReleaseID.Int64, fallbackPR, marker, body)
	} else {
		commentID, _, err = comments.Upsert(ctx, d.GitHub, s.repo.InstallationID, owner, name, fallbackPR, marker, body)
	}
	wctx := context.WithoutCancel(ctx)
	if denied(err) {
		logRefusal(ctx, d, s, "roll call", err)
		for _, m := range members {
			if err := d.Q.SetCrewCommentFailed(wctx, m.ID); err != nil {
				return err
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	if commentID != 0 {
		for _, m := range members {
			if err := d.Q.SetCrewComment(wctx, sqlc.SetCrewCommentParams{ID: m.ID, PrCommentID: pgInt8(commentID)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// denied reports GitHub refusing for lack of access: 403, or 404 for a
// repository the installation can no longer see.
// refusalDetail is what GitHub said about a refusal beyond its message: the
// permissions it expected for the endpoint and the id to quote to support.
func refusalDetail(err error) string {
	var er *gh.ErrorResponse
	if !errors.As(err, &er) || er.Response == nil {
		return ""
	}
	h := er.Response.Header
	return fmt.Sprintf("message=%q accepted_permissions=%q request_id=%s docs=%s",
		er.Message, h.Get("X-Accepted-GitHub-Permissions"), h.Get("X-GitHub-Request-Id"), er.DocumentationURL)
}

// logRefusal logs why GitHub refused a write, with what it said and what the
// installation it was sent through actually is, so a permission problem can be
// told from a wrong app or installation without guessing.
func logRefusal(ctx context.Context, d Deps, s *state, what string, err error) {
	var who string
	if s.repo != nil {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		who = d.GitHub.DescribeInstallation(dctx, s.repo.InstallationID, s.repo.FullName)
	}
	log.Printf("launch %s: %s refused: %v | %s | %s", s.l.ID, what, err, refusalDetail(err), who)
}

func denied(err error) bool {
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode == http.StatusForbidden || er.Response.StatusCode == http.StatusNotFound
	}
	return false
}
