package tokensaver

import (
	"context"
	"strings"
)

// BypassHeader lets one request opt out of every token saver. Port of the
// reference's TOKEN_SAVER_HEADER: a client that needs its body verbatim (a
// benchmark, a debugging session, a prompt-cache-sensitive workload) should not
// have to change a global setting and affect every other caller.
const BypassHeader = "X-9Router-Token-Saver"

// BypassValue is the only value that disables the savers. Matched
// case-insensitively, so "Off" and "OFF" work too.
const BypassValue = "off"

type bypassKey struct{}

// WithBypass marks ctx as opted out when the request carried the bypass header.
// Called once at the HTTP boundary so the decision travels with the request
// instead of being re-read (and re-parsed) on every saver stage.
func WithBypass(ctx context.Context, headerValue string) context.Context {
	if ctx == nil || !strings.EqualFold(strings.TrimSpace(headerValue), BypassValue) {
		return ctx
	}
	return context.WithValue(ctx, bypassKey{}, true)
}

// BypassRequested reports whether this request opted out of token savers.
func BypassRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(bypassKey{}).(bool)
	return v
}

// CavemanLevels and PonytailLevels are the intensities the reference exposes.
// Kept as a list rather than free-form strings so the dashboard dropdown, the
// settings validator and the prompt lookup cannot drift apart.
var (
	CavemanLevels  = []string{"lite", "full", "ultra"}
	PonytailLevels = []string{"lite", "full", "ultra"}
)

// NormalizeLevel maps any stored value onto a level this build understands.
//
// Two legacy spellings have to keep working: the Go dashboard shipped
// "light"/"medium" for caveman and "compact" for ponytail, and VansRouter
// databases may carry them. They are aliases, not separate prompts — "medium"
// and "compact" have no prompt of their own, so falling back to the default
// without mapping them would apply a different style than the operator picked.
func NormalizeLevel(level string, allowed []string) string {
	lvl := strings.ToLower(strings.TrimSpace(level))
	switch lvl {
	case "light":
		lvl = "lite"
	case "medium", "compact":
		lvl = "full"
	}
	for _, a := range allowed {
		if lvl == a {
			return lvl
		}
	}
	return "full"
}

// ValidLevel reports whether level is one this build knows, ignoring aliases.
func ValidLevel(level string, allowed []string) bool {
	lvl := strings.ToLower(strings.TrimSpace(level))
	for _, a := range allowed {
		if lvl == a {
			return true
		}
	}
	return false
}
