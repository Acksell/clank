package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hubclient "github.com/acksell/clank/internal/hub/client"
)

// pushResultMsg is emitted when an async push completes. err is non-nil
// on any push failure (auth required, rejected, nothing-to-push, or a
// transport error); on success it carries the remote/branch the host
// actually pushed to so the UI can render a transient confirmation.
type pushResultMsg struct {
	branch string
	result host.PushResult
	err    error
}

// pushBranchCmd pushes branch to origin on the bound host. There is no
// confirmation overlay — publishing a feature branch is non-destructive
// (never --force, see docs/publish_and_branch_defaults.md) so a single
// keypress is safe. Auth is resolved hub-side from the GitRef; the TUI
// never carries tokens.
func pushBranchCmd(client *hubclient.Client, hostname host.Hostname, ref agent.GitRef, branch string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := client.Host(hostname).PushBranch(ctx, ref, branch)
		if err != nil {
			return pushResultMsg{branch: branch, err: err}
		}
		return pushResultMsg{branch: branch, result: res}
	}
}
