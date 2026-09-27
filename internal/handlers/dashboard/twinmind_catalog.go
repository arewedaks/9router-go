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

// twinmindModelsURL is TwinMind's own catalogue endpoint. The provider has no
// OpenAI-style /models route, so the generic fetcher branch reported "does not
// support models listing" for a route that exists — the same gap Cline and
// Copilot had. The catalogue carries the tier map per model, which is worth
// keeping: a max-tier model streams an empty completion for a non-max account,
// so the operator should see the tier before importing.
// twinmindModelsURLForTest lets tests repoint the catalogue call at an
// httptest server so no real network call is made.
var twinmindModelsURLForTest = "https://app.twinmind.com/api/v3/chat/models"

// fetchTwinmindModels returns the live catalogue for the connection, falling
// back to the static registry list when the live call fails.
func (h *Handler) fetchTwinmindModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	token, _ := credentialFromData(data)
	if strings.TrimSpace(token) == "" {
		return &ModelFetchResult{
			Provider:  "twinmind",
			Models:    twinmindStaticModels(),
			Supported: true,
			Warning:   "No TwinMind credential on this connection — using the built-in fallback catalogue.",
		}, nil
	}

	req, err := http.NewRequest(http.MethodGet, twinmindModelsURLForTest, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return &ModelFetchResult{
			Provider:  "twinmind",
			Models:    twinmindStaticModels(),
			Supported: true,
			Warning:   "TwinMind catalogue unreachable (" + err.Error() + ") — using the built-in fallback catalogue.",
		}, nil
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return &ModelFetchResult{
			Provider:  "twinmind",
			Models:    twinmindStaticModels(),
			Supported: true,
			Warning:   "TwinMind catalogue returned HTTP " + strconv.Itoa(resp.StatusCode) + " — using the built-in fallback catalogue.",
		}, nil
	}

	models := parseTwinmindCatalog(raw)
	if len(models) == 0 {
		return &ModelFetchResult{
			Provider:  "twinmind",
			Models:    twinmindStaticModels(),
			Supported: true,
			Warning:   "TwinMind catalogue contained no models — using the built-in fallback catalogue.",
		}, nil
	}
	sortUpstreamModels(models)
	return &ModelFetchResult{Provider: "twinmind", Models: models, Supported: true}, nil
}

// parseTwinmindCatalog flattens the {providers:[{id,models:[{name,...}]}]}
// envelope into upstream rows, prefixing each id with the TwinMind sub-vendor
// (e.g. "google/gemini-3.7-flash") so two vendors listing the same name stay
// distinct. The display name carries the tier ("Basic"/"Pro"/"Max"), which is
// the signal the operator needs before importing a max-tier model.
func parseTwinmindCatalog(raw []byte) []UpstreamModel {
	var env struct {
		DefaultModel struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
		} `json:"default_model"`
		Providers []struct {
			ID     string `json:"id"`
			Models []struct {
				Name        string `json:"name"`
				DisplayName string `json:"display_name"`
				Tier        string `json:"minimum_tier"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	models := make([]UpstreamModel, 0, 16)
	add := func(id, name string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		models = append(models, UpstreamModel{ID: id, Name: name, Kind: inferModelKind(id)})
	}

	if env.DefaultModel.Name != "" {
		add(env.DefaultModel.Name, env.DefaultModel.DisplayName)
	}
	for _, p := range env.Providers {
		for _, m := range p.Models {
			id := p.ID + "/" + m.Name
			name := m.DisplayName
			if m.Tier != "" {
				name = strings.TrimSpace(name + " (" + m.Tier + ")")
			}
			add(id, name)
		}
	}
	return models
}

// twinmindStaticModels mirrors ProviderModels["twinmind"] so the Import modal
// still works when the live catalogue is unreachable.
func twinmindStaticModels() []UpstreamModel {
	ids := providers.GetProviderModels("twinmind")
	models := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		models = append(models, UpstreamModel{ID: id, Kind: inferModelKind(id)})
	}
	return models
}
