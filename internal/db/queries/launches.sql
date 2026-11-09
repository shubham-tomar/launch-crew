-- name: CreateLaunch :one
INSERT INTO launches
    (owner_user_id, repo_id, source, title, summary, release_tag, release_url,
     headline_changes, roles, launch_at, window_minutes, crew_cap, card_preset, template_id)
VALUES (sqlc.arg(owner_user_id)::uuid, sqlc.arg(repo_id), sqlc.arg(source), sqlc.arg(title), sqlc.arg(summary), sqlc.arg(release_tag), sqlc.arg(release_url), sqlc.arg(headline_changes), sqlc.arg(roles), sqlc.arg(launch_at), sqlc.arg(window_minutes), sqlc.arg(crew_cap), sqlc.arg(card_preset), sqlc.narg(template_id))
RETURNING *;

-- name: GetLaunch :one
SELECT * FROM launches WHERE id = $1;

-- name: ListLaunchesByOwner :many
SELECT * FROM launches
WHERE owner_user_id = $1 AND status <> 'deleted'
ORDER BY created_at DESC;

-- name: UpdateLaunch :one
UPDATE launches
SET title = $2, summary = $3, launch_at = $4, window_minutes = $5, crew_cap = $6,
    roles = $7, headline_changes = $8
WHERE id = $1
RETURNING *;

-- name: MarkLaunched :one
UPDATE launches SET status = 'launched' WHERE id = $1 AND status = 'draft' RETURNING id;

-- name: UpdateLaunchStatus :exec
UPDATE launches SET status = $2 WHERE id = $1;

-- name: SetLaunchOpenToken :exec
UPDATE launches SET open_token_hash = $2 WHERE id = $1;

-- name: GetLaunchByOpenTokenHash :one
SELECT * FROM launches WHERE open_token_hash = $1;

-- name: InsertLaunchAsset :one
INSERT INTO launch_assets (launch_id, kind, r2_url, sha) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetLaunchAsset :one
SELECT * FROM launch_assets WHERE id = $1;

-- name: UpsertCaptainDraft :one
INSERT INTO captain_drafts (launch_id, channel_id, text, thread, first_comment, asset_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (launch_id, channel_id) DO UPDATE SET
    text = EXCLUDED.text, thread = EXCLUDED.thread,
    first_comment = EXCLUDED.first_comment, asset_id = EXCLUDED.asset_id
RETURNING *;

-- name: ListCaptainDrafts :many
SELECT * FROM captain_drafts WHERE launch_id = $1 ORDER BY channel_id;

-- name: UpdateCaptainDraftStatus :exec
UPDATE captain_drafts SET status = $2, buffer_post_id = $3, note = $4 WHERE id = $1;

-- name: GetLaunchByRepoAndTag :one
SELECT * FROM launches WHERE repo_id = $1 AND release_tag = $2 LIMIT 1;

-- name: SetLaunchCardPreset :exec
UPDATE launches SET card_preset = $2 WHERE id = $1;

-- name: IncrementLaunchOpenCount :one
UPDATE launches SET open_count = open_count + 1 WHERE id = $1 RETURNING open_count;

-- name: GetLatestLaunchAssetByKind :one
SELECT * FROM launch_assets WHERE launch_id = $1 AND kind = $2 ORDER BY id DESC LIMIT 1;

-- name: GetRepo :one
SELECT * FROM repos WHERE id = $1;

-- name: ListLaunchAssetURLs :many
SELECT id, r2_url FROM launch_assets WHERE launch_id = $1;
