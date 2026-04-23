package tui

// SidebarModel is the navigation sidebar of the inbox layout.
//
// It contains two sections, both selectable with one cursor:
//
//   - Hosts (top): every registered host plus any KnownHostKinds the
//     user can provision. See sidebar_hosts.go.
//   - Worktrees (below the separator): "All" plus every git branch in
//     the active host's repo. Branch ops route through (hostname,
//     gitRef) — branches are addressed logically, not by on-disk path.
//
// Cursor model: linear `cursor int` over both sections. Section
// boundaries are computed at use-time (cursorSection / setCursor) so
// adding rows in either section doesn't require renumbering. Order:
//   [hosts...] [All] [branches...]
//
// Per AGENTS.md "per-method files" rule, host-row rendering lives in
// sidebar_hosts.go; this file is the section orchestrator.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hubclient "github.com/acksell/clank/internal/hub/client"
)

// sidebarWidth is the fixed width of the sidebar (including border).
const sidebarWidth = 30

// sidebarSection identifies which section the cursor is in. Used by
// key dispatch (e.g. [c] only meaningful in hosts; [n] only in
// worktrees) and by selection accessors that should return zero-value
// when the cursor isn't in their section.
type sidebarSection int

const (
	sectionHosts sidebarSection = iota
	sectionWorktrees
)

// branchLoadedMsg carries the result of loading branches from the daemon.
type branchLoadedMsg struct {
	branches []host.BranchInfo
	err      error
}

// branchWorktreeCreatedMsg is sent after a worktree is created for a new branch.
type branchWorktreeCreatedMsg struct {
	branch string
	err    error
}

// branchSessionStatus summarises session states for a single worktree branch.
type branchSessionStatus struct {
	Total    int // Total sessions on this worktree
	Active   int // Visible / in-progress sessions
	Done     int // Sessions marked done
	Archived int // Sessions archived
}

// IsDone returns true when the worktree has sessions and every session is
// either done or archived.
func (s branchSessionStatus) IsDone() bool {
	return s.Total > 0 && s.Active == 0
}

// IsArchived returns true when the worktree has sessions and every session
// is archived (none merely "done").
func (s branchSessionStatus) IsArchived() bool {
	return s.Total > 0 && s.Archived == s.Total
}

// SidebarModel displays hosts + worktrees and allows selection.
type SidebarModel struct {
	client *hubclient.Client
	// projectDir is the cwd the inbox was launched from. Kept for
	// display and for non-branch concerns (project filter); branch
	// operations route through (hostname, gitRef).
	projectDir string
	hostname   host.Hostname
	gitRef     agent.GitRef

	// activeHost is shared with the parent inbox; the sidebar mutates
	// it when the user selects a host row. Nil means "no active host
	// concept" (callers expected to provide one — kept as pointer to
	// avoid copying the persisted state).
	activeHost *ActiveHost

	hosts hostsSection

	branches []host.BranchInfo

	// cursor is the linear index across [hosts...][All][branches...].
	// scroll is the first row of the rendered window.
	cursor int
	scroll int

	// Session status per branch (set by the inbox when sessions are loaded).
	sessionStatus map[string]branchSessionStatus // branch name -> session status summary

	// New branch input mode (worktrees section).
	creating bool
	input    textinput.Model

	focused bool
	width   int
	height  int
	err     error
}

// NewSidebarModel creates a sidebar for the given repo identity.
// activeHost is required — the sidebar's host-row selection mutates
// it. projectDir is retained for display purposes only; branch/worktree
// ops are addressed by (hostname, gitRef).
func NewSidebarModel(client *hubclient.Client, hostname host.Hostname, gitRef agent.GitRef, projectDir string, activeHost *ActiveHost) SidebarModel {
	ti := textinput.New()
	ti.Placeholder = "branch-name"
	ti.CharLimit = 128
	ti.Prompt = "+ "
	styles := ti.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(successColor).Bold(true)
	styles.Focused.Text = lipgloss.NewStyle().Foreground(textColor)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(mutedColor)
	ti.SetStyles(styles)

	return SidebarModel{
		client:     client,
		hostname:   hostname,
		gitRef:     gitRef,
		projectDir: projectDir,
		activeHost: activeHost,
		hosts:      newHostsSection(client),
		input:      ti,
		// Default cursor lands on "All" in worktrees so the user's
		// existing flow (browse worktrees) is unchanged.
	}
}

// Init fetches both the host list and branches concurrently.
func (m *SidebarModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.hosts.loadHosts()}
	if m.hostname != "" && (m.gitRef.LocalPath != "" || m.gitRef.Endpoint != nil) {
		cmds = append(cmds, m.loadBranches())
	}
	// Position cursor on "All" by default (first row of worktrees).
	m.cursor = m.hosts.count()
	return tea.Batch(cmds...)
}

// --- Cursor / section helpers ---

// totalRows is the number of selectable rows across both sections.
// Layout: [N hosts][1 "All"][len(branches) branches].
func (m *SidebarModel) totalRows() int {
	return m.hosts.count() + 1 + len(m.branches)
}

// cursorSection returns which section the cursor is in and the
// section-local index. For sectionWorktrees, idx==0 means the "All"
// row; idx>=1 means branches[idx-1].
func (m *SidebarModel) cursorSection() (sidebarSection, int) {
	n := m.hosts.count()
	if m.cursor < n {
		return sectionHosts, m.cursor
	}
	return sectionWorktrees, m.cursor - n
}

// branchIndex returns the branches[] index for the current cursor, or
// -1 if the cursor is on "All" or in the hosts section.
func (m *SidebarModel) branchIndex() int {
	sec, idx := m.cursorSection()
	if sec != sectionWorktrees || idx == 0 {
		return -1
	}
	bidx := idx - 1
	if bidx >= len(m.branches) {
		return -1
	}
	return bidx
}

// SelectedBranch returns the currently selected branch name. Empty
// string means "All" or a host row is selected.
func (m *SidebarModel) SelectedBranch() string {
	bidx := m.branchIndex()
	if bidx < 0 {
		return ""
	}
	return m.branches[bidx].Name
}

// SelectedWorktreeDir returns the worktree directory for the cursor,
// or "" when not on a branch row.
func (m *SidebarModel) SelectedWorktreeDir() string {
	bidx := m.branchIndex()
	if bidx < 0 {
		return ""
	}
	return m.branches[bidx].WorktreeDir
}

// SelectedBranchInfo returns the BranchInfo for the cursor, or nil
// when not on a branch row.
func (m *SidebarModel) SelectedBranchInfo() *host.BranchInfo {
	bidx := m.branchIndex()
	if bidx < 0 {
		return nil
	}
	return &m.branches[bidx]
}

// SetFocused sets whether the sidebar has keyboard focus.
func (m *SidebarModel) SetFocused(focused bool) { m.focused = focused }

// Focused returns whether the sidebar has keyboard focus.
func (m *SidebarModel) Focused() bool { return m.focused }

// SetSize sets the sidebar dimensions.
func (m *SidebarModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// SetSessionStatus updates the per-branch session status displayed in the sidebar.
func (m *SidebarModel) SetSessionStatus(status map[string]branchSessionStatus) {
	m.sessionStatus = status
}

// WorktreeDirToBranch returns a map from worktree directory path to
// branch name for all branches that have an active worktree.
func (m *SidebarModel) WorktreeDirToBranch() map[string]string {
	result := make(map[string]string, len(m.branches))
	for _, b := range m.branches {
		if b.WorktreeDir != "" {
			result[b.WorktreeDir] = b.Name
		}
	}
	return result
}

// --- Update / message handling ---

// Update handles messages for the sidebar.
func (m *SidebarModel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case branchLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.branches = msg.branches
			m.err = nil
		}
		m.clampCursor()
		return nil

	case branchWorktreeCreatedMsg:
		if msg.err != nil {
			m.err = msg.err
			return nil
		}
		m.creating = false
		m.input.SetValue("")
		return m.loadBranches()

	case hostsLoadedMsg:
		// Capture the cursor's host identity before applyLoaded
		// reshuffles rows. Without this, provisioning daytona
		// (which makes the hub return [local, daytona]) would leave
		// the cursor at index 0 — but row 0 might no longer be the
		// row the user was looking at. We pin local first in
		// hub.Service.Hosts, but other reloads can still reorder.
		var prevHost host.Hostname
		if sec, idx := m.cursorSection(); sec == sectionHosts {
			if row, ok := m.hosts.rowAt(idx); ok {
				prevHost = row.name
			}
		}
		m.hosts.applyLoaded(msg.hosts, msg.err)
		if prevHost != "" {
			if i := m.hosts.indexOf(prevHost); i >= 0 {
				m.cursor = i
			}
		}
		m.clampCursor()
		return nil

	case hostProvisionedMsg:
		m.hosts.provisioning = ""
		if msg.err != nil {
			m.err = fmt.Errorf("connect %s: %w", msg.kind, msg.err)
			return nil
		}
		m.err = nil
		// Re-fetch the host list to flip the row from disconnected
		// to connected. Don't auto-select — the user explicitly chose
		// to connect; whether they want to switch their active host
		// is a separate decision (and a separate keypress).
		return m.hosts.loadHosts()
	}

	if m.creating {
		return m.updateCreating(msg)
	}

	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(keyMsg)
	}

	return nil
}

// handleKey handles keyboard input when focused and not creating.
func (m *SidebarModel) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	msg = normalizeKeyCase(msg)

	maxIdx := m.totalRows() - 1
	if maxIdx < 0 {
		maxIdx = 0
	}

	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
		if m.cursor > 0 {
			m.cursor--
			m.ensureVisible()
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
		if m.cursor < maxIdx {
			m.cursor++
			m.ensureVisible()
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("home", "g"))):
		m.cursor = 0
		m.ensureVisible()
	case key.Matches(msg, key.NewBinding(key.WithKeys("end", "G"))):
		m.cursor = maxIdx
		m.ensureVisible()
	case key.Matches(msg, key.NewBinding(key.WithKeys("n"))):
		// New branch only makes sense in the worktrees section.
		if sec, _ := m.cursorSection(); sec == sectionWorktrees {
			m.creating = true
			m.input.SetValue("")
			return m.input.Focus()
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("c"))):
		// Connect (provision) the highlighted disconnected host.
		if sec, idx := m.cursorSection(); sec == sectionHosts {
			row, ok := m.hosts.rowAt(idx)
			if ok && !row.connected {
				return m.hosts.provision(row)
			}
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("r"))):
		return tea.Batch(m.loadBranches(), m.hosts.loadHosts())
	}

	return nil
}

// updateCreating handles input while creating a new branch.
func (m *SidebarModel) updateCreating(msg tea.Msg) tea.Cmd {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		keyMsg = normalizeKeyCase(keyMsg)

		switch {
		case key.Matches(keyMsg, key.NewBinding(key.WithKeys("esc"))):
			m.creating = false
			m.input.SetValue("")
			return nil

		case key.Matches(keyMsg, key.NewBinding(key.WithKeys("enter"))):
			name := strings.TrimSpace(m.input.Value())
			if name == "" {
				m.creating = false
				return nil
			}
			return m.createWorktree(name)
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

// activateSelectedHost moves the active-host pointer to the host row
// at the cursor. No-op when the cursor isn't in the hosts section or
// the row is a not-yet-provisioned kind. Returns the save error from
// uistate; the in-memory active host is updated regardless.
//
// Called by the inbox on Enter (parent owns the Enter semantics so
// it can also switch focus to the session pane).
func (m *SidebarModel) activateSelectedHost() error {
	if m.activeHost == nil {
		return nil
	}
	sec, idx := m.cursorSection()
	if sec != sectionHosts {
		return nil
	}
	row, ok := m.hosts.rowAt(idx)
	if !ok || !row.connected {
		return nil
	}
	return m.activeHost.Set(row.name)
}

// cursorOnHost returns true when the cursor is currently on a host row.
// Used by inbox to route Enter between "activate host" vs "select branch
// and switch panes".
func (m *SidebarModel) cursorOnHost() bool {
	sec, _ := m.cursorSection()
	return sec == sectionHosts
}

// clampCursor keeps cursor in [0, totalRows-1] after rows change
// (branches loaded, hosts list refreshed). Preserves user position
// when possible.
func (m *SidebarModel) clampCursor() {
	max := m.totalRows() - 1
	if max < 0 {
		max = 0
	}
	if m.cursor > max {
		m.cursor = max
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// --- Rendering ---

// View renders the sidebar.
func (m *SidebarModel) View() string {
	w := m.width
	if w <= 0 {
		w = sidebarWidth
	}

	contentWidth := w - 4 // border (2) + padding (2)
	if contentWidth < 10 {
		contentWidth = 10
	}

	var lines []string

	// Hosts section.
	lines = append(lines, m.hosts.renderHeader())
	lines = append(lines, "")

	activeName := host.Hostname("")
	if m.activeHost != nil {
		activeName = m.activeHost.Name()
	}
	for i, row := range m.hosts.rows {
		selected := m.focused && m.cursor == i
		active := row.connected && row.name == activeName
		lines = append(lines, m.hosts.renderRow(row, active, selected, contentWidth))
	}
	if errLine := m.hosts.renderError(contentWidth); errLine != "" {
		lines = append(lines, errLine)
	}

	// Section separator.
	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(mutedColor).
		Render(strings.Repeat("─", contentWidth)))

	// Worktrees section.
	lines = append(lines, lipgloss.NewStyle().
		Foreground(primaryColor).
		Bold(true).
		Render("Worktrees"))
	lines = append(lines, "")

	// "All" entry — first row of the worktrees section.
	allCursor := m.hosts.count() // linear cursor position for "All"
	lines = append(lines, m.renderAllRow(allCursor))

	// Branch entries.
	for i, b := range m.branches {
		linearIdx := allCursor + 1 + i
		lines = append(lines, m.renderBranch(b, linearIdx, contentWidth))
	}

	// New branch input.
	if m.creating {
		lines = append(lines, "")
		m.input.SetWidth(contentWidth - 2)
		lines = append(lines, "  "+m.input.View())
	}

	// Generic error (branch load).
	if m.err != nil {
		lines = append(lines, "")
		errLine := lipgloss.NewStyle().Foreground(dangerColor).
			Render(truncateStr(m.err.Error(), contentWidth))
		lines = append(lines, errLine)
	}

	content := strings.Join(lines, "\n")

	border := lipgloss.HiddenBorder()
	borderColor := mutedColor
	if m.focused {
		border = lipgloss.RoundedBorder()
		borderColor = primaryColor
	}
	style := lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Width(contentWidth).
		Height(m.listHeight())

	return style.Render(content)
}

// renderAllRow renders the "All" entry of the worktrees section.
// linearIdx is the row's position in the linear cursor numbering so
// we can detect selection without recomputing.
func (m *SidebarModel) renderAllRow(linearIdx int) string {
	if m.cursor == linearIdx && m.focused {
		return lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("> ") +
			lipgloss.NewStyle().Foreground(textColor).Bold(true).Render("All")
	}
	if m.cursor == linearIdx {
		return lipgloss.NewStyle().Foreground(textColor).Render("  All")
	}
	return lipgloss.NewStyle().Foreground(dimColor).Render("  All")
}

// renderBranch renders a single worktree entry with diff stats.
// linearIdx is the row's position in the linear cursor numbering.
func (m *SidebarModel) renderBranch(b host.BranchInfo, linearIdx, maxWidth int) string {
	selected := m.cursor == linearIdx && m.focused

	// Diff stat string: "+123 -45" or empty for the default branch.
	var diffStr string
	if b.LinesAdded > 0 || b.LinesRemoved > 0 {
		diffStr = fmt.Sprintf("+%d -%d", b.LinesAdded, b.LinesRemoved)
	}

	// Session status badge.
	status := m.sessionStatus[b.Name]
	countBadge := ""
	badgeColor := dimColor
	if status.Total > 0 {
		if status.IsArchived() {
			countBadge = fmt.Sprintf(" (%d)", status.Total)
			badgeColor = mutedColor
		} else if status.IsDone() {
			countBadge = fmt.Sprintf(" (%d)", status.Total)
			badgeColor = successColor
		} else {
			countBadge = fmt.Sprintf(" (%d)", status.Active)
		}
	}

	suffixWidth := 0
	if diffStr != "" {
		suffixWidth += len(diffStr) + 1
	}
	suffixWidth += len(countBadge)

	name := b.Name
	maxName := maxWidth - 2 - suffixWidth
	if maxName < 6 {
		maxName = 6
	}
	if len(name) > maxName {
		name = name[:maxName-1] + "…"
	}

	nameStyle := lipgloss.NewStyle()
	switch {
	case selected:
		nameStyle = nameStyle.Foreground(textColor).Bold(true)
	case status.IsArchived():
		nameStyle = nameStyle.Foreground(mutedColor)
	case status.IsDone():
		nameStyle = nameStyle.Foreground(dimColor)
	default:
		nameStyle = nameStyle.Foreground(textColor)
	}

	prefix := "  "
	if selected {
		prefix = lipgloss.NewStyle().Foreground(primaryColor).Bold(true).Render("> ")
	}

	line := prefix + nameStyle.Render(name)

	if diffStr != "" {
		addedStr := fmt.Sprintf("+%d", b.LinesAdded)
		removedStr := fmt.Sprintf("-%d", b.LinesRemoved)
		styledDiff := " " +
			lipgloss.NewStyle().Foreground(successColor).Render(addedStr) +
			" " +
			lipgloss.NewStyle().Foreground(dangerColor).Render(removedStr)
		line += styledDiff
	}

	line += lipgloss.NewStyle().Foreground(badgeColor).Render(countBadge)

	return line
}

// listHeight returns the height available for the body (excluding border).
func (m *SidebarModel) listHeight() int {
	h := m.height - 4
	if h < 5 {
		h = 5
	}
	return h
}

// ensureVisible scrolls to keep the cursor visible. Approximation —
// the sidebar doesn't currently virtualize rows, so this only
// matters once we wire scrolling. Keeps scroll in [0, cursor].
func (m *SidebarModel) ensureVisible() {
	vh := m.listHeight() - 3
	if vh < 1 {
		vh = 1
	}
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+vh {
		m.scroll = m.cursor - vh + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// --- Daemon I/O ---

// loadBranches fetches branches from the daemon for this sidebar's repo.
func (m *SidebarModel) loadBranches() tea.Cmd {
	client := m.client
	hostname := m.hostname
	gitRef := m.gitRef
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		branches, err := client.Host(hostname).ListBranches(ctx, gitRef)
		if err != nil {
			return branchLoadedMsg{err: err}
		}
		return branchLoadedMsg{branches: branches}
	}
}

// createWorktree asks the daemon to create a worktree for the given branch.
func (m *SidebarModel) createWorktree(branch string) tea.Cmd {
	client := m.client
	hostname := m.hostname
	gitRef := m.gitRef
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := client.Host(hostname).ResolveWorktree(ctx, gitRef, branch)
		return branchWorktreeCreatedMsg{branch: branch, err: err}
	}
}
