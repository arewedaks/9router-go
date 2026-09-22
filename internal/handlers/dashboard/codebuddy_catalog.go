package dashboard

import (
	"strings"

	"9router/proxy/internal/providers"
)

// CodeBuddy (Tencent copilot.tencent.com / www.codebuddy.ai) has **no** model
// listing endpoint. Probed live against a valid account:
//
//	GET /v2/models, /v1/models            -> 404
//	GET /v2/model, /v2/models/list, ...   -> 404
//
// So the upstream "Import from /models" button can never work for it, and the
// fork used to answer "Provider codebuddy does not support models listing".
// OmniRoute solves this the same way it solves Antigravity: it ships a local
// catalogue and never calls a /models route (see
// shared/constants/providers/oauth.ts, which describes the CodeBuddy catalog as
// "GLM / Kimi / MiniMax / DeepSeek / Hunyuan" but declares no fetch endpoint).
//
// We therefore return a static catalogue instead of a hard failure, so Import
// succeeds and the operator at least gets the models the CLI actually offers.
// The list below is the union of the two CodeBuddy variants' model locks
// observed on real accounts (glm-*, minimax-*, deepseek-*, hunyuan/hy3-*),
// with the ids that upstream currently rejects dropped.

// codebuddyStaticModel pairs an upstream id with a friendly label.
type codebuddyStaticModel struct {
	ID   string
	Name string
}

// codebuddyRetiredModelIDs are ids the gateway answers with code 11102
// ("service info not found" / "model is not available"), so offering them in
// Import would hand the router a model that always fails.
//
// Measured against a live codebuddy-intl account, one request per model. Note
// that a second error code appears on this endpoint: 14003 means "too many
// requests", which is transient and must NOT be read as "model unavailable" —
// an early probe conflated the two and wrongly marked glm-5.1 as missing.
//
// VansRouter still lists several of these; they are excluded here because the
// live gateway rejects them. Remove an entry if the gateway re-enables it.
var codebuddyRetiredModelIDs = map[string]bool{
	// Confirmed 11102 on a live account.
	"deepseek-v4-flash":  true,
	"deepseek-v4-pro":    true,
	"deepseek-v3-2-volc": true,
	"deepseek-v3.2":      true,
	"deepseek-r1":        true,
	"glm-5.0":            true,
	"glm-5.0-turbo":      true,
	"glm-4.7":            true,
	"glm-4.6":            true,
	"glm-4.5":            true,
	"minimax-m2.7":       true,
	"minimax-m2.5":       true,
	"minimax-m1":         true,
	"kimi-k2":            true,
	"hy3-preview":        true,
	"hunyuan-t1":         true,
	"hunyuan-turbo":      true,
}

// codebuddyStaticCatalog is the fallback catalogue for codebuddy-cn and
// codebuddy-intl. Both variants expose the same backend lineage, so one union
// list serves both. The ids/names are taken from VansRouter's registry entries
// (open-sse/providers/registry/codebuddy-{cn,intl}.js), which list the models
// the two gateways actually serve.
var codebuddyStaticCatalog = []codebuddyStaticModel{
	// GLM (Zhipu) family.
	{"glm-5.2", "GLM 5.2"},
	{"glm-5.1", "GLM 5.1"},
	{"glm-5.0", "GLM 5.0"},
	{"glm-5.0-turbo", "GLM 5.0 Turbo"},
	{"glm-5v-turbo", "GLM 5v Turbo"},
	{"glm-4.7", "GLM 4.7"},
	{"glm-4.6", "GLM 4.6"},
	{"glm-4.5", "GLM 4.5"},
	// MiniMax family.
	{"minimax-m3", "MiniMax M3"},
	{"minimax-m2.7", "MiniMax M2.7"},
	{"minimax-m2.5", "MiniMax M2.5"},
	{"minimax-m1", "MiniMax M1"},
	// DeepSeek family.
	{"deepseek-v4.1-flash", "DeepSeek V4.1 Flash"},
	{"deepseek-v4-pro", "DeepSeek V4 Pro"},
	{"deepseek-v3-2-volc", "DeepSeek V3.2 (Volc)"},
	{"deepseek-v3.2", "DeepSeek V3.2"},
	{"deepseek-r1", "DeepSeek R1"},
	// Tencent Hunyuan.
	{"hy3-preview", "Hunyuan 3 (Preview)"},
	{"hunyuan-t1", "Hunyuan T1"},
	{"hunyuan-turbo", "Hunyuan Turbo"},
	// Moonshot Kimi (offered by the shared catalogue).
	{"kimi-k2.7", "Kimi K2.7 Code"},
	{"kimi-k2.6", "Kimi K2.6"},
	{"kimi-k2.5", "Kimi K2.5"},
	{"kimi-k2", "Kimi K2"},
}

// codebuddyStaticModels returns the fallback catalogue for a CodeBuddy
// provider, filtered through isDiscoverableCodebuddyModel so retired ids never
// reach the UI.
func codebuddyStaticModels() []UpstreamModel {
	out := make([]UpstreamModel, 0, len(codebuddyStaticCatalog))
	for _, m := range codebuddyStaticCatalog {
		if !isDiscoverableCodebuddyModel(m.ID) {
			continue
		}
		out = append(out, UpstreamModel{
			ID:   m.ID,
			Name: m.Name,
			Kind: inferModelKind(m.ID),
		})
	}
	return out
}

// isDiscoverableCodebuddyModel reports whether a catalogue id should be offered.
// It mirrors the Antigravity discoverability rules: reject empty ids, retired
// ids, and the non-chat surfaces (image/audio/tts/embedding/video) that a
// chat-completions-only client can never call.
func isDiscoverableCodebuddyModel(modelID string) bool {
	id := strings.TrimSpace(modelID)
	if id == "" {
		return false
	}
	if codebuddyRetiredModelIDs[id] {
		return false
	}
	lower := strings.ToLower(id)
	for _, token := range []string{"image", "imagen", "audio", "tts", "embedding", "embed", "video", "veo", "rerank"} {
		if lower == token ||
			strings.HasPrefix(lower, token+"-") ||
			strings.HasSuffix(lower, "-"+token) ||
			strings.Contains(lower, "-"+token+"-") {
			return false
		}
	}
	return true
}

// isCodebuddyProvider reports whether a provider id (canonical or alias)
// addresses either CodeBuddy variant.
func isCodebuddyProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "codebuddy-cn", "codebuddy-intl", "cbcn", "cbai":
		return true
	default:
		return false
	}
}

// fetchCodebuddyModels returns the local catalogue for a CodeBuddy connection.
//
// There is no live path to attempt — the provider exposes no /models route, and
// probing it on every Import would only add latency before the same outcome.
// The result is reported as Supported (with a Warning) rather than an error so
// the UI's Import button completes and the operator sees why the list is
// static.
func (h *Handler) fetchCodebuddyModels(providerID string) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	return &ModelFetchResult{
		Provider:  canonical,
		Models:    codebuddyStaticModels(),
		Supported: true,
		Warning:   "CodeBuddy exposes no /models endpoint — using the built-in catalogue.",
	}, nil
}
