-- Per-entry opt-in for marking SponsorBlock segments as chapters (the video itself is
-- never cut). Defaults to 0 (off) for existing channels and individually tracked videos.
ALTER TABLE channels ADD COLUMN IF NOT EXISTS sponsorblock_mark INTEGER NOT NULL DEFAULT 0;
ALTER TABLE individual_video_tracking ADD COLUMN IF NOT EXISTS sponsorblock_mark INTEGER NOT NULL DEFAULT 0;
