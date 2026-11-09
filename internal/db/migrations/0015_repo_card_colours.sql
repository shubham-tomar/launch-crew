-- +goose Up
-- Card background and text colours per repo, set beside the accent. Empty
-- means the default ink card with white type.
ALTER TABLE repos ADD COLUMN card_bg text NOT NULL DEFAULT '';
ALTER TABLE repos ADD COLUMN card_text text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE repos DROP COLUMN card_text;
ALTER TABLE repos DROP COLUMN card_bg;
