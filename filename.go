package main

import (
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// nonAlphanumericRe matches any character that is not an ASCII letter or digit.
var nonAlphanumericRe = regexp.MustCompile(`[^A-Za-z0-9]`)

// sanitizeDirRe matches runs of characters not safe for a portable directory component.
// Alphanumeric characters, underscores, and hyphens are preserved.
var sanitizeDirRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// filenameDatePrefixRe matches a YYYY-MM-DD or YYYYMMDD date at the start of a filename stem.
var filenameDatePrefixRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}|\d{8})`)

// episodeNamePrefixRe matches the "S<YYYY>E<MMDDhhmmss> " prefix of episode-style names
// (see episodeVideoFilenameStem), capturing the season and episode numbers.
var episodeNamePrefixRe = regexp.MustCompile(`^S(\d{4})E(\d{10}) `)

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

// normalizeVideoFilename rewrites a video filename to the legacy canonical form:
//
//	YYYY-MM-DD <cleanTitle>-<videoID><ext>
//
// Non-alphanumeric characters are stripped from the title portion. Any
// YYYYMMDD date prefix is normalized to YYYY-MM-DD. If videoID is empty,
// no suffix is appended and the whole stem (after removing the date) is cleaned.
//
// Episode-style names are returned unchanged: this also runs on existing files (e.g.
// when startup cleanup relocates no-prune videos), and renaming a file Jellyfin has
// already indexed would make it reappear as a new item.
func normalizeVideoFilename(name, videoID string) string {
	stem, ext := splitPortableFilename(name)
	if episodeNamePrefixRe.MatchString(stem) {
		return name
	}

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

// episodeNumbers returns the season and episode numbers a video is filed under in a
// TV-style media library: the season is the upload year and the episode is the upload
// time as MMDDhhmmss (UTC). Media servers identify date-named files by air date alone,
// so several uploads on one day would collapse into a single episode; the time of day
// keeps them apart. Without an upload time, the hhmmss digits come from a hash of the
// video ID so same-day uploads still get distinct numbers. ok is false when the upload
// date is unknown.
func episodeNumbers(metadata *VideoMetadata) (season, episode int, ok bool) {
	uploaded := metadata.Timestamp.UTC()
	if metadata.Timestamp.IsZero() {
		date, err := time.Parse("2006-01-02", metadata.UploadDate)
		if err != nil {
			return 0, 0, false
		}
		h := fnv.New32a()
		h.Write([]byte(metadata.ID))
		hashed := int(h.Sum32() % 1000000)
		return date.Year(), int(date.Month())*100000000 + date.Day()*1000000 + hashed, true
	}

	return uploaded.Year(), int(uploaded.Month())*100000000 + uploaded.Day()*1000000 + uploaded.Hour()*10000 + uploaded.Minute()*100 + uploaded.Second(), true
}

// episodeVideoFilenameStem returns the filename stem for a newly downloaded video:
//
//	S<YYYY>E<MMDDhhmmss> <cleanTitle>-<videoID>
//
// e.g. "S2026E1011044228 ChessTournamentSNL-IuhG712NOak". Jellyfin and Kodi parse the
// SxxEyy prefix as the episode identity, so every upload is its own episode.
func episodeVideoFilenameStem(metadata *VideoMetadata) (string, bool) {
	season, episode, ok := episodeNumbers(metadata)
	if !ok || metadata.ID == "" {
		return "", false
	}

	cleanTitle := nonAlphanumericRe.ReplaceAllString(metadata.Title, "")
	if cleanTitle == "" {
		cleanTitle = "Video"
	}
	return fmt.Sprintf("S%04dE%010d %s-%s", season, episode, cleanTitle, metadata.ID), true
}

// parseEpisodeFilename returns the season and episode numbers encoded in an
// episode-style filename, or ok=false for any other name (e.g. legacy date-named files).
func parseEpisodeFilename(name string) (season, episode string, ok bool) {
	stem, _ := splitPortableFilename(name)
	m := episodeNamePrefixRe.FindStringSubmatch(stem)
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimLeft(m[2], "0"), true
}

// listFileNames returns the names of the regular files in dir (nil if it doesn't exist).
func listFileNames(dir string) map[string]bool {
	names := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			names[entry.Name()] = true
		}
	}
	return names
}

// renameNewDownloadFiles gives the files a download just created for videoID (the
// video, .info.json, thumbnail, ...) the episode-style stem, keeping each extension.
// Files that existed before the download (existing) are never renamed: renaming a file
// a media server already indexed would make it reappear as a new item.
func renameNewDownloadFiles(channelDir, videoID, stem string, existing map[string]bool) error {
	var firstErr error
	for name := range listFileNames(channelDir) {
		if existing[name] || !strings.Contains(name, videoID) {
			continue
		}

		_, ext := splitPortableFilename(name)
		newName := stem + ext
		if newName == name {
			continue
		}
		dst := filepath.Join(channelDir, newName)
		if _, err := os.Stat(dst); err == nil {
			for idx := 1; ; idx++ {
				candidate := fmt.Sprintf("%s_%d%s", stem, idx, ext)
				if _, err := os.Stat(filepath.Join(channelDir, candidate)); os.IsNotExist(err) {
					newName = candidate
					dst = filepath.Join(channelDir, candidate)
					break
				}
			}
		}

		if err := os.Rename(filepath.Join(channelDir, name), dst); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		log.Printf("Named downloaded file: %s -> %s", name, newName)
	}
	return firstErr
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
