package tui

// hostsSection is the top section of the sidebar: lists every host the
// hub knows about, plus any KnownHostKinds that aren't yet provisioned
// (so the user can connect them with [c]).
//
// Layout choice: this file owns rendering of just the host rows
// (header + items). The parent SidebarModel handles cursor traversal
// across both sections, the section separator, and key dispatch — see
// sidebar.go.

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/acksell/clank/internal/host"
	hubclient "github.com/acksell/clank/internal/hub/client"
)

// hostsLoadedMsg carries the result of GET /hosts.
type hostsLoadedMsg struct {
	hosts []host.Hostname
	err   error
}

// hostProvisionedMsg carries the result of POST /hosts {kind}. Sent
// after the user presses [c] on a disconnected host row.
type hostProvisionedMsg struct {
	kind     string
	hostID   host.Hostname
	err      error
	duration time.Duration
}

// hostRow is one renderable entry in the hosts section. Either:
//   - a connected host (`connected=true`, name is the registered Hostname)
//   - a known kind that hasn't been provisioned (`connected=false`, name
//     is the kind label that doubles as the provision request body).
//
// The two cases collapse into one row type because the UI layout treats
// them identically: both occupy a cursor slot, both render with a
// status badge, only the right-hand hint and the [c] handler differ.
type hostRow struct {
	name      host.Hostname
	connected bool
	kind      string // non-empty when connected=false; the kind to POST
}

// hostsSection is the host-catalog view inside SidebarModel. The
// catalog refreshes on Init() and after a successful provision; we
// don't poll because /hosts is otherwise immutable for the lifetime
// of clankd.
type hostsSection struct {
	client *hubclient.Client
	rows   []hostRow

	// provisioning is set to the kind we're currently spinning up, so
	// the row can render a "[connecting...]" hint and the [c]
	// keybinding can no-op while in flight. Cleared on
	// hostProvisionedMsg regardless of err.
	provisioning string

	loaded bool // true once hostsLoadedMsg has been received at least once
	err    error
}

// newHostsSection constructs a section bound to client. The initial
// row list is empty until loadHosts completes; the parent sidebar
// kicks off the load via Init().
func newHostsSection(client *hubclient.Client) hostsSection {
	return hostsSection{client: client}
}

// loadHosts fetches GET /hosts. Errors surface in the section's err
// field rather than blowing up the whole TUI — the user can still
// drive the worktree section while the hosts list is broken.
func (h *hostsSection) loadHosts() tea.Cmd {
	client := h.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hosts, err := client.Hosts(ctx)
		return hostsLoadedMsg{hosts: hosts, err: err}
	}
}

// applyLoaded merges the hub's host list with KnownHostKinds, producing
// the row order rendered in the sidebar. Connected hosts come first
// (sorted by registration order from the hub, which puts "local" at
// the top), then any known kinds the user hasn't yet provisioned.
func (h *hostsSection) applyLoaded(hosts []host.Hostname, err error) {
	h.loaded = true
	h.err = err
	if err != nil {
		// Keep stale rows on error so the UI doesn't flash to empty.
		return
	}

	connected := map[host.Hostname]bool{}
	rows := make([]hostRow, 0, len(hosts)+len(KnownHostKinds))
	for _, name := range hosts {
		connected[name] = true
		rows = append(rows, hostRow{name: name, connected: true})
	}
	for _, kind := range KnownHostKinds {
		if connected[host.Hostname(kind)] {
			continue
		}
		rows = append(rows, hostRow{
			name:      host.Hostname(kind),
			connected: false,
			kind:      kind,
		})
	}
	h.rows = rows
}

// provision asks the hub to spin up the row's kind. Returns nil if the
// row is already connected or there's already a provision in flight —
// both are no-ops, not errors, because the user just pressed a key
// and we'd rather absorb the spurious press than surface "duplicate
// provision".
func (h *hostsSection) provision(row hostRow) tea.Cmd {
	if row.connected || row.kind == "" {
		return nil
	}
	if h.provisioning != "" {
		return nil
	}
	h.provisioning = row.kind
	client := h.client
	kind := row.kind
	return func() tea.Msg {
		// Generous timeout: Daytona end-to-end is ~10s, but cold
		// binary upload can extend that. Keep this aligned with the
		// CLI's `clank connect` timeout (90s) so behavior is the
		// same regardless of entry point.
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		start := time.Now()
		resp, err := client.ProvisionHost(ctx, kind)
		return hostProvisionedMsg{
			kind:     kind,
			hostID:   resp.HostID,
			err:      err,
			duration: time.Since(start),
		}
	}
}

// renderRow returns the styled line for one host row. The active host
// gets a check mark; the cursor row gets the standard "> " prefix and
// bold treatment to mirror the worktrees section's selection style.
func (h *hostsSection) renderRow(row hostRow, active bool, selected bool, maxWidth int) string {
	// Truncate name to fit. Hint text is rendered separately on the
	// right edge; reserve a conservative chunk for it.
	name := string(row.name)

	prefix := "  "
	if selected {
		prefix = lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("> ")
	}

	mark := " "
	if active {
		mark = lipgloss.NewStyle().Foreground(successColor).Bold(true).Render("✓")
	}

	nameStyle := lipgloss.NewStyle().Foreground(textColor)
	if selected {
		nameStyle = nameStyle.Bold(true)
	} else if !row.connected {
		nameStyle = nameStyle.Foreground(dimColor)
	}

	// Build hint: "[connecting...]" while in flight, "[c] connect"
	// when disconnected, nothing when connected (mark already says it).
	var hint string
	switch {
	case h.provisioning != "" && h.provisioning == row.kind:
		hint = lipgloss.NewStyle().Foreground(mutedColor).Render("[connecting...]")
	case !row.connected:
		hint = lipgloss.NewStyle().Foreground(mutedColor).Render("[c]")
	}

	// Truncate name if needed so prefix+mark+name+hint fits maxWidth.
	available := maxWidth - len(prefix) - 2 - 1 - lipgloss.Width(hint) - 1
	if available < 4 {
		available = 4
	}
	if len(name) > available {
		name = name[:available-1] + "…"
	}

	line := prefix + mark + " " + nameStyle.Render(name)
	if hint != "" {
		line += " " + hint
	}
	return line
}

// renderHeader returns the "HOSTS" section header line.
func (h *hostsSection) renderHeader() string {
	return lipgloss.NewStyle().
		Foreground(primaryColor).
		Bold(true).
		Render("Hosts")
}

// renderError returns a styled error line for the host section, or
// empty string if no error.
func (h *hostsSection) renderError(maxWidth int) string {
	if h.err == nil {
		return ""
	}
	msg := fmt.Sprintf("hosts: %v", h.err)
	return lipgloss.NewStyle().Foreground(dangerColor).
		Render(truncateStr(msg, maxWidth))
}

// rowAt returns the row at index i, or zero-value if out of range.
func (h *hostsSection) rowAt(i int) (hostRow, bool) {
	if i < 0 || i >= len(h.rows) {
		return hostRow{}, false
	}
	return h.rows[i], true
}

// count returns the number of selectable rows in the hosts section.
func (h *hostsSection) count() int { return len(h.rows) }
