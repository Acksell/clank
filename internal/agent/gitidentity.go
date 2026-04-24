package agent

import (
	"fmt"
	"strings"
)

// GitIdentity is the (name, email) pair used as committer/author on a
// host. The hub propagates the laptop user's `git config --global
// user.{name,email}` to every remote host it provisions so commits the
// agent makes inside a sandbox carry the user's identity rather than
// the sandbox's empty/placeholder default.
//
// Local hosts do not need this — they share the laptop's ~/.gitconfig
// directly. Only remote hosts (e.g. Daytona sandboxes) receive a
// SetIdentity call from the hub.
type GitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Validate returns an error when either field is empty or whitespace-
// only. The hub uses this to hard-fail provisioning rather than fall
// back to a synthetic identity like "clank@local" — silently mis-
// attributing commits is worse than failing loudly. Whitespace-only
// values are rejected because git accepts them and produces commits
// with blank authors, which is the same silent-mis-attribution failure
// mode in a different disguise.
func (g GitIdentity) Validate() error {
	if strings.TrimSpace(g.Name) == "" {
		return fmt.Errorf("git identity: name is required")
	}
	if strings.TrimSpace(g.Email) == "" {
		return fmt.Errorf("git identity: email is required")
	}
	return nil
}
