package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// sanitizeFilename normalizes a path component to a portable filename fragment.
func sanitizeFilename(name string) string {
	return normalizePortableComponent(name)
}

func normalizePortableComponent(name string) string {
	var b strings.Builder
	prevUnderscore := false

	for _, r := range name {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if isAlphaNum || r == '_' || r == '-' {
			b.WriteRune(r)
			prevUnderscore = false
			continue
		}

		if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}

	result := strings.Trim(b.String(), "_.- ")
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

func normalizePortableFilename(name string) string {
	stem, ext := splitPortableFilename(name)
	return normalizePortableComponent(stem) + ext
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

		newName := normalizePortableFilename(oldName)
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
