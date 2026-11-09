-- +goose Up
-- Captain-uploaded images sit beside the cards in launch_assets as kind
-- 'image', ordered by position (NULL for cards and the hero), stored as
-- uploaded. bytes is the original's size; small_url is a copy under 1 MB
-- made for Bluesky when the original is over it, else empty. include_card
-- says whether a launch's posts lead with the card slot.
ALTER TABLE launch_assets ADD COLUMN position smallint;
ALTER TABLE launch_assets ADD COLUMN bytes bigint NOT NULL DEFAULT 0;
ALTER TABLE launch_assets ADD COLUMN small_url text NOT NULL DEFAULT '';
ALTER TABLE launches ADD COLUMN include_card boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE launches DROP COLUMN include_card;
ALTER TABLE launch_assets DROP COLUMN small_url;
ALTER TABLE launch_assets DROP COLUMN bytes;
ALTER TABLE launch_assets DROP COLUMN position;
