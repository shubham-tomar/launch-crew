-- +goose Up
-- Named templates: a captain keeps several PR comment and drafting-note sets
-- and picks one per repo (GitHub releases) or per launch. A null template_id
-- means the built-in default.
CREATE TABLE captain_templates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name text NOT NULL,
    comment_template text NOT NULL DEFAULT '',
    draft_notes text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX captain_templates_user_name_key ON captain_templates (user_id, lower(name));

ALTER TABLE repos ADD COLUMN template_id uuid REFERENCES captain_templates (id) ON DELETE SET NULL;
ALTER TABLE launches ADD COLUMN template_id uuid REFERENCES captain_templates (id) ON DELETE SET NULL;

-- The single saved template becomes "My template" and stays on the repos and
-- open launches it applied to, so nothing changes for captains who had one.
INSERT INTO captain_templates (user_id, name, comment_template, draft_notes)
SELECT user_id, 'My template', comment_template, draft_notes
FROM captain_settings
WHERE comment_template <> '' OR draft_notes <> '';

UPDATE repos r SET template_id = t.id
FROM captain_templates t WHERE t.user_id = r.owner_user_id;

UPDATE launches l SET template_id = t.id
FROM captain_templates t WHERE t.user_id = l.owner_user_id AND l.status IN ('draft', 'launched');

-- +goose Down
ALTER TABLE launches DROP COLUMN template_id;
ALTER TABLE repos DROP COLUMN template_id;
DROP TABLE captain_templates;
