package daytona

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLaunch_Smoke is a real end-to-end test against Daytona. It is
// skipped unless DAYTONA_API_KEY is set, because it costs real money
// and takes ~30s wall time. Run with:
//
//	DAYTONA_API_KEY=... go test -run TestLaunch_Smoke -v -timeout 5m \
//	    ./internal/host/daytona
//
// Verifies the full happy path: create sandbox → upload binary →
// start clank-host → preview-URL roundtrip → /status 200 → Stop
// (which must actually delete the sandbox; check the dashboard
// afterwards if you suspect a leak).
func TestLaunch_Smoke(t *testing.T) {
	t.Parallel()
	apiKey := os.Getenv("DAYTONA_API_KEY")
	if apiKey == "" {
		t.Skip("DAYTONA_API_KEY not set; skipping live Daytona smoke test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	client, handle, err := Launch(ctx, LaunchOptions{
		APIKey: apiKey,
		// Allow a generous budget — first run cross-compiles
		// clank-host (~5–10s on a warm Go cache, longer cold) and
		// uploads ~20MB to Daytona.
		ReadyTimeout: 3 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Logf("sandbox=%s commandID=%s", handle.SandboxID, handle.CommandID)

	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if err := handle.Stop(stopCtx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	// /status was already proven by waitForStatus inside Launch, but
	// re-call it here to verify the returned client is wired
	// correctly (preview headers, baseURL) for callers.
	st, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	t.Logf("status: %+v", st)
}
