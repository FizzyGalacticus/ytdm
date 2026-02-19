# YouTube Media Downloader

A Go-based service for automatically downloading and managing YouTube videos from channels and individual URLs.

## Features

- **Channel Monitoring**: Automatically download new videos from subscribed channels
- **Per-Channel Retention**: Each channel can have its own video retention period
- **Individual Video Tracking**: Monitor and download specific videos with custom retention
- **Retention Management**: Automatically remove videos older than configured retention period
- **REST API**: Full API for managing channels, videos, and configuration
- **Web Interface**: Bootstrap 5-based UI for easy management
- **Concurrent Downloads**: Configurable concurrent download limits
- **Auto-Updates**: yt-dlp automatically updates itself
- **Docker Support**: Runs as a containerized service
- **Standard Library Only**: Uses only Go standard library (except yt-dlp subprocess)

## Quick Start

### Using Docker

1. Build the Docker image:
```bash
docker build -t media-downloader:slim .
```

2. Run the container:
```bash
docker run -d \
  -p 8080:8080 \
  -v $(pwd)/downloads:/downloads \
  -v $(pwd)/data:/data \
  --name media-downloader \
  media-downloader:slim
```

3. Access the web interface at `http://localhost:8080`

### Running Locally (Without Docker)

**Prerequisites:**
- Go 1.21 or later
- Python 3 with pip
- (Optional) ffmpeg for video processing

**Installation:**

1. Install yt-dlp:
```bash
pip3 install --user yt-dlp
# or
pip install yt-dlp
```

2. Install ffmpeg (optional but recommended):
```bash
# Ubuntu/Debian
sudo apt install ffmpeg

# macOS
brew install ffmpeg

# Arch Linux
sudo pacman -S ffmpeg
```

3. Build and run:
```bash
# Using the convenience script
./run-local.sh

# Or manually
go build -o media_downloader
./media_downloader
```

4. Access at http://localhost:8080

**Note:** The app will:
- Create `./downloads` directory for videos
- Create `./data` directory for configuration and state
- Use default config on first run (editable via web UI)

3. Run:
```bash
./media_downloader
```

## Configuration

The application can be configured via `config.json` or through the web interface:

```json
{
  "check_interval_seconds": 300,
  "retention_days": 30,
  "download_dir": "/downloads",
  "file_name_pattern": "%(title)s-%(id)s.%(ext)s",
  "api_port": 8080,
  "max_concurrent_downloads": 3,
  "yt_dlp_path": "yt-dlp"
}
```

### Configuration Options

- `check_interval_seconds`: How often to check for new videos (default: 300)
- `retention_days`: Default retention days for channels/videos if not specified (default: 30)
- `download_dir`: Directory where videos are stored (default: /downloads)
- `file_name_pattern`: yt-dlp filename pattern (default: %(title)s-%(id)s.%(ext)s)
- `api_port`: Port for the API/web server (default: 8080)
- `max_concurrent_downloads`: Maximum concurrent downloads (default: 3)
- `yt_dlp_path`: Path to yt-dlp executable (default: yt-dlp)
- `yt_dlp_update_interval_seconds`: How often to auto-update yt-dlp (default: 86400 = 24 hours, 0 = disabled)

## Per-Channel/Video Retention

Each channel and video can have its own retention period:
- When adding a channel/video, optionally specify `retention_days`
- If not specified, uses the global default from config
- Videos older than the retention period are automatically deleted
- Set to 0 to use the global default
- Each channel manages its own retention independently

## Channel Cutoff Date

Channels support a **cutoff date** to ignore old videos:
- Set a cutoff date when adding a channel (e.g., "2024-01-01")
- The app will **never** download videos published before this date
- Useful for subscribing to channels without downloading their entire back catalog
- Example: Set cutoff to today's date to only get future videos
- Displayed as a blue "From: DATE" badge in the web UI
- Optional field - leave blank to download all videos (respecting retention)

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

MIT License - See LICENSE file for details
