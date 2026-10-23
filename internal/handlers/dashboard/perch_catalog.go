package dashboard

// Perch model catalogue.
//
// Perch publishes no /models route: its CLI asks the account for a default
// model (GET /api/perch-terminal/public-model-default) and then names the model
// per request. Without a branch here the generic registry lookup had nothing to
// match, so Import answered "Provider perch does not support models listing".
//
// The ids below are the CLI's own Starter identifiers. They are NOT the ids the
// backend serves — asking for "deepseek-flash-starter" is what maps to
// deepseek/deepseek-v4.1-flash-1017. Each was verified against a live Starter
// account on 2026-10-23 by sending its manualModelOptionId and reading back the
// served model in the response, so the catalogue only offers ids that answer.

import "9router/proxy/internal/providers"

// perchStaticModels lists the free Starter lane. Sourced from the registry entry
// so the Import payload and /v1/models cannot drift apart.
func perchStaticModels() []UpstreamModel {
	ids := providers.GetProviderModels("perch")
	out := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, UpstreamModel{
			ID:   id,
			Name: perchModelLabel(id),
			Kind: "llm",
		})
	}
	return out
}

// fetchPerchModels serves Import from the local catalogue. Supported is true so
// the UI's Import button completes; the warning explains why the list is fixed
// rather than fetched.
func (h *Handler) fetchPerchModels(providerID string) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	return &ModelFetchResult{
		Provider:  canonical,
		Models:    perchStaticModels(),
		Supported: true,
		Warning:   "Perch exposes no /models endpoint — using the verified Starter catalogue.",
	}, nil
}

// perchModelLabel is the display name the CLI shows for each id.
func perchModelLabel(id string) string {
	switch id {
	case "deepseek-flash-starter":
		return "DeepSeek V4.1 Flash 1017 (Starter)"
	case "glm-5.3-flash-starter":
		return "GLM 5.3 Flash (Starter)"
	case "minimax-m3.1-flash-starter":
		return "MiniMax M3.1 Flash (Starter)"
	case "beam-1.0-starter":
		return "Beam 1.0 (Starter)"
	case "laguna-s":
		return "Laguna S 2.1"
	default:
		return id
	}
}
