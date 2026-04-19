package hostclient

import (
	"context"
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

// SessionsClient is the collection-scoped handle for session operations
// that don't target a single existing session. Obtained via
// HTTP.Sessions().
type SessionsClient struct {
	c *HTTP
}

// Sessions returns the collection-scoped session handle.
func (c *HTTP) Sessions() *SessionsClient {
	return &SessionsClient{c: c}
}

// Create starts a new session on the host. Returns a SessionBackend
// adapter bound to the new session and the host-resolved server URL
// (empty string for backends without an HTTP server, e.g. Claude Code).
func (s *SessionsClient) Create(ctx context.Context, sessionID string, req agent.StartRequest) (agent.SessionBackend, string, error) {
	body := struct {
		SessionID string             `json:"session_id"`
		Request   agent.StartRequest `json:"request"`
	}{sessionID, req}
	var snap struct {
		SessionID  string              `json:"session_id"`
		ExternalID string              `json:"external_id"`
		Status     agent.SessionStatus `json:"status"`
		ServerURL  string              `json:"server_url"`
	}
	if err := s.c.do(ctx, http.MethodPost, "/sessions", body, &snap); err != nil {
		return nil, "", err
	}
	return newHTTPSessionBackend(s.c, sessionID, snap.ExternalID, snap.Status), snap.ServerURL, nil
}
