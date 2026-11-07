-- +goose Up
-- How a captain hears that a release made a launch draft. No row means the
-- defaults: an @mention on GitHub, nothing else. Email goes only to an
-- address the captain verified; email_pending holds one awaiting its link.
CREATE TABLE captain_notify (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    github bool NOT NULL DEFAULT true,
    release_notes bool NOT NULL DEFAULT false,
    email bool NOT NULL DEFAULT false,
    email_address text NOT NULL DEFAULT '',
    email_pending text NOT NULL DEFAULT '',
    verify_hash bytea,
    verify_sent_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX captain_notify_verify_hash ON captain_notify (verify_hash) WHERE verify_hash IS NOT NULL;

-- Which channels already told the captain about this draft, so a retried
-- job does not mention or email twice.
ALTER TABLE launches ADD COLUMN captain_notified text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE launches DROP COLUMN captain_notified;
DROP TABLE captain_notify;
