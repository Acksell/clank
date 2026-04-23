package gitcred

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/config"
)

// settingsFileName is the basename inside ~/.clank where user-saved
// credentials live. Separate from preferences.json so an accidental
// `cat preferences.json` in a screenshare doesn't expose tokens.
const settingsFileName = "credentials.json"

// settingsFileMode is intentionally 0600 — owner read/write only. The
// file holds plaintext tokens; widening the mode would let any
// process running as the same user, plus anyone with shell access via
// `sudo -u`, exfiltrate them. We do NOT use the OS keychain in v1.
const settingsFileMode os.FileMode = 0o600

// settingsFile is the on-disk schema. Map key is the git endpoint
// host (lowercased). Forward-compat: unknown JSON keys are tolerated
// by Go's default unmarshal so an older clank reading a newer file
// just ignores extra credential fields.
type settingsFile struct {
	// Credentials maps endpoint host → stored credential. The wire
	// shape reuses [agent.GitCredential] so there's exactly one
	// schema for credentials in the codebase.
	Credentials map[string]agent.GitCredential `json:"credentials,omitempty"`
}

// SettingsDiscoverer reads user-saved credentials from
// ~/.clank/credentials.json (mode 0600). Writes happen via [SaveToken]
// from the TUI.
type SettingsDiscoverer struct {
	// pathOverride lets tests point at a tempdir without setting HOME
	// (which the parallel test runner doesn't love).
	pathOverride string
}

// FromSettings returns the production [SettingsDiscoverer] backed by
// ~/.clank/credentials.json.
func FromSettings() *SettingsDiscoverer { return &SettingsDiscoverer{} }

// Discover implements [Discoverer]. Returns [ErrNoCredential] when:
//
//   - The file doesn't exist (first run).
//   - The file exists but has no entry for ep.Host.
//
// A malformed or unreadable file is a HARD error — silently treating
// a parse failure as "no credential" would mask user mistakes (typo
// in JSON edited by hand) and force them to debug a 401 they can't
// explain.
func (s *SettingsDiscoverer) Discover(_ context.Context, ep *agent.GitEndpoint) (agent.GitCredential, error) {
	path, err := s.path()
	if err != nil {
		return agent.GitCredential{}, err
	}
	file, err := loadSettings(path)
	if os.IsNotExist(err) {
		// First-run: no settings file yet. Soft miss.
		return agent.GitCredential{}, ErrNoCredential
	}
	if err != nil {
		return agent.GitCredential{}, err
	}
	cred, ok := file.Credentials[ep.Host]
	if !ok {
		return agent.GitCredential{}, ErrNoCredential
	}
	return cred, nil
}

// SaveToken stores a token for host as HTTPS-basic in the settings
// file, creating the file (mode 0600) if necessary. Atomic via
// write-tmp + rename so a crash mid-write never corrupts existing
// entries. Empty token deletes the entry.
//
// Lives on [SettingsDiscoverer] (rather than a free function) so
// tests can write through the same pathOverride they read from.
func (s *SettingsDiscoverer) SaveToken(host, token string) error {
	if host == "" {
		return fmt.Errorf("gitcred: SaveToken: empty host")
	}
	path, err := s.path()
	if err != nil {
		return err
	}
	file, err := loadSettings(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if file.Credentials == nil {
		file.Credentials = map[string]agent.GitCredential{}
	}
	if token == "" {
		delete(file.Credentials, host)
	} else {
		cred := tokenAsBasic(token)
		if vErr := cred.Validate(); vErr != nil {
			return fmt.Errorf("gitcred: SaveToken: %w", vErr)
		}
		file.Credentials[host] = cred
	}
	return writeSettings(path, file)
}

// path resolves the on-disk settings file location, honouring
// pathOverride for tests.
func (s *SettingsDiscoverer) path() (string, error) {
	if s.pathOverride != "" {
		return s.pathOverride, nil
	}
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, settingsFileName), nil
}

func loadSettings(path string) (settingsFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Caller distinguishes via os.IsNotExist; surface verbatim
		// so SaveToken can branch on it without losing fidelity.
		return settingsFile{}, err
	}
	if err != nil {
		return settingsFile{}, fmt.Errorf("gitcred: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return settingsFile{}, nil
	}
	var f settingsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return settingsFile{}, fmt.Errorf("gitcred: parse %s: %w", path, err)
	}
	return f, nil
}

func writeSettings(path string, f settingsFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gitcred: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("gitcred: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, settingsFileMode); err != nil {
		return fmt.Errorf("gitcred: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("gitcred: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
