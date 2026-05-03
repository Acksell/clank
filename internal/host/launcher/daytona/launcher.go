// Package daytona is a thin shim that adapts the persistent
// DaytonaProvisioner (internal/provisioner/daytona) to the legacy
// hub.HostLauncher interface so existing call sites keep working
// while the broader refactor (gateway, hub removal) is rolled out
// over subsequent PRs.
//
// New code should depend on internal/provisioner/daytona directly.
// This package will be deleted once the gateway lands.
package daytona

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"

	daytonaprov "github.com/acksell/clank/internal/provisioner/daytona"
)

// _ keeps the daytonaprov import alive — the package name is
// referenced via the *daytonaprov.Provisioner field on Launcher.
var _ = (*daytonaprov.Provisioner)(nil)

// Options configures the launcher shim. Only fields the caller cares
// about for the adapter behavior; the underlying provisioner's
// configuration is constructed elsewhere.
type Options struct {
	// SuspendOnStop, when true, asks the provisioner to suspend the
	// underlying sandbox during daemon shutdown. Default false: leave
	// the sandbox running for zero cold-resume latency on the next
	// start (Daytona bills per-second; a quick laptop reboot costs
	// cents).
	SuspendOnStop bool
}

// Launcher implements hub.HostLauncher by delegating to a
// DaytonaProvisioner. The provisioner owns persistence, wake-from-stop,
// and lifecycle; the launcher just translates the result into the
// (Hostname, *hostclient.HTTP) shape the hub expects.
type Launcher struct {
	prov *daytonaprov.Provisioner
	opts Options
	log  *log.Logger
}

// New constructs a Launcher backed by an already-built provisioner.
// Callers should construct the provisioner via daytonaprov.New(...) so
// the Options validation lives in one place.
func New(prov *daytonaprov.Provisioner, opts Options, lg *log.Logger) *Launcher {
	if lg == nil {
		lg = log.Default()
	}
	return &Launcher{prov: prov, opts: opts, log: lg}
}

// Launch resolves (or wakes) the persistent sandbox for this
// single-user laptop daemon. The hardcoded "local" userID is a
// placeholder until PR 4 introduces real user identity through the
// gateway/JWT path; one row coexists cleanly with the eventual
// multi-user shape because the store keys on (user_id, provider).
//
// The provisioner returns a fully-wired Transport that injects every
// header required to reach the host (capability-bearer + Daytona
// preview-token); the shim just hands it to hostclient.NewHTTP.
func (l *Launcher) Launch(ctx context.Context, _ agent.LaunchHostSpec) (host.Hostname, *hostclient.HTTP, error) {
	ref, err := l.prov.EnsureHost(ctx, "local")
	if err != nil {
		return "", nil, fmt.Errorf("daytona launcher: %w", err)
	}
	return ref.Hostname, hostclient.NewHTTP(ref.URL, ref.Transport), nil
}

// Stop is best-effort: optionally suspends the sandbox to stop billing
// for compute, then returns. Never deletes the sandbox — the user's
// workspace lives on it. Errors are logged, not propagated, because
// Stop is typically called from a defer in shutdown paths where there
// is nowhere to surface them.
//
// Crucially, this uses SuspendByUser (not EnsureHost) so a sandbox
// that's already asleep stays asleep instead of being woken just to
// be re-suspended.
func (l *Launcher) Stop() {
	if !l.opts.SuspendOnStop {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err := l.prov.SuspendByUser(ctx, "local"); err != nil {
		l.log.Printf("daytona launcher: suspend on stop: %v", err)
	}
}
