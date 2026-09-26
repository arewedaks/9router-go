// Package paramfix centralises per-provider request-parameter corrections.
//
// Why this exists: providers reject requests with HTTP 400 over parameters that
// are perfectly valid elsewhere — a temperature Claude deprecates, an output cap
// above what the endpoint tolerates, a minimum it refuses to go below. Handled
// ad hoc, each rule ends up as a `delete body.x` buried in one executor, and a
// provider that shares that executor silently misses it.
//
// Ported from 9Router's open-sse/translator/concerns/paramSupport.js, which
// applies a rule table at one place (executors/default.js transformRequest) so
// every provider passes through it. Same shape here: one ordered rule table,
// consulted from a single call site.
//
// Rules must only ever make a request MORE acceptable. Nothing here inspects or
// rewrites content.
package paramfix

import (
	"encoding/json/v2"
	"strconv"
	"strings"
)

// Rule describes corrections for models matching Match, optionally scoped to a
// single Provider. A zero Rule matches everything, which is never useful — every
// rule must set at least Match or Provider.
type Rule struct {
	// Provider limits the rule to one provider id. Empty means any provider.
	Provider string
	// Match is tested against the model id (already lowercased).
	Match func(model string) bool
	// MatchSubstr is the simple substring form of Match.
	MatchSubstr string
	// Drop lists parameters to remove when present.
	Drop []string
	// DropMessageFields removes fields from assistant messages only. Clients that
	// talk to reasoning models replay the previous turn's reasoning back, and
	// strict validators reject the unknown field with a 400/422, which knocks the
	// provider out of every multi-turn conversation.
	DropMessageFields []string
	// FlattenContent rewrites an array content into a plain string. Cloudflare
	// Workers AI accepts only the string form and 400s on OpenAI content parts.
	FlattenContent bool
	// MaxOutputCap pins a hard ceiling for this model, in tokens. Use when the
	// endpoint's real limit is lower than the model's advertised one.
	MaxOutputCap int
	// MinOutput raises a too-small output cap to the endpoint's floor. Some
	// models 400 on anything below a minimum rather than clamping themselves.
	MinOutput int
	// ClampToModelCeiling lowers the cap to the model's advertised max output, so
	// a client asking for more than the model can emit does not get a 400.
	ClampToModelCeiling bool
}

// ruleMatch reports whether the rule applies to this provider/model pair.
func (r Rule) ruleMatch(provider, model string) bool {
	if r.Provider != "" && r.Provider != provider {
		return false
	}
	if r.MatchSubstr != "" {
		return strings.Contains(model, strings.ToLower(r.MatchSubstr))
	}
	if r.Match != nil {
		return r.Match(model)
	}
	// No model selector: the rule is scoped by provider alone, which is how the
	// message-field and content-flattening rules are written (they apply to every
	// model on that provider). Rejecting here instead would silently disable
	// them — the exact failure this table exists to prevent.
	return true
}

// maxOutputCeiling resolves the advertised output ceiling for a model, or 0 when
// unknown. Injected rather than imported so this package stays free of a
// dependency on the provider registry (which would create an import cycle the
// moment capabilities needs a rule lookup).
var maxOutputCeiling = func(provider, model string) int { return 0 }

// SetMaxOutputCeilingFunc wires the capabilities lookup used by
// ClampToModelCeiling.
func SetMaxOutputCeilingFunc(fn func(provider, model string) int) {
	if fn != nil {
		maxOutputCeiling = fn
	}
}

// Apply corrects body for this provider/model and returns it. body is modified
// in place; the return value exists for call-site convenience.
func Apply(provider, model string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		// A body we cannot parse is not ours to rewrite: passing it through keeps
		// the failure visible at the provider instead of being masked here.
		return body
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	modelLower := strings.ToLower(model)
	changed := false

	for _, rule := range Rules {
		if !rule.ruleMatch(provider, modelLower) {
			continue
		}
		for _, key := range rule.Drop {
			if _, ok := m[key]; ok {
				delete(m, key)
				changed = true
			}
		}
		if len(rule.DropMessageFields) > 0 && dropMessageFields(m, rule.DropMessageFields) {
			changed = true
		}
		if rule.FlattenContent && flattenContent(m) {
			changed = true
		}
		if ceiling := resolveOutputCeiling(rule, provider, modelLower); ceiling > 0 {
			changed = clampOutputTokens(m, ceiling) || changed
		}
		if rule.MinOutput > 0 {
			changed = raiseOutputTokens(m, rule.MinOutput) || changed
		}
	}

	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// resolveOutputCeiling returns the effective cap for the rule, taking the lower
// of the advertised model ceiling and any pinned endpoint cap.
func resolveOutputCeiling(rule Rule, provider, model string) int {
	var candidates []int
	if rule.ClampToModelCeiling {
		if c := maxOutputCeiling(provider, model); c > 0 {
			candidates = append(candidates, c)
		}
	}
	if rule.MaxOutputCap > 0 {
		candidates = append(candidates, rule.MaxOutputCap)
	}
	if len(candidates) == 0 {
		return 0
	}
	lowest := candidates[0]
	for _, c := range candidates[1:] {
		if c < lowest {
			lowest = c
		}
	}
	return lowest
}

// outputTokenKeys lists every name an output cap travels under. Clients are
// inconsistent and providers differ on which they read, so a clamp has to cover
// all of them or it silently misses the one that mattered.
var outputTokenKeys = []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}

// numericValue reads a token count that may arrive as a number or a string.
func numericValue(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

// clampOutputTokens lowers any present cap above ceiling.
func clampOutputTokens(m map[string]any, ceiling int) bool {
	changed := false
	for _, key := range outputTokenKeys {
		v, ok := m[key]
		if !ok {
			continue
		}
		n, ok := numericValue(v)
		if !ok || n <= ceiling {
			continue
		}
		m[key] = ceiling
		changed = true
	}
	return changed
}

// raiseOutputTokens lifts any present cap below floor. Only present keys are
// touched: an absent cap means "use the server default", which is a different
// request from "exactly this many tokens".
func raiseOutputTokens(m map[string]any, floor int) bool {
	changed := false
	for _, key := range outputTokenKeys {
		v, ok := m[key]
		if !ok {
			continue
		}
		n, ok := numericValue(v)
		if !ok || n >= floor {
			continue
		}
		m[key] = floor
		changed = true
	}
	return changed
}

func dropMessageFields(m map[string]any, fields []string) bool {
	msgs, ok := m["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		// Assistant turns only: that is where clients replay prior reasoning.
		if role, _ := msg["role"].(string); role != "assistant" {
			continue
		}
		for _, f := range fields {
			if _, ok := msg[f]; ok {
				delete(msg, f)
				changed = true
			}
		}
	}
	return changed
}

// flattenContent rewrites content-part arrays into plain strings.
func flattenContent(m map[string]any) bool {
	msgs, ok := m["messages"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		var b strings.Builder
		for _, p := range parts {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := part["type"].(string); ok && t == "text" {
				if text, ok := part["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
		msg["content"] = b.String()
		changed = true
	}
	return changed
}
