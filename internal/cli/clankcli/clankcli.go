// Package clankcli provides the root cobra command for the clank binary.
package clankcli

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/cli/daemoncli"
	"github.com/acksell/clank/internal/git"
	"github.com/acksell/clank/internal/gitendpoint"
	"github.com/acksell/clank/internal/host"
	hubclient "github.com/acksell/clank/internal/hub/client"
	"github.com/acksell/clank/internal/tui"
	"github.com/acksell/clank/internal/tui/uistate"
)

// activeHostFromState reads ~/.clank/tui-state.json and returns the
// persisted active host name, defaulting to HostLocal. CLI commands
// that create sessions honor this so `clank code` runs on the same
// host the user last selected in the TUI.
//
// Errors are best-effort: a malformed state file falls back to
// HostLocal rather than blocking the command. The TUI's stricter
// load (which surfaces malformed-file errors to the user) catches
// the same condition with better feedback.
func activeHostFromState() host.Hostname {
	st, err := uistate.Load()
	if err != nil {
		return host.HostLocal
	}
	name := host.Hostname(st.ActiveHost())
	if name == "" {
		return host.HostLocal
	}
	return name
}

// Command returns the root cobra command for the clank binary with all subcommands.
func Command() *cobra.Command {
	root := &cobra.Command{
		Use:   "clank",
		Short: "AI-powered coding session manager",
		Long:  "Clank manages your coding agent sessions and helps you track what's in flight.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInbox()
		},
	}

	root.AddCommand(
		codeCmd(),
		inboxCmd(),
		voiceCmd(),
		connectCmd(),
	)

	return root
}

// --- clank code ---

func codeCmd() *cobra.Command {
	var backend string
	var projectDir string
	var ticketID string
	var worktreeBranch string

	cmd := &cobra.Command{
		Use:   "code [prompt]",
		Short: "Launch a new coding agent session",
		Long: `Launch a new coding agent session managed by the Clank daemon.

If a prompt is provided, the session starts immediately and opens the
session detail TUI. Without a prompt, opens the inbox TUI.

The daemon is auto-started if not already running.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Determine prompt.
			prompt := strings.Join(args, " ")
			activeHost := activeHostFromState()
			if prompt == "" {
				// No prompt — open composing view standalone.
				return runComposing(projectDir, worktreeBranch, activeHost)
			}

			// Determine project directory. Resolve to an absolute path
			// so that GitRef.LocalPath is stable regardless of where
			// the daemon happens to be running from when it consumes
			// the request.
			if projectDir == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				projectDir = cwd
			}
			absProjectDir, err := filepath.Abs(projectDir)
			if err != nil {
				return fmt.Errorf("resolve project dir %q: %w", projectDir, err)
			}
			projectDir = absProjectDir

			// Resolve backend type.
			bt := agent.BackendOpenCode // default
			if backend == "claude" || backend == "claude-code" {
				bt = agent.BackendClaudeCode
			} else if backend != "" && backend != "opencode" {
				return fmt.Errorf("unknown backend: %s (valid: opencode, claude)", backend)
			}

			// Ensure daemon is running.
			client, err := ensureDaemon()
			if err != nil {
				return fmt.Errorf("daemon: %w", err)
			}

			// Subscribe to SSE BEFORE creating the session so we don't miss
			// events emitted during session startup.
			sseCtx, sseCancel := context.WithCancel(context.Background())
			events, err := client.Sessions().Subscribe(sseCtx)
			if err != nil {
				sseCancel()
				return fmt.Errorf("subscribe events: %w", err)
			}

			// Create the session.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			remoteURL, _ := git.RemoteURL(projectDir, "origin") // best-effort; LocalPath alone is sufficient on co-located host

			// Drop LocalPath when targeting a remote host: it's
			// meaningless wire data the host can't read.
			gitRef := agent.GitRef{
				RemoteURL:      remoteURL,
				WorktreeBranch: worktreeBranch,
			}
			if remoteURL != "" {
				ep, err := gitendpoint.Parse(remoteURL)
				if err != nil {
					return fmt.Errorf("parse remote URL %q: %w", remoteURL, err)
				}
				gitRef.Endpoint = ep
			}
			if activeHost == host.HostLocal {
				gitRef.LocalPath = projectDir
			}

			info, err := client.Sessions().Create(ctx, agent.StartRequest{
				Backend:  bt,
				Hostname: string(activeHost),
				GitRef:   gitRef,
				Prompt:   prompt,
				TicketID: ticketID,
			})
			if err != nil {
				sseCancel()
				return fmt.Errorf("create session: %w", err)
			}

			// Open session detail TUI with pre-connected event channel.
			model := tui.NewSessionViewModel(client, info.ID)
			model.SetStandalone(true)
			model.SetEventChannel(events, sseCancel)
			cleanup := redirectLogToFile()
			defer cleanup()
			p := tea.NewProgram(model)
			_, err = p.Run()
			return err
		},
	}

	cmd.Flags().StringVar(&backend, "backend", "", "Backend to use: opencode (default), claude")
	cmd.Flags().StringVar(&projectDir, "project", "", "Project directory (default: current directory)")
	cmd.Flags().StringVar(&ticketID, "ticket", "", "Link to backlog ticket ID")
	cmd.Flags().StringVar(&worktreeBranch, "worktree", "", "Git branch to work on (creates worktree if needed)")
	cmd.Flags().StringVar(&worktreeBranch, "branch", "", "Git branch to work on (creates worktree if needed)")
	_ = cmd.Flags().MarkHidden("branch") // hidden alias for familiarity

	return cmd
}

func inboxCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inbox",
		Short: "Open the agent session inbox",
		Long:  "View and manage daemon-managed coding agent sessions in an interactive TUI.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInbox()
		},
	}
}

// runInbox opens the inbox TUI. Ensures the daemon is running first.
func runInbox() error {
	client, err := ensureDaemon()
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}

	model := tui.NewInboxModel(client)
	cleanup := redirectLogToFile()
	defer cleanup()
	p := tea.NewProgram(model)
	_, err = p.Run()
	return err
}

// runComposing opens the composing view standalone (not inside inbox).
// The user types their first prompt and the session is created on send.
// activeHost is the persisted "where the next session runs" choice;
// the composing view propagates it into the StartRequest.
func runComposing(projectDir, worktreeBranch string, activeHost host.Hostname) error {
	client, err := ensureDaemon()
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}

	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		projectDir = cwd
	}
	absProjectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project dir %q: %w", projectDir, err)
	}
	projectDir = absProjectDir

	model := tui.NewSessionViewComposing(client, projectDir, activeHost)
	model.SetWorktreeBranch(worktreeBranch)
	model.SetStandalone(true)
	cleanup := redirectLogToFile()
	defer cleanup()
	p := tea.NewProgram(model)
	_, err = p.Run()
	return err
}

// redirectLogToFile sends the default logger's output to a PID-scoped
// file so that log.Printf calls from audio goroutines and other
// background work don't overwrite the TUI (stderr is not captured by
// Bubble Tea's alt screen). Returns a cleanup function that should be
// deferred.
func redirectLogToFile() func() {
	path := fmt.Sprintf("/tmp/clank-tui-%d.log", os.Getpid())
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Non-fatal: if we can't open the file, just leave stderr as-is.
		return func() {}
	}
	log.SetOutput(f)
	return func() {
		log.SetOutput(os.Stderr)
		f.Close()
	}
}

// ensureDaemon makes sure the daemon is running, starting it if needed.
// Returns a connected client.
func ensureDaemon() (*hubclient.Client, error) {
	running, _, err := hubclient.IsRunning()
	if err != nil {
		return nil, err
	}

	if !running {
		fmt.Println("Starting daemon...")
		if err := daemoncli.RunStart(false); err != nil {
			return nil, fmt.Errorf("start daemon: %w", err)
		}
	}

	client, err := hubclient.NewDefaultClient()
	if err != nil {
		return nil, err
	}

	// Verify reachable.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		return nil, fmt.Errorf("daemon not reachable: %w", err)
	}

	return client, nil
}
