-- +goose Up
-- post_formats holds the captain's per-channel post format, by Buffer
-- channel id: "story" (Instagram or Facebook) or "document" (LinkedIn, the
-- images as one swipeable PDF). A channel not listed posts normally. It
-- sits on the launch, not the draft, so rebuilding drafts keeps it.
ALTER TABLE launches ADD COLUMN post_formats jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE launches DROP COLUMN post_formats;
