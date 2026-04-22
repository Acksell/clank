package hostclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"

	"github.com/acksell/clank/internal/host"
)

// errorBodyExcerptMax bounds how many bytes of an error response body
// we keep for logging + error-message enrichment. Daytona proxy error
// pages can be tens of KiB of HTML; clank-host's own errResp bodies
// are <1 KiB. 2 KiB comfortably covers both without flooding logs.
const errorBodyExcerptMax = 2048

// HTTP is a Client that talks to a Host over HTTP. The transport is
// chosen at construction time: a Unix socket for the local clankd ↔
// clank-host case, or a TCP+TLS dialer for managed remote hosts.
type HTTP struct {
	httpc   *http.Client
	baseURL string
}

// NewHTTP constructs an HTTP client. baseURL must be a fully-qualified
// URL like "http://unix" (the host part is ignored when using a Unix
// socket transport). transport may be nil; in that case
// http.DefaultTransport is used.
func NewHTTP(baseURL string, transport http.RoundTripper) *HTTP {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &HTTP{
		httpc:   &http.Client{Transport: transport},
		baseURL: baseURL,
	}
}

// NewUnixHTTP constructs an HTTP client that dials the Host on a Unix
// socket. The base URL is "http://unix"; the actual address is the
// socket path.
func NewUnixHTTP(socketPath string) *HTTP {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return NewHTTP("http://unix", tr)
}

// Close releases the underlying transport's idle connections.
func (c *HTTP) Close() error {
	if t, ok := c.httpc.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	return nil
}

// --- request helpers ---

func (c *HTTP) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// Read the entire body up to a bound so we can both log it
		// and surface an excerpt in the returned error. Without this
		// the caller sees only "host: 400 Bad Request" with no clue
		// whether the 400 came from clank-host (JSON errResp) or
		// from an upstream proxy (HTML / different JSON shape).
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyExcerptMax))
		log.Printf("hostclient: %s %s%s -> %d (Content-Type=%q): %s",
			method, c.baseURL, path, resp.StatusCode,
			resp.Header.Get("Content-Type"), bodyExcerpt(body))
		return errorFromResp(resp, body)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// errorFromResp reads an errResp body and translates the code field
// back into the matching host sentinel error so callers can use
// errors.Is. body is the already-read response body excerpt (callers
// in `do` read it once for both logging and parsing).
func errorFromResp(resp *http.Response, body []byte) error {
	var e struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Error == "" {
		// Body wasn't clank-host's errResp shape (likely an upstream
		// proxy 4xx like Daytona's). Surface a body excerpt so the
		// failure is actionable instead of just "host: 400 Bad Request".
		e.Error = resp.Status
		if excerpt := bodyExcerpt(body); excerpt != "" {
			e.Error = fmt.Sprintf("%s: %s", resp.Status, excerpt)
		}
	}
	switch e.Code {
	case "not_found":
		return fmt.Errorf("%s: %w", e.Error, host.ErrNotFound)
	case "cannot_merge_default":
		return fmt.Errorf("%s: %w", e.Error, host.ErrCannotMergeDefault)
	case "nothing_to_merge":
		return fmt.Errorf("%s: %w", e.Error, host.ErrNothingToMerge)
	case "commit_message_required":
		return fmt.Errorf("%s: %w", e.Error, host.ErrCommitMessageRequired)
	case "main_dirty":
		return fmt.Errorf("%s: %w", e.Error, host.ErrTargetDirty)
	case "merge_conflict":
		return fmt.Errorf("%s: %w", e.Error, host.ErrMergeConflict)
	case "reserved_branch":
		return fmt.Errorf("%s: %w", e.Error, host.ErrReservedBranch)
	case "invalid_branch_name":
		return fmt.Errorf("%s: %w", e.Error, host.ErrInvalidBranchName)
	default:
		return fmt.Errorf("host: %s", e.Error)
	}
}

// bodyExcerpt returns a single-line, whitespace-collapsed view of the
// response body suitable for embedding in a log line or error message.
// Empty input yields "". Newlines and tabs become spaces so multi-line
// HTML error pages don't wreck log readability.
func bodyExcerpt(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	out := make([]byte, 0, len(body))
	for _, b := range body {
		switch b {
		case '\n', '\r', '\t':
			out = append(out, ' ')
		default:
			out = append(out, b)
		}
	}
	return string(bytes.TrimSpace(out))
}

// --- Top-level surface ---

// Status fetches the host's status snapshot.
func (c *HTTP) Status(ctx context.Context) (host.HostStatus, error) {
	var out host.HostStatus
	err := c.do(ctx, http.MethodGet, "/status", nil, &out)
	return out, err
}

// Backends lists the backends this host has registered.
func (c *HTTP) Backends(ctx context.Context) ([]host.BackendInfo, error) {
	var out []host.BackendInfo
	err := c.do(ctx, http.MethodGet, "/backends", nil, &out)
	return out, err
}
