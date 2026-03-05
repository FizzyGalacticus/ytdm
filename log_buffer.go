package main

import (
	"strings"
	"sync"
)

// LogBuffer stores recent log lines in memory.
type LogBuffer struct {
	mu         sync.RWMutex
	entries    []string
	maxEntries int
	partial    string
}

func NewLogBuffer(maxEntries int) *LogBuffer {
	if maxEntries <= 0 {
		maxEntries = 100
	}

	return &LogBuffer{
		entries:    make([]string, 0, maxEntries),
		maxEntries: maxEntries,
	}
}

// Write implements io.Writer so this can be used as a log output sink.
func (lb *LogBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	chunk := lb.partial + string(p)
	lines := strings.Split(chunk, "\n")

	// If chunk doesn't end with newline, keep the tail for next write.
	if !strings.HasSuffix(chunk, "\n") {
		lb.partial = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	} else {
		lb.partial = ""
	}

	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		lb.entries = append(lb.entries, line)
	}

	if len(lb.entries) > lb.maxEntries {
		lb.entries = lb.entries[len(lb.entries)-lb.maxEntries:]
	}

	return len(p), nil
}

func (lb *LogBuffer) GetEntries() []string {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	entries := make([]string, len(lb.entries))
	copy(entries, lb.entries)
	return entries
}
