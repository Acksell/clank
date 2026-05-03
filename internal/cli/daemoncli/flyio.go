package daemoncli

import (
	"fmt"
	"log"

	"github.com/acksell/clank/internal/config"
	flyiolauncher "github.com/acksell/clank/internal/host/launcher/flyio"
	flyioprov "github.com/acksell/clank/internal/provisioner/flyio"
	"github.com/acksell/clank/internal/store"
)

// buildFlyIOLauncher loads preferences and constructs a Fly.io
// Sprites launcher when configured. Returns (nil, nil) when
// preferences don't have a flyio block — that's the "not configured"
// signal, not an error.
//
// PublicBaseURL is required so the sprite's clank-host service can
// reach the cloud hub for git mirror clones, mirroring the Daytona
// launcher's contract.
//
// The launcher is a thin shim over a SpritesProvisioner that owns
// persistence + auto-wake. Both share the same SQL store the daemon
// uses for session metadata; the host registry lives in the hosts
// table.
func buildFlyIOLauncher(opts ServerOptions, st *store.Store) (*flyiolauncher.Launcher, error) {
	if st == nil {
		return nil, fmt.Errorf("flyio launcher: store is required")
	}
	prefs, err := config.LoadPreferences()
	if err != nil {
		return nil, fmt.Errorf("load preferences: %w", err)
	}
	if prefs.FlyIO == nil || prefs.FlyIO.APIToken == "" {
		return nil, nil
	}
	if prefs.RemoteHub == nil || prefs.RemoteHub.AuthToken == "" {
		return nil, fmt.Errorf("flyio requires preferences.remote_hub.auth_token")
	}
	if opts.PublicBaseURL == "" {
		return nil, fmt.Errorf("flyio requires --public-base-url so sprites can reach the hub")
	}

	prov, err := flyioprov.New(flyioprov.Options{
		APIToken:         prefs.FlyIO.APIToken,
		OrganizationSlug: prefs.FlyIO.OrganizationSlug,
		Region:           prefs.FlyIO.Region,
		SpriteNamePrefix: prefs.FlyIO.SpriteNamePrefix,
		RamMB:            prefs.FlyIO.RamMB,
		CPUs:             prefs.FlyIO.CPUs,
		StorageGB:        prefs.FlyIO.StorageGB,
		HubBaseURL:       opts.PublicBaseURL,
		HubAuthToken:     prefs.RemoteHub.AuthToken,
	}, st, log.Default())
	if err != nil {
		return nil, fmt.Errorf("build provisioner: %w", err)
	}

	return flyiolauncher.New(prov, log.Default()), nil
}
