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

// hostBinarySiblingName is the name we look for next to the running
// clankd binary when no explicit BinaryPath is set and source isn't
// available (the typical `go install` / packaged release layout).
const hostBinarySiblingName = "clank-host"

// buildHostBinary returns a path to a linux/<arch> build of cmd/clank-host
// suitable for upload into a Daytona sandbox.
//
// Resolution order:
//  1. opts.BinaryPath (caller-provided, trusted verbatim).
//  2. A "clank-host" file sitting next to the current executable —
//     standard packaged-release layout.
//  3. Cross-compile from source via runtime.Caller anchoring (dev mode
//     when running `go run ./cmd/clankd` from a checkout).
//
// On a `go install`-built binary with no checkout and no sibling file,
// step 3 fails with a clear error pointing the user at BinaryPath.
//
// The cache key for the source-build path is the SHA-256 of the
// produced binary, computed after the build, so a source change → new
// binary → new cache entry.
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

	if sibling, ok := siblingHostBinary(); ok {
		return sibling, nil
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

// siblingHostBinary looks for a "clank-host" file in the same
// directory as the running executable. Returns (path, true) if found
// and statable; ("", false) otherwise. Used for packaged-release
// layouts where clankd ships beside clank-host.
//
// We don't verify GOOS/GOARCH here — the operator is responsible for
// pairing a linux/<arch> clank-host with their clankd. A wrong-arch
// binary will surface as an "exec format error" inside the sandbox,
// which the launcher already surfaces via fetchLogs.
func siblingHostBinary() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	candidate := filepath.Join(filepath.Dir(exe), hostBinarySiblingName)
	if _, err := os.Stat(candidate); err != nil {
		return "", false
	}
	return candidate, true
}

// resolveHostCmdDir returns an absolute path to cmd/clank-host within
// this repo. It uses runtime.Caller to anchor the search, which only
// works when running from a source checkout (e.g. `go run ./cmd/clankd`).
// Released binaries built via `go install` / `go build` have a build-
// machine path embedded in runtime.Caller that won't exist on the
// deployment host — for those callers, [siblingHostBinary] handles
// the lookup, and the error here points users at BinaryPath as a
// last-resort escape hatch.
func resolveHostCmdDir() (string, error) {
	// Walk up from this source file: internal/host/daytona/binary.go
	// → repo root. Then into cmd/clank-host.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("daytona: runtime.Caller failed; set BinaryPath or place a clank-host binary next to clankd")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	srcDir := filepath.Join(repoRoot, "cmd", "clank-host")
	if _, err := os.Stat(srcDir); err != nil {
		return "", fmt.Errorf(
			"daytona: cmd/clank-host source not found at %s (this is expected for released binaries); "+
				"set LaunchOptions.BinaryPath to a pre-built clank-host, or place a clank-host file next to your clankd executable: %w",
			srcDir, err,
		)
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
	if err := os.Rename(binPath, dst); err == nil {
		if err := os.Chmod(dst, 0o755); err != nil {
			return "", err
		}
		return dst, nil
	}
	// Cross-device rename: fall back to atomic copy-then-rename so a
	// crashed/concurrent launch can't leave a truncated cache file
	// that future runs would happily reuse.
	if err := atomicCopy(binPath, dst); err != nil {
		return "", err
	}
	_ = os.Remove(binPath)
	if err := os.Chmod(dst, 0o755); err != nil {
		return "", err
	}
	return dst, nil
}

// atomicCopy copies src into a temp file inside the same directory as
// dst, fsyncs it, and renames it into place. Same-directory rename is
// atomic on POSIX, so concurrent readers will only ever see a
// fully-written dst (or no dst at all). The temp file is removed on
// any error path.
func atomicCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup on any non-success path.
	defer func() {
		if _, err := os.Stat(tmpPath); err == nil {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}
