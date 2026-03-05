# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Copy Go source files and module files
COPY *.go go.mod ./
COPY static ./static

# Build static binary with embedded files
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-extldflags "-static" -s -w' -o media_downloader .

# Runtime stage - use minimal alpine
FROM alpine:3.19

# Install only what's needed: python3, pip, ffmpeg, wget for healthcheck, node for yt-dlp JS extraction
RUN apk add --no-cache python3 py3-pip ffmpeg wget nodejs && \
    rm -rf /root/.cache

# Create non-root user first
RUN addgroup -g 1000 downloader && \
    adduser -D -u 1000 -G downloader downloader

# Install yt-dlp binary as the downloader user to allow self-updates
USER downloader
RUN mkdir -p /home/downloader/.local/bin && \
    wget -q -O /home/downloader/.local/bin/yt-dlp \
      https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp && \
    chmod +x /home/downloader/.local/bin/yt-dlp

# Switch back to root for directory setup
USER root

# Create /app directory
RUN mkdir -p /app && \
    chown -R downloader:downloader /app

WORKDIR /app

# Copy binary from build stage
COPY --from=builder /build/media_downloader .

# Switch to non-root user
USER downloader

# Add user's pip bin to PATH for yt-dlp, and ensure deno is accessible
ENV PATH="/home/downloader/.local/bin:/usr/bin:${PATH}"

# Expose API port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/status || exit 1

# Run the application
CMD ["./media_downloader"]
