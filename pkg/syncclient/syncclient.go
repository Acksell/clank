// Package syncclient is the laptop side of clank-sync. It generates a
// git bundle of the local repo's full history and POSTs it to a
// clank-sync endpoint over HTTP.
//
// MVP shape: explicit `clank sync push <path>` invocations. A
// fsnotify-driven background watcher with debounce + outbox is the
// next iteration; the API here stays the same so the watcher just
// drives Client.PushBundle.
package syncclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config configures a Client.
type Config struct {
	// BaseURL is the clank-sync endpoint, e.g. "https://sync.example.com".
	BaseURL string

	// AuthToken is sent as `Authorization: Bearer <token>` on every
	// upload. Required for non-permissive deployments.
	AuthToken string

	// DeviceID identifies this laptop in worktree ownership records.
	// Required for the new checkpoint flow (RegisterWorktree,
	// PushCheckpoint); legacy PushBundle ignores it. P2 will move this
	// into JWT claims.
	DeviceID string

	// HTTPClient overrides the default http.Client. Optional.
	HTTPClient *http.Client
}

// Client uploads bundles to a clank-sync server.
type Client struct {
	cfg    Config
	client *http.Client
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("syncclient: BaseURL is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Client{cfg: cfg, client: cfg.HTTPClient}, nil
}

// PushBundle creates an "all refs" git bundle of the repo at repoPath
// and POSTs it to clank-sync as <repoSlug>. repoPath must be a checkout
// (or bare repo) usable by `git -C <path>`.
//
// Repos are usually larger than fits in memory comfortably; the bundle
// streams from a temp file rather than buffering in RAM.
func (c *Client) PushBundle(ctx context.Context, repoPath, repoSlug string) error {
	if !validRepoSlug(repoSlug) {
		return fmt.Errorf("syncclient: invalid repo slug %q", repoSlug)
	}

	bundlePath, err := makeBundle(ctx, repoPath)
	if err != nil {
		return fmt.Errorf("create bundle: %w", err)
	}
	defer os.Remove(bundlePath)

	f, err := os.Open(bundlePath)
	if err != nil {
		return fmt.Errorf("open bundle: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat bundle: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/bundles?repo=" + repoSlug
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, f)
	if err != nil {
		return err
	}
	req.ContentLength = stat.Size()
	req.Header.Set("Content-Type", "application/x-git-bundle")
	if c.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.AuthToken)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("post bundle: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("clank-sync returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func makeBundle(ctx context.Context, repoPath string) (string, error) {
	tmp, err := os.CreateTemp("", "clank-bundle-*.bundle")
	if err != nil {
		return "", err
	}
	tmp.Close()
	bundlePath := tmp.Name()

	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "bundle", "create", bundlePath, "--all")
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(bundlePath)
		return "", fmt.Errorf("git bundle create: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return bundlePath, nil
}

func validRepoSlug(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") {
		return false
	}
	return true
}

// DefaultRepoSlug derives a slug from a repo path: the basename, with
// ".git" stripped. Intended for the common laptop CLI case where the
// user doesn't pass an explicit slug.
func DefaultRepoSlug(repoPath string) string {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		abs = repoPath
	}
	return strings.TrimSuffix(filepath.Base(abs), ".git")
}
