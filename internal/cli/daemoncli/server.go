package daemoncli

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	daemonclient "github.com/acksell/clank/internal/daemonclient"
	"github.com/acksell/clank/internal/socketutil"
	"github.com/acksell/clank/pkg/gateway"
	"github.com/acksell/clank/pkg/provisioner"
	clanksync "github.com/acksell/clank/pkg/sync"
)

// openHubListener creates the listener for the configured mode and a
// cleanup func that removes on-disk artifacts.
func openHubListener(opts ServerOptions) (net.Listener, func(), error) {
	if opts.Listen == "" {
		return openUnixListener()
	}
	addr, err := parseTCPListen(opts.Listen)
	if err != nil {
		return nil, nil, err
	}
	return openTCPListener(addr)
}

func openUnixListener() (net.Listener, func(), error) {
	sockPath, err := daemonclient.SocketPath()
	if err != nil {
		return nil, nil, fmt.Errorf("socket path: %w", err)
	}
	// Probe before unlink so we don't yank an active peer's listener.
	if conn, dialErr := net.DialTimeout("unix", sockPath, 200*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("clankd already running on %s", sockPath)
	}
	if err := socketutil.RemoveStale(sockPath); err != nil {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		listener.Close()
		_ = socketutil.RemoveStale(sockPath)
		return nil, nil, fmt.Errorf("chmod socket: %w", err)
	}
	cleanup := func() {
		if err := socketutil.RemoveStale(sockPath); err != nil {
			log.Printf("socket cleanup: %v", err)
		}
	}
	return listener, cleanup, nil
}

func openTCPListener(addr string) (net.Listener, func(), error) {
	if conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond); dialErr == nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("address already in use: %s", addr)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen tcp %s: %w", addr, err)
	}
	return listener, func() {}, nil
}

// parseTCPListen accepts "tcp://host:port" or "host:port" and returns the
// host:port suitable for net.Listen("tcp", ...).
func parseTCPListen(s string) (string, error) {
	if strings.HasPrefix(s, "tcp://") {
		s = strings.TrimPrefix(s, "tcp://")
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		return "", fmt.Errorf("invalid --listen %q (want tcp://host:port): %w", s, err)
	}
	return s, nil
}

// tcpAuthToken returns the static bearer that gates incoming requests
// to the TCP listener. Read from CLANK_AUTH_TOKEN env. An
// unauthenticated TCP listener would expose every session to the
// network, so we refuse startup when the env is unset.
func tcpAuthToken() (string, error) {
	if v := strings.TrimSpace(os.Getenv("CLANK_AUTH_TOKEN")); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("--listen tcp:// requires CLANK_AUTH_TOKEN env to be set")
}

// runGatewayServer mounts the daemon gateway on opts.Listen.
//
// Modes:
//   - Unix socket (default): laptop mode. File mode is the gate. Sync
//     stays nil — laptop has no S3 access and exposes no sync routes.
//     Push/pull goes through the cloud gateway (prefs.Cloud.GatewayURL).
//   - TCP (opts.Listen non-empty): self-hosted/cloud mode. Wraps with
//     bearer-token middleware enforced before reaching the gateway's
//     own auth. If CLANK_SYNC_S3_BUCKET is set, an embedded sync.Server
//     is built from CLANK_SYNC_S3_* env and mounted under /v1/.
//
// Both modes write the PID file at daemonclient.PIDPath().
func runGatewayServer(prov provisioner.Provisioner, opts ServerOptions) error {
	pidPath, err := daemonclient.PIDPath()
	if err != nil {
		return fmt.Errorf("pid path: %w", err)
	}

	var syncSrv *clanksync.Server
	if opts.Listen != "" {
		// TCP mode: build the embedded sync server when CLANK_SYNC_S3_*
		// env vars are present. Returns nil when unset (TCP without sync
		// still works — useful for proxy-only hubs).
		syncSrv, err = loadSyncFromEnv(context.Background(), log.Default())
		if err != nil {
			return fmt.Errorf("build sync server: %w", err)
		}
		if syncSrv != nil {
			log.Printf("gateway: embedded sync server enabled (S3 bucket=%s)", os.Getenv("CLANK_SYNC_S3_BUCKET"))
		}
	}

	// Pass the sprite-reachable URL of this clankd to the gateway so
	// migrate-back can tell the sprite where to upload its checkpoint.
	// PublicBaseURL is the same value already validated by the cloud
	// provisioners; reuse it. SyncAuthToken is the bearer the sprite
	// presents back to this gateway — same source as tcpAuthToken so
	// the sprite's call passes the bearer middleware.
	syncPublicURL := opts.PublicBaseURL
	syncAuthToken := ""
	if syncSrv != nil {
		if tok, err := tcpAuthToken(); err == nil {
			syncAuthToken = tok
		}
	}

	gw, err := gateway.NewGateway(gateway.Config{
		Provisioner: prov,
		Auth:        gateway.PermissiveAuth{},
		ResolveUserID: func(*http.Request) string {
			return "local" // single-user laptop today
		},
		Sync:          syncSrv,
		SyncPublicURL: syncPublicURL,
		SyncAuthToken: syncAuthToken,
	}, log.Default())
	if err != nil {
		return fmt.Errorf("build gateway: %w", err)
	}

	handler := gw.Handler()
	if opts.Listen != "" {
		token, err := tcpAuthToken()
		if err != nil {
			return err
		}
		handler = bearerAuthMiddleware(handler, token)
	}

	listener, cleanup, err := openHubListener(opts)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		listener.Close()
		return fmt.Errorf("write PID file: %w", err)
	}
	defer os.Remove(pidPath)

	srv := &http.Server{Handler: handler}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("gateway listening on %s", listener.Addr())
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case sig := <-sigCh:
		log.Printf("received signal %v, shutting down gateway", sig)
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("gateway serve: %w", err)
		}
	}

	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("gateway shutdown: %v", err)
	}
	return nil
}

// bearerAuthMiddleware enforces a static bearer with constant-time
// comparison. A mismatch returns 401 without further detail.
func bearerAuthMiddleware(next http.Handler, token string) http.Handler {
	expected := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		provided := []byte(auth[len(prefix):])
		if subtle.ConstantTimeCompare(provided, expected) != 1 {
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
