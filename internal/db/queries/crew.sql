-- name: InsertCrewMember :one
INSERT INTO crew_members
    (launch_id, user_id, buffer_account_id, github_user_id, role, role_hint, talk_title,
     invite_token_hash, state, pr_number, pr_title, token_expires_at, opened_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: GetCrewMember :one
SELECT * FROM crew_members WHERE id = $1;

-- name: GetCrewMemberByTokenHash :one
SELECT * FROM crew_members WHERE invite_token_hash = $1;

-- name: GetCrewMemberByAccount :one
SELECT * FROM crew_members WHERE launch_id = $1 AND buffer_account_id = $2;

-- name: GetCrewMemberByGitHubUser :one
SELECT * FROM crew_members WHERE launch_id = $1 AND github_user_id = $2;

-- name: ListCrewByLaunch :many
SELECT * FROM crew_members WHERE launch_id = $1 ORDER BY opened_at NULLS LAST, id;

-- name: UpdateCrewMemberState :exec
UPDATE crew_members SET state = $2 WHERE id = $1;

-- name: UpdateCrewMemberConnection :execrows
-- Never re-binds a row already bound to another user.
UPDATE crew_members SET user_id = $2, buffer_account_id = $3, state = $4
WHERE id = $1 AND (user_id IS NULL OR user_id = $2);

-- name: UpdateCrewMemberNeutralDraft :exec
UPDATE crew_members SET neutral_draft = $2 WHERE id = $1;

-- name: SetCrewMemberNeutralDraftIfAbsent :execrows
-- Two Share taps at once both draft; the first write stands.
UPDATE crew_members SET neutral_draft = $2
WHERE id = $1 AND (neutral_draft IS NULL OR btrim(neutral_draft) = '');

-- name: UpdateCrewMemberCard :exec
UPDATE crew_members SET card_asset_id = $2 WHERE id = $1;

-- name: UpdateCrewMemberComment :exec
UPDATE crew_members SET pr_comment_id = $2, state = $3 WHERE id = $1;

-- name: UpdateCrewMemberToken :exec
UPDATE crew_members SET invite_token_hash = $2, token_expires_at = $3 WHERE id = $1;

-- name: DeleteUntouchedOpened :execrows
-- Open-link rows only: GitHub rows carry a PR invite and are never swept.
DELETE FROM crew_members
WHERE state = 'opened' AND user_id IS NULL AND buffer_account_id IS NULL
  AND github_user_id IS NULL AND opened_at < $1;

-- name: CountCrewByLaunch :one
SELECT count(*) FROM crew_members WHERE launch_id = $1;

-- name: UpsertCrewDraft :one
INSERT INTO crew_drafts
    (crew_member_id, channel_id, network, text, thread, first_comment, asset_id,
     hook_type, overlap_score, flagged)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (crew_member_id, channel_id) DO UPDATE SET
    network = EXCLUDED.network, text = EXCLUDED.text, thread = EXCLUDED.thread,
    first_comment = EXCLUDED.first_comment, asset_id = EXCLUDED.asset_id,
    hook_type = EXCLUDED.hook_type, overlap_score = EXCLUDED.overlap_score,
    flagged = EXCLUDED.flagged, edited = false
RETURNING *;

-- name: ListCrewDrafts :many
SELECT * FROM crew_drafts WHERE crew_member_id = $1 ORDER BY channel_id;

-- name: GetCrewDraft :one
SELECT * FROM crew_drafts WHERE id = $1;

-- name: UpdateCrewDraftText :exec
UPDATE crew_drafts SET text = $2, edited = $3 WHERE id = $1;

-- name: UpdateCrewDraftPosting :exec
UPDATE crew_drafts SET status = $2, buffer_post_id = $3, due_at = $4 WHERE id = $1;

-- name: ListRecentQueuedTexts :many
SELECT d.text
FROM crew_drafts d
JOIN crew_members m ON m.id = d.crew_member_id
WHERE m.launch_id = $1 AND d.network = $2 AND d.status = 'queued'
ORDER BY d.created_at DESC
LIMIT $3;

-- name: UpsertVoiceProfile :one
INSERT INTO voice_profiles (buffer_account_id, profile, derived_from)
VALUES ($1, $2, $3)
ON CONFLICT (buffer_account_id) DO UPDATE SET
    profile = EXCLUDED.profile, derived_from = EXCLUDED.derived_from, updated_at = now()
RETURNING *;

-- name: GetVoiceProfile :one
SELECT * FROM voice_profiles WHERE buffer_account_id = $1;

-- name: InsertPostAttempt :one
INSERT INTO post_attempts (crew_member_id, channel_id, status)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetPostAttempt :one
SELECT * FROM post_attempts WHERE crew_member_id = $1 AND channel_id = $2;

-- name: UpdatePostAttempt :exec
UPDATE post_attempts
SET status = $3, buffer_post_id = $4, unclear = $5, sent_text = $6, sent_due_at = $7, note = $8
WHERE crew_member_id = $1 AND channel_id = $2;

-- name: SetPostAttemptSending :exec
-- sent_at anchors later reconciliation; reclaims never touch it.
UPDATE post_attempts SET sent_text = $3, sent_due_at = $4, sent_at = now()
WHERE crew_member_id = $1 AND channel_id = $2;

-- name: UpdateCrewMemberUser :exec
UPDATE crew_members SET user_id = $2 WHERE id = $1;

-- name: ClaimFailedPostAttempt :one
-- Returns the row as it was before the claim.
UPDATE post_attempts p SET status = 'pending', claimed_at = now()
FROM post_attempts old
WHERE p.crew_member_id = $1 AND p.channel_id = $2 AND p.status = 'failed'
  AND old.crew_member_id = p.crew_member_id AND old.channel_id = p.channel_id
RETURNING old.claimed_at, old.unclear, old.sent_text, old.sent_due_at, old.sent_at;

-- name: ClaimStalePostAttempt :one
-- Takes over a pending attempt claimed before older_than; returns the row as it was.
UPDATE post_attempts p SET claimed_at = now()
FROM post_attempts old
WHERE p.crew_member_id = sqlc.arg(crew_member_id) AND p.channel_id = sqlc.arg(channel_id)
  AND p.status = 'pending' AND p.claimed_at < sqlc.arg(older_than)
  AND old.crew_member_id = p.crew_member_id AND old.channel_id = p.channel_id
RETURNING old.claimed_at, old.unclear, old.sent_text, old.sent_due_at, old.sent_at;

-- name: MarkCrewMemberOpened :exec
UPDATE crew_members SET state = 'opened', opened_at = COALESCE(opened_at, $2)
WHERE id = $1 AND state IN ('invited', 'suggested', 'ticked');

-- name: MarkCrewMemberCopied :exec
UPDATE crew_members SET state = 'copied' WHERE id = $1 AND state NOT IN ('queued', 'posted', 'opted_out');

-- name: MarkCrewMemberQueued :exec
UPDATE crew_members SET state = 'queued' WHERE id = $1 AND state NOT IN ('posted', 'opted_out');

-- name: ReopenCrewMember :exec
UPDATE crew_members SET state = 'opened' WHERE id = $1 AND state NOT IN ('queued', 'posted', 'copied');

-- name: SetCrewMemberVoiced :exec
UPDATE crew_members SET voiced_at = $2, voice_profile_id = $3 WHERE id = $1;

-- name: InsertCrewDraftIfAbsent :one
INSERT INTO crew_drafts (crew_member_id, channel_id, network, text, thread, first_comment, hook_type, overlap_score)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (crew_member_id, channel_id) DO NOTHING
RETURNING *;

-- name: UpdateCrewDraftVoiced :execrows
-- Never touches an edited draft or one already handed to Buffer.
UPDATE crew_drafts
SET text = $2, thread = $3, first_comment = $4, hook_type = $5, overlap_score = $6,
    flagged = $7, from_model = $8
WHERE id = $1 AND edited = false AND status = 'draft' AND buffer_post_id IS NULL;

-- name: SaveCrewDraftEdit :execrows
UPDATE crew_drafts SET text = $2, edited = true
WHERE id = $1 AND status = 'draft' AND buffer_post_id IS NULL;

-- name: ListCrewDueByLaunch :many
SELECT d.crew_member_id, min(d.due_at)::timestamptz AS due_at
FROM crew_drafts d
JOIN crew_members m ON m.id = d.crew_member_id
WHERE m.launch_id = $1 AND d.status = 'queued' AND d.due_at IS NOT NULL
GROUP BY d.crew_member_id;

-- name: SetCrewDisplayName :exec
UPDATE crew_members SET display_name = $2 WHERE id = $1;
