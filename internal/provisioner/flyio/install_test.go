package flyio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// sha256Hex returns the hex sha256 of b. Helper for the install
// tests to produce expected sha values matching the runtime ones.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// stubFS records the order of fs ops so tests can pin
// "remove-before-write" for atomic binary replacement.
type stubFS struct {
	mu    sync.Mutex
	files map[string][]byte
	calls []string // "Remove:/p", "Write:/p:N", in order
	// onWrite (when set) overrides the default "succeed and store"
	// behavior — used to simulate ETXTBSY on the first attempt.
	onWrite func(name string, data []byte) error
}

func newStubFS() *stubFS {
	return &stubFS{files: map[string][]byte{}}
}

func (s *stubFS) Open(name string) (fs.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &stubFile{name: name, r: bytes.NewReader(data), size: int64(len(data))}, nil
}

// stubFile is a tiny fs.File over a []byte. Only fs.Stat goes
// through Open()→Stat() in our tests (when stubFS.Stat isn't called
// directly), so this is just enough to make fs.Stat happy.
type stubFile struct {
	name string
	size int64
	r    *bytes.Reader
}

func (f *stubFile) Stat() (fs.FileInfo, error) {
	return stubFileInfo{name: f.name, size: f.size}, nil
}
func (f *stubFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *stubFile) Close() error               { return nil }

var _ io.Reader = (*stubFile)(nil)

// Stat satisfies fs.StatFS so fs.Stat can find files.
func (s *stubFS) Stat(name string) (fs.FileInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return stubFileInfo{name: name, size: int64(len(data))}, nil
}

func (s *stubFS) WriteFileContext(_ context.Context, name string, data []byte, _ fs.FileMode) error {
	s.mu.Lock()
	if s.onWrite != nil {
		fn := s.onWrite
		s.mu.Unlock()
		return fn(name, data)
	}
	s.calls = append(s.calls, "Write:"+name)
	s.files[stripLeadingSlash(name)] = append([]byte(nil), data...)
	s.mu.Unlock()
	return nil
}

func (s *stubFS) ReadFileContext(_ context.Context, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "Read:"+name)
	data, ok := s.files[stripLeadingSlash(name)]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), data...), nil
}

func (s *stubFS) RemoveContext(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "Remove:"+name)
	if _, ok := s.files[stripLeadingSlash(name)]; !ok {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}
	}
	delete(s.files, stripLeadingSlash(name))
	return nil
}

func (s *stubFS) seed(name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[stripLeadingSlash(name)] = append([]byte(nil), data...)
}

func (s *stubFS) ops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

func stripLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

type stubFileInfo struct {
	name string
	size int64
}

func (s stubFileInfo) Name() string       { return s.name }
func (s stubFileInfo) Size() int64        { return s.size }
func (s stubFileInfo) Mode() fs.FileMode  { return 0o755 }
func (s stubFileInfo) ModTime() time.Time { return time.Time{} }
func (s stubFileInfo) IsDir() bool        { return false }
func (s stubFileInfo) Sys() any           { return nil }

func newTestProvisioner(t *testing.T) *Provisioner {
	t.Helper()
	return &Provisioner{log: log.New(testWriter{t: t}, "", 0)}
}

type testWriter struct{ t *testing.T }

func (tw testWriter) Write(p []byte) (int, error) {
	tw.t.Log(string(p))
	return len(p), nil
}

// TestEnsureBinaryInstalled_SkipsOnSHAMatch is the fast path: when
// the sprite already has a binary AND a sha sidecar matching what
// we want to install, we don't touch it. The upload is ~17MB; doing
// it on every daemon start would dominate provisioning time.
func TestEnsureBinaryInstalled_SkipsOnSHAMatch(t *testing.T) {
	t.Parallel()
	want := []byte("hello world binary contents")
	wantSHA := sha256Hex(want)
	stub := newStubFS()
	stub.seed("usr/local/bin/clank-host", want)
	stub.seed("usr/local/bin/clank-host.sha256", []byte(wantSHA+"\n"))

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, stub, "/usr/local/bin/clank-host", want, wantSHA); err != nil {
		t.Fatalf("install: %v", err)
	}
	got := stub.ops()
	for _, op := range got {
		if strings.HasPrefix(op, "Write:") || strings.HasPrefix(op, "Remove:") {
			t.Errorf("unexpected mutating op on sha-match fast path: %v", got)
			break
		}
	}
}

// TestEnsureBinaryInstalled_DriftDetectedBySHA pins the sha-based
// check: when the sidecar's sha differs from the embedded binary's
// sha, force a reinstall — even if the file sizes happen to match.
// Production has hit silent stale-binary cases where size collided
// across rebuilds; sha eliminates that class.
func TestEnsureBinaryInstalled_DriftDetectedBySHA(t *testing.T) {
	t.Parallel()
	stale := []byte("OLD binary, exact length matching the new")
	want := []byte("NEW binary, exact length matching the old")
	if len(stale) != len(want) {
		t.Fatal("test fixtures must have equal length to exercise the sha-vs-size distinction")
	}
	stub := newStubFS()
	stub.seed("usr/local/bin/clank-host", stale)
	stub.seed("usr/local/bin/clank-host.sha256", []byte(sha256Hex(stale)+"\n"))

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, stub, "/usr/local/bin/clank-host", want, sha256Hex(want)); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Binary and sidecar should both be replaced with the new content.
	if got := stub.files["usr/local/bin/clank-host"]; string(got) != string(want) {
		t.Errorf("binary not replaced: have %q, want %q", got, want)
	}
	if got := strings.TrimSpace(string(stub.files["usr/local/bin/clank-host.sha256"])); got != sha256Hex(want) {
		t.Errorf("sha sidecar not refreshed: have %q, want %q", got, sha256Hex(want))
	}
}

// TestEnsureBinaryInstalled_ColdInstallWritesBinaryAndSHA verifies
// that on a fresh sprite (no prior file, no sidecar) we install
// both — binary first, then the sha sidecar so the next call can
// short-circuit.
func TestEnsureBinaryInstalled_ColdInstallWritesBinaryAndSHA(t *testing.T) {
	t.Parallel()
	stub := newStubFS()
	want := []byte("fresh binary")
	wantSHA := sha256Hex(want)

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, stub, "/usr/local/bin/clank-host", want, wantSHA); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got, ok := stub.files["usr/local/bin/clank-host"]; !ok || string(got) != string(want) {
		t.Errorf("binary not written: ok=%v got=%q", ok, got)
	}
	if got, ok := stub.files["usr/local/bin/clank-host.sha256"]; !ok || strings.TrimSpace(string(got)) != wantSHA {
		t.Errorf("sha sidecar not written: ok=%v got=%q", ok, got)
	}
}

// TestEnsureBinaryInstalled_RemoveBeforeWrite is the ETXTBSY
// regression: when a stale binary needs replacement, the Remove
// must precede the Write. Without that, Linux returns ETXTBSY
// because the sprite's clank-host service is currently exec'd from
// /usr/local/bin/clank-host. The production symptom was an
// infinite reinstall storm.
func TestEnsureBinaryInstalled_RemoveBeforeWrite(t *testing.T) {
	t.Parallel()
	stub := newStubFS()
	stub.seed("usr/local/bin/clank-host", []byte("old binary"))
	stub.seed("usr/local/bin/clank-host.sha256", []byte("aaaa\n"))
	want := []byte("new binary")
	wantSHA := sha256Hex(want)

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, stub, "/usr/local/bin/clank-host", want, wantSHA); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Find Remove and Write of the binary — Read of the sha sidecar
	// runs first, then Remove and Write of the binary follow.
	got := stub.ops()
	var removeIdx, writeIdx = -1, -1
	for i, op := range got {
		if op == "Remove:/usr/local/bin/clank-host" {
			removeIdx = i
		}
		if op == "Write:/usr/local/bin/clank-host" {
			writeIdx = i
		}
	}
	if removeIdx < 0 || writeIdx < 0 {
		t.Fatalf("expected Remove and Write of binary; got %v", got)
	}
	if removeIdx > writeIdx {
		t.Errorf("Remove must precede Write (avoid ETXTBSY); ops = %v", got)
	}
}

// TestEnsureBinaryInstalled_RemoveErrorIsBestEffort confirms a
// non-ENOENT failure on the unlink path is logged and tolerated —
// the Write still runs because in practice WriteFile may still
// succeed (e.g. if the underlying syscall changed since stat).
func TestEnsureBinaryInstalled_RemoveErrorIsBestEffort(t *testing.T) {
	t.Parallel()
	stub := newStubFS()
	stub.seed("usr/local/bin/clank-host", []byte("old"))
	want := []byte("new binary")

	// Override Remove to always return EACCES — not ENOENT, so we
	// hit the "log and continue" branch.
	originalRemove := stub.RemoveContext
	stub.calls = nil
	calls := []string{}
	wfStub := stubWF{
		write: stub.WriteFileContext,
		remove: func(_ context.Context, name string) error {
			calls = append(calls, "Remove:"+name)
			return &fs.PathError{Op: "remove", Path: name, Err: syscall.EACCES}
		},
	}
	_ = originalRemove

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, wfStub, "/usr/local/bin/clank-host", want, sha256Hex(want)); err != nil {
		t.Fatalf("install: %v (Write should have proceeded despite Remove failure)", err)
	}
	if len(calls) != 1 || calls[0] != "Remove:/usr/local/bin/clank-host" {
		t.Errorf("Remove not called once: %v", calls)
	}
	// stub.calls should now contain the Write that succeeded after
	// the failed Remove.
	got := stub.ops()
	hasWrite := false
	for _, op := range got {
		if op == "Write:/usr/local/bin/clank-host" {
			hasWrite = true
			break
		}
	}
	if !hasWrite {
		t.Errorf("Write should have run after Remove failure; ops = %v", got)
	}
}

// TestEnsureBinaryInstalled_WriteErrorPropagates pins that an actual
// write failure is surfaced — it's not silently swallowed.
func TestEnsureBinaryInstalled_WriteErrorPropagates(t *testing.T) {
	t.Parallel()
	stub := newStubFS()
	bang := errors.New("simulated write failure")
	stub.onWrite = func(_ string, _ []byte) error { return bang }

	p := newTestProvisioner(t)
	want := []byte("data")
	err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, stub, "/usr/local/bin/clank-host", want, sha256Hex(want))
	if err == nil {
		t.Fatal("expected write error to surface, got nil")
	}
	if !errors.Is(err, bang) && !contains(err.Error(), bang.Error()) {
		t.Errorf("error should wrap or contain %q, got %v", bang, err)
	}
}

// stubWF lets a test override remove independently of the read path.
type stubWF struct {
	write  func(ctx context.Context, name string, data []byte, perm fs.FileMode) error
	remove func(ctx context.Context, name string) error
}

func (s stubWF) WriteFileContext(ctx context.Context, name string, data []byte, perm fs.FileMode) error {
	return s.write(ctx, name, data, perm)
}
func (s stubWF) RemoveContext(ctx context.Context, name string) error { return s.remove(ctx, name) }

// contains is a tiny strings.Contains wrapper to keep the test file
// import list short — strings is already pulled in elsewhere.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

