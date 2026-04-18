package hostclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

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
		return errorFromResp(resp)
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
// errors.Is.
func errorFromResp(resp *http.Response) error {
	var e struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Error == "" {
		e.Error = resp.Status
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
		return fmt.Errorf("%s: %w", e.Error, host.ErrMainDirty)
	case "merge_conflict":
		return fmt.Errorf("%s: %w", e.Error, host.ErrMergeConflict)
	default:
		return fmt.Errorf("host: %s", e.Error)
	}
}

// --- Client surface ---

func (c *HTTP) Status(ctx context.Context) (host.HostStatus, error) {
	var out host.HostStatus
	err := c.do(ctx, http.MethodGet, "/status", nil, &out)
	return out, err
}

func (c *HTTP) ListBackends(ctx context.Context) ([]host.BackendInfo, error) {
	var out []host.BackendInfo
	err := c.do(ctx, http.MethodGet, "/backends", nil, &out)
	return out, err
}

func (c *HTTP) ListAgents(ctx context.Context, bt agent.BackendType, projectDir string) ([]host.AgentInfo, error) {
	q := url.Values{"backend": {string(bt)}, "project_dir": {projectDir}}
	var out []host.AgentInfo
	err := c.do(ctx, http.MethodGet, "/agents?"+q.Encode(), nil, &out)
	return out, err
}

func (c *HTTP) ListModels(ctx context.Context, bt agent.BackendType, projectDir string) ([]host.ModelInfo, error) {
	q := url.Values{"backend": {string(bt)}, "project_dir": {projectDir}}
	var out []host.ModelInfo
	err := c.do(ctx, http.MethodGet, "/models?"+q.Encode(), nil, &out)
	return out, err
}

func (c *HTTP) DiscoverSessions(ctx context.Context, bt agent.BackendType, seedDir string) ([]agent.SessionSnapshot, error) {
	body := struct {
		Backend agent.BackendType `json:"backend"`
		SeedDir string            `json:"seed_dir"`
	}{Backend: bt, SeedDir: seedDir}
	var out []agent.SessionSnapshot
	err := c.do(ctx, http.MethodPost, "/discover", body, &out)
	return out, err
}

func (c *HTTP) ListRepos(ctx context.Context) ([]host.Repo, error) {
	var out []host.Repo
	err := c.do(ctx, http.MethodGet, "/repos", nil, &out)
	return out, err
}

func (c *HTTP) RegisterRepo(ctx context.Context, ref host.RepoRef, rootDir string) (host.Repo, error) {
	body := struct {
		Ref     host.RepoRef `json:"ref"`
		RootDir string       `json:"root_dir"`
	}{ref, rootDir}
	var out host.Repo
	err := c.do(ctx, http.MethodPost, "/repos", body, &out)
	return out, err
}

func (c *HTTP) ListBranchesByRepo(ctx context.Context, id host.RepoID) ([]host.BranchInfo, error) {
	var out []host.BranchInfo
	err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(string(id))+"/branches", nil, &out)
	return out, err
}

func (c *HTTP) ResolveWorktreeByRepo(ctx context.Context, id host.RepoID, branch string) (host.WorktreeInfo, error) {
	body := struct {
		Branch string `json:"branch"`
	}{branch}
	var out host.WorktreeInfo
	err := c.do(ctx, http.MethodPost, "/repos/"+url.PathEscape(string(id))+"/worktrees", body, &out)
	return out, err
}

func (c *HTTP) RemoveWorktreeByRepo(ctx context.Context, id host.RepoID, branch string, force bool) error {
	q := url.Values{
		"branch": {branch},
		"force":  {strconv.FormatBool(force)},
	}
	return c.do(ctx, http.MethodDelete, "/repos/"+url.PathEscape(string(id))+"/worktrees?"+q.Encode(), nil, nil)
}

func (c *HTTP) MergeBranchByRepo(ctx context.Context, id host.RepoID, branch, commitMessage string) (host.MergeResult, error) {
	body := struct {
		Branch        string `json:"branch"`
		CommitMessage string `json:"commit_message,omitempty"`
	}{branch, commitMessage}
	var out host.MergeResult
	err := c.do(ctx, http.MethodPost, "/repos/"+url.PathEscape(string(id))+"/worktrees/merge", body, &out)
	return out, err
}

func (c *HTTP) CreateSession(ctx context.Context, sessionID string, req agent.StartRequest) (agent.SessionBackend, host.CreateInfo, error) {
	body := struct {
		SessionID string             `json:"session_id"`
		Request   agent.StartRequest `json:"request"`
	}{sessionID, req}
	var snap struct {
		SessionID   string              `json:"session_id"`
		ExternalID  string              `json:"external_id"`
		Status      agent.SessionStatus `json:"status"`
		ProjectDir  string              `json:"project_dir"`
		WorktreeDir string              `json:"worktree_dir"`
	}
	if err := c.do(ctx, http.MethodPost, "/sessions", body, &snap); err != nil {
		return nil, host.CreateInfo{}, err
	}
	return newHTTPSessionBackend(c, sessionID, snap.ExternalID, snap.Status), host.CreateInfo{
		ProjectDir:  snap.ProjectDir,
		WorktreeDir: snap.WorktreeDir,
	}, nil
}

func (c *HTTP) StopSession(ctx context.Context, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+sessionID+"/stop", nil, nil)
}
