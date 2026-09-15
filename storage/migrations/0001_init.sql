-- schema_migrations itself is bootstrapped by the migration runner (storage/schema.go)
-- before any migration file is applied, so it is not created here.

-- Extensible source registry. Seeded with YouTube; a future source is just another row.
CREATE TABLE sources (
    id    INTEGER PRIMARY KEY,
    name  TEXT NOT NULL UNIQUE
);
INSERT INTO sources (id, name) VALUES (1, 'youtube');

-- A tracked channel/creator: source-agnostic identity + user tracking preferences.
CREATE TABLE channels (
    id                     INTEGER PRIMARY KEY,
    name                   TEXT NOT NULL,
    last_checked           TEXT,                          -- NULL = zero time.Time
    retention_days         INTEGER NOT NULL DEFAULT 0,
    disable_pruning        INTEGER NOT NULL DEFAULT 0,
    cutoff_date            TEXT,
    video_quality          TEXT NOT NULL DEFAULT '',
    video_format           TEXT NOT NULL DEFAULT '',
    download_shorts        INTEGER NOT NULL DEFAULT 0,
    skip_auto_download     INTEGER NOT NULL DEFAULT 0,
    last_error             TEXT NOT NULL DEFAULT '',
    last_error_time        TEXT,
    thumbnail_url          TEXT NOT NULL DEFAULT '',
    backlog_scan_complete  INTEGER NOT NULL DEFAULT 0
);

-- Maps a channel to its identity within a specific source (e.g. YouTube's "UC..." ID + URL).
CREATE TABLE channel_sources (
    channel_id         INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    source_id          INTEGER NOT NULL REFERENCES sources(id),
    source_channel_id  TEXT NOT NULL,        -- e.g. "UCxxxx"; the canonical, actively-tracked channel ID
    url                TEXT NOT NULL,
    PRIMARY KEY (source_id, source_channel_id)
);
CREATE UNIQUE INDEX ux_channel_sources_channel ON channel_sources(channel_id, source_id);

-- A specific video: source-agnostic core fields + unified download lifecycle.
CREATE TABLE videos (
    id                     INTEGER PRIMARY KEY,
    title                  TEXT NOT NULL DEFAULT '',
    publish_date           TEXT,
    added_at               TEXT,               -- first discovered/added
    last_checked           TEXT,
    disable_pruning        INTEGER NOT NULL DEFAULT 0,
    -- 'pruned' is reachable ONLY for rows linked via channel_videos (channel-owned). A
    -- standalone (individually-tracked) video that ages out past retention is hard-deleted
    -- entirely, never transitioned to 'pruned' -- enforced in application code, not here.
    status                 TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','downloaded','pruned')),
    download_date          TEXT,
    is_short               INTEGER NOT NULL DEFAULT 0,
    manual_download_only   INTEGER NOT NULL DEFAULT 0,
    last_error             TEXT NOT NULL DEFAULT '',
    last_error_time        TEXT
);

-- Maps a video to its identity within a specific source.
CREATE TABLE video_sources (
    video_id                    INTEGER NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    source_id                   INTEGER NOT NULL REFERENCES sources(id),
    source_video_id             TEXT NOT NULL,  -- YouTube's 11-char video ID
    url                         TEXT,           -- nullable; see MarkVideoAsDownloaded's defensive-insert path
    uploader_name               TEXT NOT NULL DEFAULT '',
    -- Cached uploader identity for videos whose channel is NOT actively tracked. Distinct
    -- from channel_sources.source_channel_id (an actively-tracked channel's canonical ID);
    -- the two are only joinable 1:1 when that uploader happens to also be tracked.
    uploader_source_channel_id  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (source_id, source_video_id)
);
CREATE UNIQUE INDEX ux_video_sources_video ON video_sources(video_id, source_id);

-- M:N join; a UNIQUE index on video_id enforces today's real "one channel per video"
-- invariant while leaving schema room to relax it later if ever needed.
CREATE TABLE channel_videos (
    channel_id  INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    video_id    INTEGER NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    PRIMARY KEY (channel_id, video_id)
);
CREATE UNIQUE INDEX ux_channel_videos_video ON channel_videos(video_id);

-- Override settings that ONLY apply to a video tracked individually (i.e. NOT governed by
-- a tracked channel's settings). Row existence for a video_id IS the "individually
-- tracked" signal -- no separate boolean flag needed.
CREATE TABLE individual_video_tracking (
    video_id         INTEGER PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
    retention_days   INTEGER NOT NULL DEFAULT 0,
    video_quality    TEXT NOT NULL DEFAULT '',
    video_format     TEXT NOT NULL DEFAULT '',
    download_shorts  INTEGER NOT NULL DEFAULT 0
);

-- Singleton idempotency marker for the one-time JSON import.
CREATE TABLE json_import_state (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    imported     INTEGER NOT NULL DEFAULT 0,
    imported_at  TEXT,
    source_path  TEXT
);
INSERT INTO json_import_state (id, imported) VALUES (1, 0);
