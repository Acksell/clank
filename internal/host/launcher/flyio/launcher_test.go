package flyio

import (
	"testing"

	hostlauncher "github.com/acksell/clank/internal/hub"
)

// _ asserts at compile time that *Launcher satisfies the
// hub.HostLauncher interface. The launcher is a thin shim; any drift
// in the hub-side contract should fail the build here rather than
// at hub registration time. Behavior tests live in
// internal/provisioner/flyio/provisioner_test.go.
var _ hostlauncher.HostLauncher = (*Launcher)(nil)

// TestNew_AcceptsNilLogger pins the convenience that a nil logger
// upgrades to log.Default(), matching the daytona shim's surface.
func TestNew_AcceptsNilLogger(t *testing.T) {
	t.Parallel()
	l := New(nil, nil)
	if l == nil {
		t.Fatal("New returned nil")
	}
	if l.log == nil {
		t.Error("nil logger should default to log.Default(), got nil")
	}
}
