package dashboard

import (
	"strings"

	"9router/proxy/internal/providers"
)

// Freebuff / Codebuff has no model listing endpoint. Probed live against a
// valid account:
//
//	GET /api/v1/models, /api/models, /api/v1/agents  -> 404 (HTML, not JSON)
//
// So the upstream "Import from /models" button can never work for it, and this
// fork answered "Provider freebuff does not support models listing" — leaving
// the operator with a connected account and zero models. OmniRoute solves this
// the same way it solves CodeBuddy: it ships a local catalogue
// (open-sse/config/providers/registry/freebuff/index.ts) and never calls a
// /models route.
//
// The ids below are not copied from that registry. They are the keys of the
// router's own `freebuffRootAgentByModel` (internal/proxy/executor/freebuff.go),
// which is the set of models this proxy can actually run: a model absent from
// that map has no root agent, so offering it in Import would hand the operator a
// row that fails on the first request. Keeping Import and the executor on one
// source is the point — the two lists drifting is how a provider ends up
// advertising models it cannot serve.
//
// The executor map is the authority; this file only adds display names.

// freebuffStaticModel pairs an upstream id with a friendly label.
type freebuffStaticModel struct {
	ID   string
	Name string
}

// freebuffStaticCatalog is the catalogue Import returns. It must stay a subset
// of the executor's root-agent map; TestFreebuffCatalogMatchesExecutor enforces
// that, so adding a model here without teaching the executor to route it fails
// the build rather than failing at request time.
var freebuffStaticCatalog = []freebuffStaticModel{
	{"deepseek/deepseek-v4-flash", "DeepSeek V4 Flash"},
	{"z-ai/glm-5.2", "GLM 5.2"},
	{"z-ai/glm-5.3-flash", "GLM 5.3 Flash"},
	{"mimo/mimo-v2.5", "MiMo v2.5"},
	{"openai/gpt-5.6-luna", "GPT-5.6 Luna"},
	{"upstage/solar-pro4", "Solar Pro 4"},
	{"meta/muse-spark-1.2-contributor", "Muse Spark 1.2 Contributor"},
	{"anthropic/claude-fable-5", "Claude Fable 5"},
}

// freebuffStaticModels returns the catalogue as importable upstream models.
func freebuffStaticModels() []UpstreamModel {
	out := make([]UpstreamModel, 0, len(freebuffStaticCatalog))
	for _, m := range freebuffStaticCatalog {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out = append(out, UpstreamModel{
			ID:   id,
			Name: m.Name,
		})
	}
	sortUpstreamModels(out)
	return out
}

// isFreebuffProvider reports whether a provider id (canonical or alias) is
// Freebuff. Uses the registry alias map rather than a literal so "fb" resolves
// the same way everywhere else does.
func isFreebuffProvider(id string) bool {
	return providers.ResolveAlias(strings.TrimSpace(id)) == "freebuff"
}
