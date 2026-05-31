# AGENTS.md

## Project Overview

`ytdm` is a Go 1.21+ service that monitors YouTube channels and specific video URLs, downloads media with `yt-dlp`, and manages retention/pruning. It includes:

- REST API (`/api/*`)
- Web UI served from `static/`
- Background scheduler for periodic checks and pruning
- Persistent state/config in `data/`

Primary runtime artifacts:

- `data/config.json`: application config
- `data/data.json`: channels/videos state
- `downloads/`: downloaded media organized by channel

## Tech Stack

- Language: Go (standard library only in app code)
- External runtime dependency: `yt-dlp` (plus optional `ffmpeg`)
- Frontend: static HTML/JS in `static/`
- Deployment: Docker / docker-compose / local binary

## Code Map

- `main.go`: app entrypoint, startup lifecycle
- `api.go`: HTTP server + REST endpoints
- `config.go`: config model and load/save behavior
- `storage.go`: persistent data/state management
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
- Request and convert thumbnails to JPG for media server compatibility
- Resolve single-video channel directory from yt-dlp uploader metadata (do not hardcode `unknown`)

5. Reconciliation behavior:
- `ReconcileDownloadedVideos` removes orphaned downloaded-video entries whose files are missing
- Channel records are durable and not auto-removed as "expired"

## API Surface (high level)

- Channels: `GET/POST/DELETE /api/channels`
- Videos: `GET/POST/DELETE /api/videos`
- Config: `GET/PUT /api/config`
- Cookies: `POST /api/cookies`, `POST /api/cookies/clear`
- Status: `GET /api/status`

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
4. Avoid introducing non-stdlib Go dependencies unless explicitly justified.
5. Treat `data/` runtime files as user state; do not hardcode local machine assumptions.
6. Preserve backwards compatibility for config fields and API JSON where practical.
7. When behavior or API changes are made:
  - update relevant documentation (for example `README.md`, API examples, and operational notes) in the same change where applicable.
  - rebuild the application docker image using docker compose if available

## Quick Validation Checklist

Before finishing a change:

1. `go test -v ./...` passes.
2. Relevant targeted tests for touched area pass.
3. API contract changes are reflected in tests and UI usage if applicable.
4. Retention/pruning rules are still consistent across channel and single-video flows.
