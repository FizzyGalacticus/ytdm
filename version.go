package main

import "strings"

// gitCommit is injected at build time via -ldflags "-X main.gitCommit=<sha>".
// Defaults to "dev" for local builds.
var gitCommit = "dev"

func getAppCommit() string {
	commit := strings.TrimSpace(gitCommit)
	if commit == "" {
		return "unknown"
	}
	return commit
}

func getShortAppCommit() string {
	commit := getAppCommit()
	if commit == "unknown" || commit == "dev" {
		return commit
	}
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
