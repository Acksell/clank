package clankcli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// connectCmd implements `clank connect <kind>`. It's the user-facing
// entry point for provisioning a remote host plane (currently only
// "daytona"). The verb is intentionally generic so future host kinds
// (e.g. "ssh", "fly") slot in without a new subcommand.
//
// The command is synchronous: it blocks until the hub reports the host
// is ready, then prints a small summary. Errors from the hub (missing
// API key, sandbox creation failure, readiness timeout) propagate
// verbatim — no fallback, no retry.
func connectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect <kind>",
		Short: "Provision and connect a remote host plane",
		Long: `Provision a new host plane (e.g. a Daytona sandbox) and register
it with the running daemon. The new host becomes available for new
sessions.

Currently supported kinds:
  daytona   Daytona sandbox (requires DAYTONA_API_KEY)`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnect(args[0])
		},
	}
	return cmd
}

// connectTimeout caps the synchronous wait. Daytona's typical end-to-
// end is ~7-10s; 90s leaves headroom for cold binary upload without
// letting a wedged provisioner hang the user's terminal indefinitely.
const connectTimeout = 90 * time.Second

func runConnect(kind string) error {
	// kind is guaranteed non-empty by cobra.ExactArgs(1) on the connect
	// command — no defensive check needed at this layer.

	client, err := ensureDaemon()
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}

	fmt.Printf("Provisioning %s host...\n", kind)

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	resp, err := client.ProvisionHost(ctx, kind)
	if err != nil {
		return fmt.Errorf("provision %s host: %w", kind, err)
	}

	fmt.Printf("Connected %s host:\n", kind)
	fmt.Printf("  host_id: %s\n", resp.HostID)
	fmt.Printf("  status:  %s\n", resp.Status)
	return nil
}
