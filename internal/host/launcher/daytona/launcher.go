// Package daytona provisions cloud sandboxes via Daytona's hosted
// control plane and registers each one with the hub catalog as a
// `*hostclient.HTTP`.
//
// We use Daytona's official Go SDK (github.com/daytonaio/daytona/libs/sdk-go)
// rather than hand-rolled REST. Earlier versions of this file built
// the request body manually and got bitten by schema drift — most
// notably, Daytona's `POST /sandbox` does not accept a top-level
// `image` field; you have to wrap a custom image in
// `buildInfo.dockerfileContent`. The SDK does that wrapping for us
// when we pass `types.ImageParams{Image: "<registry>/<name>:<tag>"}`,
// auto-generating `FROM <image>` as the dockerfile content. That's
// what makes Daytona actually pull our image instead of falling back
// to the default Python snapshot.
package daytona

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/daytonaio/daytona/libs/sdk-go/pkg/daytona"
	"github.com/daytonaio/daytona/libs/sdk-go/pkg/types"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
)

// HostPort is the TCP port clank-host listens on inside the sandbox.
// Must match the EXPOSE in cmd/clank-host/Dockerfile and the port we
// ask Daytona for a preview URL on.
const HostPort = 7878

// Options configures the Launcher.
type Options struct {
	// APIKey is the Daytona API key. Required.
	APIKey string

	// Image is the OCI image Daytona will pull. Default:
	// the published clank-host image. Override for dev/CI.
	Image string

	// HubBaseURL is the externally-reachable URL of the cloud hub —
	// the spawned clank-host clones from this and reports session
	// events back here. Required (no default).
	HubBaseURL string

	// HubAuthToken is the bearer token the spawned clank-host needs
	// to authenticate to HubBaseURL.
	HubAuthToken string

	// ExtraEnv is forwarded into the sandbox unchanged. Use this for
	// agent credentials (ANTHROPIC_API_KEY, AWS_*, OPENCODE_API_KEY,
	// etc.). Keys with empty values are dropped.
	ExtraEnv map[string]string

	// APIUrl overrides the default Daytona control-plane endpoint —
	// useful for self-hosted Daytona. Empty = SDK default
	// (https://app.daytona.io/api).
	APIUrl string

	// OrganizationID, when set, scopes operations to a specific
	// Daytona organization. Empty = the API key's default org.
	OrganizationID string

	// ProvisionTimeout caps how long Launch will wait for the
	// sandbox to reach a usable state. Default: 5 minutes — image
	// pulls can be slow on the first run with our ~1GB image.
	ProvisionTimeout time.Duration

	// Resources optionally pins CPU/memory/disk for the sandbox.
	// nil = Daytona defaults (1 CPU, 1 GiB RAM, 3 GiB disk). Our
	// image is debian + bun + opencode + claude-code, which fits
	// but doesn't have much headroom; bump if your sessions OOM.
	Resources *types.Resources

	// SDKClient overrides the daytona.Client constructor. Tests
	// inject a fake; production passes nil and we build one from
	// APIKey/APIUrl.
	SDKClient *daytona.Client
}

// Launcher provisions Daytona sandboxes on demand and registers them
// with the hub catalog as `*hostclient.HTTP` instances. Caller is
// responsible for calling Stop() at hub shutdown to delete sandboxes
// that were created during this process's lifetime.
type Launcher struct {
	opts   Options
	log    *log.Logger
	client *daytona.Client

	// We own delete-on-shutdown for sandboxes we created. Daytona
	// charges by uptime, so leaking sandboxes across crashes is
	// actively harmful. The list is append-only: a Stop() flushes
	// every entry.
	createdMu sync.Mutex
	created   []*daytona.Sandbox
}

// New constructs a Launcher. Returns an error if required options are
// missing — fail-fast at boot rather than at first session.
func New(opts Options, lg *log.Logger) (*Launcher, error) {
	if opts.HubBaseURL == "" {
		return nil, fmt.Errorf("daytona launcher: HubBaseURL is required")
	}
	if opts.HubAuthToken == "" {
		return nil, fmt.Errorf("daytona launcher: HubAuthToken is required")
	}
	if opts.Image == "" {
		opts.Image = "ghcr.io/acksell/clank-host:latest"
	}
	if opts.ProvisionTimeout == 0 {
		opts.ProvisionTimeout = 5 * time.Minute
	}
	if lg == nil {
		lg = log.Default()
	}

	c := opts.SDKClient
	if c == nil {
		if opts.APIKey == "" {
			return nil, fmt.Errorf("daytona launcher: APIKey is required (or pass an SDKClient for tests)")
		}
		var err error
		c, err = daytona.NewClientWithConfig(&types.DaytonaConfig{
			APIKey:         opts.APIKey,
			APIUrl:         opts.APIUrl,
			OrganizationID: opts.OrganizationID,
		})
		if err != nil {
			return nil, fmt.Errorf("daytona launcher: build SDK client: %w", err)
		}
	}

	return &Launcher{opts: opts, log: lg, client: c}, nil
}

// Launch implements hub.HostLauncher. Steps:
//
//  1. client.Create(ctx, ImageParams{...}) — the SDK wraps our image
//     in `FROM <image>` and submits via buildInfo.dockerfileContent.
//     With WaitForStart=true (the SDK default) this blocks until the
//     Daytona-side state reaches "started".
//  2. sandbox.GetPreviewLink(ctx, HostPort) — returns the proxied URL
//     and the token to send as `x-daytona-preview-token`.
//  3. Build a *hostclient.HTTP whose RoundTripper injects that token.
//  4. waitForHostReady — probe clank-host's /status until 2xx.
//     Daytona reports "started" when the container is up, but the
//     entrypoint still has to launch clank-host inside it. Without
//     this probe the hub's first call returns a 502 from the
//     preview proxy.
func (l *Launcher) Launch(ctx context.Context, _ agent.LaunchHostSpec) (host.Hostname, *hostclient.HTTP, error) {
	ctx, cancel := context.WithTimeout(ctx, l.opts.ProvisionTimeout)
	defer cancel()

	envVars := map[string]string{
		"CLANK_HUB_URL":   l.opts.HubBaseURL,
		"CLANK_HUB_TOKEN": l.opts.HubAuthToken,
		"CLANK_HOST_PORT": fmt.Sprintf("%d", HostPort),
	}
	for k, v := range l.opts.ExtraEnv {
		if v == "" {
			continue
		}
		envVars[k] = v
	}

	// Build the wrapping image with an EXPLICIT ENTRYPOINT.
	//
	// Daytona has a documented bug
	// (https://github.com/daytonaio/daytona/issues/3853): when the
	// generated wrapping dockerfile is just `FROM <image>`, the
	// CreateSandbox API does NOT inherit the parent image's
	// ENTRYPOINT. The sandbox falls back to `sleep infinity`, our
	// entrypoint.sh never runs, clank-host never binds, and the
	// preview proxy returns 502s — the exact symptom we hit.
	//
	// daytona.Base(...).Entrypoint(...) emits the entrypoint as an
	// explicit line in the dockerfile, which the API does honor. The
	// path here MUST match the ENTRYPOINT in cmd/clank-host/Dockerfile.
	img := daytona.Base(l.opts.Image).
		Entrypoint([]string{"/usr/local/bin/entrypoint.sh"})

	sandbox, err := l.client.Create(ctx, types.ImageParams{
		SandboxBaseParams: types.SandboxBaseParams{
			EnvVars: envVars,
			Public:  true, // preview proxy still requires the token; "public" only means port previews are reachable from outside the org.
		},
		Image:     img,
		Resources: l.opts.Resources,
	})
	if err != nil {
		return "", nil, fmt.Errorf("create sandbox: %w", err)
	}
	l.createdMu.Lock()
	l.created = append(l.created, sandbox)
	l.createdMu.Unlock()

	hostname := host.Hostname("daytona-" + safeHostnameSuffix(sandbox.ID))

	preview, err := sandbox.GetPreviewLink(ctx, HostPort)
	if err != nil {
		_ = l.deleteBackground(sandbox)
		return "", nil, fmt.Errorf("get preview link: %w", err)
	}
	if preview.URL == "" || preview.Token == "" {
		_ = l.deleteBackground(sandbox)
		return "", nil, fmt.Errorf("preview link response missing url or token: %+v", preview)
	}

	transport := &previewTokenInjector{token: preview.Token}
	client := hostclient.NewHTTP(preview.URL, transport)

	if err := l.waitForHostReady(ctx, client, sandbox.ID); err != nil {
		// Pull the sandbox's entrypoint logs so the user sees *why*
		// clank-host never came up (env-var crash, listen-address
		// mismatch, image misbuild, ...). Best-effort: a stuck
		// sandbox is still leakable, so we always tear it down.
		if logs := l.fetchEntrypointLogs(sandbox); logs != "" {
			err = fmt.Errorf("%w\n--- sandbox entrypoint logs ---\n%s\n--- end logs ---", err, logs)
		}
		_ = l.deleteBackground(sandbox)
		return "", nil, fmt.Errorf("wait for clank-host: %w", err)
	}

	l.log.Printf("daytona launcher: sandbox %s ready at %s (host=%s)", sandbox.ID, preview.URL, hostname)
	return hostname, client, nil
}

// fetchEntrypointLogs is best-effort: returns the empty string when
// the toolbox isn't reachable or the call fails. A short timeout
// keeps a hung sandbox from blocking the surrounding error path.
//
// Daytona's SessionCommandLogsResponse exposes Output (combined),
// Stdout, and Stderr as plain strings. We prefer Stdout+Stderr when
// non-empty (more readable), falling back to Output otherwise.
func (l *Launcher) fetchEntrypointLogs(s *daytona.Sandbox) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := s.Process.GetEntrypointLogs(ctx)
	if err != nil || resp == nil {
		return ""
	}
	var b strings.Builder
	if resp.Stdout != "" {
		b.WriteString("[stdout]\n")
		b.WriteString(resp.Stdout)
		b.WriteString("\n")
	}
	if resp.Stderr != "" {
		b.WriteString("[stderr]\n")
		b.WriteString(resp.Stderr)
		b.WriteString("\n")
	}
	if b.Len() == 0 && resp.Output != "" {
		b.WriteString(resp.Output)
	}
	return b.String()
}

// waitForHostReady polls the spawned clank-host's /status until it
// returns 2xx, or until the context deadline expires. Bridges the
// gap between Daytona's "container is running" signal and
// clank-host's "port is bound and serving" reality.
//
// Errors include the most recent /status error so users see *why*
// the host wasn't ready (proxy 502, transport error, etc.) rather
// than a bare "context deadline exceeded".
func (l *Launcher) waitForHostReady(ctx context.Context, c *hostclient.HTTP, sandboxID string) error {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	var lastErr error
	for {
		// Use a short per-attempt timeout so the proxy can return
		// 502 quickly on a not-yet-bound port instead of stalling
		// behind the parent ctx for many seconds.
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := c.Status(probeCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("sandbox %s never reached ready (last error: %v)", sandboxID, lastErr)
			}
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Stop deletes every sandbox the launcher created in this process.
// Idempotent. Safe to defer at hub shutdown.
func (l *Launcher) Stop() {
	l.createdMu.Lock()
	created := l.created
	l.created = nil
	l.createdMu.Unlock()
	if len(created) == 0 {
		return
	}
	// Use a fresh background context — caller's context may already be
	// cancelled by the time Stop runs (defer at shutdown).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, s := range created {
		if err := s.Delete(ctx); err != nil {
			l.log.Printf("daytona launcher: cleanup %s: %v", s.ID, err)
		}
	}
}

// deleteBackground deletes a sandbox using a fresh context so the
// cleanup isn't tied to whatever errored ctx triggered it. Best
// effort: errors are logged, not returned.
func (l *Launcher) deleteBackground(s *daytona.Sandbox) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Delete(ctx); err != nil {
		l.log.Printf("daytona launcher: cleanup %s: %v", s.ID, err)
		return err
	}
	return nil
}

// safeHostnameSuffix takes the random tail of the sandbox UUID for
// human-readable hostnames. Strips any character that would be invalid
// in a Hostname (none today — UUIDs are hex+dash — but keeps the
// suffix robust to schema drift).
func safeHostnameSuffix(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 {
		id = id[i+1:]
	}
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}
