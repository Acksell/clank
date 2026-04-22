package daytona

import (
	"context"
	"fmt"

	sdk "github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
)

// sessionID is the Daytona "session" we attach the clank-host process
// to. One per sandbox; the sandbox lifetime is the host lifetime, so
// reusing a fixed name keeps things easy to find in the dashboard.
const sessionID = "clank-host"

// startHostInSandbox uploads the clank-host binary and launches it as
// an asynchronous session command. Returns the Daytona command ID so
// the caller can stream logs / inspect exit status.
//
// The command line is constructed here (not in options) because it's
// tightly coupled to the upload destination and Daytona's preview-URL
// requirements: bind 0.0.0.0 so the proxy can reach the listener, and
// pass --allow-public to satisfy clank-host's own loopback guard.
func startHostInSandbox(ctx context.Context, sb *sdk.Sandbox, binPath string, listenPort int) (string, error) {
	// Upload binary as []byte source — the SDK supports either a path
	// string (which it re-reads with os.ReadFile) or raw bytes. We use
	// the path form to avoid loading the whole binary into memory
	// twice.
	if err := sb.FileSystem.UploadFile(ctx, binPath, remoteBinaryPath); err != nil {
		return "", fmt.Errorf("daytona: upload clank-host: %w", err)
	}

	if err := sb.Process.CreateSession(ctx, sessionID); err != nil {
		return "", fmt.Errorf("daytona: create session %q: %w", sessionID, err)
	}

	// chmod +x is a separate command. We could combine with && but
	// keeping them separate makes failures attributable.
	if _, err := sb.Process.ExecuteSessionCommand(ctx, sessionID,
		"chmod +x "+remoteBinaryPath, false, false); err != nil {
		return "", fmt.Errorf("daytona: chmod clank-host: %w", err)
	}

	// Launch async — runAsync=true returns immediately with a command
	// ID; the binary keeps running in the session until the sandbox
	// is deleted.
	startCmd := fmt.Sprintf("%s --addr 0.0.0.0:%d --allow-public", remoteBinaryPath, listenPort)
	res, err := sb.Process.ExecuteSessionCommand(ctx, sessionID, startCmd, true, false)
	if err != nil {
		return "", fmt.Errorf("daytona: launch clank-host: %w", err)
	}
	idVal, ok := res["id"]
	if !ok {
		return "", fmt.Errorf("daytona: launch clank-host: response missing command id (got %+v)", res)
	}
	id, ok := idVal.(string)
	if !ok {
		return "", fmt.Errorf("daytona: launch clank-host: command id is %T not string", idVal)
	}
	return id, nil
}
