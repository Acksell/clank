package clankcli

import (
	"context"
	"fmt"
	"os"

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
// command (`push`); a watcher (`clank sync watch`) is a follow-up
// that drives the same syncclient.PushBundle entry point.
func syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Stream git bundles to your cloud sandbox",
		Long: `clank sync uploads git bundles of your local repos to clank-sync,
which buffers and flushes them into your cloud sandbox the next time
it wakes. Useful for "edit on laptop, continue on phone" workflows.`,
	}
	cmd.AddCommand(syncPushCmd())
	return cmd
}

func syncPushCmd() *cobra.Command {
	var (
		baseURL string
		token   string
		slug    string
	)
	cmd := &cobra.Command{
		Use:   "push <repo-path>",
		Short: "Bundle the repo at <repo-path> and upload it to clank-sync",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath := args[0]
			if baseURL == "" {
				return fmt.Errorf("--base-url is required (or set CLANK_SYNC_URL)")
			}
			if slug == "" {
				slug = syncclient.DefaultRepoSlug(repoPath)
			}
			cli, err := syncclient.New(syncclient.Config{
				BaseURL:   baseURL,
				AuthToken: token,
			})
			if err != nil {
				return err
			}
			ctx := context.Background()
			if err := cli.PushBundle(ctx, repoPath, slug); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pushed %s as %q\n", repoPath, slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&baseURL, "base-url", envOrDefault("CLANK_SYNC_URL", ""), "clank-sync base URL")
	cmd.Flags().StringVar(&token, "token", envOrDefault("CLANK_SYNC_TOKEN", ""), "bearer token for clank-sync")
	cmd.Flags().StringVar(&slug, "slug", "", "explicit repo slug (default: basename of repo-path)")
	return cmd
}
