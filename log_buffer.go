package main

import (
	"regexp"
	"strings"
	"sync"
)

var scopePattern = regexp.MustCompile(`\[scope:(channel|video):([^:\]]+):([^\]]*)\]\s*`)

type LogEntry struct {
	Line      string `json:"line"`
	ScopeType string `json:"scope_type,omitempty"`
	ScopeID   string `json:"scope_id,omitempty"`
	ScopeName string `json:"scope_name,omitempty"`
}

type LogScope struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LogBuffer stores recent log lines in memory.
type LogBuffer struct {
	mu         sync.RWMutex
	entries    []LogEntry
	maxEntries int
	partial    string
}

func NewLogBuffer(maxEntries int) *LogBuffer {
	if maxEntries <= 0 {
		maxEntries = 100
	}

	return &LogBuffer{
		entries:    make([]LogEntry, 0, maxEntries),
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

		entry := LogEntry{Line: line}
		if matches := scopePattern.FindStringSubmatch(line); len(matches) == 4 {
			entry.ScopeType = strings.TrimSpace(matches[1])
			entry.ScopeID = strings.TrimSpace(matches[2])
			entry.ScopeName = strings.TrimSpace(matches[3])
			entry.Line = strings.TrimSpace(scopePattern.ReplaceAllString(line, ""))
		}

		lb.entries = append(lb.entries, entry)
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
	for i, entry := range lb.entries {
		entries[i] = entry.Line
	}
	return entries
}

func (lb *LogBuffer) GetStructuredEntries(scopeType, scopeID string) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	normalizedType := strings.TrimSpace(strings.ToLower(scopeType))
	normalizedID := strings.TrimSpace(scopeID)
	filtered := make([]LogEntry, 0, len(lb.entries))

	for _, entry := range lb.entries {
		if normalizedType != "" && strings.ToLower(entry.ScopeType) != normalizedType {
			continue
		}
		if normalizedID != "" && entry.ScopeID != normalizedID {
			continue
		}
		filtered = append(filtered, entry)
	}

	return filtered
}

func (lb *LogBuffer) GetScopes() []LogScope {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	seen := map[string]struct{}{}
	scopes := make([]LogScope, 0)

	for _, entry := range lb.entries {
		if entry.ScopeType == "" || entry.ScopeID == "" {
			continue
		}
		key := entry.ScopeType + ":" + entry.ScopeID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		scopes = append(scopes, LogScope{
			Type: entry.ScopeType,
			ID:   entry.ScopeID,
			Name: entry.ScopeName,
		})
	}

	return scopes
}
