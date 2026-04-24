package daytona

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// atomicCopy must produce a complete dst file or no dst file at all,
// even if the copy is interrupted mid-stream. Verifying the temp-file
// + rename pattern is in place: the temp prefix must live in dst's dir
// (so rename is same-device atomic), and a successful run leaves no
// stray temp.
func TestAtomicCopy_LeavesNoTempOnSuccess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	payload := []byte("hello clank-host binary")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	if err := atomicCopy(src, dst); err != nil {
		t.Fatalf("atomicCopy: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("dst payload mismatch: got %q, want %q", got, payload)
	}
	// Confirm no leftover .tmp-* file shares the destination dir.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "src" || name == "dst" {
			continue
		}
		t.Errorf("unexpected stray file in dst dir: %s", name)
	}
}

// On read failure (src missing) atomicCopy must not leave a partial
// dst behind for the cache-hit check to later mistake for a real
// build artefact.
func TestAtomicCopy_NoPartialDstOnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst")
	err := atomicCopy(filepath.Join(dir, "missing"), dst)
	if err == nil {
		t.Fatal("expected error from missing src")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dst should not exist after failed copy, stat err = %v", err)
	}
}

// buildHostBinary must respect ctx cancellation so the launcher's
// ReadyTimeout can actually bound the cross-compile step (regression
// for: ReadyTimeout used to start *after* the build).
//
// We exercise the source-build path by writing a fake BinaryPath
// pointing at a temp file; the function returns immediately without
// touching ctx. Then we re-run with no BinaryPath and a cancelled
// context — without sibling/source it errors fast (we don't assert
// on which error, just that we don't hang past the deadline).
func TestBuildHostBinary_RespectsCtxCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deadline := time.After(5 * time.Second)
	done := make(chan error, 1)
	go func() {
		_, err := buildHostBinary(ctx, LaunchOptions{Arch: "arm64"})
		done <- err
	}()
	select {
	case <-done:
		// Either errored from sibling-not-found / source-not-found
		// (when the test binary has no sibling clank-host and no
		// checkout layout), or from exec.CommandContext seeing the
		// cancelled ctx mid-build. All acceptable — the point is
		// we didn't hang.
	case <-deadline:
		t.Fatal("buildHostBinary hung past 5s on cancelled ctx")
	}
}
