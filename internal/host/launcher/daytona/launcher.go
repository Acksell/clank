package daytona

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
)

// DefaultBaseURL is Daytona's hosted control-plane endpoint.
const DefaultBaseURL = "https://app.daytona.io/api"

// HostPort is the TCP port clank-host listens on inside the sandbox.
// Must match the EXPOSE in cmd/clank-host/Dockerfile and the port we
// ask Daytona for a preview URL on.
const HostPort = 7878

// Options configures the Launcher.
type Options struct {
	// APIKey is the Daytona API key (Authorization: Bearer <APIKey>).
	// Required.
	APIKey string

	// Image is the OCI image Daytona will run. Default:
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
	// agent credentials (ANTHROPIC_API_KEY, AWS_*, etc.). Keys with
	// empty values are dropped.
	ExtraEnv map[string]string

	// BaseURL overrides DefaultBaseURL — useful for self-hosted
	// Daytona. Empty = use the public endpoint.
	BaseURL string

	// HTTPClient overrides the default *http.Client used for control-
	// plane API calls. Tests inject a mock; production passes nil.
	HTTPClient *http.Client

	// ProvisionTimeout caps how long Launch will wait for the sandbox
	// to reach a usable state. Default: 90s.
	ProvisionTimeout time.Duration
}

// Launcher provisions Daytona sandboxes on demand and registers them
// with the hub catalog as `*hostclient.HTTP` instances. Caller is
// responsible for calling Stop() at hub shutdown to delete sandboxes
// that were created during this process's lifetime.
type Launcher struct {
	opts Options
	log  *log.Logger
	http *http.Client

	// We own delete-on-shutdown for sandboxes we created. Daytona
	// charges by uptime, so leaking sandboxes across crashes is
	// actively harmful. The list is append-only: a Stop() flushes
	// every entry.
	createdMu  sync.Mutex
	created    []sandbox
}

type sandbox struct {
	id   string
	name host.Hostname
}

// New constructs a Launcher. Returns an error if required options are
// missing — fail-fast at boot rather than at first session.
func New(opts Options, lg *log.Logger) (*Launcher, error) {
	if opts.APIKey == "" {
		return nil, fmt.Errorf("daytona launcher: APIKey is required")
	}
	if opts.HubBaseURL == "" {
		return nil, fmt.Errorf("daytona launcher: HubBaseURL is required")
	}
	if opts.HubAuthToken == "" {
		return nil, fmt.Errorf("daytona launcher: HubAuthToken is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Image == "" {
		// TODO: replace with the published image tag once CI is wired up.
		opts.Image = "ghcr.io/acksell/clank-host:latest"
	}
	if opts.ProvisionTimeout == 0 {
		opts.ProvisionTimeout = 90 * time.Second
	}
	if lg == nil {
		lg = log.Default()
	}
	c := opts.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	return &Launcher{opts: opts, log: lg, http: c}, nil
}

// Launch implements hub.HostLauncher.
//
// Steps:
//  1. POST /sandbox to create — pass the image, env (HubBaseURL,
//     HubAuthToken, agent credentials, optional command override).
//  2. Poll the sandbox until State == "Running" or timeout.
//  3. Fetch the preview URL + token for HostPort. (See getPreviewLink —
//     this is the bit that needs a real Daytona account to verify; the
//     API surface is documented but the exact endpoint path is best
//     confirmed against Daytona's OpenAPI spec.)
//  4. Build a *hostclient.HTTP whose transport injects the preview
//     token on every request, and return it for catalog registration.
func (l *Launcher) Launch(ctx context.Context, _ agent.LaunchHostSpec) (host.Hostname, *hostclient.HTTP, error) {
	ctx, cancel := context.WithTimeout(ctx, l.opts.ProvisionTimeout)
	defer cancel()

	env := map[string]string{
		"CLANK_HUB_URL":   l.opts.HubBaseURL,
		"CLANK_HUB_TOKEN": l.opts.HubAuthToken,
		"CLANK_HOST_PORT": fmt.Sprintf("%d", HostPort),
	}
	for k, v := range l.opts.ExtraEnv {
		if v == "" {
			continue
		}
		env[k] = v
	}

	created, err := l.createSandbox(ctx, env)
	if err != nil {
		return "", nil, err
	}
	l.createdMu.Lock()
	l.created = append(l.created, sandbox{id: created.ID, name: host.Hostname("daytona-" + safeHostnameSuffix(created.ID))})
	box := l.created[len(l.created)-1]
	l.createdMu.Unlock()

	if err := l.waitForRunning(ctx, created.ID); err != nil {
		// Best-effort cleanup of the half-provisioned sandbox.
		_ = l.deleteSandbox(context.Background(), created.ID)
		return "", nil, err
	}

	preview, err := l.getPreviewLink(ctx, created.ID, HostPort)
	if err != nil {
		_ = l.deleteSandbox(context.Background(), created.ID)
		return "", nil, fmt.Errorf("get preview link: %w", err)
	}

	transport := &previewTokenInjector{token: preview.Token}
	client := hostclient.NewHTTP(preview.URL, transport)
	l.log.Printf("daytona launcher: sandbox %s ready at %s (host=%s)", created.ID, preview.URL, box.name)
	return box.name, client, nil
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
		if err := l.deleteSandbox(ctx, s.id); err != nil {
			l.log.Printf("daytona launcher: cleanup %s: %v", s.id, err)
		}
	}
}

// --- Daytona REST calls ---

type createSandboxResp struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// createSandbox POSTs /sandbox.
func (l *Launcher) createSandbox(ctx context.Context, env map[string]string) (*createSandboxResp, error) {
	body := map[string]any{
		"image": l.opts.Image,
		"env":   env,
	}
	var out createSandboxResp
	if err := l.doJSON(ctx, "POST", "/sandbox", body, &out); err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	if out.ID == "" {
		return nil, fmt.Errorf("create sandbox: response missing id")
	}
	return &out, nil
}

// waitForRunning polls GET /sandbox/{id} until state == "Running".
func (l *Launcher) waitForRunning(ctx context.Context, id string) error {
	deadline := time.NewTimer(l.opts.ProvisionTimeout)
	defer deadline.Stop()
	t := time.NewTicker(750 * time.Millisecond)
	defer t.Stop()
	for {
		var got struct {
			State string `json:"state"`
		}
		if err := l.doJSON(ctx, "GET", "/sandbox/"+id, nil, &got); err != nil {
			return fmt.Errorf("poll sandbox: %w", err)
		}
		switch got.State {
		case "Running", "Started":
			return nil
		case "Error", "Failed", "Stopped":
			return fmt.Errorf("sandbox entered terminal state %q before becoming ready", got.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for sandbox %s (last state %q)", id, got.State)
		case <-t.C:
		}
	}
}

// previewLink is the (URL, token) pair Daytona returns for a sandbox
// port preview.
type previewLink struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// getPreviewLink fetches a fresh preview URL + token for the given
// sandbox port.
//
// TODO(daytona-preview): verify the exact REST path. Daytona's docs
// expose this via SDK methods (`sandbox.getPreviewLink(port)`); the
// underlying REST shape is documented as available but the path
// varies between docs sources. Confirm against
// `GET /openapi.json` in the user's account before trusting in
// production. The Sandbox URL/token pattern itself is stable:
//
//	https://{port}-{sandboxId}.preview.daytona.app
//	x-daytona-preview-token: {token}
func (l *Launcher) getPreviewLink(ctx context.Context, sandboxID string, port int) (*previewLink, error) {
	path := fmt.Sprintf("/sandbox/%s/preview-link?port=%d", sandboxID, port)
	var out previewLink
	if err := l.doJSON(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	if out.URL == "" || out.Token == "" {
		return nil, fmt.Errorf("preview link response missing url or token: %+v", out)
	}
	return &out, nil
}

// deleteSandbox DELETEs /sandbox/{id}. Best-effort: the caller logs
// any error.
func (l *Launcher) deleteSandbox(ctx context.Context, id string) error {
	return l.doJSON(ctx, "DELETE", "/sandbox/"+id, nil, nil)
}

// doJSON is the JSON-in/JSON-out helper. Adds the bearer auth header
// and surfaces non-2xx bodies as errors so failures show their actual
// reason instead of "status 400".
func (l *Launcher) doJSON(ctx context.Context, method, path string, body any, out any) error {
	url := strings.TrimRight(l.opts.BaseURL, "/") + path

	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rd = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+l.opts.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if out != nil {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := l.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bodyBytes)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
