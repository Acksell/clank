package daemoncli

import (
	"fmt"
	"log"

	"github.com/acksell/clank/internal/config"
	daytonalauncher "github.com/acksell/clank/internal/host/launcher/daytona"
	daytonaprov "github.com/acksell/clank/internal/provisioner/daytona"
	"github.com/acksell/clank/internal/store"
)

// buildDaytonaLauncher loads preferences and constructs a Daytona
// launcher when configured. Returns (nil, nil) when preferences don't
// have a daytona block — that's the "not configured" signal, not an
// error.
//
// PublicBaseURL is required: sandboxes need a way to reach the cloud
// hub for git mirror clones. We refuse to wire up the launcher
// without it rather than spawning sandboxes that can't function.
//
// The launcher is a thin shim over a DaytonaProvisioner that owns
// persistence + suspend/resume. Both share the same SQL store the
// daemon uses for session metadata; the host registry lives in the
// hosts table (added by migration v17).
func buildDaytonaLauncher(opts ServerOptions, st *store.Store) (*daytonalauncher.Launcher, error) {
	if st == nil {
		return nil, fmt.Errorf("daytona launcher: store is required")
	}
	prefs, err := config.LoadPreferences()
	if err != nil {
		return nil, fmt.Errorf("load preferences: %w", err)
	}
	if prefs.Daytona == nil || prefs.Daytona.APIKey == "" {
		return nil, nil
	}
	if prefs.RemoteHub == nil || prefs.RemoteHub.AuthToken == "" {
		return nil, fmt.Errorf("daytona requires preferences.remote_hub.auth_token")
	}
	if opts.PublicBaseURL == "" {
		return nil, fmt.Errorf("daytona requires --public-base-url so sandboxes can reach the hub")
	}

	prov, err := daytonaprov.New(daytonaprov.Options{
		APIKey:       prefs.Daytona.APIKey,
		Snapshot:     prefs.Daytona.Snapshot,
		Image:        prefs.Daytona.Image,
		APIUrl:       prefs.Daytona.BaseURL, // pref name kept for back-compat; SDK calls it APIUrl
		ExtraEnv:     prefs.Daytona.ExtraEnv,
		HubBaseURL:   opts.PublicBaseURL,
		HubAuthToken: prefs.RemoteHub.AuthToken,
	}, st, log.Default())
	if err != nil {
		return nil, fmt.Errorf("build provisioner: %w", err)
	}

	return daytonalauncher.New(prov, daytonalauncher.Options{
		SuspendOnStop: prefs.Daytona.SuspendOnStop,
	}, log.Default()), nil
}
