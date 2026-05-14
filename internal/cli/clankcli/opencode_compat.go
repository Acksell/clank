package clankcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/acksell/clank/internal/agent"
	daemonclient "github.com/acksell/clank/internal/daemonclient"
)

// assertOpencodeCompatible queries `opencode --version` on both
// ends of a migration (laptop's local clank-host, remote sprite's
// clank-host via the gateway) and enforces the version-skew policy
// in agent.AssertOpencodeVersionsCompatible:
//
//   - exact match: silent OK
//   - patch-only diff: log a one-line warning to stderr, proceed
//   - minor or major diff: return an error so the caller aborts
//     the migration before any code/session work begins
//
// Failures fetching either version are reported as compatibility
// errors with the upgrade hint — better to refuse than guess.
func assertOpencodeCompatible(ctx context.Context, stderr io.Writer, local, remote *daemonclient.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	localVer, err := local.OpenCodeVersion(ctx)
	if err != nil {
		return fmt.Errorf("read laptop opencode version: %w", err)
	}
	remoteVer, err := remote.OpenCodeVersion(ctx)
	if err != nil {
		// "host unavailable" is the gateway's signal that EnsureHost
		// failed (provisioner credentials, sprite suspended, etc.).
		// The actual reason is in the gateway's logs, not in our
		// response. Tell the user where to look instead of letting
		// them debug the version check.
		if isHostUnavailable(err) {
			return fmt.Errorf("can't reach the remote sprite: %w\n\nThe gateway returned 'host unavailable', which means its provisioner.EnsureHost failed.\nCheck the gateway's logs for the underlying reason. For the dev stack:\n  make docker-logs\nLook for a line like 'gateway: ensure host for user …: …'.", err)
		}
		return fmt.Errorf("read remote opencode version: %w", err)
	}

	warn, err := agent.AssertOpencodeVersionsCompatible(localVer, remoteVer)
	if err != nil {
		var typed *agent.OpencodeIncompatibleError
		if errors.As(err, &typed) {
			// Surface the clank-pinned version explicitly so the
			// user knows exactly which version to land on, not just
			// "match the other side." If the laptop is the drifted
			// side (the common case — sprites get the pin
			// automatically on EnsureHost), suggesting the pin
			// directly avoids a guessing game.
			return fmt.Errorf(
				"%s\n\nclank pins opencode at version %s. Upgrade your laptop to match:\n  opencode upgrade --version %s\n\n(The sprite re-installs the pinned opencode automatically on its next EnsureHost; if it's the side that's drifted, restart the sprite via your remote provisioner and retry.)",
				typed.Error(), agent.PinnedOpencodeVersion, agent.PinnedOpencodeVersion,
			)
		}
		return err
	}
	if warn != nil {
		fmt.Fprintf(stderr, "  warning: %s\n", warn.String())
	}
	return nil
}

// isHostUnavailable detects the gateway's "EnsureHost failed → 502
// host unavailable" response. The string is the literal body the
// gateway writes via http.Error in proxy.go's proxyToHost when
// Provisioner.EnsureHost returns an error. Matched by substring
// because the daemonclient error wrapping prefixes a path before it.
//
// A typed error code from the gateway would be nicer, but this is
// the existing string and changing the gateway's response shape is
// a bigger surface than we want for one diagnostic message.
func isHostUnavailable(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "host unavailable")
}
