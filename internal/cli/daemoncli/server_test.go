package daemoncli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	hub "github.com/acksell/clank/internal/hub"
	hubclient "github.com/acksell/clank/internal/hub/client"
)

// TestRunHubServer_ManagesSocketAndPIDFiles verifies the lifecycle
// concerns lifted out of hub.Service in Phase 2F: runHubServer must
// create hub.sock and hub.pid on startup and remove both on Stop.
//
// This is the new home for the on-disk lifecycle coverage previously
// provided by hub_test's TestDaemonPIDFile (deleted because hub.Service
// no longer touches the filesystem).
func TestRunHubServer_ManagesSocketAndPIDFiles(t *testing.T) {
	// Redirect config.Dir() to a short HOME so we don't touch the real
	// ~/.clank during tests AND keep the resulting hub.sock path under
	// macOS's 104-char Unix socket limit. Cannot t.Parallel: HOME is
	// process-global.
	home, err := os.MkdirTemp("/tmp", "clank-srv-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)

	sockPath, err := hubclient.SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	pidPath, err := hubclient.PIDPath()
	if err != nil {
		t.Fatalf("PIDPath: %v", err)
	}
	if filepath.Base(sockPath) != "hub.sock" {
		t.Errorf("expected socket name hub.sock, got %s", filepath.Base(sockPath))
	}
	if filepath.Base(pidPath) != "hub.pid" {
		t.Errorf("expected pid name hub.pid, got %s", filepath.Base(pidPath))
	}

	// runHubServer expects the parent directory to exist (matches
	// daemoncli.go which MkdirAll's it before calling).
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	s := hub.New()
	errCh := make(chan error, 1)
	go func() { errCh <- runHubServer(s) }()

	// Wait for both files to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, sErr := os.Stat(sockPath)
		_, pErr := os.Stat(pidPath)
		if sErr == nil && pErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket %s not created: %v", sockPath, err)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("pid file %s not created: %v", pidPath, err)
	}

	// Sanity: the live hub answers a simple wire request.
	client := hubclient.NewClient(sockPath)
	pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		t.Fatalf("Ping over socket: %v", err)
	}

	// Trigger graceful shutdown via the same path SIGINT/SIGTERM uses.
	s.Stop()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runHubServer returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runHubServer did not return after Stop")
	}

	// Both artifacts must be cleaned up.
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket %s should be removed after stop, stat err=%v", sockPath, err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pid file %s should be removed after stop, stat err=%v", pidPath, err)
	}
}
