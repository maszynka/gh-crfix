// Package model handles model families, defaults, and the registry.
package model

import "strings"

// Family returns the backend family for the given model name:
// "claude" for Claude models, "codex" for OpenAI/Codex models, or "" for empty.
func Family(model string) string {
	switch {
	case model == "":
		return ""
	case model == "sonnet" || model == "opus" || model == "haiku":
		return "claude"
	case strings.HasPrefix(model, "claude-"):
		return "claude"
	default:
		return "codex"
	}
}

// DefaultGateModel returns the default gate model for the given backend.
func DefaultGateModel(backend string) string {
	switch backend {
	case "claude":
		return "sonnet"
	case "codex":
		return "gpt-5.4-mini"
	default:
		return ""
	}
}

// DefaultFixModel returns the default fix model for the given backend.
func DefaultFixModel(backend string) string {
	switch backend {
	case "claude":
		return "sonnet"
	case "codex":
		return "gpt-5.4"
	default:
		return ""
	}
}

// UsingBackendDefaults reports whether gateModel and fixModel are the defaults
// for the given backend.
func UsingBackendDefaults(backend, gateModel, fixModel string) bool {
	return gateModel == DefaultGateModel(backend) && fixModel == DefaultFixModel(backend)
}
