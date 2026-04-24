package daytona

import (
	"fmt"
	"time"
)

// LaunchOptions parameterizes Launch. Required fields fail fast at
// validation rather than substituting silent defaults — a missing
// APIKey is almost certainly a misconfiguration the user wants to
// see, not a value to invent.
type LaunchOptions struct {
	// APIKey is the Daytona personal access token (env DAYTONA_API_KEY).
	// Required.
	APIKey string

	// APIURL overrides the Daytona control-plane base URL. Optional;
	// when empty, the SDK reads DAYTONA_API_URL or defaults to
	// "https://app.daytona.io/api".
	APIURL string

	// ListenPort is the TCP port clank-host binds inside the sandbox.
	// Must be in Daytona's preview-port range (3000-9999). Default 8080.
	ListenPort int

	// BinaryPath, if set, is the absolute path to a pre-built
	// clank-host binary for the target sandbox's OS/arch. Skips
	// cross-compile. The binary will be uploaded as-is; mismatched
	// OS/arch will fail at startup inside the sandbox.
	BinaryPath string

	// Arch is the target GOARCH for the cross-compile. Daytona's
	// default snapshots are linux/amd64; override to "arm64" only
	// when targeting an ARM-based snapshot. Ignored when BinaryPath
	// is set. Default "amd64".
	Arch string

	// Snapshot is the Daytona snapshot to launch. Empty means use the
	// SDK's default sandbox image. Override when a custom snapshot
	// (e.g. one preloaded with claude/opencode) becomes available.
	Snapshot string

	// Labels are attached to the created sandbox for human-readable
	// discoverability in the Daytona dashboard. The launcher always
	// adds {"clank.host": "true"}.
	Labels map[string]string

	// ReadyTimeout caps the entire Launch sequence (create sandbox +
	// upload binary + wait for /status 200). Default 60s. Daytona
	// itself is sub-second; the wait is dominated by binary upload on
	// cold runs.
	ReadyTimeout time.Duration
}

// withDefaults returns a copy of opts with zero-value optional fields
// filled in. Required fields (APIKey) are checked separately by validate.
func (o LaunchOptions) withDefaults() LaunchOptions {
	out := o
	if out.ListenPort == 0 {
		out.ListenPort = defaultListenPort
	}
	if out.ReadyTimeout == 0 {
		out.ReadyTimeout = defaultReadyTimeout
	}
	if out.Arch == "" {
		out.Arch = defaultArch
	}
	// Defensive copy: when the caller passes a non-nil Labels map,
	// out.Labels aliases it and our labelClankHost write would
	// mutate the caller's map. Always allocate a fresh map so
	// withDefaults is a pure function of its receiver.
	labels := make(map[string]string, len(out.Labels)+1)
	for k, v := range out.Labels {
		labels[k] = v
	}
	labels[labelClankHost] = "true"
	out.Labels = labels
	return out
}

func (o LaunchOptions) validate() error {
	if o.APIKey == "" {
		return fmt.Errorf("daytona: LaunchOptions.APIKey is required (set DAYTONA_API_KEY)")
	}
	if o.ListenPort < previewPortMin || o.ListenPort > previewPortMax {
		return fmt.Errorf("daytona: LaunchOptions.ListenPort %d out of preview-URL range %d-%d",
			o.ListenPort, previewPortMin, previewPortMax)
	}
	return nil
}

const (
	defaultListenPort   = 8080
	defaultReadyTimeout = 60 * time.Second
	// defaultArch matches Daytona's standard snapshot architecture.
	// Override via LaunchOptions.Arch when targeting an ARM snapshot.
	defaultArch = "amd64"

	// Daytona's preview-URL proxy only exposes ports in this range. A
	// user setting ListenPort outside it would silently get an
	// unreachable host; fail loudly at validation instead.
	previewPortMin = 3000
	previewPortMax = 9999

	// labelClankHost tags every sandbox we create so a stray laptop
	// reboot doesn't leave invisible Daytona spend running.
	labelClankHost = "clank.host"

	// previewTokenHeader is the auth header Daytona's preview proxy
	// expects. Constant lives here so both Launch and tests share it.
	previewTokenHeader = "x-daytona-preview-token"

	// remoteBinaryPath is where we upload clank-host inside the sandbox.
	// /tmp survives until sandbox delete, which is the lifecycle we
	// want — no need for /home persistence.
	remoteBinaryPath = "/tmp/clank-host"
)
