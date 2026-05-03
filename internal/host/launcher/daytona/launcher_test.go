package daytona

import (
	"testing"

	hostlauncher "github.com/acksell/clank/internal/hub"
)

// _ asserts at compile time that *Launcher satisfies the
// hub.HostLauncher interface. The launcher is a thin shim; any
// drift in the hub-side contract should fail the build here rather
// than at hub registration time.
var _ hostlauncher.HostLauncher = (*Launcher)(nil)

// TestNew_AcceptsNilLogger pins the convenience that a nil logger
// upgrades to log.Default(), matching the rest of the launcher
// surface. Behavior tests for resolve/wake/refresh now live in
// internal/provisioner/daytona/provisioner_test.go.
func TestNew_AcceptsNilLogger(t *testing.T) {
	t.Parallel()
	l := New(nil, Options{}, nil)
	if l == nil {
		t.Fatal("New returned nil")
	}
	if l.log == nil {
		t.Error("nil logger should default to log.Default(), got nil")
	}
}
