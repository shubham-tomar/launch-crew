-- name: SetLaunchOpenTokens :exec
UPDATE launches SET open_token_hash = $2, open_token_enc = $3 WHERE id = $1;

-- name: SetLaunchReleaseID :exec
UPDATE launches SET release_id = $2 WHERE id = $1;

-- name: SetLaunchHeroAsset :exec
UPDATE launches SET hero_asset_id = $2 WHERE id = $1;

-- name: CloseLaunch :execrows
UPDATE launches SET status = 'closed' WHERE id = $1 AND status = 'launched';

-- name: InsertCaptainDraftIfAbsent :one
INSERT INTO captain_drafts (launch_id, channel_id, text, thread, first_comment, asset_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (launch_id, channel_id) DO NOTHING
RETURNING *;

-- name: ClaimCaptainDraft :execrows
-- A draft is sent once: draft -> sending, then queued, a Buffer draft
-- (status draft with a post id) or failed.
UPDATE captain_drafts SET status = 'sending' WHERE id = $1 AND status = 'draft' AND buffer_post_id IS NULL;

-- name: SetCrewTicked :exec
-- Moves pre-invite rows between suggested and ticked; later states are untouched.
UPDATE crew_members
SET state = CASE WHEN id = ANY (sqlc.arg(ticked)::uuid[]) THEN 'ticked' ELSE 'suggested' END
WHERE launch_id = sqlc.arg(launch_id) AND state IN ('suggested', 'ticked');

-- name: InviteCrewMember :execrows
UPDATE crew_members
SET invite_token_hash = $2, invite_token_enc = $3, token_expires_at = $4, state = 'invited'
WHERE id = $1 AND state = 'ticked';

-- name: SetCrewComment :exec
UPDATE crew_members SET pr_comment_id = $2, comment_failed = false WHERE id = $1;

-- name: SetCrewCommentFailed :exec
UPDATE crew_members SET comment_failed = true WHERE id = $1;

-- name: ListQueuedCrewDraftsByLaunch :many
SELECT d.id, d.crew_member_id, d.channel_id, d.buffer_post_id, m.buffer_account_id
FROM crew_drafts d
JOIN crew_members m ON m.id = d.crew_member_id
WHERE m.launch_id = $1 AND d.status = 'queued' AND d.buffer_post_id IS NOT NULL
ORDER BY m.buffer_account_id, d.channel_id;

-- name: SetCrewDraftStatus :exec
UPDATE crew_drafts SET status = $2 WHERE id = $1;

-- name: MarkCrewMemberPosted :exec
UPDATE crew_members SET state = 'posted' WHERE id = $1;

-- name: GetGitHubIdentityByUser :one
SELECT * FROM github_identities WHERE user_id = $1 ORDER BY github_user_id LIMIT 1;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1;

-- name: DeleteUnsentCaptainDrafts :exec
-- After an edit: drafts not yet sent are rebuilt from the new text.
DELETE FROM captain_drafts WHERE launch_id = $1 AND status = 'draft' AND buffer_post_id IS NULL;

-- name: ListLaunchesForUser :many
-- Launches the user owns, or that belong to a repo they own.
SELECT sqlc.embed(l) FROM launches l
LEFT JOIN repos r ON r.id = l.repo_id
WHERE (l.owner_user_id = sqlc.arg(user_id) OR r.owner_user_id = sqlc.arg(user_id)) AND l.status <> 'deleted'
ORDER BY l.created_at DESC;

-- name: ListCrewPostsByLaunch :many
-- Every crew post Buffer holds for the launch, for the captain's crew rows.
SELECT d.id, d.crew_member_id, d.channel_id, d.network, d.status, d.external_link
FROM crew_drafts d
JOIN crew_members m ON m.id = d.crew_member_id
WHERE m.launch_id = $1 AND d.buffer_post_id IS NOT NULL
ORDER BY d.crew_member_id, d.network;

-- name: SetCrewDraftPosted :exec
-- The poll's verdict on a queued crew post, with its link on the network once sent.
UPDATE crew_drafts SET status = $2, external_link = $3 WHERE id = $1;

-- name: MarkCaptainDraftFromBuffer :execrows
-- The poll's verdict on a queued captain post; a post no longer queued is left alone.
UPDATE captain_drafts SET status = $2, external_link = $3 WHERE id = $1 AND status = 'queued';

-- name: ListUnlinkedCrewPostsByLaunch :many
-- Crew posts Buffer sent whose link on the network is still unknown.
SELECT d.id, d.channel_id, d.buffer_post_id, m.buffer_account_id
FROM crew_drafts d
JOIN crew_members m ON m.id = d.crew_member_id
WHERE m.launch_id = $1 AND d.status = 'posted' AND d.external_link = '' AND d.buffer_post_id IS NOT NULL
ORDER BY m.buffer_account_id, d.channel_id;

-- name: SetCrewDraftLink :exec
UPDATE crew_drafts SET external_link = $2 WHERE id = $1 AND external_link = '';

-- name: SetCaptainDraftLink :exec
UPDATE captain_drafts SET external_link = $2 WHERE id = $1 AND external_link = '';
