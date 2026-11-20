package main

import (
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// mediaServerPath maps a file under ytdm's download directory to the absolute path the
// media server sees it at. Jellyfin only accepts an absolute path or URL for an NFO
// <thumb>, and checks that the file exists on its own filesystem, so when the two run
// in different containers the download directory is usually mounted somewhere else
// (ytdm's /app/downloads is Jellyfin's /youtube, say). mediaDir is that mount point;
// when it's empty, ytdm's own absolute path is used, which suits setups where both see
// the same path.
func mediaServerPath(downloadDir, mediaDir, file string) string {
	absFile, err := filepath.Abs(file)
	if err != nil {
		absFile = file
	}
	mediaDir = strings.TrimSpace(mediaDir)
	if mediaDir == "" {
		return absFile
	}
	absDownloads, err := filepath.Abs(downloadDir)
	if err != nil {
		return absFile
	}
	rel, err := filepath.Rel(absDownloads, absFile)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return absFile
	}
	// The media server may run on another OS, so join with the separator its
	// directory uses.
	if strings.Contains(mediaDir, `\`) && !strings.Contains(mediaDir, "/") {
		return strings.TrimRight(mediaDir, `\`) + `\` + strings.ReplaceAll(filepath.ToSlash(rel), "/", `\`)
	}
	return path.Join(filepath.ToSlash(mediaDir), filepath.ToSlash(rel))
}

// nfoLocalThumbRe matches the episode-level <thumb> ytdm writes for the local image: a
// top-level element (two-space indent) with no attributes. The poster URL carries an
// aspect attribute and the channel icon sits deeper, inside <actor>.
var nfoLocalThumbRe = regexp.MustCompile(`(?m)^  <thumb>([^<]*)</thumb>\n`)

// RepairNFOThumbPaths rewrites the local-image <thumb> of every ytdm NFO under the
// download directory to the path the media server expects, so NFOs written before the
// path was configured (or with a bare filename, which Jellyfin rejects) get fixed. A
// thumb whose image no longer exists is dropped. It returns how many files changed.
func RepairNFOThumbPaths(cfg *Config) int {
	cfg.RLock()
	downloadDir, mediaDir := cfg.DownloadDir, cfg.MediaServerDownloadDir
	cfg.RUnlock()

	fixed := 0
	err := filepath.WalkDir(downloadDir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() && p != downloadDir {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(p), ".nfo") {
			return nil
		}
		changed, err := repairNFOThumb(p, downloadDir, mediaDir)
		if err != nil {
			log.Printf("Warning: could not repair thumbnail path in %s: %v", p, err)
		} else if changed {
			fixed++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: NFO thumbnail repair stopped early: %v", err)
	}
	return fixed
}

func repairNFOThumb(nfoPath, downloadDir, mediaDir string) (bool, error) {
	data, err := os.ReadFile(nfoPath)
	if err != nil {
		return false, err
	}
	content := string(data)
	loc := nfoLocalThumbRe.FindStringSubmatchIndex(content)
	if loc == nil || !strings.Contains(content, "<episodedetails>") {
		return false, nil
	}
	current := unescapeXML(content[loc[2]:loc[3]])
	if strings.HasPrefix(current, "http://") || strings.HasPrefix(current, "https://") {
		return false, nil
	}

	thumb := localThumbForNFO(nfoPath, current)
	replacement := ""
	if thumb != "" {
		replacement = "  <thumb>" + escapeXML(mediaServerPath(downloadDir, mediaDir, thumb)) + "</thumb>\n"
	}
	if content[loc[0]:loc[1]] == replacement {
		return false, nil
	}
	updated := content[:loc[0]] + replacement + content[loc[1]:]

	tmp := nfoPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, nfoPath); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// localThumbForNFO finds the image an NFO's local <thumb> should point at: the image
// sharing the NFO's name, or else the file the thumb already names if it is still in
// the same folder.
func localThumbForNFO(nfoPath, current string) string {
	dir := filepath.Dir(nfoPath)
	stem := strings.TrimSuffix(filepath.Base(nfoPath), filepath.Ext(nfoPath))
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp"} {
		candidate := filepath.Join(dir, stem+ext)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// The thumb may be a path from another mount, so only its filename is trusted.
	name := path.Base(strings.ReplaceAll(current, `\`, "/"))
	if name != "" && name != "." && name != "/" {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func unescapeXML(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&#39;", "'", "&amp;", "&").Replace(s)
}

// isWindowsAbsPath reports a Windows-style absolute path such as D:\Media\YouTube,
// for a media server running on Windows while ytdm runs on Linux.
func isWindowsAbsPath(p string) bool {
	return len(p) >= 3 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
