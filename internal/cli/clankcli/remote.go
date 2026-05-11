package clankcli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acksell/clank/internal/config"
)

// remoteCmd registers `clank remote` — manage the named clank
// deployments (gateway + auth URLs + session) the user can target.
// Modeled on git remotes: one Active at a time, named entries,
// add/list/switch/remove subcommands.
//
// Remotes let the user keep several deployments wired up (dev docker
// stack, managed cloud, enterprise self-host) without rewriting
// preferences when they switch.
func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Manage clank-deployment remotes (like git remotes)",
		Long: `Manage named clank deployments in preferences.json.

A remote bundles a gateway URL, an auth-server URL, and the device-flow
session for one deployment. One remote is active at a time; push, pull,
migration, and the TUI auth panel all target it.`,
	}
	cmd.AddCommand(
		remoteListCmd(),
		remoteSwitchCmd(),
		remoteAddCmd(),
		remoteRemoveCmd(),
	)
	return cmd
}

func remoteListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			prefs, err := config.LoadPreferences()
			if err != nil {
				return fmt.Errorf("load preferences: %w", err)
			}
			if prefs.Remote == nil || len(prefs.Remote.Profiles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no remotes configured. Use `clank remote add <name>` to create one.")
				return nil
			}
			names := make([]string, 0, len(prefs.Remote.Profiles))
			for k := range prefs.Remote.Profiles {
				names = append(names, k)
			}
			sort.Strings(names)
			active := prefs.Remote.Active
			for _, name := range names {
				r := prefs.Remote.Profiles[name]
				marker := "  "
				if name == active {
					marker = "* "
				}
				gw := r.GatewayURL
				if gw == "" {
					gw = "(no gateway_url)"
				}
				email := ""
				if r.UserEmail != "" {
					email = "  " + r.UserEmail
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s — %s%s\n", marker, name, gw, email)
			}
			return nil
		},
	}
}

func remoteSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <name>",
		Short: "Set the active remote",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("remote name is required")
			}
			var found bool
			err := config.UpdatePreferences(func(p *config.Preferences) {
				if p.Remote == nil || p.Remote.Profiles == nil {
					return
				}
				if _, ok := p.Remote.Profiles[name]; !ok {
					return
				}
				p.Remote.Active = name
				found = true
			})
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("no remote named %q — `clank remote list` to see configured remotes", name)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "active remote: %s\n", name)
			return nil
		},
	}
}

func remoteAddCmd() *cobra.Command {
	var (
		gatewayURL string
		authURL    string
		token      string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a new remote (and set it active)",
		Long: `Add a named remote. The new remote becomes active so subsequent
push/pull calls target it. Repeating with the same name overwrites the
remote.

Token is the bearer the gateway requires. For self-hosted dev this is
whatever you set CLANK_AUTH_TOKEN to on the server. For deployments
with device flow, leave --token empty and use ` + "`clank login`" + ` to populate it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("remote name is required")
			}
			if gatewayURL == "" {
				return fmt.Errorf("--gateway-url is required")
			}
			err := config.UpdatePreferences(func(p *config.Preferences) {
				if p.Remote == nil {
					p.Remote = &config.RemoteConfig{Profiles: map[string]*config.Remote{}}
				}
				if p.Remote.Profiles == nil {
					p.Remote.Profiles = map[string]*config.Remote{}
				}
				p.Remote.Profiles[name] = &config.Remote{
					GatewayURL:  gatewayURL,
					AuthURL:     authURL,
					AccessToken: token,
				}
				p.Remote.Active = name
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added remote %q → %s (active)\n", name, gatewayURL)
			return nil
		},
	}
	cmd.Flags().StringVar(&gatewayURL, "gateway-url", "", "Gateway URL (required)")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "Auth-server URL for device flow (optional)")
	cmd.Flags().StringVar(&token, "token", "", "Bearer token for the gateway (optional — populated by `clank login` later)")
	return cmd
}

func remoteRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a remote",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			err := config.UpdatePreferences(func(p *config.Preferences) {
				if p.Remote == nil || p.Remote.Profiles == nil {
					return
				}
				delete(p.Remote.Profiles, name)
				if p.Remote.Active == name {
					p.Remote.Active = ""
					for k := range p.Remote.Profiles {
						p.Remote.Active = k
						break
					}
				}
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed remote %q\n", name)
			return nil
		},
	}
}
