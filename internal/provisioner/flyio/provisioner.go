// Package flyio implements provisioner.Provisioner using Fly.io
// Sprites (https://sprites.dev). One persistent sprite per user;
// services survive sprite hibernation and the public URL auto-wakes
// on incoming HTTP traffic.
//
// Authentication model: the public URL is set to "public" mode (no
// Sprites-org token required at the edge), and clank-host's bearer-
// token middleware is the only auth gate. The capability-token is
// generated per sprite at create time, baked into the service Args,
// and stored on the host row so it survives daemon restarts.
package flyio

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	sprites "github.com/superfly/sprites-go"

	"github.com/acksell/clank/internal/host"
	"github.com/acksell/clank/internal/provisioner"
	"github.com/acksell/clank/internal/store"
)

// HostPort is the port clank-host listens on inside the sprite. Same
// port the Sprites public URL proxies to by convention; we set it
// explicitly via the Service spec's HTTPPort to be safe across
// future Sprites defaults changes.
const HostPort = 8080

// installPath is where we write the embedded clank-host binary
// inside the sprite. /usr/local/bin is on PATH and survives sprite
// hibernation per the docs.
const installPath = "/usr/local/bin/clank-host"

// serviceName is the registered Service name on the sprite. Stable —
// reused across restarts so the running service auto-resumes from
// hibernation.
const serviceName = "clank-host"

// defaultSpriteNamePrefix is what the user's sprite is named when
// preferences don't override it. The userID is appended (PR 1 hardcodes
// "local" so the default name is "clank-host-local").
const defaultSpriteNamePrefix = "clank-host"

// Options configures the SpritesProvisioner.
type Options struct {
	APIToken         string // SPRITES_TOKEN; required when SDKClient is nil
	OrganizationSlug string // optional; default org used when empty
	Region           string // optional Sprites region

	// SpriteNamePrefix is prepended to the userID to form the sprite
	// name. Empty defaults to "clank-host" so userID="local" yields
	// "clank-host-local".
	SpriteNamePrefix string

	RamMB     int // 0 = sprite default
	CPUs      int // 0 = sprite default
	StorageGB int // 0 = sprite default

	// HubBaseURL/HubAuthToken: passed through to clank-host as
	// --git-sync-source / --git-sync-token so the sprite can clone
	// from the cloud-hub mirror.
	HubBaseURL   string
	HubAuthToken string

	// ProvisionTimeout caps how long EnsureHost waits for the sprite
	// to become reachable. Default: 5 minutes.
	ProvisionTimeout time.Duration

	// SDKClient overrides the sprites.Client constructor. Tests inject
	// a fake; production passes nil.
	SDKClient *sprites.Client
}

// Provisioner manages one persistent Sprite per (userID, "flyio").
type Provisioner struct {
	opts   Options
	log    *log.Logger
	client *sprites.Client
	store  *store.Store

	keyMuMap sync.Mutex
	keyMu    map[string]*sync.Mutex

	cacheMu sync.Mutex
	cache   map[string]*cachedHost
}

type cachedHost struct {
	sprite    *sprites.Sprite
	transport http.RoundTripper
	hostID    string
	hostname  host.Hostname
	url       string
	authToken string
}

// New constructs a Provisioner. Returns an error on missing required
// options or SDK initialization failure.
func New(opts Options, st *store.Store, lg *log.Logger) (*Provisioner, error) {
	if st == nil {
		return nil, fmt.Errorf("flyio provisioner: store is required")
	}
	if opts.HubBaseURL == "" {
		return nil, fmt.Errorf("flyio provisioner: HubBaseURL is required")
	}
	if opts.HubAuthToken == "" {
		return nil, fmt.Errorf("flyio provisioner: HubAuthToken is required")
	}
	if opts.SpriteNamePrefix == "" {
		opts.SpriteNamePrefix = defaultSpriteNamePrefix
	}
	if opts.ProvisionTimeout == 0 {
		opts.ProvisionTimeout = 5 * time.Minute
	}
	if lg == nil {
		lg = log.Default()
	}

	c := opts.SDKClient
	if c == nil {
		if opts.APIToken == "" {
			return nil, fmt.Errorf("flyio provisioner: APIToken is required (or pass an SDKClient for tests)")
		}
		c = sprites.New(opts.APIToken)
	}

	return &Provisioner{
		opts:   opts,
		log:    lg,
		client: c,
		store:  st,
		keyMu:  map[string]*sync.Mutex{},
		cache:  map[string]*cachedHost{},
	}, nil
}

// Stop is a no-op: sprites auto-sleep on idle natively, so explicit
// suspend on daemon shutdown is unnecessary.
func (p *Provisioner) Stop() {}

// EnsureHost implements provisioner.Provisioner.
//
// Detaches from the caller's cancellation: the binary upload alone is
// ~17MB and a cold install runs 30–90s for opencode setup, so a TUI
// request that times out at 10s used to cancel the install partway,
// leaving an unwritable ETXTBSY binary and triggering a reinstall
// storm on every subsequent retry. We use context.WithoutCancel +
// our own ProvisionTimeout so the work runs to completion (or its
// own timeout) regardless of whether the original caller is still
// listening — subsequent requests hit the cache.
//
// The per-userID mutex still serializes concurrent callers; they
// share the same in-flight provision rather than starting parallel
// ones that would race the same sprite.
func (p *Provisioner) EnsureHost(ctx context.Context, userID string) (provisioner.HostRef, error) {
	if userID == "" {
		return provisioner.HostRef{}, fmt.Errorf("flyio provisioner: userID is required")
	}
	mu := p.userMutex(userID)
	mu.Lock()
	defer mu.Unlock()

	// Detach from the caller's cancellation; we still bound work
	// with our own ProvisionTimeout.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.opts.ProvisionTimeout)
	defer cancel()

	// Fast path: in-memory cache from a prior EnsureHost in this
	// process. Sprites auto-wake on traffic, so we don't pre-probe;
	// the next request from upstream callers flows through the public
	// URL and Sprites' edge handles wake transparently.
	if c := p.cacheGet(userID); c != nil {
		return p.refToHost(c), nil
	}

	spriteName := p.spriteNameFor(userID)
	sprite, isNew, authToken, err := p.resolveOrCreate(ctx, userID, spriteName)
	if err != nil {
		return provisioner.HostRef{}, err
	}

	// installAndStart is idempotent: each step probes for its own
	// completion and skips when already done. We always run it (cold
	// or reuse) so half-provisioned sprites — e.g. a cold-create that
	// installed the binary but crashed before installing opencode —
	// self-heal on the next daemon start.
	if err := p.installAndStart(ctx, sprite, authToken); err != nil {
		return provisioner.HostRef{}, err
	}
	_ = isNew // retained on resolveOrCreate's return for future callers

	// Re-read the sprite to pick up the URL field populated after
	// public-mode is set.
	fresh, err := p.client.GetSprite(ctx, spriteName)
	if err != nil {
		return provisioner.HostRef{}, fmt.Errorf("get sprite %s: %w", spriteName, err)
	}
	if fresh.URL == "" {
		return provisioner.HostRef{}, fmt.Errorf("sprite %s has no public URL after provisioning; check sprites-go SDK behavior", spriteName)
	}

	transport := &bearerInjector{token: authToken}
	hostname := host.Hostname("flyio-" + safeHostnameSuffix(spriteName))

	// Probe the sprite's public URL until clank-host actually answers.
	// Sprites' "started" event in waitForServiceStarted only means the
	// process is running; the HTTP server inside clank-host may not
	// have bound its port yet, and Sprites' edge serves a 404 page
	// for that gap. Without this probe we'd cache the HostRef and
	// every subsequent proxy hit would 404 until the user retried
	// long enough for the bind to land.
	if err := waitForSpriteReady(ctx, fresh.URL, transport, p.log); err != nil {
		return provisioner.HostRef{}, fmt.Errorf("sprite %s never reached ready: %w", spriteName, err)
	}

	hostID, err := p.persistRow(ctx, userID, spriteName, string(hostname), fresh.URL, authToken, isNew)
	if err != nil {
		return provisioner.HostRef{}, fmt.Errorf("persist host row: %w", err)
	}

	cached := &cachedHost{
		sprite:    fresh,
		transport: transport,
		hostID:    hostID,
		hostname:  hostname,
		url:       fresh.URL,
		authToken: authToken,
	}
	p.cacheSet(userID, cached)
	return p.refToHost(cached), nil
}

// waitForSpriteReady polls the sprite's public URL with a HEAD on
// /status (cheap, authenticated) until clank-host responds with a
// non-edge status code. The Sprites edge serves a stable 404 HTML
// page when no service is bound to the routed port — we treat that
// as "still warming up" and keep polling until the real host
// responds (200 OK on a configured listener, 401 if auth missing,
// etc — anything that's clearly the host's mux talking).
func waitForSpriteReady(ctx context.Context, baseURL string, transport http.RoundTripper, lg *log.Logger) error {
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()

	client := &http.Client{Transport: transport}
	url := strings.TrimRight(baseURL, "/") + "/status"
	var lastBody string
	var lastStatus int
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		cancel()
		if err == nil {
			lastStatus = resp.StatusCode
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
			resp.Body.Close()
			lastBody = string(body)
			// Anything other than the Sprites edge 404 page means
			// the host's mux is responding. A real clank-host
			// returns 200 on /status; 401/403 also indicate the mux
			// is up (auth path firing).
			if !isSpritesEdge404(resp.StatusCode, body) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("ctx done (last status=%d, body snippet=%q)", lastStatus, snippet(lastBody))
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for sprite to bind (last status=%d, body snippet=%q)", lastStatus, snippet(lastBody))
		case <-t.C:
		}
		_ = lg // reserved for future debug logging
	}
}

// isSpritesEdge404 distinguishes a "no service bound" response from
// a "real host returned 404 for an unknown path". The edge page
// includes "Sprites" in its title; a clank-host 404 (which
// shouldn't happen for /status anyway) wouldn't.
func isSpritesEdge404(status int, body []byte) bool {
	if status != http.StatusNotFound {
		return false
	}
	return bytes.Contains(body, []byte("<title>404 | Sprites"))
}

// snippet trims a body string to a short single-line preview for
// timeout error messages.
func snippet(s string) string {
	const max = 120
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func (p *Provisioner) refToHost(c *cachedHost) provisioner.HostRef {
	return provisioner.HostRef{
		HostID:    c.hostID,
		URL:       c.url,
		Transport: c.transport,
		AuthToken: c.authToken,
		AutoWake:  true, // Sprites edge wakes on traffic
		Hostname:  c.hostname,
	}
}

// resolveOrCreate looks up the persistent sprite for this user, or
// creates one. Returns (sprite, isNew, authToken). On reuse paths,
// authToken is read from the store row; on cold create, a fresh token
// is minted and threaded back to the caller for installAndStart.
func (p *Provisioner) resolveOrCreate(ctx context.Context, userID, spriteName string) (*sprites.Sprite, bool, string, error) {
	row, err := p.store.GetHostByUser(ctx, userID, "flyio")
	if err == nil {
		// Try to fetch by name. If the sprite was deleted out-of-band
		// (e.g. user nuked it via the CLI), clear the row and fall
		// through to recreate.
		sprite, fetchErr := p.client.GetSprite(ctx, row.ExternalID)
		if fetchErr == nil {
			return sprite, false, row.AuthToken, nil
		}
		if isNotFound(fetchErr) {
			p.log.Printf("flyio provisioner: sprite %s for user %s not found upstream; recreating", row.ExternalID, userID)
			if delErr := p.store.DeleteHostByUser(ctx, userID, "flyio"); delErr != nil {
				p.log.Printf("flyio provisioner: clear stale row: %v", delErr)
			}
			// fall through
		} else {
			return nil, false, "", fmt.Errorf("get sprite %s: %w", row.ExternalID, fetchErr)
		}
	} else if !errors.Is(err, store.ErrHostNotFound) {
		return nil, false, "", fmt.Errorf("look up host: %w", err)
	}

	// Cold create: mint auth-token now so it can be baked into the
	// Service Args at create time.
	authToken, err := generateAuthToken()
	if err != nil {
		return nil, false, "", fmt.Errorf("generate auth-token: %w", err)
	}

	cfg := &sprites.SpriteConfig{
		Region:    p.opts.Region,
		RamMB:     p.opts.RamMB,
		CPUs:      p.opts.CPUs,
		StorageGB: p.opts.StorageGB,
	}

	sprite, err := p.createSprite(ctx, spriteName, cfg)
	if err != nil {
		return nil, false, "", err
	}
	return sprite, true, authToken, nil
}

// createSprite calls the right CreateSprite variant based on whether
// an organization slug is configured.
func (p *Provisioner) createSprite(ctx context.Context, name string, cfg *sprites.SpriteConfig) (*sprites.Sprite, error) {
	startCreate := time.Now()
	var (
		sprite *sprites.Sprite
		err    error
	)
	if p.opts.OrganizationSlug != "" {
		// SDK exposes Name (not Slug) on OrganizationInfo. Treat the
		// configured "slug" as the organization name — Sprites uses
		// the same identifier for both purposes today.
		sprite, err = p.client.CreateSpriteWithOrg(ctx, name, cfg, &sprites.OrganizationInfo{Name: p.opts.OrganizationSlug}, nil)
	} else {
		sprite, err = p.client.CreateSprite(ctx, name, cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("create sprite %s: %w", name, err)
	}
	p.log.Printf("flyio provisioner: sprite %s created in %s", name, time.Since(startCreate).Round(time.Millisecond))
	return sprite, nil
}

// installAndStart pushes the embedded clank-host binary into the
// sprite, registers it as a service, and switches the URL to public
// mode. Idempotent: every step probes for its own completion and
// skips when already done, so half-provisioned sprites self-heal on
// the next daemon start.
//
// Sprites' base image ships Claude/Gemini/Codex but NOT opencode, so
// step 2 installs it via bun (with npm fallback). Daytona doesn't
// have this concern because its base image (daytonaio/sandbox) bakes
// in opencode at build time.
func (p *Provisioner) installAndStart(ctx context.Context, sprite *sprites.Sprite, authToken string) error {
	// 0. Wake the sprite. On reuse paths the sprite may have
	//    auto-hibernated since last touched, and the SDK's control-
	//    WebSocket pool has a race where it hands back stale
	//    connections that fail with "use of closed network connection"
	//    on the first write. Hitting the public URL via plain HTTP
	//    triggers Sprites' edge to wake the VM; the URL request
	//    doesn't use the control pool, so no race. Best-effort: a
	//    cold-create sprite has no URL yet, in which case we skip.
	p.wakeViaHTTP(ctx, sprite)

	// 1. Push the clank-host binary if missing or stale.
	if err := p.ensureBinaryInstalled(ctx, sprite); err != nil {
		return err
	}

	// 2. Install opencode runtime. clank-host spawns `opencode` via
	//    PATH; without it every session-create fails with `executable
	//    file not found`.
	if err := p.ensureOpenCodeInstalled(ctx, sprite); err != nil {
		return err
	}

	// 3. Register clank-host as a service if not already there.
	if err := p.ensureServiceRunning(ctx, sprite, authToken); err != nil {
		return err
	}

	// 4. Open the URL. UpdateURLSettings is cheap and idempotent;
	//    re-apply on every run so a manually-disabled URL re-opens
	//    on next daemon start.
	if err := sprite.UpdateURLSettings(ctx, &sprites.URLSettings{Auth: "public"}); err != nil {
		return fmt.Errorf("update sprite URL to public: %w", err)
	}
	return nil
}

// ensureBinaryInstalled writes the embedded clank-host binary to the
// sprite's installPath. Skips the upload when a file of the right
// size is already there — the binary is ~17MB and uploading it on
// every daemon start would dominate provisioning time.
//
// When a stale binary needs replacement, we unlink the old file
// FIRST and only then write. The previous file may still be running
// (clank-host is a sprite Service; the running process is exec'd from
// /usr/local/bin/clank-host), and Linux returns ETXTBSY if you try to
// write to a busy executable. unlink+write works because POSIX keeps
// the old inode alive for the running process while the path now
// resolves to the new file — a standard "atomic binary replacement"
// pattern. The next service restart picks up the new binary; until
// then the running one keeps using its (now-anonymous) inode.
//
// Wrapped in retryClosedConn because the SDK's filesystem ops use
// the same racy control-WebSocket pool as exec; a stale conn in the
// pool surfaces as "use of closed network connection" on the first
// op against a freshly-woken sprite.
func (p *Provisioner) ensureBinaryInstalled(ctx context.Context, sprite *sprites.Sprite) error {
	fsys := sprite.Filesystem()
	wf, ok := fsys.(spriteFSWriter)
	if !ok {
		return fmt.Errorf("sprites filesystem does not support WriteFileContext+RemoveContext (SDK API drift)")
	}
	return p.ensureBinaryInstalledOn(ctx, fsys, wf, installPath, clankHostBinary)
}

// spriteFSWriter is the duck-typed subset of the sprites SDK
// filesystem we need for atomic binary replacement. Extracted so
// tests can inject a stub without standing up a full sprite.
type spriteFSWriter interface {
	WriteFileContext(ctx context.Context, name string, data []byte, perm fs.FileMode) error
	RemoveContext(ctx context.Context, name string) error
}

// ensureBinaryInstalledOn is the testable core of ensureBinaryInstalled.
// stat is the read-only handle for the size probe; wf is the
// write/remove handle for the replacement.
func (p *Provisioner) ensureBinaryInstalledOn(ctx context.Context, stat fs.FS, wf spriteFSWriter, path string, want []byte) error {
	var info fs.FileInfo
	statErr := retryClosedConn(ctx, p.log, func() error {
		var err error
		info, err = fs.Stat(stat, strings.TrimPrefix(path, "/"))
		return err
	})
	if statErr == nil && info.Size() == int64(len(want)) {
		return nil
	}
	if statErr == nil {
		p.log.Printf("flyio provisioner: clank-host binary size mismatch (have %d, want %d); replacing", info.Size(), len(want))
	}

	// Best-effort unlink before write. ENOENT is fine (cold install).
	// We don't gate on the Stat result because a concurrent removal
	// between probe and write would otherwise leave us trying
	// WriteFile on a path that's still ETXTBSY.
	_ = retryClosedConn(ctx, p.log, func() error {
		err := wf.RemoveContext(ctx, path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		// Surface non-ENOENT errors but don't fail the install on
		// them — WriteFile may still succeed in some edge cases.
		p.log.Printf("flyio provisioner: pre-write remove of %s: %v (continuing)", path, err)
		return nil
	})

	return retryClosedConn(ctx, p.log, func() error {
		if err := wf.WriteFileContext(ctx, path, want, 0o755); err != nil {
			return fmt.Errorf("install clank-host binary: %w", err)
		}
		return nil
	})
}

// ensureOpenCodeInstalled probes for opencode at the canonical
// service-PATH location and installs it when missing. Sprites'
// service processes have a system PATH that includes /usr/local/bin
// but not user-local bin dirs (~/.bun/bin etc.), so we install via
// bun (or npm fallback) wherever the package manager wants and then
// symlink into /usr/local/bin where the clank-host service can find
// it.
//
// The probe + install runs every EnsureHost cold-cache miss, so the
// fast path on a fully-provisioned sprite is a single 1-second exec.
// First-time install is 30-90s.
func (p *Provisioner) ensureOpenCodeInstalled(ctx context.Context, sprite *sprites.Sprite) error {
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer probeCancel()
	probeErr := retryClosedConn(probeCtx, p.log, func() error {
		return sprite.CommandContext(probeCtx, "/usr/local/bin/opencode", "--version").Run()
	})
	if probeErr == nil {
		return nil // already installed at the canonical path
	}

	installCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	// Install via bun first (sprites already use bun for the
	// pre-installed Claude/Gemini/Codex CLIs); fall back to npm if
	// bun isn't available.
	//
	// Locating the binary is non-trivial: bun's "opencode-ai" package
	// installs a JS wrapper at $BUN_INSTALL/bin/opencode which dispatches
	// to a platform-specific binary under node_modules/opencode-linux-x64-{musl,glibc}.
	// We MUST symlink the wrapper, not the platform binary — the
	// platform binary's dynamic-linker may be missing (e.g. musl loader
	// on a glibc system), which surfaces as a confusing "not found"
	// from the shell on a successfully-symlinked-but-unrunnable file.
	//
	// To be robust to whatever bun decides about install paths, we
	// VERIFY each candidate by invoking `--version` and only accept
	// it if exit 0. This catches the musl-loader-missing case.
	//
	// Verbose output is intentional — without it, debugging "the
	// installer claimed success but opencode isn't on PATH" requires
	// SSH'ing into the sprite.
	const script = `set -e

echo "::: opencode install"

# Pick a package manager. Sprites have bun for the pre-installed
# AI CLIs, so it should always be present.
PM=""
if command -v bun >/dev/null 2>&1; then
  PM="bun"
  echo "::: using bun ($(bun --version))"
  bun install -g opencode-ai 2>&1
elif command -v npm >/dev/null 2>&1; then
  PM="npm"
  echo "::: using npm ($(npm --version))"
  npm install -g opencode-ai 2>&1
else
  echo "::: ERROR: neither bun nor npm available" >&2
  exit 1
fi

# Try candidate paths in priority order. Wrappers FIRST, package
# binaries LAST — and only accept a candidate that actually executes.
echo "::: locating opencode"

# Bun's global bin dir. Resolved at runtime since the install path
# is sprites-specific (we saw /.sprite/languages/bun/install/global/...).
BUN_BIN=""
if [ "$PM" = "bun" ]; then
  if [ -n "$BUN_INSTALL_BIN" ] && [ -d "$BUN_INSTALL_BIN" ]; then
    BUN_BIN="$BUN_INSTALL_BIN"
  elif [ -n "$BUN_INSTALL" ] && [ -d "$BUN_INSTALL/bin" ]; then
    BUN_BIN="$BUN_INSTALL/bin"
  else
    # Walk up from the bun executable to find its prefix.
    BUN_BIN=$(dirname "$(command -v bun)")
  fi
fi

CANDIDATES="
/usr/local/bin/opencode
$BUN_BIN/opencode
$HOME/.bun/bin/opencode
/root/.bun/bin/opencode
/.bun/bin/opencode
/usr/local/bin/opencode
"

ACTUAL=""
for cand in $CANDIDATES; do
  [ -z "$cand" ] && continue
  if [ -x "$cand" ] && "$cand" --version >/dev/null 2>&1; then
    ACTUAL="$cand"
    echo "::: found working binary at $cand"
    break
  elif [ -x "$cand" ]; then
    echo "::: $cand exists but failed --version"
  fi
done

# Last-resort: filesystem scan, but exclude node_modules so we never
# pick up a platform-specific package binary that needs a missing
# dynamic linker.
if [ -z "$ACTUAL" ]; then
  echo "::: scanning filesystem (excluding node_modules)"
  for cand in $(find / -name 'opencode' -type f -executable -not -path '*/node_modules/*' 2>/dev/null); do
    if "$cand" --version >/dev/null 2>&1; then
      ACTUAL="$cand"
      echo "::: scan found working binary at $cand"
      break
    fi
  done
fi

if [ -z "$ACTUAL" ]; then
  echo "::: ERROR: no working opencode binary found after install" >&2
  echo "::: PATH=$PATH" >&2
  echo "::: BUN_INSTALL=$BUN_INSTALL BUN_INSTALL_BIN=$BUN_INSTALL_BIN" >&2
  echo "::: candidates tried:" >&2
  for cand in $CANDIDATES; do
    [ -z "$cand" ] && continue
    if [ -e "$cand" ]; then
      echo ":::   $cand exists; --version output:" >&2
      "$cand" --version 2>&1 | sed 's/^/:::     /' >&2 || true
    fi
  done >&2
  exit 1
fi

# Symlink to /usr/local/bin so the service's PATH can resolve it.
if [ "$ACTUAL" != "/usr/local/bin/opencode" ]; then
  echo "::: symlinking $ACTUAL -> /usr/local/bin/opencode"
  ln -sf "$ACTUAL" /usr/local/bin/opencode
fi

# Verify the canonical path one more time end-to-end.
echo "::: verifying /usr/local/bin/opencode"
/usr/local/bin/opencode --version
echo "::: done"
`
	var out []byte
	err := retryClosedConn(installCtx, p.log, func() error {
		cmd := sprite.CommandContext(installCtx, "sh", "-c", script)
		var runErr error
		out, runErr = cmd.CombinedOutput()
		return runErr
	})
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if len(trimmed) > 8192 {
			trimmed = "..." + trimmed[len(trimmed)-8192:]
		}
		return fmt.Errorf("install opencode (sprite=%s): %w\n--- install output ---\n%s\n--- end output ---", sprite.Name(), err, trimmed)
	}
	p.log.Printf("flyio provisioner: installed opencode on sprite %s", sprite.Name())
	return nil
}

// ensureServiceRunning registers the clank-host Service if absent,
// or recreates it when its persisted Cmd/Args drifted from what the
// current daemon expects (e.g. a binary upgrade dropped a flag).
//
// Sprites' Service definition is persisted on the sprite — when the
// VM hibernates and wakes, the saved Cmd+Args are exec'd verbatim.
// If those args were stale (e.g. they referenced --git-sync-source,
// removed in PR 3 phase 3c), the new clank-host binary refuses to
// start with "flag provided but not defined", the service crash-
// loops, and the sprite's edge serves 404 to every incoming request.
// Detecting that drift here and recreating the service is what lets
// existing sprites self-heal across daemon upgrades.
func (p *Provisioner) ensureServiceRunning(ctx context.Context, sprite *sprites.Sprite, authToken string) error {
	wantReq := buildServiceRequest(authToken)

	var existing *sprites.ServiceWithState
	var existingErr error
	getErr := retryClosedConn(ctx, p.log, func() error {
		s, err := sprite.GetService(ctx, serviceName)
		existing = s
		existingErr = err
		return err
	})
	if getErr == nil && existing != nil {
		if serviceMatches(&existing.Service, wantReq) {
			// Already registered with the right shape — auto-resumes
			// from sprite hibernation on incoming traffic.
			return nil
		}
		p.log.Printf("flyio provisioner: service %s args drifted; recreating", serviceName)
		if err := retryClosedConn(ctx, p.log, func() error {
			return sprite.DeleteService(ctx, serviceName)
		}); err != nil {
			return fmt.Errorf("delete drifted clank-host service: %w", err)
		}
	} else if getErr != nil && !isNotFound(existingErr) {
		return fmt.Errorf("get clank-host service: %w", getErr)
	}

	var stream *sprites.ServiceStream
	if err := retryClosedConn(ctx, p.log, func() error {
		var err error
		stream, err = sprite.CreateService(ctx, serviceName, wantReq)
		return err
	}); err != nil {
		return fmt.Errorf("create clank-host service: %w", err)
	}
	if err := waitForServiceStarted(stream); err != nil {
		return fmt.Errorf("wait for clank-host service started: %w", err)
	}
	return nil
}

// buildServiceRequest is the canonical Service definition this
// daemon expects. Extracted so ensureServiceRunning can compare a
// persisted service against the same shape it would create.
func buildServiceRequest(authToken string) *sprites.ServiceRequest {
	port := HostPort
	return &sprites.ServiceRequest{
		Cmd: installPath,
		Args: []string{
			"--listen", fmt.Sprintf("tcp://[::]:%d", HostPort),
			"--listen-auth-token", authToken,
		},
		HTTPPort: &port,
	}
}

// serviceMatches reports whether a persisted Service shape matches
// what we'd create now. Auth-token args are wildcarded — token value
// rotates per daemon-cluster restart but the service is still
// reachable through the existing token saved in the store, so
// recreating just because the token changed is wasteful.
func serviceMatches(have *sprites.Service, want *sprites.ServiceRequest) bool {
	if have.Cmd != want.Cmd {
		return false
	}
	if (have.HTTPPort == nil) != (want.HTTPPort == nil) {
		return false
	}
	if have.HTTPPort != nil && want.HTTPPort != nil && *have.HTTPPort != *want.HTTPPort {
		return false
	}
	return argsEquivalent(have.Args, want.Args)
}

// argsEquivalent compares two arg slices, treating
// --listen-auth-token <value> as a wildcard so a token rotation
// doesn't trigger a service recreate. Every other arg must match
// exactly and in order.
func argsEquivalent(have, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	for i := 0; i < len(have); i++ {
		if i > 0 && want[i-1] == "--listen-auth-token" {
			continue // wildcarded value
		}
		if have[i] != want[i] {
			return false
		}
	}
	return true
}

// waitForServiceStarted drains a Service log stream until the
// service either reports started, or surfaces an error/exit.
// Returning early lets EnsureHost respond before the stream is
// fully consumed; the SDK closes the stream on Service teardown.
func waitForServiceStarted(stream *sprites.ServiceStream) error {
	defer stream.Close()
	for {
		evt, err := stream.Next()
		if err != nil {
			return err
		}
		if evt == nil {
			return fmt.Errorf("service stream closed before reaching 'started' state")
		}
		switch evt.Type {
		case "started":
			return nil
		case "error":
			return fmt.Errorf("service start failed: %s", evt.Data)
		case "exit":
			code := -1
			if evt.ExitCode != nil {
				code = *evt.ExitCode
			}
			return fmt.Errorf("service exited (code=%d) before reaching 'started' state", code)
		}
	}
}

// persistRow upserts the host row.
func (p *Provisioner) persistRow(ctx context.Context, userID, externalID, hostname, url, authToken string, isNew bool) (string, error) {
	now := time.Now()

	hostID := ""
	if existing, err := p.store.GetHostByUser(ctx, userID, "flyio"); err == nil {
		hostID = existing.ID
	} else if !errors.Is(err, store.ErrHostNotFound) {
		return "", err
	}
	if hostID == "" {
		hostID = newHostID()
	}

	rec := store.Host{
		ID:         hostID,
		UserID:     userID,
		Provider:   "flyio",
		ExternalID: externalID,
		Hostname:   hostname,
		Status:     store.HostStatusRunning,
		LastURL:    url,
		LastToken:  "", // Sprites have no provider-edge token; leave empty
		AuthToken:  authToken,
		AutoWake:   true,
		UpdatedAt:  now,
	}
	if isNew {
		rec.CreatedAt = now
	}
	if err := p.store.UpsertHost(ctx, rec); err != nil {
		return "", err
	}
	return hostID, nil
}

// SuspendHost is a near-no-op: Sprites auto-hibernate when traffic
// drops, so explicit suspend isn't needed for cost control. We log
// for parity with the Daytona impl and return nil.
func (p *Provisioner) SuspendHost(ctx context.Context, hostID string) error {
	row, err := p.store.GetHostByID(ctx, hostID)
	if err != nil {
		return fmt.Errorf("look up host %s: %w", hostID, err)
	}
	p.log.Printf("flyio provisioner: SuspendHost is a no-op for sprite %s (auto-hibernate)", row.ExternalID)
	return nil
}

// DestroyHost permanently deletes the sprite and the store row.
func (p *Provisioner) DestroyHost(ctx context.Context, hostID string) error {
	row, err := p.store.GetHostByID(ctx, hostID)
	if err != nil {
		return fmt.Errorf("look up host %s: %w", hostID, err)
	}
	if err := p.client.DeleteSprite(ctx, row.ExternalID); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("delete sprite %s: %w", row.ExternalID, err)
		}
		// already gone upstream; proceed to remove the row
	}
	if err := p.store.DeleteHostByID(ctx, hostID); err != nil {
		return fmt.Errorf("delete host row %s: %w", hostID, err)
	}
	p.cacheDrop(row.UserID)
	return nil
}

// userMutex returns (lazily creating) the per-userID mutex. Two
// concurrent EnsureHost calls for the same user serialize so they
// converge on a single sprite.
func (p *Provisioner) userMutex(userID string) *sync.Mutex {
	p.keyMuMap.Lock()
	defer p.keyMuMap.Unlock()
	if mu, ok := p.keyMu[userID]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	p.keyMu[userID] = mu
	return mu
}

func (p *Provisioner) cacheGet(userID string) *cachedHost {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	return p.cache[userID]
}

func (p *Provisioner) cacheSet(userID string, c *cachedHost) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	p.cache[userID] = c
}

func (p *Provisioner) cacheDrop(userID string) {
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	delete(p.cache, userID)
}

// spriteNameFor derives a sprite name from a userID. Trims to a
// length and character set that's safe for Sprites' name validation.
func (p *Provisioner) spriteNameFor(userID string) string {
	suffix := safeSpriteSuffix(userID)
	if suffix == "" {
		// Should not happen — userID is required; fall back to a
		// deterministic placeholder so the error is recoverable in
		// dev rather than a panic.
		suffix = "anonymous"
	}
	return p.opts.SpriteNamePrefix + "-" + suffix
}

// generateAuthToken returns ~256 bits of URL-safe random as the
// bearer-token for clank-host's middleware.
func generateAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newHostID mints a store-internal host ID. Matches the Daytona
// provisioner's ULID format so logs and dashboards see uniformly-
// shaped IDs.
func newHostID() string {
	return ulid.Make().String()
}

// safeSpriteSuffix sanitizes a userID for use in a sprite name.
// Sprites names accept lowercase alphanumerics + hyphen; strip
// everything else.
func safeSpriteSuffix(userID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(userID) {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// safeHostnameSuffix returns the trailing segment of a sprite name,
// capped at 12 chars. Mirrors the Daytona naming convention so
// upstream callers (hub catalog, session metadata) see uniformly-
// shaped Hostname values.
func safeHostnameSuffix(spriteName string) string {
	if i := strings.LastIndex(spriteName, "-"); i >= 0 {
		spriteName = spriteName[i+1:]
	}
	if len(spriteName) > 12 {
		spriteName = spriteName[:12]
	}
	return spriteName
}

// isNotFound checks whether an error looks like a 404 from the
// sprites-go SDK. The SDK doesn't expose a typed not-found error in
// the version we're pinning, so we fall back to a string match. This
// is fragile but contained — if the SDK ever exports a typed error,
// switch to errors.As.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "404")
}

// isClosedConnErr matches the SDK's "use of closed network
// connection" / EOF / unexpected-close errors caused by a stale
// control-WebSocket pool entry. These are the symptom of a known
// race in sprites-go's pool: a connection is checked out before its
// readloop has had a chance to mark it closed. Retrying gives the
// SDK time to evict the dead conn and dial fresh on the next
// checkout.
func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "websocket: close") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset")
}

// retryClosedConn runs fn up to 4 times, retrying with backoff on
// connection-layer errors that signal a stale pool entry in the
// sprites-go SDK. Other errors return immediately. The total
// retry budget is bounded by the provided context.
//
// Backoff: 200ms, 600ms, 1.5s, 3s — total max ~5.3s before giving
// up. The SDK's readloop typically detects a dead conn within a
// few hundred ms, so the first retry usually wins.
func retryClosedConn(ctx context.Context, lg *log.Logger, fn func() error) error {
	delays := []time.Duration{200 * time.Millisecond, 600 * time.Millisecond, 1500 * time.Millisecond, 3 * time.Second}
	var lastErr error
	for attempt, delay := range delays {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isClosedConnErr(err) {
			return err
		}
		if lg != nil {
			lg.Printf("flyio provisioner: control conn closed (attempt %d/%d): %v; retrying in %s", attempt+1, len(delays), err, delay)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return fmt.Errorf("retry canceled: %w (last error: %v)", ctx.Err(), lastErr)
		}
	}
	return fmt.Errorf("after %d retries, control connection still failing: %w", len(delays), lastErr)
}

// wakeViaHTTP triggers Sprites' edge to wake the sprite from
// hibernation by issuing a single HTTP GET to the sprite's public
// URL. This avoids the control-WebSocket pool entirely so the wake
// path can't race with stale connections. Best-effort: a sprite
// with no URL (cold-create before UpdateURLSettings) or a
// transient fetch failure isn't fatal — the subsequent control-
// channel ops will retry on their own.
func (p *Provisioner) wakeViaHTTP(ctx context.Context, sprite *sprites.Sprite) {
	if sprite.URL == "" {
		return
	}
	wakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(wakeCtx, "GET", sprite.URL+"/", nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// A wake-fail isn't fatal; the control-channel ops below
		// retry. Log so the user knows what we tried.
		p.log.Printf("flyio provisioner: wake %s via HTTP: %v (continuing)", sprite.Name(), err)
		return
	}
	resp.Body.Close()
}
