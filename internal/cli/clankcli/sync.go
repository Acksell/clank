package clankcli

import (
	cryptorand "crypto/rand"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acksell/clank/pkg/syncclient"
)

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// syncCmd registers `clank sync` subcommands. MVP exposes a single
// command (`push`); a watcher (`clank sync watch`) is a follow-up.
func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Push checkpoints of your local repos to clank-sync",
		Long: `clank sync uploads a worktree checkpoint (head + working-tree state)
to clank-sync. Worktree IDs are cached per-repo at <repo>/.clank/worktree-id;
the device identity is cached at ~/.config/clank/device-id and shared
across repos.

The cloud gateway's MigrateWorktree(to_sprite) flow consumes the most
recent committed checkpoint when you ask to "Run in cloud".`,
	}
	cmd.AddCommand(syncPushCmd())
	return cmd
}

func syncPushCmd() *cobra.Command {
	var (
		baseURL  string
		token    string
		display  string
		deviceID string
	)
	cmd := &cobra.Command{
		Use:   "push <repo-path>",
		Short: "Build and upload a checkpoint of the repo at <repo-path>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath := args[0]
			if baseURL == "" {
				return fmt.Errorf("--base-url is required (or set CLANK_SYNC_URL)")
			}

			absRepo, err := filepath.Abs(repoPath)
			if err != nil {
				return fmt.Errorf("resolve repo path: %w", err)
			}

			if deviceID == "" {
				deviceID, err = ensureDeviceID()
				if err != nil {
					return fmt.Errorf("device id: %w", err)
				}
			}

			cli, err := syncclient.New(syncclient.Config{
				BaseURL:   baseURL,
				AuthToken: token,
				DeviceID:  deviceID,
			})
			if err != nil {
				return err
			}
			ctx := context.Background()

			worktreeID, err := loadWorktreeID(absRepo)
			if err != nil {
				return fmt.Errorf("load cached worktree id: %w", err)
			}
			if worktreeID == "" {
				name := display
				if name == "" {
					name = filepath.Base(absRepo)
				}
				worktreeID, err = cli.RegisterWorktree(ctx, name)
				if err != nil {
					return fmt.Errorf("register worktree: %w", err)
				}
				if err := saveWorktreeID(absRepo, worktreeID); err != nil {
					return fmt.Errorf("cache worktree id: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "registered worktree %s as %q\n", worktreeID, name)
			}

			res, err := cli.PushCheckpoint(ctx, worktreeID, absRepo)
			if err != nil {
				return fmt.Errorf("push checkpoint: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pushed checkpoint %s (HEAD %s)\n",
				res.CheckpointID, shortSHA(res.Manifest.HeadCommit))
			return nil
		},
	}
	cmd.Flags().StringVar(&baseURL, "base-url", envOrDefault("CLANK_SYNC_URL", ""), "clank-sync base URL")
	cmd.Flags().StringVar(&token, "token", envOrDefault("CLANK_SYNC_TOKEN", ""), "bearer token for clank-sync")
	cmd.Flags().StringVar(&display, "display-name", "", "display name for newly-registered worktrees (default: basename of repo-path)")
	cmd.Flags().StringVar(&deviceID, "device-id", envOrDefault("CLANK_DEVICE_ID", ""), "device id (default: ~/.config/clank/device-id, auto-generated on first run)")
	return cmd
}

// ensureDeviceID returns this laptop's stable device id, generating
// one on first call. Stored at ~/.config/clank/device-id.
func ensureDeviceID() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(configDir, "clank")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "device-id")
	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	id := "dev-" + hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func loadWorktreeID(repoPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repoPath, ".clank", "worktree-id"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func saveWorktreeID(repoPath, id string) error {
	dir := filepath.Join(repoPath, ".clank")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "worktree-id"), []byte(id+"\n"), 0o644)
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
