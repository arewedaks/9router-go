package paramfix

import "strings"

// Rules is the ordered rule table. Every entry must earn its place with an
// observed failure — an upstream 400/422 that a request would otherwise hit.
//
// Ported from 9Router's open-sse/translator/concerns/paramSupport.js. The
// OpenCode muse-spark floor is ours: the reference has no equivalent rule and
// would 400 on the same request, which is why it is listed here rather than
// copied.
var Rules = []Rule{
	// ─── Anthropic / Claude ─────────────────────────────────────────────────
	// Temperature is deprecated upstream: Anthropic rejects it with a 400 rather
	// than ignoring it. (9Router #1748)
	{MatchSubstr: "claude", Drop: []string{"temperature"}},

	// GitHub Copilot's gpt-5.4 does not accept temperature.
	{Provider: "github", MatchSubstr: "gpt-5.4", Drop: []string{"temperature"}},

	// Copilot Claude rejects thinking/reasoning_effort, except the opus/sonnet
	// 4.6 line which supports them. (9Router #713)
	{
		Provider: "github",
		Match: func(m string) bool {
			if !strings.Contains(m, "claude") {
				return false
			}
			is46 := strings.Contains(m, "opus") && strings.Contains(m, "4.6") ||
				strings.Contains(m, "sonnet") && strings.Contains(m, "4.6")
			return !is46
		},
		Drop: []string{"thinking", "reasoning_effort"},
	},

	// ─── Strict OpenAI-compatible validators ────────────────────────────────
	// These reject unknown assistant-message fields outright. Agent clients that
	// talk to reasoning models (Hermes and friends) echo the previous turn's
	// reasoning back on every assistant message; the strict validators answer
	// 400/422 ("extra_forbidden"), which removes the provider from every
	// multi-turn conversation. Providers that REQUIRE the field (DeepSeek, Kimi)
	// are deliberately absent — they are handled by the reasoning injector.
	{Provider: "groq", DropMessageFields: []string{"reasoning_content", "reasoning", "reasoning_details"}},
	{Provider: "mistral", DropMessageFields: []string{"reasoning_content", "reasoning", "reasoning_details"}},
	{Provider: "cerebras", DropMessageFields: []string{"reasoning_content", "reasoning", "reasoning_details"}},

	// ─── Cloudflare Workers AI ──────────────────────────────────────────────
	// Its oneOf root schema accepts content only as a plain string and rejects
	// the OpenAI content-part array. (9Router #1926)
	{Provider: "cloudflare-ai", FlattenContent: true},

	// ─── Output-cap ceilings ────────────────────────────────────────────────
	// VolcEngine Ark: the GLM-5 line is capped below its advertised ceiling.
	{Provider: "volcengine-ark", MatchSubstr: "glm-5", ClampToModelCeiling: true},

	// Ark caps the Kimi family at 32768 while the advertised ceiling is far
	// higher (Kimi-K2.7-Code resolves to 262144), so ClampToModelCeiling alone
	// leaves it uncapped and the request 400s with "integer above maximum value,
	// expected <= 32768". Pin the endpoint cap; the model ceiling still applies
	// via min() when a variant is lower. (9Router #kimimax)
	{
		Provider:            "volcengine-ark",
		MatchSubstr:         "kimi",
		MaxOutputCap:        32768,
		ClampToModelCeiling: true,
	},

	// ─── Output-cap floors ──────────────────────────────────────────────────
	// OpenCode free muse-spark 400s on any max_output_tokens below 16:
	//   "`max_output_tokens` The number must be `>= 16`."
	// A caller asking for a two-word answer (a smoke test, "say OK") therefore
	// fails on a model that would have served it. The reference has no such rule
	// — it never sends a cap that small — but any client may, so the floor is
	// enforced here rather than left to the caller's luck.
	{Provider: "opencode", MatchSubstr: "muse-spark", MinOutput: 16},
	{Provider: "opencode-go", MatchSubstr: "muse-spark", MinOutput: 16},
	{Provider: "opencode", MatchSubstr: "n-1.2-contributor", MinOutput: 16},
	{Provider: "opencode", MatchSubstr: "n-1.3-contributor", MinOutput: 16},
	{Provider: "opencode-go", MatchSubstr: "n-1.2-contributor", MinOutput: 16},
	{Provider: "opencode-go", MatchSubstr: "n-1.3-contributor", MinOutput: 16},
}
