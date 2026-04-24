package daytona

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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

// siblingHostBinary should not return a path when no sibling exists.
// The positive case is hard to test without manipulating os.Executable
// — covered by manual e2e via the launcher.
func TestSiblingHostBinary_AbsentReturnsFalse(t *testing.T) {
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	candidate := filepath.Join(filepath.Dir(exe), hostBinarySiblingName)
	if _, err := os.Stat(candidate); err == nil {
		t.Skipf("clank-host happens to exist next to test binary at %s; skipping negative case", candidate)
	}
	if path, ok := siblingHostBinary(); ok {
		t.Fatalf("expected (false), got (%q, true)", path)
	}
}
