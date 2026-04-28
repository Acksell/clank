package agent

import "errors"

// ClaudeProviderID and ClaudeProviderName tag every entry in
// DefaultClaudeModels so the TUI's model picker can render a uniform
// provider label without consulting a remote catalog.
const (
	ClaudeProviderID   = "anthropic"
	ClaudeProviderName = "Anthropic"
)

// DefaultClaudeModels is the hardcoded catalog of Claude models surfaced
// by the model picker. The Claude CLI doesn't expose a catalog endpoint,
// so we ship a static list rather than scraping anthropic.com or
// silently falling back to "default".
//
// Order matters: the picker renders entries in this order, with the
// first row pre-selected.
var DefaultClaudeModels = []ModelInfo{
	{ID: "claude-opus-4-7", Name: "Claude Opus 4.7", ProviderID: ClaudeProviderID, ProviderName: ClaudeProviderName},
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", ProviderID: ClaudeProviderID, ProviderName: ClaudeProviderName},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5", ProviderID: ClaudeProviderID, ProviderName: ClaudeProviderName},
}

// ErrModelChangeUnsupported is returned by SessionBackend.SetModel for
// backends that have no concept of a runtime-cycleable model
// (e.g. OpenCode, where model is a per-message override on Send).
var ErrModelChangeUnsupported = errors.New("model change not supported by this backend")
