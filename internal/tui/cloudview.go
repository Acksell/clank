package tui

// cloudview.go — TUI panel for the cloud: Supabase auth
// (sign in / sign up) and a user-info card backed by cloud /me.
//
// Mirrors the providerauth.go pattern: a single tea.Model with a phase
// enum, hand-rolled email/password text inputs, async HTTP wrapped in
// tea.Cmd. Reads/writes session tokens via internal/config.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/acksell/clank/internal/cloud"
	"github.com/acksell/clank/internal/config"
)

// cloudViewPhase walks the user through the auth flow. The TUI
// transitions phases in response to async messages (cloudSignInResultMsg,
// cloudMeResultMsg) and key presses.
type cloudViewPhase int

const (
	cloudPhaseLoading        cloudViewPhase = iota
	cloudPhaseNotConfigured                 // endpoints missing in prefs
	cloudPhaseSignedOut                     // login/signup form
	cloudPhaseAuthenticating                // spinner; HTTP in flight
	cloudPhaseSignedIn                      // me-card + actions
	cloudPhaseError                         // message + retry
)

// cloudFocus tracks which form field the keyboard is on while the
// user is in cloudPhaseSignedOut.
type cloudFocus int

const (
	cloudFocusEmail cloudFocus = iota
	cloudFocusPassword
	cloudFocusSubmit
)

// cloudMode toggles between sign-in and sign-up flows. Same form;
// different submit handler.
type cloudMode int

const (
	cloudModeSignIn cloudMode = iota
	cloudModeSignUp
)

// --- async messages -------------------------------------------------

// cloudSignInResultMsg is the response from cloud.Client.SignIn /
// SignUp. err is non-nil on any failure (including 401).
type cloudSignInResultMsg struct {
	session *cloud.Session
	err     error
}

// cloudMeResultMsg is the response from cloud.Client.Me. err is
// cloud.ErrNotSignedUp, cloud.ErrUnauthorized, or a transport error.
type cloudMeResultMsg struct {
	me  *cloud.MeResponse
	err error
}

// --- model ----------------------------------------------------------

type cloudView struct {
	width, height int
	focused       bool

	phase cloudViewPhase
	mode  cloudMode
	focus cloudFocus

	// email / password textinputs.
	email    textinput.Model
	password textinput.Model

	// spinner shown during cloudPhaseAuthenticating.
	spinner spinner.Model

	// session is the most recent successful auth result. Persisted to
	// prefs on transition to cloudPhaseSignedIn.
	session *cloud.Session

	// me is the most recent /me response, used to render the info card.
	me *cloud.MeResponse

	// err carries the message rendered in cloudPhaseError.
	err string

	// endpoints are loaded from prefs on Init / re-entry.
	endpoints cloud.Endpoints

	// client is constructed from endpoints once they're known. Nil
	// until cloudPhaseSignedOut or later.
	client *cloud.Client
}

func newCloudView() cloudView {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(primaryColor)

	return cloudView{
		phase:    cloudPhaseLoading,
		mode:     cloudModeSignIn,
		focus:    cloudFocusEmail,
		email:    newCloudInput("you@example.com", false),
		password: newCloudInput("password", true),
		spinner:  sp,
	}
}

func newCloudInput(placeholder string, masked bool) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = 256
	ti.Prompt = "› "
	if masked {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	styles := ti.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(primaryColor)
	styles.Focused.Text = lipgloss.NewStyle().Foreground(textColor)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(mutedColor)
	ti.SetStyles(styles)
	ti.SetWidth(48)
	return ti
}

// Init is called when the inbox transitions to screenCloud. Loads
// prefs to decide between cloudPhaseNotConfigured, cloudPhaseSignedOut,
// or cloudPhaseSignedIn (with a /me refetch).
func (m *cloudView) Init() tea.Cmd {
	prefs, _ := config.LoadPreferences()
	m.endpoints = resolveEndpoints(&prefs)
	m.client = cloud.New(m.endpoints, nil)

	if !endpointsConfigured(m.endpoints) {
		m.phase = cloudPhaseNotConfigured
		return nil
	}

	// If we already have a session and it hasn't expired, attempt /me
	// to verify it's still valid and to populate the user-info card.
	if prefs.Cloud != nil && prefs.Cloud.AccessToken != "" && !cloudTokenExpired(prefs.Cloud) {
		m.session = &cloud.Session{
			AccessToken:  prefs.Cloud.AccessToken,
			RefreshToken: prefs.Cloud.RefreshToken,
			UserID:       prefs.Cloud.UserID,
			UserEmail:    prefs.Cloud.UserEmail,
			ExpiresAt:    prefs.Cloud.ExpiresAt,
		}
		m.phase = cloudPhaseAuthenticating
		return tea.Batch(m.spinner.Tick, m.fetchMeCmd())
	}

	m.phase = cloudPhaseSignedOut
	m.email.SetValue("")
	m.password.SetValue("")
	m.email.Focus()
	m.password.Blur()
	m.focus = cloudFocusEmail
	return nil
}

func (m *cloudView) SetSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *cloudView) SetFocused(f bool) {
	m.focused = f
	if !f {
		m.email.Blur()
		m.password.Blur()
		return
	}
	m.applyFocus()
}

// IsConfigured reports whether the prefs hold endpoint URLs. Used by
// the inbox to decide whether the Cloud sidebar item should be enabled.
func (m *cloudView) IsConfigured() bool {
	return endpointsConfigured(m.endpoints)
}

// Update handles the panel's messages. The inbox dispatches here when
// screen == screenCloud.
func (m cloudView) Update(msg tea.Msg) (cloudView, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case cloudSignInResultMsg:
		if msg.err != nil {
			m.phase = cloudPhaseError
			m.err = "sign-in failed: " + msg.err.Error()
			return m, nil
		}
		if msg.session.AccessToken == "" {
			// Email-confirmation flow: Supabase issued no tokens.
			m.phase = cloudPhaseError
			m.err = "check your inbox to confirm your email, then sign in"
			return m, nil
		}
		m.session = msg.session
		_ = persistSession(msg.session)
		// Fetch /me to populate the info card.
		m.phase = cloudPhaseAuthenticating
		return m, tea.Batch(m.spinner.Tick, m.fetchMeCmd())

	case cloudMeResultMsg:
		if errors.Is(msg.err, cloud.ErrUnauthorized) {
			// Token expired — clear and re-prompt.
			m.session = nil
			_ = clearSession()
			m.phase = cloudPhaseSignedOut
			m.email.Focus()
			m.focus = cloudFocusEmail
			m.err = "session expired; please sign in again"
			return m, nil
		}
		if errors.Is(msg.err, cloud.ErrNotSignedUp) {
			// Authenticated but no the cloud row yet — surface as the
			// info card with a "you haven't signed up" hint instead
			// of bouncing back to the form. The user can run
			// `curl -X POST /signup` (or hit a future button) to fix.
			m.me = nil
			m.phase = cloudPhaseSignedIn
			m.err = "Authenticated, but you haven't completed signup. Run /signup via the cloud to claim a handle."
			return m, nil
		}
		if msg.err != nil {
			m.phase = cloudPhaseError
			m.err = "fetching profile: " + msg.err.Error()
			return m, nil
		}
		m.me = msg.me
		m.err = ""
		m.phase = cloudPhaseSignedIn
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m cloudView) handleKey(msg tea.KeyPressMsg) (cloudView, tea.Cmd) {
	switch m.phase {
	case cloudPhaseSignedOut:
		return m.handleKeySignedOut(msg)
	case cloudPhaseSignedIn:
		return m.handleKeySignedIn(msg)
	case cloudPhaseError:
		// Any key returns to the form.
		m.phase = cloudPhaseSignedOut
		m.err = ""
		m.applyFocus()
		return m, nil
	}
	return m, nil
}

func (m cloudView) handleKeySignedOut(msg tea.KeyPressMsg) (cloudView, tea.Cmd) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
		m.advanceFocus()
		return m, nil

	case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
		m.retreatFocus()
		return m, nil

	case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+s"))):
		// ctrl+s toggles between sign-in and sign-up modes.
		if m.mode == cloudModeSignIn {
			m.mode = cloudModeSignUp
		} else {
			m.mode = cloudModeSignIn
		}
		return m, nil

	case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		// Enter on email moves to password; Enter on password (or submit)
		// triggers the auth call.
		switch m.focus {
		case cloudFocusEmail:
			m.focus = cloudFocusPassword
			m.applyFocus()
			return m, nil
		case cloudFocusPassword, cloudFocusSubmit:
			email := strings.TrimSpace(m.email.Value())
			password := m.password.Value()
			if email == "" || password == "" {
				m.err = "email and password are required"
				return m, nil
			}
			m.err = ""
			m.phase = cloudPhaseAuthenticating
			return m, tea.Batch(m.spinner.Tick, m.signInCmd(email, password))
		}
		return m, nil
	}

	// Otherwise forward to the focused input.
	var cmd tea.Cmd
	switch m.focus {
	case cloudFocusEmail:
		m.email, cmd = m.email.Update(msg)
	case cloudFocusPassword:
		m.password, cmd = m.password.Update(msg)
	}
	return m, cmd
}

func (m cloudView) handleKeySignedIn(msg tea.KeyPressMsg) (cloudView, tea.Cmd) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("o"))):
		// Sign out: revoke + clear local session.
		if m.session != nil && m.client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			m.client.SignOut(ctx, m.session.AccessToken)
		}
		_ = clearSession()
		m.session = nil
		m.me = nil
		m.email.SetValue("")
		m.password.SetValue("")
		m.focus = cloudFocusEmail
		m.applyFocus()
		m.phase = cloudPhaseSignedOut
		return m, nil

	case key.Matches(msg, key.NewBinding(key.WithKeys("r"))):
		// Refresh /me.
		if m.session != nil {
			m.phase = cloudPhaseAuthenticating
			return m, tea.Batch(m.spinner.Tick, m.fetchMeCmd())
		}
	}
	return m, nil
}

func (m *cloudView) advanceFocus() {
	switch m.focus {
	case cloudFocusEmail:
		m.focus = cloudFocusPassword
	case cloudFocusPassword:
		m.focus = cloudFocusSubmit
	case cloudFocusSubmit:
		m.focus = cloudFocusEmail
	}
	m.applyFocus()
}

func (m *cloudView) retreatFocus() {
	switch m.focus {
	case cloudFocusEmail:
		m.focus = cloudFocusSubmit
	case cloudFocusPassword:
		m.focus = cloudFocusEmail
	case cloudFocusSubmit:
		m.focus = cloudFocusPassword
	}
	m.applyFocus()
}

func (m *cloudView) applyFocus() {
	m.email.Blur()
	m.password.Blur()
	switch m.focus {
	case cloudFocusEmail:
		m.email.Focus()
	case cloudFocusPassword:
		m.password.Focus()
	}
}

// --- async commands -------------------------------------------------

func (m *cloudView) signInCmd(email, password string) tea.Cmd {
	client := m.client
	mode := m.mode
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var (
			s   *cloud.Session
			err error
		)
		if mode == cloudModeSignUp {
			s, err = client.SignUp(ctx, email, password)
		} else {
			s, err = client.SignIn(ctx, email, password)
		}
		return cloudSignInResultMsg{session: s, err: err}
	}
}

func (m *cloudView) fetchMeCmd() tea.Cmd {
	client := m.client
	tok := ""
	if m.session != nil {
		tok = m.session.AccessToken
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		me, err := client.Me(ctx, tok)
		return cloudMeResultMsg{me: me, err: err}
	}
}

// --- view -----------------------------------------------------------

func (m cloudView) View() string {
	header := lipgloss.NewStyle().
		Foreground(textColor).
		Bold(true).
		Render("☁  Cloud")

	var body string
	switch m.phase {
	case cloudPhaseLoading:
		body = lipgloss.NewStyle().Foreground(mutedColor).Render(m.spinner.View() + " loading…")
	case cloudPhaseNotConfigured:
		body = m.viewNotConfigured()
	case cloudPhaseSignedOut:
		body = m.viewSignedOut()
	case cloudPhaseAuthenticating:
		label := "signing in…"
		if m.session != nil {
			label = "fetching profile…"
		}
		body = lipgloss.NewStyle().Foreground(mutedColor).Render(m.spinner.View() + " " + label)
	case cloudPhaseSignedIn:
		body = m.viewSignedIn()
	case cloudPhaseError:
		body = lipgloss.NewStyle().Foreground(dangerColor).Render(m.err) + "\n\n" +
			lipgloss.NewStyle().Foreground(mutedColor).Render("press any key to try again")
	}

	return header + "\n\n" + body
}

func (m cloudView) viewNotConfigured() string {
	muted := lipgloss.NewStyle().Foreground(mutedColor)
	return muted.Render(strings.Join([]string{
		"Cloud is not configured.",
		"",
		"Set the endpoint URLs in your preferences (~/.config/clank/preferences.json):",
		"",
		`  "cloud": {`,
		`    "supabase_project_url": "https://<project>.supabase.co",`,
		`    "supabase_anon_key":    "<publishable anon key>",`,
		`    "the cloud_url":         "https://your-cloud.example.com"`,
		`  }`,
		"",
		"Or point at a self-hosted the cloud by setting your own URLs.",
	}, "\n"))
}

func (m cloudView) viewSignedOut() string {
	modeLabel := "Sign in"
	if m.mode == cloudModeSignUp {
		modeLabel = "Sign up"
	}

	emailLine := "Email     " + m.email.View()
	passLine := "Password  " + m.password.View()

	submitStyle := lipgloss.NewStyle().Foreground(textColor)
	if m.focus == cloudFocusSubmit {
		submitStyle = submitStyle.Bold(true).Foreground(primaryColor)
	}
	submitLine := submitStyle.Render("[ " + modeLabel + " ]")

	help := lipgloss.NewStyle().Foreground(mutedColor).Render(
		"tab: next field   enter: submit   ctrl+s: switch to " + altMode(m.mode))

	parts := []string{emailLine, passLine, "", submitLine, "", help}
	if m.err != "" {
		parts = append([]string{lipgloss.NewStyle().Foreground(dangerColor).Render(m.err), ""}, parts...)
	}
	return strings.Join(parts, "\n")
}

func (m cloudView) viewSignedIn() string {
	muted := lipgloss.NewStyle().Foreground(mutedColor)
	text := lipgloss.NewStyle().Foreground(textColor)

	rows := []string{}

	if m.session != nil {
		rows = append(rows,
			text.Render("Signed in as ")+text.Bold(true).Render(m.session.UserEmail),
			muted.Render("user_id   "+truncate(m.session.UserID, 36)),
		)
		if m.session.ExpiresAt > 0 {
			ttl := time.Until(time.Unix(m.session.ExpiresAt, 0))
			rows = append(rows, muted.Render("expires   "+humanTTL(ttl)))
		}
		rows = append(rows, "")
	}

	if m.me != nil {
		if len(m.me.Hubs) > 0 {
			h := m.me.Hubs[0]
			rows = append(rows,
				text.Bold(true).Render("Hub"),
				muted.Render("subdomain "+h.Subdomain),
				muted.Render("region    "+h.Region),
				muted.Render("status    "+h.Status),
				muted.Render("url       "+h.PublicURL),
				"",
			)
		}
		if len(m.me.Hosts) > 0 {
			h := m.me.Hosts[0]
			rows = append(rows,
				text.Bold(true).Render("Host"),
				muted.Render("provider  "+h.Provider),
				muted.Render("hostname  "+h.Hostname),
				muted.Render("status    "+h.Status),
				"",
			)
		} else if len(m.me.Hubs) > 0 {
			rows = append(rows,
				muted.Render("(no host yet — POST /provision against the cloud to create one)"),
				"",
			)
		}
	} else if m.err != "" {
		// /me returned ErrNotSignedUp or similar.
		rows = append(rows, muted.Render(m.err), "")
	}

	rows = append(rows, muted.Render("r: refresh   o: sign out"))

	return strings.Join(rows, "\n")
}

func altMode(m cloudMode) string {
	if m == cloudModeSignIn {
		return "sign up"
	}
	return "sign in"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func humanTTL(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// --- helpers --------------------------------------------------------

func resolveEndpoints(prefs *config.Preferences) cloud.Endpoints {
	var ep cloud.Endpoints
	if prefs != nil && prefs.Cloud != nil {
		ep.SupabaseProjectURL = prefs.Cloud.SupabaseProjectURL
		ep.SupabaseAnonKey = prefs.Cloud.SupabaseAnonKey
		ep.ClankctlURL = prefs.Cloud.ClankctlURL
	}
	return ep
}

func endpointsConfigured(ep cloud.Endpoints) bool {
	return ep.SupabaseProjectURL != "" && ep.SupabaseAnonKey != "" && ep.ClankctlURL != ""
}

// cloudTokenExpired returns true if AccessToken is past its expiry,
// with a small skew buffer so we don't try a request that would 401
// in flight.
func cloudTokenExpired(c *config.CloudPreference) bool {
	if c == nil || c.ExpiresAt == 0 {
		return false
	}
	return time.Now().Unix() > c.ExpiresAt-30
}

func persistSession(s *cloud.Session) error {
	return config.UpdatePreferences(func(p *config.Preferences) {
		if p.Cloud == nil {
			p.Cloud = &config.CloudPreference{}
		}
		p.Cloud.AccessToken = s.AccessToken
		p.Cloud.RefreshToken = s.RefreshToken
		p.Cloud.UserEmail = s.UserEmail
		p.Cloud.UserID = s.UserID
		p.Cloud.ExpiresAt = s.ExpiresAt
	})
}

func clearSession() error {
	return config.UpdatePreferences(func(p *config.Preferences) {
		if p.Cloud == nil {
			return
		}
		p.Cloud.AccessToken = ""
		p.Cloud.RefreshToken = ""
		p.Cloud.UserEmail = ""
		p.Cloud.UserID = ""
		p.Cloud.ExpiresAt = 0
	})
}
