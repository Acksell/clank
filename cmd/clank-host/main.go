// clank-host is the Host plane binary. It owns the BackendManagers and
// SessionBackends and serves the host HTTP API on either a Unix socket
// or a TCP port.
//
// In production it is spawned as a child process by clankd (the Hub).
// clankd connects via internal/host/client.NewUnixHTTP for the local
// laptop case, or internal/host/client.NewRemoteHTTP for managed
// remote hosts (e.g. running inside a Daytona sandbox).
//
// Usage:
//
//	clank-host --socket /path/to/host.sock                  # local laptop
//	clank-host --addr 127.0.0.1:8080                        # local TCP
//	clank-host --addr 0.0.0.0:8080 --allow-public           # public bind
//
// Exactly one of --socket or --addr must be set. --allow-public is
// required when --addr binds anything other than a loopback address;
// this is a footgun guard against accidentally exposing the control
// plane on the user's network. Inside a Daytona sandbox the network
// boundary is the security perimeter, so the launcher passes
// --allow-public explicitly.
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
	"syscall"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostmux "github.com/acksell/clank/internal/host/mux"
	"github.com/acksell/clank/internal/socketutil"
)

func main() {
	socket := flag.String("socket", "", "Path to Unix socket to listen on (mutually exclusive with --addr)")
	addr := flag.String("addr", "", "TCP address to listen on, e.g. 127.0.0.1:8080 (mutually exclusive with --socket)")
	allowPublic := flag.Bool("allow-public", false, "Allow --addr to bind a non-loopback interface")
	flag.Parse()

	if err := run(*socket, *addr, *allowPublic); err != nil {
		fmt.Fprintf(os.Stderr, "clank-host: %v\n", err)
		os.Exit(1)
	}
}

func run(socket, addr string, allowPublic bool) error {
	lg := log.New(os.Stderr, "[clank-host] ", log.LstdFlags)

	if (socket == "") == (addr == "") {
		return fmt.Errorf("exactly one of --socket or --addr is required")
	}

	ln, cleanup, err := openListener(socket, addr, allowPublic)
	if err != nil {
		return err
	}
	defer cleanup()

	// Build host with both backend managers. Phase 1: hard-coded set
	// matching daemoncli's old population.
	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode:   host.NewOpenCodeBackendManager(),
			agent.BackendClaudeCode: host.NewClaudeBackendManager(),
		},
		Log: lg,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.Init(ctx, func(agent.BackendType) ([]string, error) { return nil, nil }); err != nil {
		lg.Printf("warning: host.Init: %v", err)
	}

	srv := &http.Server{Handler: hostmux.New(svc, lg).Handler()}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	serveErr := make(chan error, 1)
	go func() {
		lg.Printf("listening on %s", ln.Addr())
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
	lg.Println("stopped")
	return nil
}

// openListener returns the configured net.Listener plus a cleanup
// callback that callers should defer. Cleanup is a no-op for TCP and
// removes the socket file for Unix.
func openListener(socket, addr string, allowPublic bool) (net.Listener, func(), error) {
	if socket != "" {
		return openUnixListener(socket)
	}
	return openTCPListener(addr, allowPublic)
}

func openUnixListener(socket string) (net.Listener, func(), error) {
	// Remove stale socket file from a prior crashed run. Refuses to
	// touch non-socket files so a bad --socket value cannot clobber
	// user data.
	if err := socketutil.RemoveStale(socket); err != nil {
		return nil, nil, err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, nil, fmt.Errorf("listen %s: %w", socket, err)
	}
	// Restrict the socket to the owner. Without an explicit chmod the
	// socket inherits the process umask and any local user could reach
	// the host control plane.
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("chmod %s: %w", socket, err)
	}
	cleanup := func() {
		if err := socketutil.RemoveStale(socket); err != nil {
			fmt.Fprintf(os.Stderr, "[clank-host] socket cleanup: %v\n", err)
		}
	}
	return ln, cleanup, nil
}

func openTCPListener(addr string, allowPublic bool) (net.Listener, func(), error) {
	hostPart, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	// Empty host means "all interfaces" — same risk profile as a
	// non-loopback IP. Force the user to be explicit.
	//
	// TODO(security): --allow-public opens an unauthenticated HTTP
	// listener. Today the only public-bind caller is the Daytona
	// launcher (internal/host/daytona/serve.go), and Daytona's edge
	// gateway gates traffic behind the per-sandbox preview token
	// (x-daytona-preview-token) — without that header no packets
	// reach this process. Any future caller that bypasses the
	// Daytona gateway MUST add bearer-token (or mTLS) auth in the
	// hostmux before calling --allow-public.
	if !isLoopbackAddr(hostPart) && !allowPublic {
		return nil, nil, fmt.Errorf(
			"--addr %q binds a non-loopback interface; pass --allow-public to confirm. "+
				"This guard prevents accidentally exposing clank-host on the network without auth",
			addr,
		)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen tcp %s: %w", addr, err)
	}
	return ln, func() {}, nil
}

// isLoopbackAddr reports whether host resolves exclusively to loopback.
// Empty host (bind-all), unspecified IPs (0.0.0.0, ::), and any
// hostname that doesn't parse as a loopback IP all return false.
//
// "localhost" is special-cased as loopback to avoid a DNS lookup on
// the hot path. Any other hostname is rejected as not-loopback —
// users can pass an explicit 127.0.0.1 if they need it.
func isLoopbackAddr(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
