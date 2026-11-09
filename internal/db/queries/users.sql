-- name: CreateUser :one
INSERT INTO users DEFAULT VALUES RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: UpsertGitHubIdentity :one
INSERT INTO github_identities (github_user_id, user_id, login)
VALUES ($1, $2, $3)
ON CONFLICT (github_user_id) DO UPDATE SET login = EXCLUDED.login
RETURNING *;

-- name: GetGitHubIdentity :one
SELECT * FROM github_identities WHERE github_user_id = $1;

-- name: InsertOptOut :exec
INSERT INTO opt_outs (github_user_id) VALUES ($1) ON CONFLICT DO NOTHING;

-- name: IsOptedOut :one
SELECT EXISTS (SELECT 1 FROM opt_outs WHERE github_user_id = $1);

-- name: ListOptOutsIn :many
SELECT github_user_id FROM opt_outs WHERE github_user_id = ANY (@ids::bigint[]);

-- name: UpsertRepo :one
INSERT INTO repos (installation_id, owner_user_id, full_name, github_repo_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT (installation_id, full_name) DO UPDATE
SET github_repo_id = EXCLUDED.github_repo_id
RETURNING *;

-- name: GetRepoByInstallationAndName :one
SELECT * FROM repos WHERE installation_id = sqlc.arg(installation_id) AND lower(full_name) = lower(sqlc.arg(full_name)::text);

-- name: GetReposByInstallation :many
SELECT * FROM repos WHERE installation_id = $1;

-- name: ListReposByOwner :many
SELECT * FROM repos WHERE owner_user_id = $1 ORDER BY full_name;

-- name: UpdateRepoFlags :one
UPDATE repos SET enabled = $2, allow_prerelease = $3 WHERE id = $1 RETURNING *;

-- Merging user from_id into into_id. No user-keyed UNIQUE exists, so plain
-- updates cannot collide.

-- name: MergeUserConnections :exec
UPDATE buffer_connections SET user_id = sqlc.arg(into_id) WHERE user_id = sqlc.arg(from_id);

-- name: MergeUserIdentities :exec
UPDATE github_identities SET user_id = sqlc.arg(into_id) WHERE user_id = sqlc.arg(from_id);

-- name: MergeUserRepos :exec
UPDATE repos SET owner_user_id = sqlc.arg(into_id) WHERE owner_user_id = sqlc.arg(from_id);

-- name: MergeUserLaunches :exec
UPDATE launches SET owner_user_id = sqlc.arg(into_id) WHERE owner_user_id = sqlc.arg(from_id);

-- name: MergeUserCrew :exec
UPDATE crew_members SET user_id = sqlc.arg(into_id) WHERE user_id = sqlc.arg(from_id);

-- name: DeleteUser :exec
DELETE FROM users WHERE id = $1;

-- name: SetUserRememberMe :exec
UPDATE users SET remember_me = $2 WHERE id = $1;

-- name: UpdateRepoBranding :one
UPDATE repos SET logo_url = $2, logo_png = $3, accent = $4, card_bg = $5, card_text = $6 WHERE id = $1 RETURNING *;
