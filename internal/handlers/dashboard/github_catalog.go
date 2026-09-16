package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// GitHub Copilot model discovery.
//
// Unlike CodeBuddy, Copilot DOES expose a live per-account catalogue at
//
//	GET https://api.githubcopilot.com/models
//
// authenticated with the Copilot bearer token + the standard Copilot chat
// headers. The response shape is
//
//	{ "data": [ { "id", "name", "policy", "capabilities",
//	              "supported_endpoints", ... } ] }
//
// Crucially, only models the account is ENTITLED to appear in the live response.
// Parsing it directly is therefore the entitlement filter: importing it never
// advertises a model the account cannot call (which is what produced upstream
// `400 ... not supported` errors in the reference). Note that entitlement is
// carried by policy.state, NOT by model_picker_enabled — see
// isRoutableGitHubChatModel for why that distinction cost real models.
//
// This mirrors OmniRoute's open-sse/services/githubCopilotModels.ts, including
// its two design decisions:
//
//   - Filtering is CAPABILITY-driven, not an id allowlist. A hardcoded list
//     silently drops newly-entitled models (grok-4.6, mai-code-1.1-flash, …) and
//     has to be edited for every upstream release.
//   - A curated STATIC catalogue is kept purely as an offline/unauthed fallback,
//     so Import degrades to a useful list instead of failing when discovery is
//     unreachable. It is never used to gate a successful live response.

// githubCopilotModelsURL is the per-account catalogue endpoint.
const githubCopilotModelsURL = "https://api.githubcopilot.com/models"

// githubCopilotStaticFallbackModels is the offline fallback catalogue, ported
// from OmniRoute's GITHUB_COPILOT_STATIC_FALLBACK_MODELS. It is a curated set of
// known-good chat ids used ONLY when live discovery is unavailable.
var githubCopilotStaticFallbackModels = []string{
	"claude-fable-5",
	"claude-opus-5",
	"claude-opus-4.8-fast",
	"claude-opus-4.8",
	"claude-opus-4.7",
	"claude-opus-4.6",
	"claude-sonnet-4.6",
	"claude-opus-4.5",
	"claude-sonnet-5",
	"claude-sonnet-4.5",
	"claude-haiku-4.5",
	"gemini-3.1-pro-preview",
	"gemini-3.7-flash",
	"gemini-3.6-flash",
	"gemini-3.5-flash",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.4-mini",
	"gpt-5.4-nano",
	"gpt-5.3-codex",
	"gpt-5-mini",
	"gpt-4o-2024-11-20",
	"gpt-4o-mini",
	"gpt-4-0125-preview",
	"kimi-k2.7-code",
	"mai-code-1-flash",
	"mai-code-1.1-flash",
	"grok-4.6",
}

// isGitHubProvider reports whether a provider id (canonical or alias) addresses
// GitHub Copilot.
func isGitHubProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "github", "gh", "copilot":
		return true
	default:
		return false
	}
}

// githubCopilotCatalogItem is one row of the live /models response. Only the
// fields we inspect are declared; the rest is ignored by the decoder.
//
// model_picker_enabled is intentionally absent: it is an editor-UI hint, not an
// entitlement signal, and filtering on it emptied the catalogue for real
// accounts (see isRoutableGitHubChatModel).
type githubCopilotCatalogItem struct {
	ID          string `json:"id"`
	Model       string `json:"model"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Policy      struct {
		State string `json:"state"`
	} `json:"policy"`
	Capabilities struct {
		Type                string   `json:"type"`
		SupportedEndpoints  []string `json:"supported_endpoints"`
		SupportsParallelTCs bool     `json:"supports_parallel_tool_calls"`
	} `json:"capabilities"`
	SupportedEndpoints []string `json:"supported_endpoints"`
}

// githubCopilotCatalogEnvelope is the top-level /models response.
type githubCopilotCatalogEnvelope struct {
	Data   []githubCopilotCatalogItem `json:"data"`
	Models []githubCopilotCatalogItem `json:"models"`
}

// isRoutableGitHubChatModel decides whether a live /models row is a routable
// chat model.
//
// Capability-driven (rename-robust) rather than an id allowlist: any entitled
// model whose capabilities.type is "chat" (or that carries a chat-shaped
// supported_endpoints entry) is kept, so a newly-entitled model shows up with no
// code change.
//
// Entitlement is governed by policy.state, which is the authoritative signal: an
// account only gets to call models marked "enabled". model_picker_enabled is
// deliberately NOT used as a filter — it means "show this in the editor model
// picker", and every entitled model can legitimately carry false (verified
// against a live account: 9 models with policy.state=enabled all reported
// model_picker_enabled=false). Treating it as entitlement silently empties the
// catalogue, so the field is parsed for completeness but never gates a row.
func isRoutableGitHubChatModel(item githubCopilotCatalogItem) bool {
	if state := strings.TrimSpace(item.Policy.State); state != "" && state != "enabled" {
		return false
	}

	if capType := strings.TrimSpace(item.Capabilities.Type); capType != "" {
		return capType == "chat"
	}

	// No capabilities.type — fall back to the supported_endpoints shape. A chat
	// model exposes /chat/completions, /responses, or /v1/messages.
	endpoints := item.SupportedEndpoints
	if len(endpoints) == 0 {
		endpoints = item.Capabilities.SupportedEndpoints
	}
	if len(endpoints) > 0 {
		for _, e := range endpoints {
			s := strings.TrimSpace(e)
			if strings.Contains(s, "/chat/completions") ||
				strings.Contains(s, "/responses") ||
				strings.Contains(s, "/v1/messages") {
				return true
			}
		}
		return false
	}

	// Neither signal present: keep it unless the id looks like a known non-chat
	// utility. This keeps discovery permissive without re-introducing a brittle
	// positive allowlist.
	id := strings.ToLower(strings.TrimSpace(item.ID))
	if id == "" {
		id = strings.ToLower(strings.TrimSpace(item.Model))
	}
	if id == "" {
		return false
	}
	return !strings.Contains(id, "embedding") && id != "gpt-41-copilot"
}

// fetchGitHubModels performs live discovery, falling back to the static
// catalogue when the endpoint is unreachable or unauthenticated. The fallback is
// reported as a Warning rather than an error so Import still completes.
func (h *Handler) fetchGitHubModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	if canonical == "" || isGitHubProvider(providerID) {
		canonical = "github"
	}

	token, _ := credentialFromData(data)
	// credentialFromData prefers accessToken, but for Copilot the catalogue needs
	// the *Copilot* bearer token — the GitHub access token is not accepted by
	// api.githubcopilot.com. Prefer the derived copilotToken when present.
	if ct := copilotTokenFromData(data); ct != "" {
		token = ct
	}
	if strings.TrimSpace(token) == "" {
		// No credential: hand back the static catalogue so the operator sees the
		// shape of what a connected account would offer.
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    githubStaticFallbackModels(),
			Supported: true,
			Warning:   "No Copilot credential on this connection — using the built-in fallback catalogue.",
		}, nil
	}

	req, err := http.NewRequest(http.MethodGet, githubCopilotModelsURL, nil)
	if err != nil {
		return nil, err
	}
	// The Copilot bearer token authenticates the catalogue call, and the CLI
	// identity headers are what unlock the full entitled set.
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range providers.GitHubCopilotChatHeaders("application/json", "user") {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    githubStaticFallbackModels(),
			Supported: true,
			Warning:   "Live Copilot catalogue unreachable (" + err.Error() + ") — using the built-in fallback catalogue.",
		}, nil
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    githubStaticFallbackModels(),
			Supported: true,
			Warning:   "Copilot catalogue returned HTTP " + strconv.Itoa(resp.StatusCode) + " — using the built-in fallback catalogue.",
		}, nil
	}

	models := parseGitHubCopilotCatalog(raw)
	if len(models) == 0 {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    githubStaticFallbackModels(),
			Supported: true,
			Warning:   "Copilot catalogue contained no entitled chat models — using the built-in fallback catalogue.",
		}, nil
	}

	sortUpstreamModels(models)
	return &ModelFetchResult{
		Provider:  canonical,
		Models:    models,
		Supported: true,
	}, nil
}

// parseGitHubCopilotCatalog turns a /models body into upstream model rows,
// keeping only entitled chat models (see isRoutableGitHubChatModel). Order is
// the upstream order, deduplicated by id.
func parseGitHubCopilotCatalog(raw []byte) []UpstreamModel {
	var env githubCopilotCatalogEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}

	items := env.Data
	if len(items) == 0 {
		items = env.Models
	}

	seen := make(map[string]bool)
	models := make([]UpstreamModel, 0, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = strings.TrimSpace(item.Model)
		}
		if id == "" || seen[id] {
			continue
		}
		if !isRoutableGitHubChatModel(item) {
			continue
		}
		seen[id] = true

		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.DisplayName)
		}
		models = append(models, UpstreamModel{ID: id, Name: name, Kind: inferModelKind(id)})
	}
	return models
}

// copilotTokenFromData returns the Copilot bearer token stored on a GitHub
// connection, if any.
//
// The device-flow handler derives a Copilot token at save time and stores it
// under providerSpecificData.copilotToken. The generic credentialFromData
// prefers the top-level accessToken (the GitHub token), which the Copilot API
// rejects, so the catalogue call must read this field first.
func copilotTokenFromData(data string) string {
	if strings.TrimSpace(data) == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return ""
	}
	psd, ok := m["providerSpecificData"].(map[string]any)
	if !ok {
		return ""
	}
	if v, ok := psd["copilotToken"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// githubStaticFallbackModels builds UpstreamModel rows from the curated id list.
func githubStaticFallbackModels() []UpstreamModel {
	models := make([]UpstreamModel, 0, len(githubCopilotStaticFallbackModels))
	for _, id := range githubCopilotStaticFallbackModels {
		models = append(models, UpstreamModel{ID: id, Kind: inferModelKind(id)})
	}
	return models
}
