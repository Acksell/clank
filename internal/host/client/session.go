package hostclient

import (
	"context"
	"net/http"
)

// SessionClient is a per-session handle. Obtained via HTTP.Session(id).
//
// Most per-session operations live on the SessionBackend returned by
// Sessions().Create — the SessionClient handle is for operations the
// hub initiates against a session id without holding the backend
// (currently just Stop).
type SessionClient struct {
	c  *HTTP
	id string
}

// Session returns a handle for the session with the given hub-side id.
func (c *HTTP) Session(id string) *SessionClient {
	return &SessionClient{c: c, id: id}
}

// Stop releases the session on the host.
func (s *SessionClient) Stop(ctx context.Context) error {
	return s.c.do(ctx, http.MethodPost, "/sessions/"+s.id+"/stop", nil, nil)
}
