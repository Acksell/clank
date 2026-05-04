// clank-host is the Host plane binary. It owns the BackendManagers and
// SessionBackends and serves the host HTTP API on either a Unix socket
// (default for laptop hubs) or a TCP listener (for cloud sandboxes /
// in-process LocalLauncher tests).
//
// In production it is spawned as a child process by clankd (the Hub).
// clankd connects via internal/host/client and routes every HUB-tagged
// operation through the wire.
//
// Usage:
//
//	clank-host --socket /path/to/host.sock
//	clank-host --listen tcp://127.0.0.1:0    # auto-pick port
//
// On startup prints "listening on <addr>" — parents that picked port 0
// read this to discover the bound address.
//
// On SIGINT/SIGTERM the server shuts down gracefully and host.Service
// stops every registered backend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostmux "github.com/acksell/clank/internal/host/mux"
	hoststore "github.com/acksell/clank/internal/host/store"
	"github.com/acksell/clank/internal/socketutil"
)

func main() {
	socket := flag.String("socket", "", "Path to Unix socket to listen on (mutually exclusive with --listen)")
	listen := flag.String("listen", "", "Listener address: tcp://host:port (use :0 for auto-pick) or unix:///path. Mutually exclusive with --socket.")
	gitSyncSource := flag.String("git-sync-source", "", "Cloud-hub base URL to clone from instead of GitHub (e.g. http://hub.internal:7878). Empty = clone from RemoteURL directly. Used by sandboxes.")
	gitSyncToken := flag.String("git-sync-token", "", "Bearer token paired with --git-sync-source. Injected as Authorization header on clone.")
	listenAuthToken := flag.String("listen-auth-token", os.Getenv("CLANK_HOST_AUTH_TOKEN"), "Bearer token required on every HTTP request. Empty disables the check (laptop-local mode). Defaults to $CLANK_HOST_AUTH_TOKEN.")
	dataDir := flag.String("data-dir", os.Getenv("CLANK_HOST_DATA_DIR"), "Directory for host-side persistent state (host.db). Defaults to $CLANK_HOST_DATA_DIR; if neither is set, falls back to $HOME/.clank-host. PR 3+ stores session metadata here.")
	flag.Parse()

	if *socket == "" && *listen == "" {
		fmt.Fprintln(os.Stderr, "clank-host: --socket or --listen is required")
		os.Exit(2)
	}
	if *socket != "" && *listen != "" {
		fmt.Fprintln(os.Stderr, "clank-host: --socket and --listen are mutually exclusive")
		os.Exit(2)
	}

	addr := *listen
	if addr == "" {
		addr = "unix://" + *socket
	}
	if err := run(addr, *gitSyncSource, *gitSyncToken, *listenAuthToken, *dataDir); err != nil {
		log.Fatalf("clank-host: %v", err)
	}
}

// resolveDataDir returns the host's persistent data directory.
// Resolution: explicit flag > $CLANK_HOST_DATA_DIR (handled by flag
// default) > $HOME/.clank-host. Creates the directory if missing.
func resolveDataDir(dataDir string) (string, error) {
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		dataDir = filepath.Join(home, ".clank-host")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	return dataDir, nil
}

// run binds the listener for addr (a "tcp://host:port" or "unix:///path"
// URL) and serves the host API on it until SIGINT/SIGTERM.
func run(addr, gitSyncSource, gitSyncToken, listenAuthToken, dataDirOpt string) error {
	lg := log.New(os.Stderr, "[clank-host] ", log.LstdFlags)

	ln, kind, sockPath, err := openListener(addr)
	if err != nil {
		return err
	}

	// PR 3: open the host's persistent SQLite for session metadata.
	// Crash on init failure — running without persistence would
	// silently lose session state. The host store lives separately
	// from the daemon's clank.db (which is the provisioner's host
	// registry).
	resolvedDataDir, err := resolveDataDir(dataDirOpt)
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}
	dbPath := filepath.Join(resolvedDataDir, "host.db")
	hostStore, err := hoststore.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open host store %s: %w", dbPath, err)
	}
	defer hostStore.Close()
	lg.Printf("host store opened at %s", dbPath)

	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode:   host.NewOpenCodeBackendManager(),
			agent.BackendClaudeCode: host.NewClaudeBackendManager(),
		},
		Log:           lg,
		GitSyncSource: gitSyncSource,
		GitSyncToken:  gitSyncToken,
		SessionsStore: hostStore,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.Init(ctx, func(agent.BackendType) ([]string, error) { return nil, nil }); err != nil {
		lg.Printf("warning: host.Init: %v", err)
	}

	mux := hostmux.New(svc, lg)
	mux.SetAuthToken(listenAuthToken)
	srv := &http.Server{Handler: mux.Handler()}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serveErr := make(chan error, 1)
	go func() {
		// Print the actual bound address so parents that asked for
		// port :0 (LocalLauncher) can read it from stderr.
		lg.Printf("listening on %s://%s", kind, ln.Addr().String())
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case sig := <-sigCh:
		lg.Printf("received signal %v, shutting down", sig)
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
	}

	shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		lg.Printf("http shutdown: %v", err)
	}
	svc.Shutdown()
	cancel()
	if sockPath != "" {
		if err := socketutil.RemoveStale(sockPath); err != nil {
			lg.Printf("socket cleanup: %v", err)
		}
	}
	lg.Println("stopped")
	return nil
}

// openListener parses addr and binds the appropriate listener. Returns
// the listener, the scheme ("tcp" or "unix"), and the socket path (for
// unix mode; empty for tcp).
func openListener(addr string) (net.Listener, string, string, error) {
	switch {
	case strings.HasPrefix(addr, "tcp://"):
		host := strings.TrimPrefix(addr, "tcp://")
		ln, err := net.Listen("tcp", host)
		if err != nil {
			return nil, "", "", fmt.Errorf("listen tcp %s: %w", host, err)
		}
		return ln, "tcp", "", nil

	case strings.HasPrefix(addr, "unix://"):
		path := strings.TrimPrefix(addr, "unix://")
		// Remove stale socket file from a prior crashed run. Refuses to
		// touch non-socket files so a bad path cannot clobber user data.
		if err := socketutil.RemoveStale(path); err != nil {
			return nil, "", "", err
		}
		ln, err := net.Listen("unix", path)
		if err != nil {
			return nil, "", "", fmt.Errorf("listen %s: %w", path, err)
		}
		// Restrict the socket to the owner.
		if err := os.Chmod(path, 0o600); err != nil {
			_ = ln.Close()
			return nil, "", "", fmt.Errorf("chmod %s: %w", path, err)
		}
		return ln, "unix", path, nil

	default:
		return nil, "", "", fmt.Errorf("unsupported listen address %q (want tcp:// or unix://)", addr)
	}
}
