-- name: ListCaptainTemplates :many
SELECT * FROM captain_templates WHERE user_id = $1 ORDER BY lower(name), created_at;

-- name: CountCaptainTemplates :one
SELECT count(*) FROM captain_templates WHERE user_id = $1;

-- name: GetCaptainTemplate :one
SELECT * FROM captain_templates WHERE id = $1;

-- name: CreateCaptainTemplate :one
INSERT INTO captain_templates (user_id, name, comment_template, draft_notes)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateCaptainTemplate :one
UPDATE captain_templates
SET name = $3, comment_template = $4, draft_notes = $5, updated_at = now()
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: DeleteCaptainTemplate :execrows
DELETE FROM captain_templates WHERE id = $1 AND user_id = $2;

-- name: SetRepoTemplate :one
UPDATE repos SET template_id = $2 WHERE id = $1 RETURNING *;
