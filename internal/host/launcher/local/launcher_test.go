package local

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// buildClankHost compiles cmd/clank-host into a t.TempDir() and returns
// the absolute path to the binary. Built once per test process via the
// caller's t.Cleanup wiring (caching across tests is overkill for the
// MVP pace — this rebuild is a few hundred ms).
func buildClankHost(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "clank-host")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	// Find the repo root: this file lives at internal/host/launcher/local/.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/clank-host")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build clank-host: %v\n%s", err, out)
	}
	return bin
}

func TestLauncher_LaunchesAndStops(t *testing.T) {
	t.Parallel()
	bin := buildClankHost(t)
	launcher := New(Options{BinPath: bin}, nil)
	t.Cleanup(launcher.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name, client, err := launcher.Launch(ctx, agent.LaunchHostSpec{Provider: "local-stub"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if name == "" {
		t.Errorf("empty hostname")
	}

	// The host responds on /status — proves the spawned process is up
	// and the URL the launcher returned actually points to it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err := client.Status(ctx)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := client.Status(ctx); err != nil {
		t.Fatalf("status: %v", err)
	}
}

// TestLauncher_LaunchIsIdempotent pins the persistent-host contract:
// repeated Launch calls within a daemon lifetime return the same
// hostname + URL, not a fresh subprocess each time. This is what
// turns the launcher from a per-session "spawn-and-track" toy into
// the persistent-host abstraction the rest of the codebase now
// expects.
func TestLauncher_LaunchIsIdempotent(t *testing.T) {
	t.Parallel()
	bin := buildClankHost(t)
	launcher := New(Options{BinPath: bin}, nil)
	t.Cleanup(launcher.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const n = 3
	var firstName, lastName string
	for i := 0; i < n; i++ {
		name, client, err := launcher.Launch(ctx, agent.LaunchHostSpec{Provider: "local-stub"})
		if err != nil {
			t.Fatalf("Launch[%d]: %v", i, err)
		}
		// Don't close the client between calls — the launcher caches it.
		_ = client
		if i == 0 {
			firstName = string(name)
		}
		lastName = string(name)
	}
	if firstName != lastName {
		t.Errorf("Launch should return stable hostname across calls; got %q then %q", firstName, lastName)
	}
}

// TestLauncher_StopClearsChild verifies Stop releases the cached
// child so a subsequent Launch spawns a fresh subprocess. After
// Stop, the in-memory cache must be empty so the next Launch is
// not a probe-on-dead-process.
func TestLauncher_StopClearsChild(t *testing.T) {
	t.Parallel()
	bin := buildClankHost(t)
	launcher := New(Options{BinPath: bin}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := launcher.Launch(ctx, agent.LaunchHostSpec{Provider: "local-stub"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	launcher.Stop()
	launcher.mu.Lock()
	cleared := launcher.current == nil
	launcher.mu.Unlock()
	if !cleared {
		t.Error("Stop should clear current; the cached child remains")
	}
}
