package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// nonAlphanumericRe matches any character that is not an ASCII letter or digit.
var nonAlphanumericRe = regexp.MustCompile(`[^A-Za-z0-9]`)

// sanitizeDirRe matches runs of characters not safe for a portable directory component.
// Alphanumeric characters, underscores, and hyphens are preserved.
var sanitizeDirRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// filenameDatePrefixRe matches a YYYY-MM-DD or YYYYMMDD date at the start of a filename stem.
var filenameDatePrefixRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}|\d{8})`)

// sanitizeFilename normalizes a channel name to a portable directory component.
// Runs of non-safe characters are replaced with a single underscore.
func sanitizeFilename(name string) string {
	result := strings.Trim(sanitizeDirRe.ReplaceAllString(name, "_"), "_- ")
	if result == "" {
		return "unnamed"
	}
	return result
}

func splitPortableFilename(name string) (stem, ext string) {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".info.json") {
		suffixLen := len(".info.json")
		return name[:len(name)-suffixLen], name[len(name)-suffixLen:]
	}

	ext = filepath.Ext(name)
	if ext == "" {
		return name, ""
	}

	return strings.TrimSuffix(name, ext), ext
}

// normalizeVideoFilename rewrites a video filename to the canonical form:
//
//	YYYY-MM-DD <cleanTitle>-<videoID><ext>
//
// Non-alphanumeric characters are stripped from the title portion. Any
// YYYYMMDD date prefix is normalized to YYYY-MM-DD. If videoID is empty,
// no suffix is appended and the whole stem (after removing the date) is cleaned.
func normalizeVideoFilename(name, videoID string) string {
	stem, ext := splitPortableFilename(name)

	dateStr := ""
	rest := stem
	if m := filenameDatePrefixRe.FindString(stem); m != "" {
		if len(m) == 8 {
			// YYYYMMDD → YYYY-MM-DD
			dateStr = m[:4] + "-" + m[4:6] + "-" + m[6:]
		} else {
			dateStr = m
		}
		rest = strings.TrimLeft(strings.TrimPrefix(stem, m), " _-")
	}

	title := rest
	if videoID != "" {
		if strings.HasSuffix(title, "-"+videoID) {
			title = title[:len(title)-len("-"+videoID)]
		} else if idx := strings.LastIndex(title, videoID); idx >= 0 && idx+len(videoID) == len(title) {
			title = strings.TrimRight(title[:idx], " _-")
		}
	}

	cleanTitle := nonAlphanumericRe.ReplaceAllString(title, "")

	var b strings.Builder
	if dateStr != "" {
		b.WriteString(dateStr)
		b.WriteByte(' ')
	}
	b.WriteString(cleanTitle)
	if videoID != "" {
		b.WriteByte('-')
		b.WriteString(videoID)
	}
	b.WriteString(ext)
	return b.String()
}

func (d *Downloader) normalizeDownloadedFilenames(channelDir, videoID string) error {
	if strings.TrimSpace(videoID) == "" {
		return nil
	}

	entries, err := os.ReadDir(channelDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var firstErr error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		oldName := entry.Name()
		if !strings.Contains(oldName, videoID) {
			continue
		}

		newName := normalizeVideoFilename(oldName, videoID)
		if newName == oldName {
			continue
		}

		src := filepath.Join(channelDir, oldName)
		dst := filepath.Join(channelDir, newName)

		if _, statErr := os.Stat(dst); statErr == nil {
			base, ext := splitPortableFilename(newName)
			for idx := 1; ; idx++ {
				candidate := fmt.Sprintf("%s_%d%s", base, idx, ext)
				candidatePath := filepath.Join(channelDir, candidate)
				if _, err := os.Stat(candidatePath); os.IsNotExist(err) {
					newName = candidate
					dst = candidatePath
					break
				}
			}
		}

		if renameErr := os.Rename(src, dst); renameErr != nil {
			if firstErr == nil {
				firstErr = renameErr
			}
			continue
		}

		log.Printf("Normalized filename: %s -> %s", oldName, newName)
	}

	return firstErr
}
