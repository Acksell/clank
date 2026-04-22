package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIsLoopbackAddr_Table is the cheap exhaustive unit covering the
// only place we decide whether a TCP bind needs --allow-public.
//
// Regression guard: the matrix below documents every shape we expect
// to see flow in from the user (CLI) or from the daytona launcher.
func TestIsLoopbackAddr_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false}, // unspecified IPv4 → all interfaces
		{"::", false},      // unspecified IPv6 → all interfaces
		{"", false},        // bare ":8080" → bind-all
		{"192.168.1.5", false},
		{"example.com", false}, // hostnames other than localhost are not trusted
		{"127.0.0.2", true},    // entire 127/8 is loopback
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			if got := isLoopbackAddr(tc.host); got != tc.want {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

// TestRun_RejectsBothSocketAndAddr ensures the CLI fails fast when the
// caller specifies both transports. Equally, neither must be rejected.
func TestRun_RejectsTransportMisconfig(t *testing.T) {
	t.Parallel()

	if err := run("", "", false); err == nil {
		t.Error("run with neither --socket nor --addr: expected error, got nil")
	}
	if err := run("/tmp/x.sock", "127.0.0.1:1", false); err == nil {
		t.Error("run with both --socket and --addr: expected error, got nil")
	}
}

// TestRun_PublicBindRequiresFlag is a unit-level guard on the
// --allow-public footgun. We don't actually open a listener — the
// guard fires before bind. Using port 0 keeps this hermetic.
func TestRun_PublicBindRequiresFlag(t *testing.T) {
	t.Parallel()

	err := run("", "0.0.0.0:0", false)
	if err == nil {
		t.Fatal("expected error for 0.0.0.0 without --allow-public, got nil")
	}
	if !strings.Contains(err.Error(), "allow-public") {
		t.Errorf("error should mention --allow-public, got: %v", err)
	}
}

// TestClankHost_TCPListenerServes is the wire-level integration: build
// the binary, launch it on an ephemeral loopback TCP port, and confirm
// /status responds. This is the daytona transport path minus the
// preview-URL proxy. AGENTS.md: real integration over real sockets,
// no mocks.
func TestClankHost_TCPListenerServes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires building clank-host binary")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "clank-host")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build clank-host: %v", err)
	}

	// Pick a free port by listening then immediately releasing. Race
	// window is small in practice and acceptable for a single test;
	// the alternative (port 0 + parsing stderr) couples the test to
	// log format.
	port := pickFreePort(t)
	addr := net.JoinHostPort("127.0.0.1", port)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "--addr", addr)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start clank-host: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_, _ = cmd.Process.Wait()
	})

	if err := waitForHTTP(ctx, "http://"+addr+"/status"); err != nil {
		t.Fatalf("clank-host /status never came up on %s: %v", addr, err)
	}
}

// TestClankHost_PublicBindExitsNonZero is the integration counterpart
// to TestRun_PublicBindRequiresFlag — verifies the safeguard actually
// terminates the process with a non-zero exit, not just returns an
// error from run().
func TestClankHost_PublicBindExitsNonZero(t *testing.T) {
	if testing.Short() {
		t.Skip("requires building clank-host binary")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "clank-host")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build clank-host: %v", err)
	}

	cmd := exec.Command(binPath, "--addr", "0.0.0.0:0")
	// Capture stderr so a regression that silently allows public bind
	// is at least debuggable from the test log.
	cmd.Stderr = io.Discard
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected non-zero exit, got err=%v", err)
	}
	if exitErr.ExitCode() == 0 {
		t.Errorf("expected non-zero exit code for public bind, got 0")
	}
}

func pickFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		t.Fatalf("split host/port: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close port-picker listener: %v", err)
	}
	return port
}

func waitForHTTP(ctx context.Context, url string) error {
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = errors.New(resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timed out without a concrete error")
	}
	return lastErr
}
