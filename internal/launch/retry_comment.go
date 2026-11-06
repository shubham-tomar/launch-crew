package launch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/jobs"
)

// MaxCommentRetries is how many times the captain can retry one person's
// failed PR comment. After that the captain copies the link instead.
const MaxCommentRetries = 3

// ErrRetryLimit means the person's comment was already retried MaxCommentRetries times.
var ErrRetryLimit = errors.New("launch: comment retry limit reached")

// RetryComment queues the PR comment for a crew member whose comment failed.
// It clears the failed flag, counts the retry and enqueues a SendComments job
// in one transaction. A member who is not in this launch, or whose comment
// has not failed, is ErrNotFound.
func RetryComment(ctx context.Context, d Deps, launchID, memberID uuid.UUID) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var retries int
	err = tx.QueryRow(ctx,
		`SELECT comment_retries FROM crew_members WHERE id = $1 AND launch_id = $2 AND comment_failed FOR UPDATE`,
		memberID, launchID).Scan(&retries)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("retry comment: load member: %w", err)
	}
	if retries >= MaxCommentRetries {
		return ErrRetryLimit
	}
	if _, err := tx.Exec(ctx,
		`UPDATE crew_members SET comment_failed = false, comment_retries = comment_retries + 1 WHERE id = $1`, memberID); err != nil {
		return fmt.Errorf("retry comment: update member: %w", err)
	}
	if _, err := jobs.Enqueue(ctx, sqlc.New(tx), jobs.SendComments, CommentsPayload{LaunchID: launchID, MemberID: memberID}, time.Time{}, &launchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// commentRetriesLeft returns, for each member of the launch whose comment
// failed, how many retries remain. Members without a failed comment are absent.
func commentRetriesLeft(ctx context.Context, d Deps, launchID uuid.UUID) (map[uuid.UUID]int, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id, comment_retries FROM crew_members WHERE launch_id = $1 AND comment_failed`, launchID)
	if err != nil {
		return nil, fmt.Errorf("launch: comment retries: %w", err)
	}
	defer rows.Close()
	left := map[uuid.UUID]int{}
	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		left[id] = max(0, MaxCommentRetries-n)
	}
	return left, rows.Err()
}
