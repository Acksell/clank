// Package flyio is a thin shim that adapts the persistent
// SpritesProvisioner (internal/provisioner/flyio) to the legacy
// hub.HostLauncher interface. Mirrors the daytona shim
// (internal/host/launcher/daytona) and goes away once the gateway
// (PR 3) calls the provisioner directly.
package flyio

import (
	"context"
	"fmt"
	"log"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	flyioprov "github.com/acksell/clank/internal/provisioner/flyio"
)

// Launcher implements hub.HostLauncher by delegating to a
// SpritesProvisioner. The provisioner owns persistence + auto-wake;
// the launcher just translates HostRef into the (Hostname, *HTTP)
// shape the hub expects.
type Launcher struct {
	prov *flyioprov.Provisioner
	log  *log.Logger
}

// New constructs a Launcher backed by an already-built provisioner.
func New(prov *flyioprov.Provisioner, lg *log.Logger) *Launcher {
	if lg == nil {
		lg = log.Default()
	}
	return &Launcher{prov: prov, log: lg}
}

// Launch resolves (or creates) the persistent sprite for this
// single-user laptop daemon. The hardcoded "local" userID matches
// the Daytona shim until PR 4 introduces real user identity.
func (l *Launcher) Launch(ctx context.Context, _ agent.LaunchHostSpec) (host.Hostname, *hostclient.HTTP, error) {
	ref, err := l.prov.EnsureHost(ctx, "local")
	if err != nil {
		return "", nil, fmt.Errorf("flyio launcher: %w", err)
	}
	return ref.Hostname, hostclient.NewHTTP(ref.URL, ref.Transport), nil
}

// Stop is a no-op: sprites auto-hibernate on idle natively, so
// explicit suspend on daemon shutdown is unnecessary.
func (l *Launcher) Stop() {}
