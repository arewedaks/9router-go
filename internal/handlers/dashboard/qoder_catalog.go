package dashboard

import "9router/proxy/internal/providers"

// qoder_catalog.go — Import catalogue for Qoder.
//
// Qoder publishes no /models route. Its agent surface
// (api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation) is a
// COSY-signed SSE endpoint, and every plain listing candidate answers 404
// (openapi.qoder.sh/api/v1/models, api3.qoder.sh/algo/api/v{1,2}/model/list).
// Import therefore answered "Provider qoder does not support models listing"
// and a freshly signed-in account imported zero models.
//
// The ids come from the shipped free-model catalogue, which carries the same
// list as the upstream registry (qwen3.8-max-preview … minimax-m3), so they
// cannot drift from what the dashboard already badges as free.
//
// ponytail: no live probe. If Qoder publishes a model list, drop this file and
// write a fetcher like kiro_catalog.go.

// qoderStaticModels serves Import from the catalogue.
func qoderStaticModels() []UpstreamModel {
	ids := providers.CatalogModelIDs("qoder")
	out := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, UpstreamModel{ID: id, Name: id, Kind: "llm"})
	}
	return out
}

func (h *Handler) fetchQoderModels(providerID string) (*ModelFetchResult, error) {
	return &ModelFetchResult{
		Provider:  providers.ResolveAlias(providerID),
		Models:    qoderStaticModels(),
		Supported: true,
		Warning:   "Qoder exposes no /models endpoint — using the catalogue of model ids it serves.",
	}, nil
}
