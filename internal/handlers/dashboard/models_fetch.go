package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// ModelFetchResult is the payload returned by the "Import from /models"
// action on the provider detail page (upstream GET /api/providers/[id]/models).
type ModelFetchResult struct {
	Provider  string          `json:"provider"`
	Models    []UpstreamModel `json:"models"`
	Warning   string          `json:"warning,omitempty"`
	Supported bool            `json:"supported"`
	Error     string          `json:"error,omitempty"`
	Status    int             `json:"status,omitempty"`
}

// UpstreamModel is one entry from a provider's /models endpoint. Providers
// return wildly different shapes, so the important identity fields are
// normalised while the raw object is preserved for kind detection.
type UpstreamModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
	// IsFree mirrors the dashboard's Free badge (see providers.IsModelFreeBadge)
	// so the Import modal can show the same signal the detail page does. It is
	// filled by the handler, which knows the canonical provider id.
	IsFree bool `json:"isFree,omitempty"`
}

// upstreamResponse covers the common list envelopes returned by providers:
// OpenAI uses {"data":[...]}, Google/Ollama use {"models":[...]}, some endpoints use {"results":[...]}.
type upstreamResponse struct {
	Data    []map[string]any `json:"data"`
	Models  []map[string]any `json:"models"`
	Results []map[string]any `json:"results"`
}

// modelFetcher describes how to list models for a specific provider family.
type modelFetcher struct {
	url    string
	method string
	// authHeader is applied with the credential value (e.g. "x-api-key").
	authHeader string
	// authQuery appends ?key=<credential> when set (Google style).
	authQuery string
	// bearer adds an Authorization: Bearer <credential> header.
	bearer  bool
	headers map[string]string
}

// knownModelFetchers mirrors the upstream v map in
// server/app/api/providers/[id]/models/route.js.
var knownModelFetchers = map[string]modelFetcher{
	"anthropic": {
		url:        "https://api.anthropic.com/v1/models",
		method:     http.MethodGet,
		authHeader: "x-api-key",
		headers:    map[string]string{"anthropic-version": "2023-06-01"},
	},
	"gemini": {
		url:       "https://generativelanguage.googleapis.com/v1beta/models",
		method:    http.MethodGet,
		authQuery: "key",
	},
	"google": {
		url:       "https://generativelanguage.googleapis.com/v1beta/models",
		method:    http.MethodGet,
		authQuery: "key",
	},
	"openai": {
		url:    "https://api.openai.com/v1/models",
		method: http.MethodGet,
		bearer: true,
	},
	"openrouter": {
		url:    "https://openrouter.ai/api/v1/models",
		method: http.MethodGet,
		bearer: true,
	},
	"deepseek": {
		url:    "https://api.deepseek.com/models",
		method: http.MethodGet,
		bearer: true,
	},
	"groq": {
		url:    "https://api.groq.com/openai/v1/models",
		method: http.MethodGet,
		bearer: true,
	},
	"mistral": {
		url:    "https://api.mistral.ai/v1/models",
		method: http.MethodGet,
		bearer: true,
	},
	"xai": {
		url:    "https://api.x.ai/v1/models",
		method: http.MethodGet,
		bearer: true,
	},
	"nvidia": {
		url:    "https://integrate.api.nvidia.com/v1/models",
		method: http.MethodGet,
		bearer: true,
	},
	"cerebras": {
		url:    "https://api.cerebras.ai/v1/models",
		method: http.MethodGet,
		bearer: true,
	},
	"ollama": {
		url:    "http://localhost:11434/api/tags",
		method: http.MethodGet,
	},
	// OpenCode Zen. Both tiers publish an OpenAI-style /models route, so they
	// need no bespoke fetcher — only a registry entry, which they lacked: the
	// provider page answered "does not support models listing" for a route that
	// returns 200. The keyless free tier authenticates with the literal key
	// "public" (KnownProviders["opencode"].DefaultAPIKey), so the bearer header
	// is always populated; the client header matches the one the executor sends.
	// Upstream lists 71 ids here and 38 on the Go tier (verified live).
	"opencode": {
		url:    "https://opencode.ai/zen/v1/models",
		method: http.MethodGet,
		bearer: true,
		headers: map[string]string{
			"x-opencode-client": "desktop",
		},
	},
	"opencode-go": {
		url:    "https://opencode.ai/zen/go/v1/models",
		method: http.MethodGet,
		bearer: true,
		headers: map[string]string{
			"x-opencode-client": "desktop",
		},
	},
}

// credentialFromData pulls the usable secret out of a connection's JSON blob.
// Order matters: the most specific field wins. Never returned to the client.
func credentialFromData(data string) (token, baseURL string) {
	if strings.TrimSpace(data) == "" {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return "", ""
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	token = str("apiKey", "api_key", "accessToken", "access_token", "token", "copilotToken")
	if psd, ok := m["providerSpecificData"].(map[string]any); ok {
		if v, ok := psd["baseUrl"].(string); ok && strings.TrimSpace(v) != "" {
			baseURL = strings.TrimSpace(v)
		}
		if token == "" {
			if v, ok := psd["copilotToken"].(string); ok {
				token = strings.TrimSpace(v)
			}
		}
	}
	if baseURL == "" {
		baseURL = str("baseUrl", "base_url", "endpoint")
	}
	return token, baseURL
}

// fetchUpstreamModels returns the live model list for a connection, mirroring
// the three branches in the upstream route:
//
//  1. OpenAI-compatible endpoint  -> {baseUrl}/models  (Bearer)
//  2. Anthropic-compatible endpoint -> {baseUrl}/models (x-api-key)
//  3. Known registry provider     -> hardcoded endpoint + auth style
func (h *Handler) fetchUpstreamModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	token, baseURL := credentialFromData(data)

	// Antigravity has no OpenAI-style /models endpoint; its catalogue lives
	// behind the Cloud Code v1internal:fetchAvailableModels POST. Handle it
	// before the generic registry lookup, which has no entry for it.
	if isAntigravityProvider(canonical) || isAntigravityProvider(providerID) {
		return h.fetchAntigravityModels(canonical, data, timeout)
	}

	// Freebuff / Codebuff has no /models route either (404 on every candidate),
	// so it too is served from a local catalogue — see freebuff_catalog.go.
	// Without this a connected account imported zero models and the UI said
	// "does not support models listing".
	if isFreebuffProvider(canonical) || isFreebuffProvider(providerID) {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    freebuffStaticModels(),
			Supported: true,
		}, nil
	}

	// CodeBuddy (both variants) has no /models route at all, so it can only be
	// served from a local catalogue — see codebuddy_catalog.go. Handle it before
	// the generic registry lookup, which has no entry for it and would otherwise
	// report "does not support models listing".
	if isCodebuddyProvider(canonical) || isCodebuddyProvider(providerID) {
		return h.fetchCodebuddyModels(canonical)
	}

	// GitHub Copilot has a live per-account catalogue at api.githubcopilot.com/models
	// that requires the Copilot bearer token plus the CLI identity headers, which
	// the generic registry branch cannot express. Handle it explicitly; it falls
	// back to a static list when the live call fails.
	if isGitHubProvider(canonical) || isGitHubProvider(providerID) {
		return h.fetchGitHubModels(canonical, data, timeout)
	}

	// Cline / ClinePass publish live catalogues at /api/v1/ai/cline/models and
	// /api/v1/ai/cline/recommended-models, neither of which is an OpenAI-style
	// /models route the generic branch could discover. Handle them explicitly,
	// keeping their model namespaces disjoint (see cline_catalog.go).
	if isClineFamily(canonical) || isClineFamily(providerID) {
		return h.fetchClineModels(providerID, data, timeout)
	}

	// Cloudflare Workers AI has no /models route: its catalogue lives at
	// /accounts/{accountId}/ai/models/search, which needs the account id in the
	// path and paginates. The registry branch below cannot express either, and
	// VansRouter's derived URL answers 405. See cloudflare_catalog.go.
	if isCloudflareProvider(canonical) || isCloudflareProvider(providerID) {
		return h.fetchCloudflareModels(providerID, data, timeout)
	}

	// Kiro publishes its catalogue through a CodeWhisperer control-plane call
	// (ListAvailableModels), not an OpenAI-style /models route, and the account's
	// entitlement differs from the static registry list. Handle it explicitly so
	// Import reflects what the account can actually call.
	if isKiroProvider(canonical) || isKiroProvider(providerID) {
		return h.fetchKiroModels(canonical, data, timeout)
	}

	client := &http.Client{Timeout: timeout}

	// Branch 1 & 2: user-defined compatible endpoints need a base URL.
	if providers.IsCustomCompatible(providerID) || providers.IsCustomCompatible(canonical) {
		var modelsPath string
		if baseURL == "" && h.repo != nil {
			if node, nodeData, err := h.repo.GetProviderNodeByID(providerID); err == nil && node != nil && nodeData != nil {
				baseURL = nodeData.BaseURL
				modelsPath = nodeData.ModelsPath
			} else if node, nodeData, err := h.repo.GetProviderNodeByID(canonical); err == nil && node != nil && nodeData != nil {
				baseURL = nodeData.BaseURL
				modelsPath = nodeData.ModelsPath
			} else if node, nodeData, err := h.repo.GetProviderNodeByPrefix(providerID); err == nil && node != nil && nodeData != nil {
				baseURL = nodeData.BaseURL
				modelsPath = nodeData.ModelsPath
			}
		}
		if baseURL == "" {
			return nil, fmt.Errorf("no base URL configured for OpenAI/Anthropic compatible provider")
		}
		isAnthropic := strings.HasPrefix(strings.ToLower(providerID), providers.CustomAnthropicPrefix) ||
			strings.HasPrefix(strings.ToLower(canonical), providers.CustomAnthropicPrefix)
		base := strings.TrimRight(baseURL, "/")
		if isAnthropic {
			base = strings.TrimSuffix(base, "/messages")
		}
		if modelsPath == "" {
			modelsPath = "/models"
		}
		cleanPath := "/" + strings.TrimLeft(modelsPath, "/")
		url := base
		if !strings.HasSuffix(base, cleanPath) {
			url = base + cleanPath
		}
		headers := map[string]string{"Content-Type": "application/json"}
		if isAnthropic {
			if token != "" {
				headers["x-api-key"] = token
				headers["Authorization"] = "Bearer " + token
			}
			headers["anthropic-version"] = "2023-06-01"
		} else {
			if token != "" {
				headers["Authorization"] = "Bearer " + token
			}
		}
		return h.doModelRequest(client, canonical, url, http.MethodGet, headers, nil)
	}

	// Branch 3: known registry provider.
	f, ok := knownModelFetchers[canonical]
	if !ok || f.url == "" {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    []UpstreamModel{},
			Supported: false,
			Error:     fmt.Sprintf("Provider %s does not support models listing", canonical),
		}, nil
	}
	if token == "" && f.authQuery == "" && f.authHeader == "" && f.bearer {
		// A keyless provider carries no stored credential; its registry default
		// key (e.g. opencode's literal "public") is the credential. Without this
		// the fetch fails even though the endpoint is reachable anonymously.
		if cfg, ok := providers.KnownProviders[canonical]; ok && cfg.DefaultAPIKey != "" {
			token = cfg.DefaultAPIKey
		} else {
			return nil, fmt.Errorf("no valid credential found for %s", canonical)
		}
	}

	headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	for k, v := range f.headers {
		headers[k] = v
	}
	query := map[string]string{}
	if f.bearer {
		headers["Authorization"] = "Bearer " + token
	}
	if f.authHeader != "" {
		headers[f.authHeader] = token
	}
	if f.authQuery != "" {
		query[f.authQuery] = token
	}
	return h.doModelRequest(client, canonical, f.url, f.method, headers, query)
}

// doModelRequest issues the HTTP call and normalises the response.
func (h *Handler) doModelRequest(client *http.Client, provider, url, method string, headers, query map[string]string) (*ModelFetchResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if len(query) > 0 {
		q := req.URL.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &ModelFetchResult{
			Provider: provider,
			Models:   []UpstreamModel{},
			Error:    fmt.Sprintf("failed to fetch models: %d %s", resp.StatusCode, strings.TrimSpace(string(body))),
			Status:   resp.StatusCode,
		}, nil
	}

	// 2 MB cap keeps a misbehaving endpoint from exhausting phone memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}

	var parsed upstreamResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		var bareList []map[string]any
		if err2 := json.Unmarshal(raw, &bareList); err2 == nil {
			parsed.Data = bareList
		} else {
			return nil, fmt.Errorf("decode models response: %w", err)
		}
	}

	list := parsed.Data
	if len(list) == 0 {
		list = parsed.Models
	}
	if len(list) == 0 {
		list = parsed.Results
	}
	out := make([]UpstreamModel, 0, len(list))
	for _, entry := range list {
		id := firstString(entry, "id", "name", "model", "slug")
		if id == "" {
			continue
		}
		label := id
		if v := firstString(entry, "display_name", "displayName"); v != "" {
			label = v
		}
		out = append(out, UpstreamModel{
			ID:   id,
			Name: label,
			Kind: inferModelKind(id),
		})
	}
	return &ModelFetchResult{Provider: provider, Models: out, Supported: true}, nil
}

// firstString returns the first non-empty string value among keys.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// inferModelKind guesses a model's service kind from its identifier, matching
// the upstream heuristics (embedding / image / tts / stt / video …).
func inferModelKind(id string) string {
	s := strings.ToLower(id)
	switch {
	case strings.Contains(s, "embed"):
		return "embedding"
	case strings.Contains(s, "whisper") || strings.Contains(s, "transcribe"):
		return "stt"
	case strings.Contains(s, "tts") || strings.Contains(s, "speech"):
		return "tts"
	case strings.Contains(s, "dall-e") || strings.Contains(s, "image") || strings.Contains(s, "imagen"):
		return "image"
	case strings.Contains(s, "video") || strings.Contains(s, "veo"):
		return "video"
	default:
		return "llm"
	}
}
