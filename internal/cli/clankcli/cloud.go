package clankcli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acksell/clank/internal/config"
)

// cloudCmd registers `clank cloud` — manage the named cloud profiles
// in preferences.json. One profile is `active` at a time; push/pull
// and the TUI cloud panel target that profile's gateway/auth URLs.
//
// Profiles let the user keep several deployments wired up (dev docker
// stack, managed cloud, enterprise self-host) without rewriting
// preferences when they switch.
func cloudCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud",
		Short: "Manage cloud deployment profiles",
		Long: `Manage named cloud profiles in preferences.json.

A profile bundles a gateway URL, an auth URL, and the device-flow
session for one cloud deployment. One profile is active at a time;
push/pull and the TUI cloud panel use it.`,
	}
	cmd.AddCommand(
		cloudListCmd(),
		cloudSwitchCmd(),
		cloudAddCmd(),
		cloudRemoveCmd(),
	)
	return cmd
}

func cloudListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured cloud profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			prefs, err := config.LoadPreferences()
			if err != nil {
				return fmt.Errorf("load preferences: %w", err)
			}
			if prefs.Cloud == nil || len(prefs.Cloud.Profiles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no cloud profiles configured. Use `clank cloud add <name>` to create one.")
				return nil
			}
			names := make([]string, 0, len(prefs.Cloud.Profiles))
			for k := range prefs.Cloud.Profiles {
				names = append(names, k)
			}
			sort.Strings(names)
			active := prefs.Cloud.Active
			for _, name := range names {
				p := prefs.Cloud.Profiles[name]
				marker := "  "
				if name == active {
					marker = "* "
				}
				gw := p.GatewayURL
				if gw == "" {
					gw = "(no gateway_url)"
				}
				email := ""
				if p.UserEmail != "" {
					email = "  " + p.UserEmail
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s — %s%s\n", marker, name, gw, email)
			}
			return nil
		},
	}
}

func cloudSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <name>",
		Short: "Set the active cloud profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("profile name is required")
			}
			return config.UpdatePreferences(func(p *config.Preferences) {
				if p.Cloud == nil || p.Cloud.Profiles == nil {
					return
				}
				if _, ok := p.Cloud.Profiles[name]; !ok {
					return
				}
				p.Cloud.Active = name
			})
		},
	}
}

func cloudAddCmd() *cobra.Command {
	var (
		gatewayURL string
		authURL    string
		token      string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a new cloud profile (and set it active)",
		Long: `Add a named cloud profile. The new profile becomes active so
subsequent push/pull calls target it. Repeating with the same name
overwrites the profile.

Token is the bearer the cloud gateway requires. For self-hosted dev,
this is whatever you set CLANK_AUTH_TOKEN to on the server. For
production deployments with device flow, leave --token empty and use
` + "`clank login`" + ` (coming) to populate it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("profile name is required")
			}
			if gatewayURL == "" {
				return fmt.Errorf("--gateway-url is required")
			}
			return config.UpdatePreferences(func(p *config.Preferences) {
				if p.Cloud == nil {
					p.Cloud = &config.CloudConfig{Profiles: map[string]*config.CloudProfile{}}
				}
				if p.Cloud.Profiles == nil {
					p.Cloud.Profiles = map[string]*config.CloudProfile{}
				}
				p.Cloud.Profiles[name] = &config.CloudProfile{
					GatewayURL:  gatewayURL,
					AuthURL:     authURL,
					AccessToken: token,
				}
				p.Cloud.Active = name
			})
		},
	}
	cmd.Flags().StringVar(&gatewayURL, "gateway-url", "", "Gateway URL (required)")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "Auth-server URL for device flow (optional)")
	cmd.Flags().StringVar(&token, "token", "", "Bearer token for the gateway (optional — populated by `clank login` later)")
	return cmd
}

func cloudRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a cloud profile",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			return config.UpdatePreferences(func(p *config.Preferences) {
				if p.Cloud == nil || p.Cloud.Profiles == nil {
					return
				}
				delete(p.Cloud.Profiles, name)
				if p.Cloud.Active == name {
					p.Cloud.Active = ""
					for k := range p.Cloud.Profiles {
						p.Cloud.Active = k
						break
					}
				}
			})
		},
	}
}
