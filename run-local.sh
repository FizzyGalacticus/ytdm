#!/bin/bash
set -e

echo "Setting up local environment..."

# Check if yt-dlp is installed
if ! command -v yt-dlp &> /dev/null; then
    echo "yt-dlp not found. Installing..."
    if command -v pip3 &> /dev/null; then
        pip3 install --user yt-dlp
    elif command -v pip &> /dev/null; then
        pip install --user yt-dlp
    else
        echo "ERROR: pip not found. Please install Python and pip first."
        exit 1
    fi
fi

# Check if ffmpeg is installed
if ! command -v ffmpeg &> /dev/null; then
    echo "WARNING: ffmpeg not found. Some video conversions may fail."
    echo "Install with: sudo apt install ffmpeg  (Ubuntu/Debian)"
    echo "           or: brew install ffmpeg     (macOS)"
fi

# Create necessary directories
mkdir -p ./downloads
mkdir -p ./data

echo ""
echo "Building application..."
go build -o ytdm

echo ""
echo "Starting Media Downloader..."
echo "Web interface: http://localhost:8080"
echo "Press Ctrl+C to stop"
echo ""

./ytdm
