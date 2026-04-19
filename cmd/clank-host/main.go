// clank-host is the Host plane binary. It owns the BackendManagers and
// SessionBackends and serves the host HTTP API on a Unix socket.
//
// In production it is spawned as a child process by clankd (the Hub).
// clankd connects via internal/host/client.NewUnixHTTP and routes every
// HUB-tagged operation through the wire.
//
// Usage:
//
//	clank-host --socket /path/to/host.sock
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
	"syscall"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostmux "github.com/acksell/clank/internal/host/mux"
	"github.com/acksell/clank/internal/host/repostore"
)

func main() {
	socket := flag.String("socket", "", "Path to Unix socket to listen on (required)")
	dbPath := flag.String("db", "", "Path to host repo registry JSON file (default: <socket-dir>/host.json)")
	flag.Parse()

	if *socket == "" {
		fmt.Fprintln(os.Stderr, "clank-host: --socket is required")
		os.Exit(2)
	}

	resolvedDB := *dbPath
	if resolvedDB == "" {
		// Co-locate the registry file with the socket. The Hub owns the
		// socket dir's lifecycle, so the registry lives and dies with the
		// host's runtime state. Operators that want a different layout
		// pass --db.
		resolvedDB = filepath.Join(filepath.Dir(*socket), "host.json")
	}

	if err := run(*socket, resolvedDB); err != nil {
		log.Fatalf("clank-host: %v", err)
	}
}

func run(socket, dbPath string) error {
	lg := log.New(os.Stderr, "[clank-host] ", log.LstdFlags)

	// Remove stale socket file from a prior crashed run. Best-effort.
	_ = os.Remove(socket)

	rs, err := repostore.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open repo store: %w", err)
	}
	defer rs.Close()

	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socket, err)
	}

	// Build host with both backend managers. Phase 1: hard-coded set
	// matching daemoncli's old population.
	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode:   host.NewOpenCodeBackendManager(),
			agent.BackendClaudeCode: host.NewClaudeBackendManager(),
		},
		Log:       lg,
		RepoStore: rs,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// knownDirs returns nil here: the host has no persistence layer of
	// its own. Phase 1 daemon does its own warm-up via the catalog API
	// after spawning. (The Store lives on the Hub.)
	if err := svc.Init(ctx, func(agent.BackendType) ([]string, error) { return nil, nil }); err != nil {
		lg.Printf("warning: host.Run: %v", err)
	}

	srv := &http.Server{Handler: hostmux.New(svc, lg).Handler()}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serveErr := make(chan error, 1)
	go func() {
		lg.Printf("listening on %s", socket)
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
	_ = os.Remove(socket)
	lg.Println("stopped")
	return nil
}
