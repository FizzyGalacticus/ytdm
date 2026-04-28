package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// filenameDatePatternNew matches YYYY-MM-DD at the start of a filename (current format).
var filenameDatePatternNew = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(?:\D|$)`)

// filenameDatePatternLegacy matches YYYYMMDD at the start of a filename (legacy format).
var filenameDatePatternLegacy = regexp.MustCompile(`^(\d{8})(?:\D|$)`)

type StartupPruneResult struct {
	VideosPruned int
	FilesRemoved int
	FilesMoved   int
	Warnings     []string
}

// RunStartupChannelPruneScan performs a one-time channel file cleanup based on
// dates parsed from filenames before background listeners start.
func RunStartupChannelPruneScan(config *Config, storage *Storage) StartupPruneResult {
	return runStartupChannelPruneScanAt(time.Now(), config, storage)
}

func runStartupChannelPruneScanAt(now time.Time, config *Config, storage *Storage) StartupPruneResult {
	result := StartupPruneResult{}

	config.RLock()
	disablePruning := config.DisablePruning
	downloadDir := config.DownloadDir
	defaultRetention := config.RetentionDays
	config.RUnlock()

	if disablePruning {
		return result
	}

	channels := storage.GetChannels()
	for _, channel := range channels {
		if channel.DisablePruning {
			continue
		}

		retentionDays := EffectiveRetentionDays(channel.RetentionDays, defaultRetention)
		if retentionDays <= 0 && channel.CutoffDate.IsZero() {
			continue
		}

		channelDirs := existingChannelDirs(downloadDir, channel.Name)
		if len(channelDirs) == 0 {
			continue
		}

		entriesByDir := make(map[string][]os.DirEntry, len(channelDirs))
		for _, channelDir := range channelDirs {
			entries, err := os.ReadDir(channelDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				result.Warnings = append(result.Warnings, fmt.Sprintf("failed to read channel dir %s: %v", channelDir, err))
				continue
			}
			entriesByDir[channelDir] = entries
		}
		if len(entriesByDir) == 0 {
			continue
		}

		cutoffTime := RetentionCutoff(now, retentionDays)

		for _, tracked := range channel.DownloadedVideos {
			if tracked.ID == "" {
				continue
			}

			matches := findChannelFilesAcrossDirs(entriesByDir, tracked.ID)
			if len(matches) == 0 {
				continue
			}

			if tracked.DisablePruning {
				movedCount, moveErr := moveMatchedFilesToSanitizedChannelDir(downloadDir, channel.Name, tracked.ID, matches)
				if moveErr != nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("failed moving no-prune files for video %s in %s: %v", tracked.ID, channel.Name, moveErr))
				}
				result.FilesMoved += movedCount
				continue
			}

			videoDate, ok := inferTrackedVideoDate(tracked, matches)
			if !ok {
				continue
			}

			shouldPruneByRetention := retentionDays > 0 && NormalizeToUTC(videoDate).Before(cutoffTime)
			shouldPruneByCutoff := ShouldPruneByChannelCutoff(videoDate, channel.CutoffDate)
			if !shouldPruneByRetention && !shouldPruneByCutoff {
				continue
			}

			removedCount, removeErr := removeMatchedVideoFiles(matches)
			if removeErr != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("failed removing files for video %s in %s: %v", tracked.ID, channel.Name, removeErr))
				continue
			}

			result.VideosPruned++
			result.FilesRemoved += removedCount

			if err := storage.RemoveDownloadedVideo(channel.ID, tracked.ID); err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("failed updating storage for %s/%s: %v", channel.ID, tracked.ID, err))
			}
		}

		for _, channelDir := range channelDirs {
			removeIfEmpty(channelDir)
		}
	}

	if result.VideosPruned > 0 || result.FilesRemoved > 0 {
		log.Printf("Startup prune scan removed %d video(s), %d file(s)", result.VideosPruned, result.FilesRemoved)
	}
	if result.FilesMoved > 0 {
		log.Printf("Startup prune scan moved %d no-prune file(s) to sanitized locations", result.FilesMoved)
	}

	return result
}

func existingChannelDirs(downloadDir, channelName string) []string {
	candidates := []string{
		filepath.Join(downloadDir, sanitizeFilename(channelName)),
		filepath.Join(downloadDir, legacySanitizeFilename(channelName)),
		filepath.Join(downloadDir, channelName),
	}

	seen := map[string]struct{}{}
	result := make([]string, 0, len(candidates))
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}

		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		result = append(result, dir)
	}

	return result
}

func legacySanitizeFilename(name string) string {
	invalid := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|", "\x00"}
	result := name
	for _, char := range invalid {
		result = strings.ReplaceAll(result, char, "_")
	}

	for i := 0; i < 32; i++ {
		result = strings.ReplaceAll(result, string(rune(i)), "")
	}
	result = strings.ReplaceAll(result, string(rune(127)), "")

	result = strings.Trim(result, " .")
	if result == "" {
		return "unnamed"
	}

	return result
}

func findChannelFilesForVideoID(entries []os.DirEntry, channelDir, videoID string) []string {
	matches := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !hasFileContainingID([]string{name}, videoID) {
			continue
		}
		matches = append(matches, filepath.Join(channelDir, name))
	}

	return matches
}

func findChannelFilesAcrossDirs(entriesByDir map[string][]os.DirEntry, videoID string) []string {
	matches := make([]string, 0)
	for dir, entries := range entriesByDir {
		dirMatches := findChannelFilesForVideoID(entries, dir, videoID)
		matches = append(matches, dirMatches...)
	}

	return matches
}

func inferTrackedVideoDate(tracked DownloadedVideo, matchedPaths []string) (time.Time, bool) {
	var earliest time.Time
	for _, p := range matchedPaths {
		d, ok := parseDateFromFilename(filepath.Base(p))
		if !ok {
			continue
		}
		if earliest.IsZero() || d.Before(earliest) {
			earliest = d
		}
	}

	if !earliest.IsZero() {
		return earliest, true
	}

	if !tracked.PublishDate.IsZero() {
		return NormalizeToUTC(tracked.PublishDate), true
	}

	if !tracked.DownloadDate.IsZero() {
		return NormalizeToUTC(tracked.DownloadDate), true
	}

	return time.Time{}, false
}

func parseDateFromFilename(name string) (time.Time, bool) {
	// Try YYYY-MM-DD at the start of the filename (current format).
	if match := filenameDatePatternNew.FindStringSubmatch(name); len(match) >= 2 {
		if t, err := time.Parse("2006-01-02", match[1]); err == nil {
			return NormalizeToUTC(t), true
		}
	}

	// Fall back to YYYYMMDD at the start of the filename (legacy format).
	if match := filenameDatePatternLegacy.FindStringSubmatch(name); len(match) >= 2 {
		if t, err := ParseYouTubeUploadDateUTC(match[1]); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

func removeMatchedVideoFiles(paths []string) (int, error) {
	removed := 0
	var firstErr error

	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}

	if firstErr != nil {
		return removed, firstErr
	}

	return removed, nil
}

func moveMatchedFilesToSanitizedChannelDir(downloadDir, channelName, videoID string, paths []string) (int, error) {
	targetDir := filepath.Join(downloadDir, sanitizeFilename(channelName))
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return 0, err
	}

	moved := 0
	var firstErr error

	for _, src := range paths {
		base := filepath.Base(src)
		normalizedBase := normalizeVideoFilename(base, videoID)
		dst := filepath.Join(targetDir, normalizedBase)

		if src == dst {
			continue
		}

		if _, statErr := os.Stat(dst); statErr == nil {
			stem, ext := splitPortableFilename(normalizedBase)
			for idx := 1; ; idx++ {
				candidate := fmt.Sprintf("%s_%d%s", stem, idx, ext)
				candidatePath := filepath.Join(targetDir, candidate)
				if _, err := os.Stat(candidatePath); os.IsNotExist(err) {
					dst = candidatePath
					break
				}
			}
		}

		if err := os.Rename(src, dst); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		moved++
	}

	if firstErr != nil {
		return moved, firstErr
	}

	return moved, nil
}

func removeIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		return
	}
	_ = os.Remove(dir)
}
