# YouTube Media Downloader

[![Tests](https://github.com/FizzyGalacticus/ytdm/actions/workflows/test-and-publish.yml/badge.svg)](https://github.com/FizzyGalacticus/ytdm/actions/workflows/test-and-publish.yml)
[![Coverage](https://codecov.io/gh/FizzyGalacticus/ytdm/branch/master/graph/badge.svg)](https://codecov.io/gh/FizzyGalacticus/ytdm)

Automated YouTube video downloader with channel monitoring, retention management, and a web interface.

## Features

- Monitor YouTube channels and automatically download new videos
- Per-channel and per-video retention policies with cutoff dates
- Web UI for configuration and management
- Channels list sorted alphabetically with expandable downloaded-video panels
- Download status indicator for individually tracked videos
- Color-coded scoped logs with channel/video filtering
- REST API for programmatic control
- Cookie support for bypassing rate limits
- Automatic yt-dlp updates
- Docker support with multi-stage builds
- Concurrent downloads with configurable limits
- SQLite (default) or PostgreSQL storage, with a command to move data between them
- Webhook notifications (ntfy or generic JSON) for new videos, pending videos, failures, and expiring cookies

## Quick Start

### Docker (Recommended)

```bash
# Using docker-compose
docker-compose up -d

# Or manually
docker build -t ytdm .
docker run -d -p 8080:8080 \
  -v $(pwd)/data:/app/data \
  -v $(pwd)/downloads:/app/downloads \
  ytdm
```

Access the web UI at `http://localhost:8080`

### Local

**Requirements:** Go 1.21+, Python 3, yt-dlp, ffmpeg (optional)

```bash
# Quick start script
./run-local.sh

# Or manually
go build -o ytdm
./ytdm
```

## Configuration

All configuration can be managed through the web UI or by editing `data/config.json`:

```json
{
  "check_interval_seconds": "5m0s",
  "retention_days": 7,
  "disable_pruning": false,
  "download_dir": "../downloads",
  "file_name_pattern": "%(title)s-%(id)s.%(ext)s",
  "max_concurrent_downloads": 3,
  "yt_dlp": {
    "path": "yt-dlp",
    "update_interval_seconds": "24h0m0s",
    "cookies_browser": "firefox",
    "cookies_file": "data/cookies.txt",
    "extractor_sleep_interval_seconds": "0s",
    "download_throughput_limit": "",
    "restrict_filenames": false,
    "cache_dir": "data/yt-dlp-cache"
  }
}
```

### Key Settings

**Note:** Duration fields in `data/config.json` use Go duration strings (e.g. `"5m0s"`, `"24h0m0s"`). The web UI still accepts seconds and converts internally.

- **check_interval_seconds**: How often to check for new videos (default: `5m0s`)
- **retention_days**: Default retention period in days (default: 7)
- **disable_pruning**: Disable all automatic pruning globally (default: false)
- **max_concurrent_downloads**: Number of simultaneous downloads (default: 3)
- **yt_dlp**: Settings for yt-dlp
  - **path**: Path to yt-dlp executable
  - **update_interval_seconds**: How often to auto-update yt-dlp (e.g. `24h0m0s`, `0s` to disable)
  - **cookies_browser**: Extract cookies from browser (`firefox` or `chrome`)
  - **cookies_file**: Path to Netscape format cookies file
  - **extractor_sleep_interval_seconds**: Sleep between extractor requests (e.g. `0s` to disable)
  - **download_throughput_limit**: Limit download speed (e.g. `100K`, `4.2M`)
  - **restrict_filenames**: Use yt-dlp's filename restrictions
  - **cache_dir**: Cache directory used by yt-dlp

### Per-Channel Settings

Each channel can override the global retention with its own retention period and cutoff date:

- **Retention Days**: Keep videos for N days (0 = use global setting)
- **Keep indefinitely (disable pruning)**: Skip automatic pruning for this channel/video entry
- **Cutoff Date**: Only download videos published on or after this date

Channel monitoring discovery behavior:

- If `cutoff_date` is set: videos on/after `cutoff_date` are eligible for first-time download (cutoff-first backlog behavior)
- If `cutoff_date` is not set: discovery uses `publish_date >= now - retention_days`

Single-entry requested videos are always attempted when present in the list (publish date is not used as a download gate for single entries).
Pruning is based on download age (`now - retention_days`), with per-entry No Prune still respected.
Channel cutoff date is used for channel discovery eligibility and does not trigger pruning by publish date.
For channel entries, videos already downloaded and later pruned are tracked in persistence to prevent re-download loops.

How new channel videos are found:

- ytdm first asks YouTube's RSS feed for the channel (one cheap request).
- YouTube has been answering that feed with `404` for many channels since October 2026. When it does, ytdm falls back to a flat yt-dlp listing of the channel (all tabs: Videos, Live, Shorts, in about one request), then looks up only the videos it has never seen, newest first, stopping at the first one older than the discovery window. Logs show this as `RSS feed unavailable (...), using channel listing` followed by a `Channel listing: N listed, N extracted, N unavailable` summary; it's expected while the feed is down.
- Videos that can't be looked up (members-only without a member's cookies, private, region-locked) are skipped without failing the channel check, and retried after a few hours. Upcoming premieres and live streams are picked up once they've aired.
- A big first backlog (e.g. an old cutoff date) is looked up at most 50 videos per check; the rest follows on later checks.

## Cookie Support

YouTube may require authentication to avoid rate limiting. Two options:

### 1. Browser Cookies (Automatic)
Select browser in the Configuration tab. Requires the browser to be running and logged into YouTube.

### 2. Paste Cookies (Manual)
1. Export cookies using a browser extension (Cookie Editor, etc.)
2. Paste Netscape format cookies in the Configuration tab
3. Click "Save Pasted Cookies"

Example format:
```
# Netscape HTTP Cookie File
.youtube.com	TRUE	/	TRUE	1805237469	COOKIE_NAME	cookie_value
```

## Database

ytdm stores channels and videos in SQLite (`data/data.db`) by default and needs nothing else to run. It can use PostgreSQL instead, configured with environment variables:

| Variable | Purpose |
| --- | --- |
| `YTDM_POSTGRES_DATABASE` | Database name. **Required** for Postgres: ytdm never assumes a default database. |
| `YTDM_POSTGRES_HOST`, `YTDM_POSTGRES_PORT` | Server (default `localhost`, `5432`) |
| `YTDM_POSTGRES_USER`, `YTDM_POSTGRES_PASSWORD` | Credentials |
| `YTDM_POSTGRES_SSLMODE` | `disable`, `require` (driver default), `verify-ca`, or `verify-full` |
| `YTDM_POSTGRES_SCHEMA` | Optional schema for ytdm's tables instead of the database's default (normally `public`); created if missing |
| `YTDM_POSTGRES_URL` | Alternative to the variables above: `postgres://user:password@host:5432/database?sslmode=disable`. Must include the database name. |
| `YTDM_DB_DRIVER` | `sqlite` or `postgres`. Optional: setting any Postgres variable selects Postgres, and `sqlite` forces SQLite even when they are set. |
| `YTDM_SQLITE_PATH` | SQLite file (default `data/data.db`) |

Postgres is never required. If its settings are incomplete (for example no database name), or the server still can't be reached after about 30 seconds of retries at startup, ytdm logs a warning and runs on SQLite instead. The status bar shows the active database and a *Database fallback* badge when this happens. Fix the settings and restart to switch to Postgres.

The schema is created and migrated automatically on startup for either backend. `config.json` and `cookies.txt` stay files in `data/` either way.

On PostgreSQL, reads run in parallel over a small connection pool (up to 8 connections), so the web UI, channel checks, and download workers don't wait on each other the way they do on SQLite's single connection. Writes are still applied one at a time, exactly as on SQLite, so behavior is identical on both backends.

### Moving between SQLite and PostgreSQL

`ytdm migrate-db` copies every channel and video from one backend to the other, keeping IDs, and checks the row counts before committing. Stop the running service first so no writes are missed:

```bash
# SQLite -> PostgreSQL (reads data/data.db, writes to the Postgres from the env vars)
YTDM_POSTGRES_HOST=db YTDM_POSTGRES_USER=ytdm YTDM_POSTGRES_PASSWORD=secret YTDM_POSTGRES_DATABASE=ytdm \
  ./ytdm migrate-db --to postgres

# PostgreSQL -> SQLite
YTDM_POSTGRES_URL=postgres://ytdm:secret@db:5432/ytdm ./ytdm migrate-db --to sqlite --sqlite-path data/data.db

# In Docker
docker compose run --rm ytdm ./ytdm migrate-db --to postgres
```

The destination must be empty; pass `--overwrite` to replace what's there. The source is only read, so after a successful copy you can switch back at any time (set `YTDM_DB_DRIVER=sqlite`, or remove the Postgres variables). `--sqlite-path` and `--postgres-url` override the environment. Unlike startup, `migrate-db` never falls back: it fails if the Postgres settings are missing or invalid.

## Notifications

ytdm can send a webhook notification when something happens. Configure it in **Configuration > Notifications** in the web UI, or in the `notifications` section of `data/config.json`.

### Events

| Event | Sent when |
| --- | --- |
| `video.downloaded` | A new video finished downloading and is available |
| `video.pending` | A channel picked up a new video it won't download automatically (auto-download is off, or the video is under 2 minutes). A scan that finds many at once sends one summary instead (`pending_summary_threshold`, default 5). |
| `download.failed` | A download attempt failed |
| `cookies.expired` | yt-dlp failed in a way that suggests the YouTube cookies are missing or expired ("confirm you're not a bot", login required, ...) |
| `channel.check_failed` | A channel's feed couldn't be fetched |

Each webhook subscribes to any subset of events (no list means all of them). Failure alerts (`download.failed`, `cookies.expired`, `channel.check_failed`) repeat at most once per `repeat_cooldown` (default `6h`) for the same video, channel, or cookies problem.

Who gets notified:
- **Channels**: each channel has a *Send notifications* toggle (`disable_notifications` in the API). It covers that channel's downloaded, pending, and failure events.
- **Individually tracked videos**: one global switch, `individual_videos` (default on).
- `cookies.expired` isn't tied to a channel or video and goes to every subscribed webhook.

### ntfy

Set `type` to `ntfy` (the default) and `url` to the topic URL. Notifications include the channel avatar as the icon and open the video when tapped. They can also carry:
- **Thumbnail image**: `attachment` is `url` (YouTube's thumbnail, the default), `upload` (the downloaded `.jpg` is uploaded to ntfy, which needs attachments enabled on the server), or `none`.
- **Action buttons**: up to 3 `view` (open a URL or app deeplink) or `http` (send a request) buttons.
- **Auth**: `token` (access token) or `username`/`password`.
- **Priority, tags/emoji, markdown**, and a custom tap URL (`click`).

```json
"notifications": {
  "individual_videos": true,
  "app_url": "https://ytdm.example.com",
  "repeat_cooldown": "6h0m0s",
  "pending_summary_threshold": 5,
  "webhooks": [
    {
      "name": "Phone",
      "type": "ntfy",
      "url": "https://ntfy.sh/my-ytdm-topic",
      "token": "tk_...",
      "events": ["video.downloaded", "video.pending", "cookies.expired"],
      "attachment": "url",
      "template": {
        "actions": [
          { "action": "view", "label": "Open in YouTube app", "url": "vnd.youtube://{{.VideoID}}" },
          { "action": "http", "label": "Refresh Jellyfin", "url": "https://jellyfin.example.com/Library/Refresh", "method": "POST",
            "headers": { "X-Emby-Token": "..." }, "clear": true }
        ]
      },
      "event_templates": {
        "cookies.expired": { "priority": 5, "click": "{{.AppURL}}" }
      }
    }
  ]
}
```

### Generic JSON webhooks

Set `type` to `json` to `POST` the event as JSON to any URL, with optional `headers`, `token` (Bearer), or `username`/`password`. The body has the event fields below in snake_case (`event`, `channel_name`, `video_id`, `video_url`, `thumbnail_url`, `error`, ...) plus `rendered_title`, `rendered_message`, and `rendered_click`.

### Templates

Title, message, tap URL, icon, and action URLs/bodies are [Go templates](https://pkg.go.dev/text/template). Available fields: `{{.Event}}`, `{{.Title}}`, `{{.VideoID}}`, `{{.VideoURL}}`, `{{.ThumbnailURL}}`, `{{.PublishDate}}`, `{{.ChannelID}}`, `{{.ChannelName}}`, `{{.ChannelURL}}`, `{{.ChannelThumbnailURL}}`, `{{.Error}}`, `{{.Count}}`, and `{{.AppURL}}`.

`template` applies to every event and `event_templates` overrides it for one event. Any field left empty falls back to the built-in message for that event. An action button whose URL renders empty (e.g. `{{.VideoURL}}` on a cookies alert) is left out.

Use **Send test** in the UI (or `POST /api/notifications/test`) to send a sample based on the most recently downloaded video.

## API Endpoints

### Channels
- `GET /api/channels` - List all channels
- `POST /api/channels` - Add a channel
- `DELETE /api/channels/{id}` - Remove a channel

### Videos
- `GET /api/videos` - List all videos
- `POST /api/videos` - Add a video
- `DELETE /api/videos/{id}` - Remove a video

- `PUT /api/channels/{id}` - Update channel settings, including `disable_notifications`

### Configuration
- `GET /api/config` - Get configuration (includes `notifications` and the list of `notification_events`)
- `PUT /api/config` - Update configuration (send a `notifications` object to replace the notification settings)

### Notifications
- `POST /api/notifications/test` - Send a test notification. Body: `{"index": 0}` for a saved webhook, `{"webhook": {...}}` for unsaved settings, or empty for every saved webhook

### Cookies
- `POST /api/cookies` - Save pasted cookies
- `POST /api/cookies/clear` - Clear all cookies

### Status
- `GET /api/status` - Service status
- `GET /api/logs` - Recent logs
  - Optional query params: `scope_type=channel|video`, `scope_id=<id>`

## Directory Structure

```
ytdm/
├── data/
│   ├── config.json      # Application configuration
│   ├── data.db          # Channel/video state (SQLite; unused with PostgreSQL)
│   └── cookies.txt      # YouTube cookies
├── downloads/           # Downloaded videos (organized by channel)
├── static/
│   ├── index.html      # Web UI
│   └── app.js          # UI JavaScript
├── *.go                # Source files
├── *_test.go           # Test files
├── Dockerfile
├── docker-compose.yml
└── run-local.sh        # Local run script
```

## Testing

```bash
# Run all tests
go test -v ./...

# Run specific test suite
go test -v -run TestStorage
go test -v -run TestConfig
go test -v -run TestVideoInfo

# Also run the storage suite against PostgreSQL (each test gets its own schema)
YTDM_TEST_POSTGRES_URL='postgres://user:pass@localhost:5432/ytdm_test?sslmode=disable' go test ./storage
```

## Development

yt-dlp runs as a subprocess.

**Dependencies:** keep third-party Go modules to an absolute minimum. Prefer the standard library, and add a module only when the alternative is reimplementing something substantial (a database driver, for example), choosing the option with the fewest transitive dependencies. Today the only direct dependencies are the two database drivers: `modernc.org/sqlite` (pure-Go SQLite) and `github.com/lib/pq` (pure-Go PostgreSQL, no dependencies of its own). Notifications, templating, and HTTP all use the standard library.

**Project structure:**
- `main.go` - Entry point and lifecycle management
- `config.go` - Configuration with thread-safe operations
- `storage/` - Persistent data management (SQLite and PostgreSQL)
- `database.go` - Database selection from env vars and `ytdm migrate-db`
- `notify.go` - Webhook notifications (ntfy and JSON)
- `downloader.go` - yt-dlp wrapper
- `scheduler.go` - Background task scheduling
- `api.go` - REST API and web server
- `updater.go` - yt-dlp auto-updater

## Auto-Update Feature

**yt-dlp** automatically updates itself to the latest version:
- Default: Updates every 24 hours
- Configurable via web UI or API
- Set to 0 to disable auto-updates
- Uses yt-dlp's built-in self-update mechanism (`yt-dlp -U`)

**ffmpeg** updates require rebuilding the Docker image:
```bash
docker build -t ytdm:latest --no-cache .
```

## API Endpoints

### Channels

- `GET /api/channels` - List all channels
- `POST /api/channels` - Add a new channel
  ```json
  {
    "name": "Channel Name",
    "url": "https://youtube.com/@channelname",
    "retention_days": 60,
    "cutoff_date": "2024-01-01T00:00:00Z"
  }
  ```
  - `cutoff_date` (optional): Don't download videos published before this date
- `DELETE /api/channels/{id}` - Remove a channel

### Videos

- `GET /api/videos` - List all videos
- `POST /api/videos` - Add a new video
  ```json
  {
    "title": "Video Title",
    "url": "https://youtube.com/watch?v=VIDEO_ID",
    "retention_days": 90
  }
  ```
  - `added_date` is set automatically when omitted.
- `DELETE /api/videos/{id}` - Remove a video

### Configuration

- `GET /api/config` - Get current configuration
- `PUT /api/config` - Update configuration
  ```json
  {
    "check_interval_seconds": 600,
    "retention_days": 60
  }
  ```

### Status

- `GET /api/status` - Get service status
- `GET /api/logs` - Get recent logs (supports `scope_type` and `scope_id` filters)

## Directory Structure

```
ytdm/
├── main.go              # Entry point
├── config.go            # Configuration management
├── storage.go           # Persistent data storage
├── downloader.go        # yt-dlp wrapper
├── scheduler.go         # Background task scheduler
├── api.go               # REST API handlers
├── static/
│   └── index.html       # Web interface
├── Dockerfile           # Docker build configuration
├── .dockerignore        # Docker ignore file
└── README.md           # This file
```

## File Organization

Downloaded videos are organized by channel, named so a Jellyfin/Kodi **TV Shows** library treats each channel as a series and each upload as its own episode:
```
/downloads/
├── Saturday_Night_Live/
│   ├── S2026E1011042913 CopTalkShowSNL-RbXnGCrYNKs.mp4
│   ├── S2026E1011042913 CopTalkShowSNL-RbXnGCrYNKs.nfo        # episode metadata (title, air date, plot)
│   ├── S2026E1011042913 CopTalkShowSNL-RbXnGCrYNKs.jpg        # thumbnail
│   ├── S2026E1011042913 CopTalkShowSNL-RbXnGCrYNKs.info.json  # yt-dlp metadata
│   └── ...
└── Channel_Name_2/
    └── ...
```

- The season is the upload year and the episode number is the upload time (`MMDDhhmmss`, UTC), so several uploads on the same day don't collapse into one episode. (Jellyfin identifies date-named files by air date alone and merges same-day uploads into "versions" of a single episode.) Videos without an upload time get a stable per-video number for the time-of-day digits.
- The `.nfo` is named after the video file, which is what Jellyfin reads, and uses Jellyfin/Kodi's `<episodedetails>` format.
- Videos downloaded before this naming (`YYYY-MM-DD <Title>-<id>.mp4`, with `.info.nfo` metadata) are left exactly as they are, so media servers don't re-index them as new items. ytdm still tracks, checks and prunes them; all file matching is by video ID.
- For `mp4`, H.264 video and AAC audio are preferred over AV1/VP9 and Opus so files play directly on practically every player and Jellyfin client; YouTube only offers H.264 up to 1080p, so `best` quality with `mp4` tops out there. `webm`/`mkv` keep yt-dlp's default preference.
- The downloaded file is renamed after yt-dlp finishes, so the `file_name_pattern` setting only affects yt-dlp's temporary name.

## Logging

All operations are logged to stdout, including:
- Video downloads
- Removal of old videos
- API requests
- Configuration changes
- Errors and warnings

The web UI logs tab supports:
- Color-coded channel/video scoped log lines
- Structured filtering by channel or individual video entry
- Jump-to-logs actions from the Channels and Videos views
- Channel-scoped lifecycle visibility including feed checks, download attempts, download outcomes, and channel prune events

Metadata lookups prioritize non-yt-dlp sources first (for example, YouTube oEmbed) and only fall back to yt-dlp when required.

## Error Handling

The service is designed to handle errors gracefully:
- Failed downloads don't stop other downloads
- API errors return proper HTTP status codes
- Service continues running even if individual operations fail
- Automatic retry on transient failures

## Graceful Shutdown

The application handles system signals (SIGINT/SIGTERM) gracefully:
- **In-progress downloads**: Always complete before shutdown
- **Pending work**: Skipped to speed up shutdown
- **Timeout**: 5-minute maximum wait for downloads to finish
- **Clean exit**: All resources properly released

When you press Ctrl+C or send a termination signal:
1. Service stops accepting new download tasks
2. In-progress downloads are allowed to complete
3. Once all downloads finish, service exits cleanly
4. If downloads take longer than 5 minutes, forces exit

## Testing

The project includes comprehensive unit tests covering utility functions, storage operations, configuration management, and skip detection logic.

### Running Tests

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Run specific tests
go test -v -run TestSanitizeFilename

# Run with coverage report
go test ./... -cover

# Generate detailed coverage report
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out

# View coverage in browser
go tool cover -html=coverage.out
```

## License

MIT License
