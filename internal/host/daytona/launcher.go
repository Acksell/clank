package daytona

import (
	"context"
	"errors"
	"fmt"
	"time"

	hostclient "github.com/acksell/clank/internal/host/client"
	sdk "github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
)

// Launch provisions a Daytona sandbox, uploads + starts clank-host
// inside it, and returns a fully wired hostclient pointed at the
// preview URL together with a Handle for shutdown.
//
// On any error after sandbox creation, Launch best-effort deletes the
// sandbox before returning, so callers don't leak Daytona spend on
// partial failures. If that cleanup itself fails, both errors are
// joined with errors.Join — the launch error first.
//
// The whole sequence is bounded by opts.ReadyTimeout. Daytona's own
// create+start is sub-second; the budget is dominated by binary upload
// (cold runs) and the readiness probe.
func Launch(ctx context.Context, opts LaunchOptions) (*hostclient.HTTP, *Handle, error) {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		return nil, nil, err
	}

	binPath, err := buildHostBinary(opts)
	if err != nil {
		return nil, nil, err
	}

	c, err := newSDKClient(opts)
	if err != nil {
		return nil, nil, err
	}

	launchCtx, cancel := context.WithTimeout(ctx, opts.ReadyTimeout)
	defer cancel()

	sb, err := createSandbox(launchCtx, c, opts)
	if err != nil {
		return nil, nil, err
	}

	// From here on, any error must trigger sandbox cleanup; otherwise
	// a half-launched sandbox sits running on the user's account.
	cleanup := func(prev error) error {
		// Use a fresh, short context — the parent may already be
		// cancelled (e.g. timeout) and we still need Delete to land.
		delCtx, delCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer delCancel()
		if delErr := sb.Delete(delCtx); delErr != nil {
			return errors.Join(prev, fmt.Errorf("daytona: cleanup delete sandbox %s: %w", sb.ID, delErr))
		}
		return prev
	}

	cmdID, err := startHostInSandbox(launchCtx, sb, binPath, opts.ListenPort)
	if err != nil {
		return nil, nil, cleanup(err)
	}

	preview, err := sb.GetPreviewLink(launchCtx, opts.ListenPort)
	if err != nil {
		return nil, nil, cleanup(fmt.Errorf("daytona: GetPreviewLink port %d: %w", opts.ListenPort, err))
	}
	if preview == nil || preview.URL == "" || preview.Token == "" {
		return nil, nil, cleanup(fmt.Errorf("daytona: GetPreviewLink returned empty URL/token"))
	}

	client, err := hostclient.NewRemoteHTTP(preview.URL, map[string]string{
		previewTokenHeader: preview.Token,
	})
	if err != nil {
		return nil, nil, cleanup(fmt.Errorf("daytona: build remote hostclient: %w", err))
	}

	if err := waitForStatus(launchCtx, client); err != nil {
		// Best-effort: pull the clank-host process logs from Daytona
		// so the failure message contains the actual cause (e.g.
		// "exec format error" for arch mismatch) rather than just
		// "502 Bad Gateway".
		if logs := fetchLogs(sb, cmdID); logs != "" {
			err = fmt.Errorf("%w\nclank-host logs:\n%s", err, logs)
		}
		return nil, nil, cleanup(err)
	}

	return client, &Handle{
		SandboxID: sb.ID,
		CommandID: cmdID,
		sandbox:   sb,
	}, nil
}

// waitForStatus polls /status until it returns 200 or the context
// expires. The interval starts small and backs off mildly — the
// expected wait is "next request after binary boot", which is
// sub-second in practice.
func waitForStatus(ctx context.Context, c *hostclient.HTTP) error {
	const (
		initialInterval = 200 * time.Millisecond
		maxInterval     = 1500 * time.Millisecond
	)
	interval := initialInterval
	var lastErr error
	for {
		_, err := c.Status(ctx)
		if err == nil {
			return nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return fmt.Errorf("daytona: clank-host /status not ready before deadline: last error %v: %w", lastErr, ctx.Err())
		case <-time.After(interval):
		}

		interval *= 2
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}

// fetchLogs is a best-effort retrieval of the clank-host process'
// stdout+stderr from Daytona, used to enrich error messages on
// readiness failure. Uses a fresh short-lived context because the
// caller's context is typically already at deadline.
func fetchLogs(sb *sdk.Sandbox, commandID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := sb.Process.GetSessionCommandLogs(ctx, sessionID, commandID)
	if err != nil || resp == nil {
		return ""
	}
	out := resp.Output
	if out == "" {
		out = resp.Stdout + resp.Stderr
	}
	return out
}
