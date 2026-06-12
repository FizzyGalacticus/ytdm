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

// LogBuffer stores recent log lines in memory with a separate ring buffer per scope.
// Unscoped (global) entries share one ring buffer; each channel/video gets its own.
type LogBuffer struct {
	mu         sync.RWMutex
	global     []LogEntry
	scoped     map[string][]LogEntry
	maxEntries int
	partial    string
}

func NewLogBuffer(maxEntries int) *LogBuffer {
	if maxEntries <= 0 {
		maxEntries = 100
	}
	return &LogBuffer{
		global:     make([]LogEntry, 0, maxEntries),
		scoped:     make(map[string][]LogEntry),
		maxEntries: maxEntries,
	}
}

func (lb *LogBuffer) SetMaxEntries(n int) {
	if n <= 0 {
		n = 100
	}
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.maxEntries = n
	lb.global = trimToMax(lb.global, n)
	for key, bucket := range lb.scoped {
		lb.scoped[key] = trimToMax(bucket, n)
	}
}

func trimToMax(entries []LogEntry, max int) []LogEntry {
	if len(entries) > max {
		return entries[len(entries)-max:]
	}
	return entries
}

// Write implements io.Writer so this can be used as a log output sink.
func (lb *LogBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	chunk := lb.partial + string(p)
	lines := strings.Split(chunk, "\n")

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

			key := entry.ScopeType + ":" + entry.ScopeID
			bucket := append(lb.scoped[key], entry)
			if len(bucket) > lb.maxEntries {
				bucket = bucket[len(bucket)-lb.maxEntries:]
			}
			lb.scoped[key] = bucket
		} else {
			lb.global = append(lb.global, entry)
			if len(lb.global) > lb.maxEntries {
				lb.global = lb.global[len(lb.global)-lb.maxEntries:]
			}
		}
	}

	return len(p), nil
}

func (lb *LogBuffer) getStructuredEntries(scopeType, scopeID string) []LogEntry {
	normalizedType := strings.TrimSpace(strings.ToLower(scopeType))
	normalizedID := strings.TrimSpace(scopeID)

	if normalizedType != "" && normalizedID != "" {
		key := normalizedType + ":" + normalizedID
		bucket := lb.scoped[key]
		result := make([]LogEntry, len(bucket))
		copy(result, bucket)
		return result
	}

	var result []LogEntry
	if normalizedType == "" {
		result = append(result, lb.global...)
	}
	for key, bucket := range lb.scoped {
		if normalizedType != "" && !strings.HasPrefix(key, normalizedType+":") {
			continue
		}
		result = append(result, bucket...)
	}
	return result
}

func (lb *LogBuffer) GetEntries() []string {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	all := lb.getStructuredEntries("", "")
	entries := make([]string, len(all))
	for i, e := range all {
		entries[i] = e.Line
	}
	return entries
}

func (lb *LogBuffer) GetStructuredEntries(scopeType, scopeID string) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.getStructuredEntries(scopeType, scopeID)
}

func (lb *LogBuffer) GetScopes() []LogScope {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	scopes := make([]LogScope, 0, len(lb.scoped))
	for _, bucket := range lb.scoped {
		if len(bucket) == 0 {
			continue
		}
		last := bucket[len(bucket)-1]
		scopes = append(scopes, LogScope{
			Type: last.ScopeType,
			ID:   last.ScopeID,
			Name: last.ScopeName,
		})
	}
	return scopes
}
