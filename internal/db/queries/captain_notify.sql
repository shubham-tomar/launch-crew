-- name: GetCaptainNotify :one
SELECT * FROM captain_notify WHERE user_id = $1;

-- name: SetCaptainNotifyChannels :one
-- The captain's choices. Email stays off until an address is verified.
INSERT INTO captain_notify (user_id, github, release_notes, email)
VALUES ($1, $2, $3, false)
ON CONFLICT (user_id) DO UPDATE
SET github = EXCLUDED.github, release_notes = EXCLUDED.release_notes,
    email = $4 AND captain_notify.email_address <> '', updated_at = now()
RETURNING *;

-- name: StartCaptainEmailVerify :one
-- Records the address awaiting verification and the hash of its link token.
INSERT INTO captain_notify (user_id, email_pending, verify_hash, verify_sent_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (user_id) DO UPDATE
SET email_pending = EXCLUDED.email_pending, verify_hash = EXCLUDED.verify_hash,
    verify_sent_at = now(), updated_at = now()
RETURNING *;

-- name: VerifyCaptainEmail :one
-- The link was opened in time: the pending address becomes the address and
-- email turns on.
UPDATE captain_notify
SET email_address = email_pending, email_pending = '', verify_hash = NULL, email = true, updated_at = now()
WHERE verify_hash = $1 AND email_pending <> '' AND verify_sent_at > $2
RETURNING *;

-- name: ClearCaptainEmail :exec
UPDATE captain_notify
SET email = false, email_address = '', email_pending = '', verify_hash = NULL, updated_at = now()
WHERE user_id = $1;

-- name: MarkCaptainNotified :exec
UPDATE launches SET captain_notified = array_append(captain_notified, sqlc.arg(channel)::text)
WHERE id = sqlc.arg(id) AND NOT (sqlc.arg(channel)::text = ANY (captain_notified));

-- name: CountReposByOwner :one
SELECT count(*) FROM repos WHERE owner_user_id = $1;
