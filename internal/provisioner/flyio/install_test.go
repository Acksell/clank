package flyio

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"sync"
	"syscall"
	"testing"
	"time"
)

// stubFS records the order of fs ops so tests can pin
// "remove-before-write" for atomic binary replacement.
type stubFS struct {
	mu    sync.Mutex
	files map[string][]byte
	calls []string // "Remove:/p", "Write:/p", in order
	// onWrite (when set) overrides the default "succeed and store"
	// behavior — used to simulate write failures.
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

// stubFile is a tiny fs.File over a []byte; just enough for fs.Stat.
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

// Stat satisfies fs.StatFS so fs.Stat hits stubFS directly.
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

// TestEnsureBinaryInstalled_SkipsOnSizeMatch is the fast path: when
// the sprite already has a binary of the right size we don't touch
// it — the upload is ~17MB, doing it on every daemon start would
// dominate provisioning time.
func TestEnsureBinaryInstalled_SkipsOnSizeMatch(t *testing.T) {
	t.Parallel()
	want := []byte("hello world binary contents")
	stub := newStubFS()
	stub.seed("usr/local/bin/clank-host", want)

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, "/usr/local/bin/clank-host", want); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := stub.ops(); len(got) != 0 {
		t.Errorf("unexpected fs ops on size-match fast path: %v", got)
	}
}

// TestEnsureBinaryInstalled_ColdInstallWritesOnly verifies that on a
// fresh sprite (no prior file), we proceed straight to write. Remove
// runs first as a no-op safety net (returns ENOENT and we move on).
func TestEnsureBinaryInstalled_ColdInstallWritesOnly(t *testing.T) {
	t.Parallel()
	stub := newStubFS()
	want := []byte("fresh binary")

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, "/usr/local/bin/clank-host", want); err != nil {
		t.Fatalf("install: %v", err)
	}
	got := stub.ops()
	if len(got) != 2 || got[0] != "Remove:/usr/local/bin/clank-host" || got[1] != "Write:/usr/local/bin/clank-host" {
		t.Errorf("ops = %v, want [Remove:/usr/…, Write:/usr/…]", got)
	}
}

// TestEnsureBinaryInstalled_RemoveBeforeWrite is the ETXTBSY
// regression: when a stale binary needs replacement, the Remove
// must precede the Write. Without that, Linux returns ETXTBSY
// because the sprite's clank-host service is currently exec'd from
// /usr/local/bin/clank-host.
func TestEnsureBinaryInstalled_RemoveBeforeWrite(t *testing.T) {
	t.Parallel()
	stub := newStubFS()
	stub.seed("usr/local/bin/clank-host", []byte("old binary, smaller"))
	want := []byte("new binary, larger payload")

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, "/usr/local/bin/clank-host", want); err != nil {
		t.Fatalf("install: %v", err)
	}
	got := stub.ops()
	if len(got) != 2 {
		t.Fatalf("expected 2 ops (Remove, Write), got %v", got)
	}
	if got[0] != "Remove:/usr/local/bin/clank-host" {
		t.Errorf("first op should be Remove (avoid ETXTBSY); got %q", got[0])
	}
	if got[1] != "Write:/usr/local/bin/clank-host" {
		t.Errorf("second op should be Write; got %q", got[1])
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

	calls := []string{}
	wfStub := stubWF{
		write: stub.WriteFileContext,
		remove: func(_ context.Context, name string) error {
			calls = append(calls, "Remove:"+name)
			return &fs.PathError{Op: "remove", Path: name, Err: syscall.EACCES}
		},
	}

	p := newTestProvisioner(t)
	if err := p.ensureBinaryInstalledOn(context.Background(), stub, wfStub, "/usr/local/bin/clank-host", want); err != nil {
		t.Fatalf("install: %v (Write should have proceeded despite Remove failure)", err)
	}
	if len(calls) != 1 || calls[0] != "Remove:/usr/local/bin/clank-host" {
		t.Errorf("Remove not called once: %v", calls)
	}
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
// write failure is surfaced — not silently swallowed.
func TestEnsureBinaryInstalled_WriteErrorPropagates(t *testing.T) {
	t.Parallel()
	stub := newStubFS()
	bang := errors.New("simulated write failure")
	stub.onWrite = func(_ string, _ []byte) error { return bang }

	p := newTestProvisioner(t)
	err := p.ensureBinaryInstalledOn(context.Background(), stub, stub, "/usr/local/bin/clank-host", []byte("data"))
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
// import list short.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
