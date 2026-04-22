package daemoncli

import (
	"context"
	"fmt"
	"os"

	hostclient "github.com/acksell/clank/internal/host/client"
	"github.com/acksell/clank/internal/host/daytona"
	"github.com/acksell/clank/internal/hub"
)

// daytonaLauncher adapts daytona.Launch to hub.HostLauncher. The
// adapter is intentionally thin: it captures LaunchOptions at
// registration time and forwards Launch calls verbatim. Putting the
// adapter here (not in internal/host/daytona) keeps the daytona
// package independent of hub — matching the layering intent in
// docs/daytona_plan.md.
type daytonaLauncher struct {
	opts daytona.LaunchOptions
}

// Launch satisfies hub.HostLauncher. *daytona.Handle already
// implements hub.RemoteHostHandle (single Stop(ctx) error method) so
// no extra wrapper is needed for the handle.
func (l *daytonaLauncher) Launch(ctx context.Context) (*hostclient.HTTP, hub.RemoteHostHandle, error) {
	client, handle, err := daytona.Launch(ctx, l.opts)
	if err != nil {
		return nil, nil, err
	}
	return client, handle, nil
}

// registerDaytonaLauncher registers the daytona launcher on the hub if
// DAYTONA_API_KEY is set in the environment. Absent the key, the
// daytona kind is simply unregistered — POST /hosts {kind:"daytona"}
// will then surface a clear "no launcher registered" error to the
// user, which is the correct UX (don't silently default).
//
// Returns an error only for unexpected misconfiguration; a missing
// API key is not an error here, just a no-op.
func registerDaytonaLauncher(d *hub.Service) error {
	key := os.Getenv("DAYTONA_API_KEY")
	if key == "" {
		return nil
	}
	opts := daytona.LaunchOptions{
		APIKey: key,
		APIURL: os.Getenv("DAYTONA_API_URL"),
	}
	if _, err := d.RegisterHostLauncher("daytona", &daytonaLauncher{opts: opts}); err != nil {
		return fmt.Errorf("register daytona launcher: %w", err)
	}
	return nil
}
