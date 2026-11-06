-- name: GetCaptainSettings :one
SELECT * FROM captain_settings WHERE user_id = $1;

-- name: UpsertCaptainSettings :one
INSERT INTO captain_settings (user_id, comment_template, draft_notes)
VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO UPDATE
SET comment_template = EXCLUDED.comment_template, draft_notes = EXCLUDED.draft_notes, updated_at = now()
RETURNING *;
