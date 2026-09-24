package dashboard

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// Antigravity (Google Cloud Code "cloudcode-pa") does not expose a plain
// OpenAI-style /models endpoint. Its catalogue is served by a POST to
// /v1internal:fetchAvailableModels (a newer alias) or /v1internal:models, on
// one of two hosts. This mirrors the upstream OmniRoute discovery chain
// (open-sse/config/antigravityUpstream.ts + providerModels/normalizers.ts):
//
//	URLs are tried in order until one answers 2xx with a non-empty catalogue.
//	The response is {"models": {<id>: {displayName…}}} but an array under
//	"models" is also tolerated.
//	Internal/tab/chat-slot entries and retired ids are dropped so the imported
//	list matches what the native client actually offers.
//
// The daily-* host is frequently blocked or slow from consumer networks (it
// returned an empty reply in our live probe), so the stable cloudcode-pa host
// is tried too — order-independent because we fall through on failure.

var antigravityDiscoveryPaths = []string{
	"/v1internal:fetchAvailableModels",
	"/v1internal:models",
}

// antigravityDiscoveryHosts lists the candidate bases, mirroring
// ANTIGRAVITY_DISCOVERY_BASE_URLS. daily-* first (freshest catalogue) then the
// stable host.
var antigravityDiscoveryHosts = []string{
	"https://daily-cloudcode-pa.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

// antigravityUserAgent is the default IDE User-Agent used only as a last-resort
// fallback. The canonical builder lives in the providers package
// (AntigravityUserAgent), which knows about the ide|cli client profiles.
const antigravityUserAgent = "antigravity/ide/2.11.0 darwin/arm64"

// The catalogue filter (non-chat blocklists + IsDiscoverableAntigravityModel)
// lives in internal/providers so the quota tracker and the model picker share
// one definition. A second copy is how the quota panel came to report seven
// fewer models than the picker offered.
// antigravityDiscoveryPayload is the normalised shape we extract from either
// envelope form.
type antigravityDiscoveryPayload struct {
	Models map[string]antigravityModelEntry `json:"models"`
	// Some proxies wrap the map under "data".
	Data struct {
		Models map[string]antigravityModelEntry `json:"models"`
	} `json:"data"`
}

type antigravityModelEntry struct {
	DisplayName string `json:"displayName"`
	Name        string `json:"name"`
	Model       string `json:"model"`
	IsInternal  bool   `json:"isInternal"`
}

// normalizeAntigravityModels converts either envelope (map keyed by id, or an
// array of objects) into a flat, de-duplicated list of user-callable models.
func normalizeAntigravityModels(raw []byte) []UpstreamModel {
	// Try the primary shape: {"models": {id: {...}}}.
	var payload antigravityDiscoveryPayload
	_ = json.Unmarshal(raw, &payload)

	entries := payload.Models
	if len(entries) == 0 {
		entries = payload.Data.Models
	}
	if len(entries) > 0 {
		out := make([]UpstreamModel, 0, len(entries))
		for id, entry := range entries {
			if entry.IsInternal || !providers.IsDiscoverableAntigravityModel(id) {
				continue
			}
			label := antigravityFriendlyName(id, firstNonEmpty(entry.DisplayName, entry.Name))
			out = append(out, UpstreamModel{
				ID:   id,
				Name: label,
				Kind: inferModelKind(id),
			})
		}
		return sortUpstreamModels(out)
	}

	// Tolerate the array shape: {"models": [{"id"/"name"/"model": …}]}.
	var arr struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]UpstreamModel, 0, len(arr.Models))
	for _, item := range arr.Models {
		id := firstString(item, "id", "model", "name")
		if id == "" || !providers.IsDiscoverableAntigravityModel(id) {
			continue
		}
		if v, ok := item["isInternal"].(bool); ok && v {
			continue
		}
		label := antigravityFriendlyName(id, firstString(item, "displayName", "display_name"))
		out = append(out, UpstreamModel{ID: id, Name: label, Kind: inferModelKind(id)})
	}
	return sortUpstreamModels(out)
}

// sortUpstreamModels orders models by id for a stable UI, de-duplicating.
func sortUpstreamModels(in []UpstreamModel) []UpstreamModel {
	seen := make(map[string]bool, len(in))
	out := make([]UpstreamModel, 0, len(in))
	for _, m := range in {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	// Simple insertion sort keeps the code dependency-free and the list small.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// antigravityFriendlyName returns a human label for a discovered id. The
// upstream payload frequently omits displayName for tiered variants (e.g.
// "gemini-3.8-flash-tiered" arrives with no label), so fall back to the
// curated catalogue and finally to a title-cased form of the id so the UI
// never shows a raw slug.
func antigravityFriendlyName(id, upstreamName string) string {
	if s := strings.TrimSpace(upstreamName); s != "" && s != id {
		return s
	}
	for _, m := range antigravityStaticCatalog {
		if m.ID == id {
			return m.Name
		}
	}
	return humanizeModelID(id)
}

// humanizeModelID turns "gemini-3.8-flash-tiered" into
// "Gemini 3.8 Flash (Tiered)" when no curated label exists.
func humanizeModelID(id string) string {
	parts := strings.Split(id, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		switch strings.ToLower(p) {
		case "gemini", "gpt", "oss", "claude", "tts":
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		default:
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	joined := strings.Join(parts, " ")
	// Parenthesise a trailing effort tier for readability.
	for _, tier := range []string{"High", "Medium", "Low", "Tiered", "Thinking"} {
		suffix := " " + tier
		if strings.HasSuffix(joined, suffix) {
			joined = strings.TrimSuffix(joined, suffix) + " (" + tier + ")"
			break
		}
	}
	return joined
}

// fetchAntigravityModels performs the Antigravity-specific discovery. It tries
// each host/path combination until one returns a non-empty catalogue, and
// falls back to the static public catalogue when the network path is
// unavailable so "Import" still succeeds on a phone with a flaky connection.
func (h *Handler) fetchAntigravityModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providersResolveAlias(providerID)
	token, _ := credentialFromData(data)
	if strings.TrimSpace(token) == "" {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    antigravityStaticModels(),
			Supported: true,
			Warning:   "OAuth token unavailable — using the local Antigravity catalogue.",
		}, nil
	}

	client := &http.Client{Timeout: timeout}
	var lastErr string

	// Discovery must present the same client identity chat will later use, so the
	// catalogue matches what the router can actually call. The profile is the
	// provider-wide setting; reading it from the connection would let a stale
	// per-account value disagree with routing.
	userAgent := antigravityUserAgent
	if settings, err := h.repo.GetSettings(); err == nil && settings != nil {
		userAgent = providers.AntigravityUserAgent(
			providers.NormalizeAntigravityClientProfile(settings.AntigravityClientProfile))
	}

	for _, host := range antigravityDiscoveryHosts {
		for _, path := range antigravityDiscoveryPaths {
			url := host + path
			models, status, err := h.doAntigravityDiscovery(client, url, token, userAgent)
			if err != nil {
				lastErr = err.Error()
				continue
			}
			if len(models) > 0 {
				return &ModelFetchResult{
					Provider:  canonical,
					Models:    models,
					Supported: true,
				}, nil
			}
			// 2xx but empty catalogue — remember and keep trying.
			lastErr = fmt.Sprintf("%s returned an empty catalogue (HTTP %d)", url, status)
		}
	}

	// Every live host failed: degrade to the local catalogue rather than
	// blocking the user. The warning surfaces in the UI.
	return &ModelFetchResult{
		Provider:  canonical,
		Models:    antigravityStaticModels(),
		Supported: true,
		Warning:   fmt.Sprintf("Live discovery unavailable — using the local Antigravity catalogue. (%s)", truncate(lastErr, 120)),
	}, nil
}

// doAntigravityDiscovery issues the POST and normalises the response. It
// returns the model list, the HTTP status (for diagnostics) and any transport
// error.
func (h *Handler) doAntigravityDiscovery(client *http.Client, url, token, userAgent string) ([]UpstreamModel, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if userAgent == "" {
		userAgent = antigravityUserAgent
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, resp.StatusCode, fmt.Errorf("%s: HTTP %d %s", url, resp.StatusCode, truncate(strings.TrimSpace(string(body)), 120))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return normalizeAntigravityModels(raw), resp.StatusCode, nil
}

// providersResolveAlias is a tiny indirection so tests can stub resolution if
// ever needed; currently it just forwards to providers.ResolveAlias.
func providersResolveAlias(id string) string {
	return resolveAliasFn(id)
}
