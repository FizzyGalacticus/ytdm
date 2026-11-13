-- Per-channel opt-out for new-video webhook notifications. Defaults to 0 (notify), the
-- same "disable_*" convention as disable_pruning.
ALTER TABLE channels ADD COLUMN IF NOT EXISTS disable_notifications INTEGER NOT NULL DEFAULT 0;
