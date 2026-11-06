-- +goose Up
-- Per-repo card branding: a captain-set logo (stored re-encoded, so cards never
-- fetch a captain URL at render time) and an accent colour. Empty means the
-- GitHub owner's avatar and the default accent. display_name caches the
-- contributor's GitHub profile name for the card; empty falls back to the login.
ALTER TABLE repos ADD COLUMN logo_url text NOT NULL DEFAULT '';
ALTER TABLE repos ADD COLUMN logo_png bytea;
ALTER TABLE repos ADD COLUMN accent text NOT NULL DEFAULT '';
ALTER TABLE crew_members ADD COLUMN display_name text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE crew_members DROP COLUMN display_name;
ALTER TABLE repos DROP COLUMN accent;
ALTER TABLE repos DROP COLUMN logo_png;
ALTER TABLE repos DROP COLUMN logo_url;
