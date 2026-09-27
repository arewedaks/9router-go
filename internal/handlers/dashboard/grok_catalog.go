package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strconv"
	"time"

	"9router/proxy/internal/providers"
)

// grokModelsURL is Grok CLI's catalogue endpoint. The provider is an OAuth
// CLI session that talks the Responses API at cli-chat-proxy.grok.com, which
// does serve an OpenAI-style /models route — but the generic fetcher had no
// registry entry for it, so the Import action answered "does not support
// models listing" for a route that exists. Same gap Cline and TwinMind had.
// grokModelsURLForTest lets tests repoint the call at an httptest server.
var grokModelsURLForTest = "https://cli-chat-proxy.grok.com/v1/models"

// grokClientHeaders mirrors the identity headers the executor sends
// (internal/proxy/grokcli.go); upstream rejects requests without them.
func grokClientHeaders() map[string]string {
	return map[string]string{
		"User-Agent":               "grok-shell/0.2.99 (linux; x86_64)",
		"x-grok-client-identifier": "grok-shell",
		"x-grok-client-version":    "0.2.99",
	}
}

// grokCatalog is the subset of the /models payload the import needs.
type grokCatalog struct {
	Data []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Context int    `json:"context_window"`
		Efforts []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"reasoning_efforts"`
	} `json:"data"`
}

// fetchGrokCLIModels returns the live catalogue for the connection, falling
// back to the static registry list when the live call fails.
func (h *Handler) fetchGrokCLIModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	token, _ := credentialFromData(data)
	if token == "" {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    grokStaticModels(),
			Supported: true,
			Warning:   "No Grok credential on this connection — using the built-in fallback catalogue.",
		}, nil
	}

	req, err := http.NewRequest(http.MethodGet, grokModelsURLForTest, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range grokClientHeaders() {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    grokStaticModels(),
			Supported: true,
			Warning:   "Grok catalogue unreachable (" + err.Error() + ") — using the built-in fallback catalogue.",
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    grokStaticModels(),
			Supported: true,
			Warning:   "Grok catalogue returned HTTP " + strconv.Itoa(resp.StatusCode) + " — using the built-in fallback catalogue.",
		}, nil
	}

	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if rerr != nil {
		return nil, rerr
	}
	var cat grokCatalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    grokStaticModels(),
			Supported: true,
			Warning:   "Grok catalogue unreadable — using the built-in fallback catalogue.",
		}, nil
	}

	models := parseGrokCatalog(cat)
	if len(models) == 0 {
		models = grokStaticModels()
	}
	return &ModelFetchResult{Provider: canonical, Models: models, Supported: true}, nil
}

// parseGrokCatalog flattens the catalogue into import rows. Grok advertises
// one model with selectable reasoning_effort levels; each level becomes its
// own importable id (grok-4.7-xhigh, grok-4.7-high, …) so the operator can
// pin an effort per route, matching the registry's "-high"/"-low" convention.
// The plain id is kept as a shortcut for the default effort.
func parseGrokCatalog(cat grokCatalog) []UpstreamModel {
	out := make([]UpstreamModel, 0, len(cat.Data)*4)
	for _, m := range cat.Data {
		out = append(out, UpstreamModel{ID: m.ID, Name: m.Name})
		for _, e := range m.Efforts {
			out = append(out, UpstreamModel{
				ID:   m.ID + "-" + e.ID,
				Name: m.Name + " (" + e.ID + ")",
			})
		}
	}
	return out
}

// grokStaticModels mirrors the registry fallback for offline/error cases.
func grokStaticModels() []UpstreamModel {
	out := make([]UpstreamModel, 0, 8)
	for _, id := range providers.GetProviderModels("grok-cli") {
		out = append(out, UpstreamModel{ID: id})
	}
	return out
}
