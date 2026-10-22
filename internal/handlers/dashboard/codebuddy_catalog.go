package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
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
	case "codebuddy-cn", "codebuddy-intl", "cbcn", "cbai", "workbuddy", "wb":
		return true
	default:
		return false
	}
}

// fetchCodebuddyModels returns the local catalogue for a CodeBuddy-family
// connection.
//
// There is no live path to attempt — the provider exposes no /models route, and
// probing it on every Import would only add latency before the same outcome.
// The result is reported as Supported (with a Warning) rather than an error so
// the UI's Import button completes and the operator sees why the list is
// static.
//
// WorkBuddy is a separate service (www.workbuddy.ai) with its own, much smaller
// live catalogue, so it must not inherit the CodeBuddy list: ids that the
// CodeBuddy gateway still serves (glm-5.0, glm-4.7, minimax-m2.5, deepseek-r1,
// …) are rejected by WorkBuddy, and importing them seeds models that fail on
// first use.
func (h *Handler) fetchCodebuddyModels(providerID string) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	if canonical == "workbuddy" {
		models := providers.GetProviderModels(canonical)
		out := make([]UpstreamModel, 0, len(models))
		for _, id := range models {
			out = append(out, UpstreamModel{ID: id, Name: id, Kind: inferModelKind(id)})
		}
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    out,
			Supported: true,
			Warning:   "WorkBuddy exposes no /models endpoint — using WorkBuddy's own catalogue.",
		}, nil
	}
	return &ModelFetchResult{
		Provider:  canonical,
		Models:    codebuddyStaticModels(),
		Supported: true,
		Warning:   "CodeBuddy exposes no /models endpoint — using the built-in catalogue.",
	}, nil
}

// codebuddyProbeModel returns a known-good model id for the CodeBuddy-family
// probe. Using a model the variant actually serves avoids the "unknown model"
// rejection that probing with arbitrary ids (e.g. gpt-4o-mini) would get.
//
// WorkBuddy answers on a smaller catalogue than CodeBuddy, so the probe id is
// taken from the provider's own registry entry rather than the CodeBuddy table.
func codebuddyProbeModel(provider string) string {
	if providers.ResolveAlias(provider) == "workbuddy" {
		if models := providers.GetProviderModels("workbuddy"); len(models) > 0 {
			return models[0]
		}
	}
	return codebuddyStaticCatalog[0].ID
}

// probeCodebuddyConnection probes a CodeBuddy account via a chat completion
// request. Unlike the generic probe, it injects the X-Conversation-* header
// family that the intl gateway requires to avoid HTTP 400 code 11128
// "conversation id missing". The CN gateway (copilot.tencent.com) does not
// gate on these yet but they are harmless to send.
func (h *Handler) probeCodebuddyConnection(ctx context.Context, connID string, raw map[string]any, provider string, out connectionTestResult) connectionTestResult {
	token := connString(raw, "accessToken", "access_token", "apiKey")
	if token == "" {
		out.Error = "No credential found on this connection (looked for accessToken / apiKey)."
		return out
	}

	// Derive the base URL from the provider variant. All three variants accept
	// the same request body shape; only the host and the X-Conversation headers
	// differ. WorkBuddy is its own service on its own host — routing it to
	// codebuddy.ai probed an account against the wrong gateway, so a perfectly
	// valid WorkBuddy credential reported as failing.
	baseURL := "https://copilot.tencent.com/v2/chat/completions"
	switch {
	case strings.EqualFold(provider, "workbuddy") || strings.EqualFold(provider, "wb"):
		baseURL = "https://www.workbuddy.ai/v2/chat/completions"
	case strings.EqualFold(provider, "codebuddy-intl") || strings.EqualFold(provider, "cbai"):
		baseURL = "https://www.codebuddy.ai/v2/chat/completions"
	}
	if h.codebuddyBaseOverride != "" {
		baseURL = h.codebuddyBaseOverride
	}

	cfg := providers.KnownProviders[provider]
	return h.probeCodebuddyAgainstURL(ctx, baseURL, connID, token, cfg, out, provider)
}

// probeCodebuddyAgainstURL is the testable core: it fires one probe against a
// fixed base URL and classifies the response. Split out so tests can point at
// an httptest server without mutating the global provider registry.
//
// The body is built by executor.BuildCodebuddyProbeBody and the headers by
// executor.CodebuddyConversationHeadersFor, so the probe sends the SAME shape a
// real chat request sends. A hand-rolled body is refused by the gateway's client
// fingerprint (code 11128) or the non-stream guard (code 11101), which would
// make a healthy account look broken.
func (h *Handler) probeCodebuddyAgainstURL(ctx context.Context, baseURL, connID, token string, cfg providers.ProviderConfig, out connectionTestResult, provider ...string) connectionTestResult {
	probeProvider := ""
	if len(provider) > 0 {
		probeProvider = provider[0]
	}
	body, err := executor.BuildCodebuddyProbeBody(codebuddyProbeModel(probeProvider))
	if err != nil {
		out.Error = "Could not build CodeBuddy probe body: " + err.Error()
		return out
	}

	client := h.httpClientForTest()
	ctx, cancel := context.WithTimeout(ctx, connectionTestTimeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		out.Error = "Could not build CodeBuddy probe request: " + err.Error()
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	if ua := cfg.StaticHeaders["User-Agent"]; ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	for k, v := range executor.CodebuddyConversationHeadersFor(connID) {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			out.Error = fmt.Sprintf("Test timed out after %ds.", int(connectionTestTimeout().Seconds()))
			return out
		}
		out.Error = "CodeBuddy probe request failed: " + err.Error()
		return out
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, connectionTestMaxBody))
	text := string(bodyBytes)
	out.Status = resp.StatusCode

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		out.Valid = true
		out.TestedAt = time.Now().UTC().Format(time.RFC3339)

	case resp.StatusCode == 401 || resp.StatusCode == 403:
		out.Error = fmt.Sprintf("Credential rejected (HTTP %d): %s",
			resp.StatusCode, truncate(sanitizeUpstreamText(text), 240))

	case resp.StatusCode == 400:
		// 400 from CodeBuddy carries a structured body: {"code":N,"msg":"..."}.
		// The token was accepted here (401/403 would have fired otherwise), so a
		// gateway-side rejection is NOT a credential verdict — report it amber.
		var wrapper struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(bodyBytes, &wrapper) == nil && wrapper.Code != 0 {
			out.Valid = true
			out.Warning = fmt.Sprintf("Probe reached the gateway but was rejected (code %d: %s). The credential was accepted.",
				wrapper.Code, truncate(sanitizeUpstreamText(wrapper.Msg), 200))
			return out
		}
		out.Valid = true
		out.Warning = fmt.Sprintf("Probe was inconclusive (HTTP 400) — the request was rejected but the credential was not. Detail: %s",
			truncate(sanitizeUpstreamText(text), 200))

	case resp.StatusCode == 429:
		out.Valid = true
		out.Warning = "Provider rate-limited the probe (HTTP 429). The credential was not judged."

	default:
		out.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(sanitizeUpstreamText(text), 240))
	}
	return out
}
