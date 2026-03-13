# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Copy Go source files and module files
COPY *.go go.mod ./
COPY static ./static

# Build static binary with embedded files
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-extldflags "-static" -s -w' -o ytdm .

# Runtime stage - use minimal alpine
FROM alpine:3.19

# Install only what's needed: python3, pip, ffmpeg, wget for healthcheck, node for yt-dlp JS extraction
RUN apk add --no-cache python3 py3-pip ffmpeg wget nodejs && \
    rm -rf /root/.cache

# Install yt-dlp binary
RUN wget -q -O /usr/local/bin/yt-dlp \
      https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp && \
    chmod +x /usr/local/bin/yt-dlp

# Create /app directory
RUN mkdir -p /app

WORKDIR /app

# Copy binary from build stage
COPY --from=builder /build/ytdm .

# Expose API port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/api/status || exit 1

# Run the application
CMD ["./ytdm"]
