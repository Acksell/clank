package daytona

import (
	"context"
	"fmt"

	sdk "github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	dtypes "github.com/daytonaio/daytona/libs/sdk-go/pkg/types"
)

// newSDKClient builds a Daytona SDK client from LaunchOptions. We
// always go through NewClientWithConfig (rather than NewClient) so the
// APIKey path is explicit and never silently picks up a stale
// DAYTONA_API_KEY from the daemon's environment when the caller
// intended to override it.
func newSDKClient(opts LaunchOptions) (*sdk.Client, error) {
	cfg := &dtypes.DaytonaConfig{APIKey: opts.APIKey}
	if opts.APIURL != "" {
		cfg.APIUrl = opts.APIURL
	}
	c, err := sdk.NewClientWithConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("daytona: NewClientWithConfig: %w", err)
	}
	return c, nil
}

// createSandbox provisions a fresh sandbox from opts.Snapshot (or the
// SDK's default if empty). Blocks until the sandbox reaches the
// started state — the SDK's default behavior; we keep it because the
// rest of Launch needs an addressable sandbox to upload into.
//
// Caller is responsible for Delete on failure paths after this point.
func createSandbox(ctx context.Context, c *sdk.Client, opts LaunchOptions) (*sdk.Sandbox, error) {
	base := dtypes.SandboxBaseParams{
		Labels: opts.Labels,
	}

	// SnapshotParams is the cheaper path (no image build). Use it
	// always; an empty Snapshot string lets Daytona pick its default.
	// If we later need a custom image (claude/opencode preinstalled),
	// switch to ImageParams behind an opts.Image field.
	params := dtypes.SnapshotParams{
		SandboxBaseParams: base,
		Snapshot:          opts.Snapshot,
	}

	sb, err := c.Create(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("daytona: Create sandbox: %w", err)
	}
	return sb, nil
}
