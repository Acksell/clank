package daytona

import (
	"context"
	"fmt"

	sdk "github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
)

// Handle is what callers (the hub, primarily) keep around to control a
// running Daytona-hosted clank-host. Stop is the only mutating op; the
// rest is identification metadata for logs / dashboards.
type Handle struct {
	// SandboxID is the Daytona sandbox identifier. Surfaces in the
	// Daytona dashboard URL.
	SandboxID string
	// CommandID is the async session-command id for the clank-host
	// process itself. Useful for streaming logs via
	// sb.Process.GetSessionCommandLogsStream.
	CommandID string

	sandbox *sdk.Sandbox
}

// Stop deletes the sandbox. This is the only supported teardown — we
// don't try to gracefully kill clank-host first because Daytona's
// sandbox deletion already SIGKILLs all processes, and there's no
// state inside the sandbox we want to flush.
func (h *Handle) Stop(ctx context.Context) error {
	if h.sandbox == nil {
		return fmt.Errorf("daytona.Handle.Stop: nil sandbox (already stopped?)")
	}
	if err := h.sandbox.Delete(ctx); err != nil {
		return fmt.Errorf("daytona: delete sandbox %s: %w", h.SandboxID, err)
	}
	h.sandbox = nil
	return nil
}
