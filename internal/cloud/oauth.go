package cloud

// oauth.go — Supabase Auth PKCE client. Implements OAuth 2.0
// Authorization Code with PKCE (RFC 7636) against Supabase's
// /auth/v1/* endpoints. Browser-based flow with a localhost callback;
// suitable for desktop CLIs where the user can open a browser.
//
// Why Supabase Auth (not OAuth Server): the user already runs a
// Supabase project for their gateway. Supabase Auth has full OAuth
// provider support (GitHub, Google, etc.) configured via the
// dashboard, returns JWTs the gateway can verify via JWKS, and
// requires zero client registration. The PKCE flow tracks the
// installed-app contract from RFC 8252.
//
// Not used in SSH / container environments: localhost callbacks
// can't reach the user's laptop from a remote shell. Device flow
// could be re-added as a fallback later (auto-detected via
// $SSH_TTY).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// OAuthClient runs the PKCE dance against a single Supabase project.
// Construct once per login attempt; not reused after Login returns.
type OAuthClient struct {
	// SupabaseURL is the project root, e.g.
	// "https://abc123.supabase.co". OAuth endpoints sit under
	// /auth/v1/*.
	SupabaseURL string

	// AnonKey is the project's publishable anon key. Required for
	// all /auth/v1/* calls (sent in the `apikey` header).
	AnonKey string

	// Provider is the OAuth provider Supabase should sign in with
	// — e.g. "github", "google", "azure". Required: forwarded as
	// the `provider` query param on /authorize.
	Provider string

	// OpenBrowser is the function called to launch the user's
	// browser. Optional; defaults to the platform's standard
	// open command. Tests inject a no-op.
	OpenBrowser func(target string) error

	// CallbackHosts is the set of bind hosts the localhost listener
	// will try in order. Default ["127.0.0.1", "localhost"]; Supabase
	// allows either as a wildcard in "Additional Redirect URLs".
	CallbackHosts []string

	// HTTPClient is used for the /auth/v1/token exchange. Optional;
	// defaults to a 30s-timeout client.
	HTTPClient *http.Client
}

// ErrLoginCancelled is returned when the user aborts the flow
// (closes the browser, or sends ^C in the parent context).
var ErrLoginCancelled = errors.New("cloud: login cancelled")

// ErrLoginTimeout is returned when the OAuth provider doesn't
// redirect back within the timeout window. Default 5 minutes
// (passed via ctx; oauth.go itself doesn't set a budget).
var ErrLoginTimeout = errors.New("cloud: login timed out")

// Login runs the full PKCE flow:
//
//  1. Generate code_verifier + code_challenge (S256).
//  2. Bind a localhost listener on a random free port.
//  3. Open the user's browser to <SupabaseURL>/auth/v1/authorize
//     with redirect_to pointing at the localhost listener.
//  4. Wait for the provider's redirect (?code=... or ?error=...).
//  5. Exchange the code at <SupabaseURL>/auth/v1/token?grant_type=pkce.
//  6. Return the resulting Session.
//
// The context's deadline bounds the wait — caller is expected to
// pass ctx with a reasonable timeout (e.g. 5 minutes). The browser
// always opens; if OpenBrowser fails, the URL is returned in the
// error so the user can paste it manually.
func (c *OAuthClient) Login(ctx context.Context) (*Session, error) {
	if c.SupabaseURL == "" {
		return nil, fmt.Errorf("oauth: SupabaseURL is required")
	}
	if c.AnonKey == "" {
		return nil, fmt.Errorf("oauth: AnonKey is required")
	}
	if c.Provider == "" {
		c.Provider = "github"
	}
	openBrowser := c.OpenBrowser
	if openBrowser == nil {
		openBrowser = defaultOpenBrowser
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	verifier, challenge, err := generatePKCEPair()
	if err != nil {
		return nil, fmt.Errorf("oauth: generate PKCE pair: %w", err)
	}

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, fmt.Errorf("oauth: generate state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	listener, redirectURL, err := bindLocalhost(c.CallbackHosts)
	if err != nil {
		return nil, fmt.Errorf("oauth: bind localhost: %w", err)
	}
	defer listener.Close()

	authorizeURL := buildAuthorizeURL(c.SupabaseURL, c.Provider, redirectURL, challenge, state)

	// Channel for the callback handler to deliver the result.
	type callbackResult struct {
		code  string
		err   error
		state string
	}
	resultCh := make(chan callbackResult, 1)

	// Single-shot callback handler. We accept any path so providers
	// that drop query params on redirect (or that strip the path)
	// still hit us — we just key on the presence of `code`.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errStr := q.Get("error"); errStr != "" {
			desc := q.Get("error_description")
			renderCallbackPage(w, false, "Login failed: "+errStr+" — "+desc)
			resultCh <- callbackResult{err: fmt.Errorf("oauth: %s: %s", errStr, desc)}
			return
		}
		code := q.Get("code")
		gotState := q.Get("state")
		if code == "" {
			renderCallbackPage(w, false, "Login failed: no code in callback.")
			resultCh <- callbackResult{err: fmt.Errorf("oauth: callback missing code")}
			return
		}
		renderCallbackPage(w, true, "")
		resultCh <- callbackResult{code: code, state: gotState}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener) //nolint:errcheck — Close triggers a benign net.ErrClosed.
	defer srv.Close()

	// Best-effort: open the browser. If it fails, return the URL
	// in the error so the user can copy it manually.
	if err := openBrowser(authorizeURL); err != nil {
		return nil, fmt.Errorf("oauth: open browser failed (visit %s): %w", authorizeURL, err)
	}

	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrLoginTimeout
		}
		return nil, ErrLoginCancelled
	case res := <-resultCh:
		if res.err != nil {
			return nil, res.err
		}
		if res.state != state {
			return nil, fmt.Errorf("oauth: state mismatch (possible CSRF) — got %q, want %q", res.state, state)
		}
		return c.exchangeCode(ctx, httpClient, res.code, verifier)
	}
}

// Refresh exchanges a refresh_token for a fresh access_token.
// Used when the cached access_token has expired but the refresh
// token is still valid.
func (c *OAuthClient) Refresh(ctx context.Context, refreshToken string) (*Session, error) {
	if c.SupabaseURL == "" || c.AnonKey == "" {
		return nil, fmt.Errorf("oauth: SupabaseURL and AnonKey are required")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	tokenURL := strings.TrimRight(c.SupabaseURL, "/") + "/auth/v1/token?grant_type=refresh_token"
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	return doTokenExchange(ctx, httpClient, tokenURL, c.AnonKey, body)
}

// --- internals ------------------------------------------------------

func (c *OAuthClient) exchangeCode(ctx context.Context, hc *http.Client, code, verifier string) (*Session, error) {
	tokenURL := strings.TrimRight(c.SupabaseURL, "/") + "/auth/v1/token?grant_type=pkce"
	body, _ := json.Marshal(map[string]string{
		"auth_code":     code,
		"code_verifier": verifier,
	})
	return doTokenExchange(ctx, hc, tokenURL, c.AnonKey, body)
}

// supabaseTokenResponse mirrors Supabase Auth's token response shape.
type supabaseTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	User         struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

type supabaseErrorResponse struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func doTokenExchange(ctx context.Context, hc *http.Client, tokenURL, anonKey string, body []byte) (*Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", anonKey)
	req.Header.Set("Authorization", "Bearer "+anonKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		var errResp supabaseErrorResponse
		_ = json.Unmarshal(respBody, &errResp)
		msg := errResp.Message
		if msg == "" {
			msg = errResp.ErrorDescription
		}
		if msg == "" {
			msg = strings.TrimSpace(string(respBody))
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%w: %s", ErrUnauthorized, msg)
		}
		return nil, fmt.Errorf("token exchange: status %d: %s", resp.StatusCode, msg)
	}

	var tok supabaseTokenResponse
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token exchange: empty access_token in response")
	}
	expires := tok.ExpiresAt
	if expires == 0 && tok.ExpiresIn > 0 {
		expires = time.Now().Unix() + tok.ExpiresIn
	}
	return &Session{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		UserID:       tok.User.ID,
		UserEmail:    tok.User.Email,
		ExpiresAt:    expires,
	}, nil
}

// generatePKCEPair returns (code_verifier, code_challenge) per
// RFC 7636 §4.1. Verifier is a 64-byte URL-safe base64 string
// (well within RFC's 43-128 char range). Challenge is base64url
// of SHA256(verifier).
func generatePKCEPair() (verifier, challenge string, err error) {
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// bindLocalhost tries each host in `hosts` in turn (e.g. 127.0.0.1
// then localhost) on port 0 (kernel-assigned) and returns the first
// successful listener plus the redirect URL Supabase should call
// back to. Defaults the host list when empty.
func bindLocalhost(hosts []string) (net.Listener, string, error) {
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1", "localhost"}
	}
	var lastErr error
	for _, h := range hosts {
		l, err := net.Listen("tcp", h+":0")
		if err != nil {
			lastErr = err
			continue
		}
		port := l.Addr().(*net.TCPAddr).Port
		redirect := fmt.Sprintf("http://%s:%d", h, port)
		return l, redirect, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no callback hosts to try")
	}
	return nil, "", lastErr
}

// buildAuthorizeURL constructs the Supabase /auth/v1/authorize URL
// with PKCE parameters. URL-encodes redirect_to since it contains
// scheme + port.
func buildAuthorizeURL(supabaseURL, provider, redirectTo, challenge, state string) string {
	base := strings.TrimRight(supabaseURL, "/") + "/auth/v1/authorize"
	v := url.Values{}
	v.Set("provider", provider)
	v.Set("redirect_to", redirectTo)
	v.Set("code_challenge", challenge)
	v.Set("code_challenge_method", "S256")
	v.Set("state", state)
	return base + "?" + v.Encode()
}

// renderCallbackPage writes a minimal HTML page to the browser
// telling the user the flow completed. Inlined CSS so we don't
// have to ship assets.
func renderCallbackPage(w http.ResponseWriter, success bool, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	title := "Signed in to clank"
	body := `<p>You can close this tab and return to your terminal.</p>`
	if !success {
		title = "Sign-in failed"
		body = `<p>Something went wrong: ` + escapeHTML(errMsg) + `</p><p>Return to your terminal and try again.</p>`
	}
	_, _ = io.WriteString(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>`+title+`</title>
<style>
body { font-family: -apple-system, system-ui, sans-serif; max-width: 480px; margin: 80px auto; padding: 0 24px; color: #1a1a1a; line-height: 1.5; }
h1 { font-size: 1.4rem; margin: 0 0 8px; }
p { color: #555; }
</style>
</head>
<body>
<h1>`+title+`</h1>`+body+`
</body>
</html>
`)
}

func escapeHTML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// defaultOpenBrowser is the fallback opener used when OAuthClient
// doesn't override OpenBrowser. macOS → `open`, Linux → `xdg-open`,
// Windows → rundll32. Returns an error if the command fails to start;
// the browser may still succeed after this returns nil (it's just
// `cmd.Start`, not `cmd.Run`).
func defaultOpenBrowser(target string) error {
	if target == "" {
		return fmt.Errorf("no URL to open")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
