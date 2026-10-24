package dashboard

import "9router/proxy/internal/providers"

// qoder_catalog.go — Import catalogue for Qoder.
//
// Qoder publishes no public /models route. The signed one it does have
// (GET api3.qoder.sh/algo/api/v2/model/list, COSY-signed like the chat path)
// answers per-plan catalogues and is not reachable with a plain bearer call;
// every unsigned listing candidate answers 404. Import therefore used to reply
// "Provider qoder does not support models listing".
//
// The ids below are the upstream model KEYS the agent surface accepts, verified
// against a live account — not the display names its /model/list advertises.
// Sending a display name ("qwen3.8-max-preview", "deepseek-v4-pro") is refused
// with code 112; sending the key ("qmodel_38max", "dfmodel") streams a reply.

// qoderModelNames maps an upstream key to the display name the Qoder client
// shows. Keys absent here fall back to the key itself.
var qoderModelNames = map[string]string{
	"auto":          "Auto",
	"ultimate":      "Ultimate",
	"performance":   "Performance",
	"efficient":     "Efficient",
	"lite":          "Lite",
	"qmodel_38max":  "Qwen3.8-Max",
	"qmodel":        "Qwen3.7-Plus",
	"qmodel_latest": "Qwen3.7-Max",
	"qfmodel":       "Qwen3.8-Flash",
	"kmodel_latest": "Kimi-K3.1-Pro",
	"kmodel":        "Kimi-K2.8-Preview",
	"gmodel":        "GLM-5.3",
	"gfmodel":       "GLM-5.3-Flash",
	"dmodel":        "DeepSeek-V4-Pro",
	"dfmodel":       "DeepSeek-Flash",
	"mmodel":        "MiniMax-M3.1-Flash",
}

// qoderStaticModels serves Import from the registry, which carries the upstream
// model keys. A key outside the signed-in account's plan is refused upstream
// with code 112 and surfaces as a "not included in your current plan" error
// rather than an empty reply.
//
// ponytail: no live probe. If Qoder publishes an unsigned model list, drop this
// file and write a fetcher like kiro_catalog.go.
func qoderStaticModels() []UpstreamModel {
	ids := providers.GetProviderModels("qoder")
	out := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		name := qoderModelNames[id]
		if name == "" {
			name = id
		}
		out = append(out, UpstreamModel{ID: id, Name: name, Kind: "llm"})
	}
	return out
}

func (h *Handler) fetchQoderModels(providerID string) (*ModelFetchResult, error) {
	return &ModelFetchResult{
		Provider:  providers.ResolveAlias(providerID),
		Models:    qoderStaticModels(),
		Supported: true,
		Warning:   "Qoder exposes no public /models endpoint — using the registry list of model keys it accepts. Models outside your plan answer with a plan error.",
	}, nil
}
