package daytona

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// buildHostBinary returns a path to a linux/arm64 build of cmd/clank-host
// suitable for upload into a Daytona sandbox.
//
// If opts.BinaryPath is set, it's returned as-is (the caller has
// pre-built or pre-staged the binary; we trust the path). Otherwise we
// cross-compile from source into a content-addressed cache directory
// (~/.cache/clank/clank-host-linux-arm64-<sha>) so repeat launches
// across `clankd` invocations are instant.
//
// The cache key is the SHA-256 of the produced binary, computed after
// the build, so a source change → new binary → new cache entry. We do
// not attempt to predict the SHA from sources (would require hashing
// the entire module graph); the cost of one redundant build per source
// change is acceptable for a launcher used at session start.
func buildHostBinary(opts LaunchOptions) (string, error) {
	if opts.BinaryPath != "" {
		// Trust caller-provided paths verbatim. Fail fast if the file
		// is missing — silently ignoring the option would mask a
		// CI/dev-env misconfiguration.
		if _, err := os.Stat(opts.BinaryPath); err != nil {
			return "", fmt.Errorf("daytona: BinaryPath %q: %w", opts.BinaryPath, err)
		}
		return opts.BinaryPath, nil
	}

	out, err := os.MkdirTemp("", "clank-host-build-*")
	if err != nil {
		return "", fmt.Errorf("daytona: build tmpdir: %w", err)
	}
	binPath := filepath.Join(out, "clank-host")

	// Resolve the source dir relative to this file. Walking up from
	// the daytona package to the repo root and into cmd/clank-host
	// keeps the cross-compile self-contained — no $CWD assumption.
	srcDir, err := resolveHostCmdDir()
	if err != nil {
		return "", err
	}

	cmd := exec.Command("go", "build", "-o", binPath, srcDir)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+opts.Arch, "CGO_ENABLED=0")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("daytona: cross-compile clank-host (linux/%s): %w", opts.Arch, err)
	}

	sha, err := fileSHA256(binPath)
	if err != nil {
		return "", err
	}

	cached, err := promoteToCache(binPath, sha, opts.Arch)
	if err != nil {
		// Cache is best-effort; if we can't promote, fall back to the
		// tmp build. The launcher will still work; subsequent runs
		// will rebuild.
		return binPath, nil
	}
	return cached, nil
}

// resolveHostCmdDir returns an absolute path to cmd/clank-host within
// this repo. It uses runtime.Caller to anchor the search, so the
// package can be invoked from any CWD.
func resolveHostCmdDir() (string, error) {
	// Walk up from this source file: internal/host/daytona/binary.go
	// → repo root. Then into cmd/clank-host.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("daytona: runtime.Caller failed; cannot locate cmd/clank-host source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	srcDir := filepath.Join(repoRoot, "cmd", "clank-host")
	if _, err := os.Stat(srcDir); err != nil {
		return "", fmt.Errorf("daytona: cmd/clank-host not found at %s: %w", srcDir, err)
	}
	return srcDir, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("daytona: open for sha: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("daytona: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// promoteToCache moves binPath into the user cache dir keyed by sha
// and returns the cached path. Idempotent: if the cached file already
// exists, the source is removed and the cached path returned unchanged.
func promoteToCache(binPath, sha, arch string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheDir, "clank")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "clank-host-linux-"+arch+"-"+sha[:12])
	if _, err := os.Stat(dst); err == nil {
		// Cache hit. Remove the redundant tmp build to keep /tmp tidy.
		_ = os.Remove(binPath)
		return dst, nil
	}
	if err := os.Rename(binPath, dst); err != nil {
		// Cross-device rename: fall back to copy.
		if err := copyFile(binPath, dst); err != nil {
			return "", err
		}
		_ = os.Remove(binPath)
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return "", err
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
