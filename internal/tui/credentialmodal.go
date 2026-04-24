package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/gitcred"
	"github.com/acksell/clank/internal/host"
	hubclient "github.com/acksell/clank/internal/hub/client"
)

// credentialModalResultMsg is emitted when the credential modal closes.
// retry is true when the user wants the inbox to re-issue the original
// push (after either pasting a new token or just hitting [r]). saveErr
// is non-nil when a [t]oken paste failed to persist; the inbox surfaces
// it via m.err. The modal handles all token-saving internally so the
// inbox stays ignorant of credential storage paths.
type credentialModalResultMsg struct {
	retry   bool
	saveErr error
}

// credentialModalMode is the modal's internal state machine.
type credentialModalMode int

const (
	// credModalPrompt shows the auth-required error plus action keys
	// ([r]etry / [t]oken / [esc]). This is the entry state.
	credModalPrompt credentialModalMode = iota
	// credModalInput shows a textinput for pasting a PAT. Enter saves
	// + retries; esc returns to credModalPrompt.
	credModalInput
)

// credentialModalModel is shown when a push fails with
// host.ErrPushAuthRequired (after the hub's automatic single-retry has
// already been exhausted). Its job is to (a) tell the user which
// endpoint needs a token and (b) collect one if they have one. The
// modal never carries tokens out of itself — they're written to
// ~/.clank/credentials.json synchronously and the next push picks
// them up via the discoverer stack.
type credentialModalModel struct {
	// Push context — captured at modal creation so [r]etry can
	// re-issue exactly the same push without the inbox having to
	// remember it.
	client   *hubclient.Client
	hostname host.Hostname
	gitRef   agent.GitRef
	branch   string

	// endpointHost is the git remote host (e.g. "github.com"). Used
	// as the storage key when SaveToken is called and rendered in
	// the prompt so the user knows which credential they're
	// supplying.
	endpointHost string

	// pushErr is the original push error displayed to the user. We
	// keep the full error string (rather than a sanitized "auth
	// required") because git's stderr often hints at the cause
	// (e.g. "Support for password authentication was removed").
	pushErr error

	mode  credentialModalMode
	input textinput.Model

	width  int
	height int
}

// newCredentialModal constructs a modal for the given push context.
// endpointHost must be the git endpoint host (the storage key); it
// comes from host.PushAuthRequiredError.EndpointHost. Caller is
// responsible for showing the modal (m.showCredModal = true) and
// calling SetSize.
func newCredentialModal(
	client *hubclient.Client,
	hostname host.Hostname,
	gitRef agent.GitRef,
	branch string,
	endpointHost string,
	pushErr error,
) credentialModalModel {
	ti := textinput.New()
	ti.Placeholder = "ghp_… or gho_…"
	ti.CharLimit = 256
	ti.Prompt = "› "
	// EchoPassword keeps shoulder-surfers and screenshares from
	// catching the token. We don't use EchoNone because zero
	// feedback makes it impossible to tell whether a paste worked.
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	styles := ti.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(primaryColor).Bold(true)
	styles.Focused.Text = lipgloss.NewStyle().Foreground(textColor)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(mutedColor)
	ti.SetStyles(styles)

	return credentialModalModel{
		client:       client,
		hostname:     hostname,
		gitRef:       gitRef,
		branch:       branch,
		endpointHost: endpointHost,
		pushErr:      pushErr,
		mode:         credModalPrompt,
		input:        ti,
	}
}

func (m *credentialModalModel) SetSize(w, h int) {
	m.width = w
	m.height = h
	innerW := m.overlayInnerWidth()
	if innerW > 0 {
		m.input.SetWidth(innerW - 4) // leave room for prompt + padding
	}
}

func (m *credentialModalModel) overlayInnerWidth() int {
	w := 60
	if m.width > 0 && m.width < w+6 {
		w = m.width - 6
	}
	if w < 24 {
		w = 24
	}
	return w
}

// Update routes key events based on the modal's current mode.
// Non-key messages (cursor blink) are forwarded to the textinput so
// the cursor animates while the user is typing.
func (m *credentialModalModel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		msg = normalizeKeyCase(msg)

		switch m.mode {
		case credModalPrompt:
			return m.updatePrompt(msg)
		case credModalInput:
			return m.updateInput(msg)
		}
	}

	if m.mode == credModalInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
	return nil
}

func (m *credentialModalModel) updatePrompt(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
		return func() tea.Msg { return credentialModalResultMsg{} }
	case key.Matches(msg, key.NewBinding(key.WithKeys("r"))):
		// Retry without changing credentials — useful when the
		// user has fixed their `gh auth login` state in another
		// terminal since the previous attempt. The hub's
		// CachingDiscoverer was already invalidated by the first
		// failure, so the next push re-runs the discovery stack
		// from scratch.
		return func() tea.Msg { return credentialModalResultMsg{retry: true} }
	case key.Matches(msg, key.NewBinding(key.WithKeys("t"))):
		m.mode = credModalInput
		return m.input.Focus()
	}
	return nil
}

func (m *credentialModalModel) updateInput(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
		// Bail back to the prompt without saving. Don't close the
		// modal entirely — user might still want [r]etry.
		m.input.Reset()
		m.input.Blur()
		m.mode = credModalPrompt
		return nil
	case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		token := strings.TrimSpace(m.input.Value())
		if token == "" {
			// Empty token is a no-op — leaves them in the input
			// box so they can paste again. Avoids accidentally
			// deleting an existing entry on a stray Enter.
			return nil
		}
		if err := gitcred.FromSettings().SaveToken(m.endpointHost, token); err != nil {
			return func() tea.Msg {
				return credentialModalResultMsg{saveErr: err}
			}
		}
		// Token saved. Close the modal AND retry the push. The
		// fresh token wins on the next discovery pass because
		// SettingsDiscoverer is in the stack.
		return func() tea.Msg { return credentialModalResultMsg{retry: true} }
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

func (m *credentialModalModel) View() string {
	var sb strings.Builder
	innerW := m.overlayInnerWidth()

	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(dangerColor).
		Width(innerW).
		Render("Push needs authentication")
	sb.WriteString(title)
	sb.WriteString("\n")

	sep := lipgloss.NewStyle().
		Foreground(mutedColor).
		Render(strings.Repeat("─", innerW))
	sb.WriteString(sep)
	sb.WriteString("\n\n")

	// Endpoint line — explicit so the user knows which credential
	// store entry will be created.
	endpointLabel := lipgloss.NewStyle().Foreground(dimColor).Render("Endpoint: ")
	endpointVal := lipgloss.NewStyle().Foreground(secondaryColor).Bold(true).Render(m.endpointHost)
	sb.WriteString(endpointLabel + endpointVal)
	sb.WriteString("\n")

	branchLabel := lipgloss.NewStyle().Foreground(dimColor).Render("Branch:   ")
	branchVal := lipgloss.NewStyle().Foreground(textColor).Render(m.branch)
	sb.WriteString(branchLabel + branchVal)
	sb.WriteString("\n\n")

	// Underlying error (truncated to keep modal compact).
	if m.pushErr != nil {
		errLine := truncateForModal(m.pushErr.Error(), innerW)
		errStyled := lipgloss.NewStyle().Foreground(dangerColor).Width(innerW).Render(errLine)
		sb.WriteString(errStyled)
		sb.WriteString("\n\n")
	}

	switch m.mode {
	case credModalPrompt:
		hint := lipgloss.NewStyle().Foreground(dimColor).Render(
			"r: retry   t: paste token   esc: cancel",
		)
		sb.WriteString(hint)
	case credModalInput:
		label := lipgloss.NewStyle().Foreground(dimColor).
			Render(fmt.Sprintf("Personal access token for %s:", m.endpointHost))
		sb.WriteString(label)
		sb.WriteString("\n")
		inputStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(mutedColor).
			Padding(0, 1).
			Width(innerW)
		sb.WriteString(inputStyle.Render(m.input.View()))
		sb.WriteString("\n")
		hint := lipgloss.NewStyle().Foreground(dimColor).Render(
			"enter: save & retry   esc: back",
		)
		sb.WriteString(hint)
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dangerColor).
		Padding(1, 2).
		Render(sb.String())
}

// truncateForModal trims an error string to fit on a single line
// inside the modal. Keeps the leading + trailing edges (skipping the
// middle) because git stderr usually puts the most actionable bits
// at the start ("fatal: …") and the end ("see https://… for a PAT").
func truncateForModal(s string, width int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if width <= 0 || len(s) <= width {
		return s
	}
	if width < 8 {
		return s[:width]
	}
	keep := (width - 3) / 2
	return s[:keep] + "..." + s[len(s)-keep:]
}
