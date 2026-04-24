package gitendpoint

import (
	"net/url"
	"strings"
)

// RedactURL returns a log-safe rendering of a git remote URL by
// stripping any embedded userinfo (https://user:tok@host/...) and
// query/fragment components. It is intended for error messages and
// logs that surface a URL the user mistyped or the parser rejected.
//
// On total parse failure (scp-style "git@host:path", malformed input)
// the function falls back to a coarse "<scheme://host>" or "<redacted>"
// shape: the goal is to never let a credential leak into a log line,
// even when the input doesn't look like a normal URL. Callers should
// prefer the parsed [agent.GitEndpoint.String] when they have one.
func RedactURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// scp-style "git@host:owner/repo" — net/url won't parse it usefully
	// (it sees an opaque URL with empty scheme). Detect and re-render
	// without any "user@" part since scp-style userinfo is a username,
	// not a secret, but we redact it anyway to keep the contract
	// "no credentials in logs ever" simple to reason about.
	if !strings.Contains(raw, "://") {
		if at := strings.Index(raw, "@"); at >= 0 {
			return "<redacted>@" + raw[at+1:]
		}
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Last resort: keep only the scheme prefix so the user can
		// still tell why it was rejected without leaking the secret.
		if i := strings.Index(raw, "://"); i >= 0 {
			return raw[:i] + "://<redacted>"
		}
		return "<redacted>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
