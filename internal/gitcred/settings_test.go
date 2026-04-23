package gitcred

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestSettings(t *testing.T) *SettingsDiscoverer {
	t.Helper()
	dir := t.TempDir()
	return &SettingsDiscoverer{pathOverride: filepath.Join(dir, "credentials.json")}
}

func TestSettingsDiscoverer_FirstRunIsSoftMiss(t *testing.T) {
	t.Parallel()
	s := newTestSettings(t)
	_, err := s.Discover(context.Background(), validEp(t, "github.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential on first run", err)
	}
}

func TestSettingsDiscoverer_RoundTripToken(t *testing.T) {
	t.Parallel()
	s := newTestSettings(t)
	if err := s.SaveToken("github.com", "ghp_token"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	cred, err := s.Discover(context.Background(), validEp(t, "github.com"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cred.Password != "ghp_token" {
		t.Fatalf("password = %q, want ghp_token", cred.Password)
	}
}

func TestSettingsDiscoverer_FileMode0600(t *testing.T) {
	t.Parallel()
	s := newTestSettings(t)
	if err := s.SaveToken("github.com", "tok"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	info, err := os.Stat(s.pathOverride)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Mask off non-perm bits for cross-platform safety.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 0600 (token file must be owner-only)", perm)
	}
}

func TestSettingsDiscoverer_DeleteWithEmptyToken(t *testing.T) {
	t.Parallel()
	s := newTestSettings(t)
	if err := s.SaveToken("github.com", "tok"); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := s.SaveToken("github.com", ""); err != nil {
		t.Fatalf("SaveToken empty: %v", err)
	}
	_, err := s.Discover(context.Background(), validEp(t, "github.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential after delete", err)
	}
}

func TestSettingsDiscoverer_MalformedFileIsHardError(t *testing.T) {
	t.Parallel()
	// Hand-edited typo must surface, not silently degrade to anonymous.
	s := newTestSettings(t)
	if err := os.MkdirAll(filepath.Dir(s.pathOverride), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(s.pathOverride, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := s.Discover(context.Background(), validEp(t, "github.com"))
	if err == nil || errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want hard parse error", err)
	}
}

func TestSettingsDiscoverer_MultipleHostsIsolated(t *testing.T) {
	t.Parallel()
	s := newTestSettings(t)
	if err := s.SaveToken("github.com", "gh-tok"); err != nil {
		t.Fatalf("save github: %v", err)
	}
	if err := s.SaveToken("gitlab.com", "gl-tok"); err != nil {
		t.Fatalf("save gitlab: %v", err)
	}
	gh, err := s.Discover(context.Background(), validEp(t, "github.com"))
	if err != nil {
		t.Fatalf("discover github: %v", err)
	}
	gl, err := s.Discover(context.Background(), validEp(t, "gitlab.com"))
	if err != nil {
		t.Fatalf("discover gitlab: %v", err)
	}
	if gh.Password == gl.Password {
		t.Fatalf("hosts share password (%q) — entries crossed", gh.Password)
	}
}
