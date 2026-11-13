# AGENTS.md

## Project Overview

`ytdm` is a Go 1.21+ service that monitors YouTube channels and specific video URLs, downloads media with `yt-dlp`, and manages retention/pruning. It includes:

- REST API (`/api/*`)
- Web UI served from `static/`
- Background scheduler for periodic checks and pruning
- Persistent state/config in `data/`

Primary runtime artifacts:

- `data/config.json`: application config (including notification webhooks)
- `data/data.db`: channels/videos state in SQLite (default), or an optional PostgreSQL database selected via `YTDM_POSTGRES_*` env vars
- `downloads/`: downloaded media organized by channel

## Tech Stack

- Language: Go (standard library in app code; see the dependency policy below)
- Storage: SQLite via `modernc.org/sqlite` (default) or PostgreSQL via `github.com/lib/pq`
- External runtime dependency: `yt-dlp` (plus optional `ffmpeg`)
- Frontend: static HTML/JS in `static/`
- Deployment: Docker / docker-compose / local binary

## Code Map

- `main.go`: app entrypoint, startup lifecycle
- `api.go`: HTTP server + REST endpoints
- `config.go`: config model and load/save behavior
- `storage/`: persistent data/state management for both SQLite and PostgreSQL
- `database.go`: backend selection from env vars and the `ytdm migrate-db` command
- `notify.go`: webhook notifications (event types, ntfy and JSON delivery, templates)
- `scheduler.go`: periodic jobs
- `downloader.go`: `yt-dlp` interaction and download flow
- `updater.go`: `yt-dlp` auto-update logic
- `startup_cleanup.go`: startup reconciliation/cleanup
- `*_test.go`: test coverage by domain

## Domain Rules To Preserve

1. Pruning controls:
- Global pruning toggle via config field `disable_pruning`
- Per-entry pruning toggle via `disable_pruning` on channels/videos/downloaded entries
- Pruning decisions are based on download age/date only (not publish date)

2. Channel download eligibility uses publish date gates:
- Respect channel `cutoff_date` when set
- When `cutoff_date` is set, discovery is cutoff-first (download backlog since cutoff at least once)
- If `cutoff_date` is not set, use retention threshold (`now - retention_days`) for discovery window
- Persist pruned channel video IDs so already-downloaded-and-pruned videos are not re-downloaded repeatedly

3. Single video entry behavior differs:
- Always attempt download when entry exists
- `added_date` is auto-set on create if missing
- Pruning uses download age (not publish-date gate)
- After pruning removes media, standalone video entries are removed from `videos[]`

4. Metadata/file handling:
- Keep `*.info.json` sidecars after download (pruned later with media)
- New downloads are named `S<YYYY>E<MMDDhhmmss> <Title>-<id>.<ext>` with a matching `<stem>.nfo` (`<episodedetails>`) so Jellyfin TV libraries don't merge same-day uploads; never rename existing files (legacy `YYYY-MM-DD <Title>-<id>` names must stay as-is), and keep all file matching by video ID
- For `mp4`, prefer H.264 + AAC in the format selector
- Request and convert thumbnails to JPG for media server compatibility
- Resolve single-video channel directory from yt-dlp uploader metadata (do not hardcode `unknown`)

5. Storage backends (SQLite and PostgreSQL):
- Write storage queries once, in SQLite `?` placeholder style; `storage/db.go` rebinds them for Postgres and binds bools as 0/1
- Keep both schemas column-for-column identical: flags are `INTEGER` 0/1 and timestamps RFC 3339 `TEXT` on both
- Every schema change needs a migration with the same version number in both `storage/migrations/sqlite/` and `storage/migrations/postgres/`; `CopyDatabase` relies on matching versions and column names
- Use `INSERT ... RETURNING id` rather than `LastInsertId` (unsupported by the Postgres driver)
- New tables must be added to `copyTables` in `storage/copy.go` (and `identityTables` if they auto-increment)
- Postgres is optional: ytdm must always start. When Postgres is intended but unusable, ytdm runs on the separate `data/fallback.db` (never the main `data.db`, which may hold stale pre-migration data) with a warning in `/api/status`; the next successful Postgres start merges it in with `storage.ReconcileInto` (add-only, statuses only advance) and renames it to a backup. Only `migrate-db` treats bad settings as errors
- Backend choice order: `YTDM_DB_DRIVER`, then `data/database.json` (written atomically by migrations and the first Postgres start, recording the Postgres identity), then auto-detect (Postgres only if no `data.db` exists). Never let adding Postgres settings strand SQLite data
- Migrations between backends (UI `POST /api/database/migrate`, CLI `migrate-db`) default to merge (`storage.MergeDatabase`): upsert by YouTube IDs, keep target-only rows, never regress a video's status. Replace (`CopyDatabase`) wipes the target. The live switch (`Storage.MigrateTo`) holds the write lock during the copy and saves the preference before switching
- Schema migrations must stay re-runnable: use IF NOT EXISTS / ON CONFLICT DO NOTHING, and add a `migrationProbes` entry for any statement that can't be repeated (e.g. SQLite ADD COLUMN)
- A Postgres connection must name its database explicitly (`YTDM_POSTGRES_DATABASE` or the URL path); never rely on libpq defaults. `YTDM_POSTGRES_SCHEMA` optionally moves the tables out of the default schema
- On Postgres, reads run concurrently but writes are serialized in-process (`storage/db.go`), preserving SQLite's transaction semantics; keep it that way unless every read-then-write transaction is made race-safe
- Verify storage changes against Postgres too: `YTDM_TEST_POSTGRES_URL=... go test ./storage`

6. Notifications:
- Webhooks subscribe to typed events (`video.downloaded`, `video.pending`, `download.failed`, `cookies.expired`, `channel.check_failed`), not a single on/off switch
- Channel events respect the channel's `disable_notifications`; individually tracked video events respect the single global `notifications.individual_videos` switch
- Failure alerts go through the repeat cooldown; keep new failure events rate-limited the same way
- Delivery is asynchronous and must never block or fail a download

7. Reconciliation behavior:
- `ReconcileDownloadedVideos` removes orphaned downloaded-video entries whose files are missing
- Channel records are durable and not auto-removed as "expired"

## API Surface (high level)

- Channels: `GET/POST/DELETE /api/channels`
- Videos: `GET/POST/DELETE /api/videos`
- Config: `GET/PUT /api/config`
- Cookies: `POST /api/cookies`, `POST /api/cookies/clear`
- Status: `GET /api/status`
- Notifications: `POST /api/notifications/test`
- Database: `GET /api/database`, `POST /api/database/migrate`

When changing request/response types, keep API tests in sync.

## Local Development

Typical commands:

```bash
# run all tests
go test -v ./...

# run targeted tests
go test -v -run TestConfig
go test -v -run TestStorage
go test -v -run TestDownloader

# local run
./run-local.sh
# or
go build -o ytdm && ./ytdm
```

## Docker

```bash
docker-compose up -d
# or
docker build -t ytdm .
```

## Agent Working Guidelines For This Repo

1. Keep changes minimal and focused; preserve existing behavior unless explicitly requested.
2. Prefer extending existing structs/flows over introducing new abstractions in this small flat package layout.
3. Update/add tests for behavior changes, especially in retention, pruning, scheduling, and API payloads.
4. Use as few third-party dependencies as possible. Prefer the standard library; add a Go module only when the alternative is reimplementing something substantial (such as a database driver), pick the option with the fewest transitive dependencies, and justify it in the change. The same applies to frontend libraries in `static/`.
5. Treat `data/` runtime files as user state; do not hardcode local machine assumptions.
6. Preserve backwards compatibility for config fields and API JSON where practical.
7. When behavior or API changes are made:
  - update relevant documentation (for example `README.md`, API examples, and operational notes) in the same change where applicable.
  - rebuild the application docker image using docker compose if available

## Git Rules

**Never auto-commit and push directly to `master`.** Always create a feature branch, commit there, and open a PR. Direct commits to `master` bypass review and can immediately break production.

## Quick Validation Checklist

Before finishing a change:

1. `go test -v ./...` passes.
2. Relevant targeted tests for touched area pass.
3. API contract changes are reflected in tests and UI usage if applicable.
4. Retention/pruning rules are still consistent across channel and single-video flows.
5. Run `go fmt ./...` to ensure consistent formatting across all go files
