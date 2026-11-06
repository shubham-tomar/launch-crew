-- +goose Up
-- Where a sent post lives on its network, filled in by the status poll.
ALTER TABLE captain_drafts ADD COLUMN external_link text NOT NULL DEFAULT '';
ALTER TABLE crew_drafts ADD COLUMN external_link text NOT NULL DEFAULT '';

-- Per-captain templates: the PR comment wording and notes the model reads
-- while drafting the crew's posts. Empty means the built-in default.
CREATE TABLE captain_settings (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    comment_template text NOT NULL DEFAULT '',
    draft_notes text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE captain_settings;
ALTER TABLE crew_drafts DROP COLUMN external_link;
ALTER TABLE captain_drafts DROP COLUMN external_link;
