package gitcred

import "errors"

// ErrNoCredential is returned by a [Discoverer] (or by [Stack.Discover])
// when no credential is available for the requested endpoint. It is
// the only "soft" failure mode — every other error is a hard config
// problem (bad settings file, gh CLI crash, etc.) and must surface to
// the user rather than silently degrade to anonymous.
var ErrNoCredential = errors.New("gitcred: no credential available")
