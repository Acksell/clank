package host

import (
	"path/filepath"
	"strings"
)

// repoLabelFromURL derives a short display label from a git remote URL.
// It returns the repo name (without .git suffix) extracted from the path
// component of the URL. fallback is returned when the URL cannot be parsed
// into a meaningful name.
//
// Examples:
//
//	"https://github.com/acme/api.git" → "api"
//	"git@github.com:acme/api.git"     → "api"
//	"https://github.com/acme/api"     → "api"
func repoLabelFromURL(remoteURL, fallback string) string {
	u := strings.TrimSuffix(remoteURL, ".git")

	// SCP-style: "git@github.com:owner/repo" — colon separates host from path.
	if idx := strings.Index(u, ":"); idx != -1 && !strings.Contains(u[:idx], "/") {
		u = u[idx+1:]
	} else {
		// HTTPS / SSH URL: take the path component.
		if idx := strings.Index(u, "//"); idx != -1 {
			u = u[idx+2:]
		}
		// Drop the host part (everything up to the first '/').
		if idx := strings.Index(u, "/"); idx != -1 {
			u = u[idx+1:]
		}
	}

	name := filepath.Base(u)
	if name == "" || name == "." || name == "/" {
		return fallback
	}
	return name
}
