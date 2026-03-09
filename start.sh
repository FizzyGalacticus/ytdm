#!/bin/bash
set -e

echo "Building ytdm..."
docker-compose build

echo ""
echo "Starting ytdm service..."
docker-compose up -d

echo ""
echo "Service is starting... waiting for health check..."
sleep 5

echo ""
echo "Checking service status..."
docker-compose ps

echo ""
echo "==================================="
echo "Media Downloader is now running!"
echo "Web interface: http://localhost:8080"
echo "API endpoint: http://localhost:8080/api"
echo "==================================="
echo ""
echo "To view logs: docker-compose logs -f"
echo "To stop: docker-compose down"
echo "To restart: docker-compose restart"
