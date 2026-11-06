-- +goose Up
-- How many times the captain pressed "Retry comment" for this person. The
-- retry is capped so a permission problem cannot be hammered.
ALTER TABLE crew_members ADD COLUMN comment_retries int NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE crew_members DROP COLUMN comment_retries;
