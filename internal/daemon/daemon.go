// Package daemon implements the Clank background daemon.
//
// The daemon manages coding agent sessions (OpenCode, Claude Code) as child
// processes, aggregates their events, and exposes an HTTP API over a Unix
// domain socket for the TUI and CLI to consume.
package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/config"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	"github.com/acksell/clank/internal/store"
	"github.com/acksell/clank/internal/voice"
	"github.com/coder/websocket"
)

// Daemon is the long-lived background process that manages agent sessions.
type Daemon struct {
	sockPath string
	pidPath  string
	listener net.Listener

	mu       sync.RWMutex
	sessions map[string]*managedSession // keyed by daemon session ID
	// subscribers receive all events broadcast by the daemon.
	subMu       sync.RWMutex
	subscribers map[string]chan agent.Event // keyed by subscriber ID

	startTime time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// BackendManagers maps each backend type to its manager. The manager
	// creates SessionBackend instances and owns shared resources (e.g.,
	// OpenCode servers). Managers that also implement agent.AgentLister or
	// agent.SessionDiscoverer gain agent listing and session discovery
	// capabilities automatically.
	//
	// Set BEFORE Run(); Run() consumes this map to construct the embedded
	// host.Service. After Run() the canonical access path is hostClient.
	BackendManagers map[agent.BackendType]agent.BackendManager

	// host is the in-process Host plane. Built in Run() from
	// BackendManagers. Lifecycle (Init/Shutdown) is owned by the Daemon
	// while we're a single process; Phase 2+ moves it to clank-host.
	host *host.Service
	// hostClient is the Hub-side abstraction over host. May be injected
	// by the caller before Run() (production clankd path: HTTP client
	// over a Unix socket to the clank-host subprocess). If nil at Run()
	// entry, the daemon constructs an in-process host from
	// BackendManagers — used by tests and by the legacy single-process
	// path.
	//
	// All HUB-tagged code paths go through this so the call shape matches
	// the wire path.
	hostClient hostclient.Client

	// Store is the optional SQLite persistence layer. When non-nil, session
	// metadata is written through on every mutation and loaded on startup.
	// When nil (e.g. in tests), the daemon operates purely in-memory.
	Store *store.Store

	// primaryAgentsRefreshMu guards primaryAgentsRefreshInFlight.
	primaryAgentsRefreshMu       sync.Mutex
	primaryAgentsRefreshInFlight map[string]bool // keyed by "backend\x00projectDir"

	// voice is the singleton voice session (nil when inactive).
	voice          *voice.Session
	voiceAudioConn *websocket.Conn

	log *log.Logger
}

// managedSession tracks a running agent session.
type managedSession struct {
	info         agent.SessionInfo
	backend      agent.SessionBackend   // nil until started
	watchOnly    bool                   // true when backend was started via Watch() (no prompt sent yet)
	pendingPerms []agent.PermissionData // queue of permission prompts awaiting responses
}

// New creates a new daemon instance. It does not start listening.
func New() (*Daemon, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, fmt.Errorf("config dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir config dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Daemon{
		sockPath:                     filepath.Join(dir, "daemon.sock"),
		pidPath:                      filepath.Join(dir, "daemon.pid"),
		sessions:                     make(map[string]*managedSession),
		subscribers:                  make(map[string]chan agent.Event),
		startTime:                    time.Now(),
		ctx:                          ctx,
		cancel:                       cancel,
		log:                          log.New(os.Stderr, "[clank-daemon] ", log.LstdFlags|log.Lmsgprefix),
		BackendManagers:              make(map[agent.BackendType]agent.BackendManager),
		primaryAgentsRefreshInFlight: make(map[string]bool),
	}, nil
}

// NewWithPaths creates a daemon with explicit socket and PID file paths.
// Used for testing where we don't want to use the default config directory.
func NewWithPaths(sockPath, pidPath string) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())
	return &Daemon{
		sockPath:                     sockPath,
		pidPath:                      pidPath,
		sessions:                     make(map[string]*managedSession),
		subscribers:                  make(map[string]chan agent.Event),
		startTime:                    time.Now(),
		ctx:                          ctx,
		cancel:                       cancel,
		log:                          log.New(os.Stderr, "[clank-daemon] ", log.LstdFlags|log.Lmsgprefix),
		BackendManagers:              make(map[agent.BackendType]agent.BackendManager),
		primaryAgentsRefreshInFlight: make(map[string]bool),
	}
}

// SocketPath returns the Unix socket path for the daemon.
func SocketPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.sock"), nil
}

// PIDPath returns the PID file path.
func PIDPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.pid"), nil
}

// IsRunning checks if a daemon is already running by reading the PID file
// and verifying the process exists.
func IsRunning() (bool, int, error) {
	pidPath, err := PIDPath()
	if err != nil {
		return false, 0, err
	}
	data, err := os.ReadFile(pidPath)
	if os.IsNotExist(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0, nil // corrupt PID file, treat as not running
	}
	// Check if process exists by sending signal 0.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, pid, nil
	}
	err = proc.Signal(syscall.Signal(0))
	if err != nil {
		// Process doesn't exist, clean up stale PID file.
		os.Remove(pidPath)
		sockPath, _ := SocketPath()
		if sockPath != "" {
			os.Remove(sockPath)
		}
		return false, pid, nil
	}
	return true, pid, nil
}

// Run starts the daemon, listening on the Unix socket. It blocks until
// the context is cancelled or a termination signal is received.
func (d *Daemon) Run() error {
	// Clean up stale socket.
	os.Remove(d.sockPath)

	listener, err := net.Listen("unix", d.sockPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.sockPath, err)
	}
	d.listener = listener
	// Make socket accessible.
	if err := os.Chmod(d.sockPath, 0o600); err != nil {
		listener.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	// Write PID file.
	if err := os.WriteFile(d.pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		listener.Close()
		return fmt.Errorf("write PID file: %w", err)
	}

	d.log.Printf("daemon started (pid=%d, socket=%s)", os.Getpid(), d.sockPath)

	// Load persisted sessions from the store (if available).
	if d.Store != nil {
		sessions, err := d.Store.LoadSessions()
		if err != nil {
			d.log.Printf("warning: failed to load sessions from store: %v", err)
		} else {
			d.mu.Lock()
			for _, info := range sessions {
				// Sessions loaded from DB have no backend — they can't
				// be actively busy. Normalize stale statuses to idle so
				// the inbox doesn't show spinners or dead markers for
				// sessions interrupted by a daemon restart.
				if info.Status == agent.StatusBusy || info.Status == agent.StatusStarting || info.Status == agent.StatusDead {
					info.Status = agent.StatusIdle
				}
				d.sessions[info.ID] = &managedSession{info: info, backend: nil}
			}
			d.mu.Unlock()
			if len(sessions) > 0 {
				d.log.Printf("loaded %d sessions from store", len(sessions))
			}
		}
	}

	// Construct the in-process Host plane from the configured
	// BackendManagers, UNLESS the caller injected a HostClient (the
	// production clankd path, where clank-host runs as a subprocess and
	// daemon talks to it over a Unix socket).
	//
	// In InProcess mode the daemon owns the host's lifetime: it must
	// call host.Run / host.Shutdown. In HTTP mode the subprocess owns
	// the host; the daemon only owns the client connection.
	if d.hostClient == nil {
		d.host = host.New(host.Options{
			BackendManagers: d.BackendManagers,
			Log:             d.log,
		})
		d.hostClient = hostclient.NewInProcess(d.host)

		// Initialize backend managers via host.Service. The knownDirs callback
		// is closed over d.Store so warm-up uses the persisted project list.
		knownDirs := func(bt agent.BackendType) ([]string, error) {
			if d.Store == nil {
				return nil, nil
			}
			return d.Store.KnownProjectDirs(bt)
		}
		if err := d.host.Run(d.ctx, knownDirs); err != nil {
			d.log.Printf("warning: host.Run: %v", err)
		}
	}

	// Warm primary agent caches AFTER the reconciler has started. The
	// cache warmer uses GetOrStartServer which will wait for the reconciler
	// to finish starting servers rather than starting them itself.
	d.warmPrimaryAgentCaches()

	mux := http.NewServeMux()
	d.registerRoutes(mux)

	server := &http.Server{Handler: mux}

	// Handle termination signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start HTTP server in background.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			d.log.Printf("http serve error: %v", err)
		}
	}()

	// Wait for shutdown signal or context cancellation.
	select {
	case sig := <-sigCh:
		d.log.Printf("received signal %v, shutting down", sig)
	case <-d.ctx.Done():
		d.log.Printf("context cancelled, shutting down")
	}

	return d.shutdown(server)
}

// shutdown gracefully stops the daemon.
func (d *Daemon) shutdown(server *http.Server) error {
	d.cancel()

	// Stop the voice session if active.
	d.mu.Lock()
	if d.voice != nil {
		d.log.Println("stopping voice session")
		d.voice.Close()
		d.voice = nil
	}
	if d.voiceAudioConn != nil {
		d.voiceAudioConn.CloseNow()
		d.voiceAudioConn = nil
	}
	d.mu.Unlock()

	// Shut down the host plane. host.Shutdown() stops every backend in its
	// registry, which is the canonical owner now that createSession /
	// activateBackend / handleSendMessage all go through hostClient.CreateSession.
	// Daemon must not double-stop.
	if d.host != nil {
		d.host.Shutdown()
	}
	if d.hostClient != nil {
		_ = d.hostClient.Close()
	}

	// Close the persistence store.
	if d.Store != nil {
		if err := d.Store.Close(); err != nil {
			d.log.Printf("error closing store: %v", err)
		}
	}

	// Close all subscriber channels.
	d.subMu.Lock()
	for id, ch := range d.subscribers {
		close(ch)
		delete(d.subscribers, id)
	}
	d.subMu.Unlock()

	// Shutdown HTTP server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		d.log.Printf("http shutdown error: %v", err)
	}

	// Clean up files.
	os.Remove(d.sockPath)
	os.Remove(d.pidPath)

	d.wg.Wait()
	d.log.Printf("daemon stopped")
	return nil
}

// SetLogOutput redirects the daemon's logger to the given writer.
// Call before Run() to capture all daemon output.
func (d *Daemon) SetLogOutput(w io.Writer) {
	d.log.SetOutput(w)
}

// SetHostClient injects a host client. Call before Run(). When set, the
// daemon does NOT construct its own in-process host.Service and the
// caller is responsible for the host plane's lifetime (e.g. the clankd
// subprocess supervisor for clank-host). When unset, daemon falls back
// to building an in-process host from BackendManagers.
func (d *Daemon) SetHostClient(c hostclient.Client) {
	d.hostClient = c
}

// Stop requests the daemon to shut down.
func (d *Daemon) Stop() {
	d.cancel()
}
