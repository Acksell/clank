package agent

// GitEndpoint is the parsed, structured form of a git remote URL. It
// answers "where does this repo live" without saying anything about
// "how do I authenticate to it" — that is GitCredential's job.
//
// Inspired by go-git's transport.Endpoint. Kept dependency-free so the
// agent package stays importable from anywhere; the parser that
// produces these values lives in internal/hub/endpoint.go and is the
// sole importer of go-git/v5.

import (
	"fmt"
	"strings"
)

// GitEndpointProtocol enumerates the wire protocols we recognise. A
// typed alias prevents typos at call sites and lets a switch be
// exhaustive-checked by tooling.
type GitEndpointProtocol string

const (
	GitProtoHTTPS GitEndpointProtocol = "https"
	GitProtoHTTP  GitEndpointProtocol = "http"
	GitProtoSSH   GitEndpointProtocol = "ssh"
	GitProtoGit   GitEndpointProtocol = "git"
	GitProtoFile  GitEndpointProtocol = "file"
)

// GitEndpoint is the canonical parsed identity of a git remote.
//
// Fields mirror go-git's transport.Endpoint so a future port to native
// go-git Clone is mechanical. Path never has a leading "/" and never
// the trailing ".git" — both are normalised by the parser so two URLs
// that point at the same repo produce equal endpoints.
type GitEndpoint struct {
	Protocol GitEndpointProtocol `json:"protocol"`
	User     string              `json:"user,omitempty"` // ssh: typically "git"
	Host     string              `json:"host"`           // empty only for file:// without authority
	Port     int                 `json:"port,omitempty"` // 0 = default for protocol
	Path     string              `json:"path"`           // "owner/repo" — no leading "/", no trailing ".git"
}

// Validate enforces the field invariants documented above. The parser
// is expected to produce only-valid endpoints; this method exists so
// any GitEndpoint that crosses the wire is checked on receipt.
func (e *GitEndpoint) Validate() error {
	if e == nil {
		return fmt.Errorf("nil endpoint")
	}
	switch e.Protocol {
	case GitProtoHTTPS, GitProtoHTTP, GitProtoSSH, GitProtoGit, GitProtoFile:
	default:
		return fmt.Errorf("unknown protocol %q", e.Protocol)
	}
	if e.Path == "" {
		return fmt.Errorf("endpoint path is empty")
	}
	if strings.HasPrefix(e.Path, "/") {
		return fmt.Errorf("endpoint path %q has leading slash", e.Path)
	}
	if strings.HasSuffix(e.Path, ".git") {
		return fmt.Errorf("endpoint path %q has trailing .git", e.Path)
	}
	if e.Protocol != GitProtoFile && e.Host == "" {
		return fmt.Errorf("non-file endpoint missing host")
	}
	if e.Port < 0 || e.Port > 65535 {
		return fmt.Errorf("port %d out of range", e.Port)
	}
	return nil
}

// String renders the endpoint back to a wire URL. SSH endpoints render
// in URL form (ssh://user@host[:port]/path.git), not scp form, because
// URL form is unambiguous to git and to humans reading logs.
//
// This is the inverse of the parser; round-tripping through Parse →
// String → Parse must produce an equal endpoint.
func (e *GitEndpoint) String() string {
	if e == nil {
		return ""
	}
	if e.Protocol == GitProtoFile {
		// file:// has no user/port; host may be empty (file:///path).
		if e.Host != "" {
			return "file://" + e.Host + "/" + e.Path + ".git"
		}
		return "file:///" + e.Path + ".git"
	}
	var b strings.Builder
	b.WriteString(string(e.Protocol))
	b.WriteString("://")
	if e.User != "" {
		b.WriteString(e.User)
		b.WriteByte('@')
	}
	b.WriteString(e.Host)
	if e.Port != 0 {
		fmt.Fprintf(&b, ":%d", e.Port)
	}
	b.WriteByte('/')
	b.WriteString(e.Path)
	b.WriteString(".git")
	return b.String()
}

// IsLocal reports whether the endpoint refers to a path on the local
// filesystem. Used by the credential resolver to decide whether
// ssh-agent auth is admissible.
func (e *GitEndpoint) IsLocal() bool {
	return e != nil && e.Protocol == GitProtoFile
}

// CloneURL returns a wire URL suitable for `git clone`. Differs from
// String() only for file:// endpoints, where the trailing ".git"
// String() appends would refer to a directory that doesn't exist on
// disk under the original local path. For all network protocols this
// is identical to String() — github/gitlab/etc. accept both forms.
func (e *GitEndpoint) CloneURL() string {
	if e == nil {
		return ""
	}
	if e.Protocol == GitProtoFile {
		if e.Host != "" {
			return "file://" + e.Host + "/" + e.Path
		}
		return "file:///" + e.Path
	}
	return e.String()
}
