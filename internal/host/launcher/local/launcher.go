// Package local implements a HostLauncher that spawns clank-host
// subprocesses on a local TCP port. It is the dev/test stub for the
// persistent-host flow on a single laptop daemon.
//
// Persistence semantics: Launch is idempotent for the lifetime of a
// daemon process — the first call spawns a clank-host subprocess on a
// random port; subsequent calls return the same hostname + URL after
// a /status probe confirms the child is healthy. If the probe fails
// (e.g. the child crashed) the launcher kills any remnant and respawns.
// Subprocesses do not survive daemon restarts, so cross-restart
// persistence is not promised here — that's a cloud-launcher concern.
//
// Stop signals the single child to shut down and waits up to 5 seconds
// for it to exit.
package local

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	"github.com/oklog/ulid/v2"
)

// Launcher manages a single persistent clank-host subprocess for the
// laptop daemon. The process is spawned lazily on the first Launch
// and is cached + reused for subsequent calls.
type Launcher struct {
	opts Options
	log  *log.Logger

	mu      sync.Mutex
	current *child
}

// Options configures the Launcher.
type Options struct {
	// BinPath is the path to the clank-host binary. Empty resolves via
	// PATH at Launch time.
	BinPath string

	// GitSyncSource is the cloud-hub base URL the spawned clank-host
	// should clone from. Empty (the default) means the spawned host
	// clones directly from each session's RemoteURL — useful for
	// laptop-side dev runs but not for sandbox emulation.
	GitSyncSource string

	// GitSyncToken is the bearer token paired with GitSyncSource.
	GitSyncToken string
}

type child struct {
	cmd    *exec.Cmd
	addr   string // tcp://host:port the child bound to
	name   host.Hostname
	client *hostclient.HTTP
}

// New constructs a Launcher. Pass an Options zero-value for the
// simplest "spawn clank-host, no sync source" configuration.
func New(opts Options, lg *log.Logger) *Launcher {
	if lg == nil {
		lg = log.Default()
	}
	return &Launcher{opts: opts, log: lg}
}

// Launch returns the persistent clank-host subprocess for this
// daemon. If one is already running and answering /status, the cached
// (Hostname, *HTTP) is returned without respawning. Otherwise the
// previous child (if any) is killed and a fresh process is spawned.
func (l *Launcher) Launch(ctx context.Context, _ agent.LaunchHostSpec) (host.Hostname, *hostclient.HTTP, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Fast path: existing child probes healthy.
	if l.current != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := l.current.client.Status(probeCtx)
		cancel()
		if err == nil {
			return l.current.name, l.current.client, nil
		}
		l.log.Printf("local launcher: cached child %s probe failed (%v); respawning", l.current.name, err)
		l.killCurrentLocked()
	}

	bin := l.opts.BinPath
	if bin == "" {
		resolved, err := exec.LookPath("clank-host")
		if err != nil {
			return "", nil, fmt.Errorf("clank-host not in PATH: %w", err)
		}
		bin = resolved
	}

	args := []string{"--listen", "tcp://127.0.0.1:0"}
	if l.opts.GitSyncSource != "" {
		args = append(args, "--git-sync-source", l.opts.GitSyncSource)
	}
	if l.opts.GitSyncToken != "" {
		args = append(args, "--git-sync-token", l.opts.GitSyncToken)
	}
	// Use a background context here, not the per-Launch ctx — the child
	// outlives the request that spawned it. CancelFunc is taken care
	// of by Stop().
	cmd := exec.Command(bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start clank-host: %w", err)
	}

	addr, err := waitForListenLine(stderr, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return "", nil, err
	}
	// Continue draining stderr so the child doesn't block on a full
	// pipe. Tag the lines so logs stay legible.
	go drainTagged(stderr, l.log, "[clank-host:"+addr+"] ")

	httpURL := strings.Replace(addr, "tcp://", "http://", 1)
	// Use the random tail of the ULID for the name — the first 10
	// chars are the timestamp prefix and collide for any two launches
	// in the same millisecond. We only spawn one child, but the name
	// must remain unique across daemon restarts so RegisterHost on
	// the hub side treats the new process as a different host (the
	// underlying subprocess truly is a different OS process).
	id := ulid.Make().String()
	name := host.Hostname("local-" + id[len(id)-8:])
	client := hostclient.NewHTTP(httpURL, nil)

	l.current = &child{cmd: cmd, addr: addr, name: name, client: client}
	l.log.Printf("local launcher: spawned host %s at %s", name, httpURL)
	return name, client, nil
}

// Stop signals the persistent clank-host child to shut down and
// waits up to 5 seconds for it to exit. Safe to call from a defer.
// After Stop returns, a subsequent Launch will spawn a fresh process.
func (l *Launcher) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.killCurrentLocked()
}

// killCurrentLocked terminates l.current and clears it. Caller must
// hold l.mu.
func (l *Launcher) killCurrentLocked() {
	c := l.current
	if c == nil {
		return
	}
	l.current = nil
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Signal(os.Interrupt)
	}
	done := make(chan struct{})
	go func() {
		_ = c.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-done
	}
	if c.client != nil {
		_ = c.client.Close()
	}
}

// waitForListenLine reads from the child's stderr until it sees the
// "listening on tcp://addr:port" line printed by clank-host. Returns
// the URL ("tcp://addr:port") or an error if no line appears within
// the timeout.
//
// We deliberately read line-by-line so unrelated log output before the
// listen line doesn't confuse us — clank-host's log prefix lets us
// match precisely.
func waitForListenLine(r io.Reader, timeout time.Duration) (string, error) {
	type result struct {
		addr string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		s := bufio.NewScanner(r)
		// Bigger buffer than default in case logs are wide.
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for s.Scan() {
			line := s.Text()
			if i := strings.Index(line, "listening on tcp://"); i >= 0 {
				addr := strings.TrimSpace(line[i+len("listening on "):])
				ch <- result{addr: addr}
				return
			}
		}
		if err := s.Err(); err != nil {
			ch <- result{err: err}
		} else {
			ch <- result{err: io.EOF}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return "", fmt.Errorf("clank-host did not announce listen addr: %w", r.err)
		}
		return r.addr, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out waiting for clank-host listen line")
	}
}

// drainTagged forwards r's lines to lg with a prefix. Stops on EOF.
func drainTagged(r io.Reader, lg *log.Logger, prefix string) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		lg.Printf("%s%s", prefix, s.Text())
	}
}
