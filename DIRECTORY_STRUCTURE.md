# Directory Structure - Consolidated Data

## Overview
All persistent data, configurations, and cookies now live in a single `data/` subdirectory. This simplifies Docker volume management and keeps the project root clean.

## Directory Layout

```
media_downloader/
├── data/                          # All persistent data & config
│   ├── config.json               # Application configuration
│   ├── data.json                 # Channels, videos, errors
│   └── cookies.txt               # YouTube cookies
├── downloads/                     # Video storage
├── static/                        # Web UI files
├── Dockerfile
├── docker-compose.yml
├── media_downloader              # Binary
├── *.go                          # Source files
└── ...other files
```

## Docker Compose Mounts

```yaml
volumes:
  - ./data:/app/data              # Config, cookies, channel data
  - ./downloads:/app/downloads    # Downloaded videos
```

Only **2 directories** need to be mounted (vs 4 individual files before)!

## File Paths

### Local Execution
- Working dir: `/home/dustin/media_downloader`
- config.json: `data/config.json`
- data.json: `data/data.json`
- cookies.txt: `data/cookies.txt`
- downloads: `downloads/`

### Docker Execution
- Working dir: `/app` (in container)
- config.json: `/app/data/config.json` (mounted from `./data`)
- data.json: `/app/data/data.json` (mounted from `./data`)
- cookies.txt: `/app/data/cookies.txt` (mounted from `./data`)
- downloads: `/app/downloads` (mounted from `./downloads`)

## Configuration

Inside `data/config.json`:
```json
{
  "download_dir": "../downloads",
  "cookies_file": "./cookies.txt"
}
```

This works because when running from `data/` as working directory:
- `../downloads` = parent directory
- `./cookies.txt` = same directory

## Usage

### Local Run
```bash
./media_downloader
# Reads from ./data/config.json
# Downloads to ./downloads
```

### Docker Run
```bash
docker-compose build
docker-compose up -d
# Reads from ./data/config.json (mounted to /app/data)
# Downloads to ./downloads (mounted to /app/downloads)
```

Both environments work identically!
