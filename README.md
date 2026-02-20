# YouTube Media Downloader

Automated YouTube video downloader with channel monitoring, retention management, and a web interface.

## Features

- Monitor YouTube channels and automatically download new videos
- Per-channel and per-video retention policies with cutoff dates
- Web UI for configuration and management
- REST API for programmatic control
- Cookie support for bypassing rate limits
- Automatic yt-dlp updates
- Docker support with multi-stage builds
- Concurrent downloads with configurable limits

## Quick Start

### Docker (Recommended)

```bash
# Using docker-compose
docker-compose up -d

# Or manually
docker build -t media-downloader .
docker run -d -p 8080:8080 \
  -v $(pwd)/data:/app/data \
  -v $(pwd)/downloads:/app/downloads \
  media-downloader
```

Access the web UI at `http://localhost:8080`

### Local

**Requirements:** Go 1.21+, Python 3, yt-dlp, ffmpeg (optional)

```bash
# Quick start script
./run-local.sh

# Or manually
go build -o media_downloader
./media_downloader
```

## Configuration

All configuration can be managed through the web UI or by editing `data/config.json`:

```json
{
  "check_interval_seconds": 300,
  "retention_days": 7,
  "download_dir": "../downloads",
  "file_name_pattern": "%(title)s-%(id)s.%(ext)s",
  "max_concurrent_downloads": 3,
  "yt_dlp_update_interval_seconds": 86400,
  "cookies_browser": "firefox",
  "cookies_file": "data/cookies.txt"
}
```

### Key Settings

- **check_interval_seconds**: How often to check for new videos (default: 300)
- **retention_days**: Default retention period in days (default: 7)
- **max_concurrent_downloads**: Number of simultaneous downloads (default: 3)
- **cookies_browser**: Extract cookies from browser (`firefox` or `chrome`)
- **cookies_file**: Path to Netscape format cookies file

### Per-Channel Settings

Each channel can override the global retention with its own retention period and cutoff date:

- **Retention Days**: Keep videos for N days (0 = use global setting)
- **Cutoff Date**: Only download videos published on or after this date

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

## API Endpoints

### Channels
- `GET /api/channels` - List all channels
- `POST /api/channels` - Add a channel
- `DELETE /api/channels/{id}` - Remove a channel

### Videos
- `GET /api/videos` - List all videos
- `POST /api/videos` - Add a video
- `DELETE /api/videos/{id}` - Remove a video

### Configuration
- `GET /api/config` - Get configuration
- `PUT /api/config` - Update configuration

### Cookies
- `POST /api/cookies` - Save pasted cookies
- `POST /api/cookies/clear` - Clear all cookies

### Status
- `GET /api/status` - Service status

## Directory Structure

```
media_downloader/
├── data/
│   ├── config.json      # Application configuration
│   ├── data.json        # Channel/video state
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
```

## Development

Built with Go 1.21 using only the standard library (yt-dlp runs as subprocess).

**Project structure:**
- `main.go` - Entry point and lifecycle management
- `config.go` - Configuration with thread-safe operations
- `storage.go` - Persistent data management
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
docker build -t media-downloader:slim --no-cache .
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

## Directory Structure

```
media_downloader/
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

Downloaded videos are organized by channel:
```
/downloads/
├── Channel_Name_1/
│   ├── video1.mp4
│   └── video2.mp4
└── Channel_Name_2/
    └── video3.mp4
```

## Logging

All operations are logged to stdout, including:
- Video downloads
- Removal of old videos
- API requests
- Configuration changes
- Errors and warnings

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

## License

MIT License
