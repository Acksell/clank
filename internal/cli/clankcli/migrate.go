package clankcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acksell/clank/internal/agent"
	daemonclient "github.com/acksell/clank/internal/daemonclient"
)

func migrateCmd() *cobra.Command {
	var (
		direction  string
		worktreeID string
		deviceID   string
		repoPath   string
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate a worktree's ownership between laptop and sandbox",
		Long: `Migrate a synced worktree to (or from) the cloud sandbox.

The gateway orchestrates the full flow: pre-checks the synced
checkpoint, wakes the sandbox, downloads the bundles from object
storage, applies them on the sandbox, and atomically transfers
ownership. After a successful to_sprite migration the next
session-create against the worktree resolves to the synced state on
the sandbox — no clone, no SSH credentials needed.

Default behavior auto-detects the worktree id (from
<repo>/.clank/worktree-id) and the device id (~/.config/clank/device-id),
making the common "migrate the repo I'm currently in" call a
zero-flag invocation.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := strings.ToLower(direction)
			if dir == "" {
				dir = string(daemonclient.MigrateToSprite)
			}
			switch daemonclient.MigrateDirection(dir) {
			case daemonclient.MigrateToSprite, daemonclient.MigrateToLaptop:
			default:
				return fmt.Errorf("--direction must be to_sprite or to_laptop, got %q", direction)
			}

			if repoPath == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				repoPath = cwd
			}
			absRepo, err := filepath.Abs(repoPath)
			if err != nil {
				return fmt.Errorf("resolve repo path: %w", err)
			}

			if worktreeID == "" {
				worktreeID, err = agent.ReadLocalWorktreeID(absRepo)
				if err != nil {
					return fmt.Errorf("read cached worktree id: %w", err)
				}
				if worktreeID == "" {
					return fmt.Errorf("no worktree id cached at %s/.clank/worktree-id — run `clank sync push` first, or pass --worktree-id", absRepo)
				}
			}

			if deviceID == "" {
				configDir, err := os.UserConfigDir()
				if err == nil {
					data, err := os.ReadFile(filepath.Join(configDir, "clank", "device-id"))
					if err == nil {
						deviceID = strings.TrimSpace(string(data))
					}
				}
				if deviceID == "" {
					return fmt.Errorf("no device id at ~/.config/clank/device-id — run `clank sync push` once to auto-generate, or pass --device-id")
				}
			}

			client, err := daemonclient.NewDefaultClient()
			if err != nil {
				return fmt.Errorf("daemon client: %w", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			res, err := client.MigrateWorktree(ctx, worktreeID, deviceID, daemonclient.MigrateDirection(dir))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "migrated worktree %s → %s/%s (checkpoint %s)\n",
				res.WorktreeID, res.NewOwnerKind, res.NewOwnerID, res.CheckpointID)
			return nil
		},
	}

	cmd.Flags().StringVar(&direction, "direction", "", "Migration direction: to_sprite (default) | to_laptop")
	cmd.Flags().StringVar(&worktreeID, "worktree-id", "", "Worktree ID to migrate (default: read from <repo>/.clank/worktree-id)")
	cmd.Flags().StringVar(&deviceID, "device-id", envOrDefault("CLANK_DEVICE_ID", ""), "Laptop device id (default: ~/.config/clank/device-id)")
	cmd.Flags().StringVar(&repoPath, "repo", "", "Repo directory to read .clank/worktree-id from (default: current directory)")
	return cmd
}
