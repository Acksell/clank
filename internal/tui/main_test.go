package tui

import (
	"os"
	"testing"
)

// TestMain isolates the package's tests from the developer's real
// ~/.clank/preferences.json by pinning CLANK_DIR to a per-run tempdir.
// Without this, tests that depend on the default-backend value (e.g.
// TestCompose_BackendToggle) flake based on whatever the user happens
// to have configured locally.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "clank-tui-tests-*")
	if err != nil {
		panic("create CLANK_DIR tempdir: " + err.Error())
	}

	if err := os.Setenv("CLANK_DIR", dir); err != nil {
		panic("set CLANK_DIR: " + err.Error())
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
