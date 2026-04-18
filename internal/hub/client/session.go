package hubclient

import (
	"context"

	"github.com/acksell/clank/internal/agent"
)

// SessionClient is bound to a session ID. All id-scoped session
// operations live here, including Get (which materialises the handle
// into the underlying SessionInfo).
type SessionClient struct {
	c  *Client
	id string
}

// Session returns a handle for the given session id.
func (c *Client) Session(id string) *SessionClient {
	return &SessionClient{c: c, id: id}
}

// ID returns the bound session id.
func (s *SessionClient) ID() string { return s.id }

// Get returns the SessionInfo for this session.
func (s *SessionClient) Get(ctx context.Context) (*agent.SessionInfo, error) {
	var info agent.SessionInfo
	if err := s.c.get(ctx, "/sessions/"+s.id, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Messages returns the full message history for this session.
func (s *SessionClient) Messages(ctx context.Context) ([]agent.MessageData, error) {
	var messages []agent.MessageData
	if err := s.c.get(ctx, "/sessions/"+s.id+"/messages", &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// Send sends a follow-up message to the running session.
func (s *SessionClient) Send(ctx context.Context, opts agent.SendMessageOpts) error {
	return s.c.post(ctx, "/sessions/"+s.id+"/message", opts, nil)
}

// Abort interrupts the running session.
func (s *SessionClient) Abort(ctx context.Context) error {
	return s.c.post(ctx, "/sessions/"+s.id+"/abort", nil, nil)
}

// Revert reverts the session to messageID, removing all subsequent messages.
func (s *SessionClient) Revert(ctx context.Context, messageID string) error {
	body := struct {
		MessageID string `json:"message_id"`
	}{MessageID: messageID}
	return s.c.post(ctx, "/sessions/"+s.id+"/revert", body, nil)
}

// Fork forks the session from messageID. Empty messageID forks the entire
// session (from start).
func (s *SessionClient) Fork(ctx context.Context, messageID string) (*agent.SessionInfo, error) {
	body := struct {
		MessageID string `json:"message_id,omitempty"`
	}{MessageID: messageID}
	var info agent.SessionInfo
	if err := s.c.post(ctx, "/sessions/"+s.id+"/fork", body, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// MarkRead marks the session as read.
func (s *SessionClient) MarkRead(ctx context.Context) error {
	return s.c.post(ctx, "/sessions/"+s.id+"/read", nil, nil)
}

// ToggleFollowUp toggles the follow-up flag and returns the new state.
func (s *SessionClient) ToggleFollowUp(ctx context.Context) (bool, error) {
	var resp struct {
		FollowUp bool `json:"follow_up"`
	}
	if err := s.c.post(ctx, "/sessions/"+s.id+"/followup", nil, &resp); err != nil {
		return false, err
	}
	return resp.FollowUp, nil
}

// SetVisibility sets the visibility state of the session.
func (s *SessionClient) SetVisibility(ctx context.Context, visibility agent.SessionVisibility) error {
	body := struct {
		Visibility agent.SessionVisibility `json:"visibility"`
	}{Visibility: visibility}
	return s.c.post(ctx, "/sessions/"+s.id+"/visibility", body, nil)
}

// SetDraft sets or clears the draft text for the session.
func (s *SessionClient) SetDraft(ctx context.Context, draft string) error {
	body := struct {
		Draft string `json:"draft"`
	}{Draft: draft}
	return s.c.post(ctx, "/sessions/"+s.id+"/draft", body, nil)
}

// Delete stops and removes the session.
func (s *SessionClient) Delete(ctx context.Context) error {
	return s.c.do(ctx, "DELETE", "/sessions/"+s.id, nil, nil)
}

// ReplyPermission replies to a permission request.
func (s *SessionClient) ReplyPermission(ctx context.Context, permissionID string, allow bool) error {
	body := map[string]bool{"allow": allow}
	return s.c.post(ctx, "/sessions/"+s.id+"/permissions/"+permissionID+"/reply", body, nil)
}

// PendingPermissions returns all pending permissions for the session.
func (s *SessionClient) PendingPermissions(ctx context.Context) ([]agent.PermissionData, error) {
	var perms []agent.PermissionData
	if err := s.c.get(ctx, "/sessions/"+s.id+"/pending-permission", &perms); err != nil {
		return nil, err
	}
	return perms, nil
}
