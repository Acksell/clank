package agent

import "fmt"

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

// Validate returns an error when either field is empty. The hub uses
// this to hard-fail provisioning rather than fall back to a synthetic
// identity like "clank@local" — silently mis-attributing commits is
// worse than failing loudly.
func (g GitIdentity) Validate() error {
	if g.Name == "" {
		return fmt.Errorf("git identity: name is required")
	}
	if g.Email == "" {
		return fmt.Errorf("git identity: email is required")
	}
	return nil
}
