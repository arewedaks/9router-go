package paramfix

import (
	"encoding/json/v2"
	"regexp"
	"strings"
)

// UnknownArgument identifies a parameter an upstream rejected as unrecognised.
// Returns "" when the response is not that kind of rejection.
//
// Two shapes are covered because OpenAI-compatible gateways disagree on how to
// say the same thing:
//
//	{"message":"Unrecognized request arguments supplied: store","type":"invalid_request_error"}
//	{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted"}
//
// The first comes from Upstage and friends, the second from pydantic-based
// gateways such as Mistral. Both mean the same thing: a field the client sent
// has no counterpart upstream and the whole request was refused because of it.
var (
	unrecognizedRe = regexp.MustCompile(`(?i)unrecognized request arguments? supplied:\s*([A-Za-z0-9_.]+)`)
	forbiddenLocRe = regexp.MustCompile(`"loc"\s*:\s*\[\s*"body"\s*,\s*"([A-Za-z0-9_.]+)"\s*\]`)
)

// UnknownArgumentFromErrorBody extracts the offending field name from an upstream
// error body, or "" when the body is not an unrecognised-argument rejection.
//
// Only top-level field names are returned. `loc` paths that name a message field
// (["body","messages",0,"x"]) are deliberately not supported: dropping a field
// from inside a message changes conversation content, which is not a safe
// automatic repair.
func UnknownArgumentFromErrorBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if m := unrecognizedRe.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	if m := forbiddenLocRe.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}

// DropFields removes the named top-level fields from an OpenAI request body and
// reports whether anything changed. A body that cannot be parsed is returned
// unchanged: it is not ours to rewrite.
//
// Repeated across retries, so a response that names several fields is repaired
// one round at a time rather than silently dropping fields no upstream complained
// about. The caller bounds the number of attempts.
func DropFields(body []byte, fields ...string) ([]byte, bool) {
	if len(body) == 0 || len(fields) == 0 {
		return body, false
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, false
	}
	changed := false
	for _, f := range fields {
		if f == "" {
			continue
		}
		if _, ok := m[f]; ok {
			delete(m, f)
			changed = true
		}
	}
	if !changed {
		return body, false
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// isSafeToDrop reports whether a rejected field may be removed automatically.
//
// The allowlist is the point of this function. Auto-dropping whatever an upstream
// names would let a provider silently disable a parameter the caller depends on —
// `messages` or `model` would turn a working request into a different one. Only
// parameter names that are either pure transport hints or already established as
// fatal-when-present are droppable.
var safeToDrop = map[string]bool{
	"store":            true, // persistence toggle; never required to serve a request
	"metadata":         true, // opaque bookkeeping the upstream has no column for
	"stream_options":   true, // SSE usage-reporting extension, optional by definition
	"user":             true, // end-user tag for abuse tracking; not a request input
	"parallel_tool_calls": true, // a hint; models without it still answer
	"reasoning_effort": true, // deprecated by several vendors, ignored by others
	"service_tier":     true, // billing hint; has no effect on the answer
	"prompt_cache_key": true, // cache routing hint, purely an optimisation
}

// IsSafeToDrop reports whether the auto-retry may remove this field.
func IsSafeToDrop(field string) bool { return safeToDrop[strings.ToLower(field)] }
