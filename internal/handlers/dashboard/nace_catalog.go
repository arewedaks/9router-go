package dashboard

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// naceModelsResponse is the shape of GET /v1/models at console.nace.ai.
//
// Measured 2026-11-06: the listing is {"models":[{"name","description",
// "release_date","alias_for"}]} — the id is `name`, not OpenAI's data[].id, and
// the endpoint requires the nace_sk_ key (without it: 401 authentication_error).
// Parsing OpenAI's shape returned nothing, so the importer silently served the
// static fallback and never reflected the account's real catalogue.
type naceModelsResponse struct {
	Models []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		ReleaseDate string `json:"release_date"`
		AliasFor    string `json:"alias_for"`
	} `json:"models"`
}

// fetchNaceModels queries Nace's live /v1/models endpoint using the connection's API key,
// falling back to the verified Drex catalog if the live endpoint is unavailable or empty.
func (h *Handler) fetchNaceModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	if canonical == "" {
		canonical = "nace"
	}
	token, baseURL := credentialFromData(data)
	if baseURL == "" {
		baseURL = "https://console.nace.ai/v1"
	}
	url := strings.TrimRight(baseURL, "/") + "/models"

	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 20 * time.Second
	}

	if token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Accept", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
					var list naceModelsResponse
					if json.Unmarshal(raw, &list) == nil && len(list.Models) > 0 {
						var models []UpstreamModel
						for _, m := range list.Models {
							id := strings.TrimSpace(m.Name)
							if id == "" {
								continue
							}
							models = append(models, UpstreamModel{
								ID:   id,
								Name: naceModelName(id, m.AliasFor),
								Kind: "llm",
							})
						}
						if len(models) > 0 {
							return &ModelFetchResult{
								Provider:  canonical,
								Models:    models,
								Supported: true,
							}, nil
						}
					}
				}
			}
		}
	}

	// Fallback to verified static Drex catalogue
	return &ModelFetchResult{
		Provider:  canonical,
		Models:    naceStaticModels(),
		Supported: true,
		Warning:   "Using verified Drex catalogue.",
	}, nil
}

func naceStaticModels() []UpstreamModel {
	ids := providers.GetProviderModels("nace")
	out := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, UpstreamModel{
			ID:   id,
			Name: naceModelLabel(id),
			Kind: "llm",
		})
	}
	return out
}

// naceModelName builds the display name for a catalogue entry. The alias case is
// worth calling out ("Drex Latest -> drex-v2.0"): an alias id is served by a
// pinned version, and this is the only place that relationship is visible in the
// model list.
func naceModelName(id, aliasFor string) string {
	label := naceModelLabel(id)
	if a := strings.TrimSpace(aliasFor); a != "" {
		return label + " → " + a
	}
	return label
}

// naceModelLabel is the short display name for a Drex id, used for the static
// fallback and as the prefix of a live entry.
func naceModelLabel(id string) string {
	switch id {
	case "drex-max":
		return "Drex Max"
	case "drex-max-v1.1":
		return "Drex Max v1.1"
	case "drex-max-finance":
		return "Drex Max Finance"
	case "drex-v2.0":
		return "Drex v2.0"
	case "drex-v1.5":
		return "Drex 1.5"
	case "drex-latest":
		return "Drex Latest"
	default:
		return id
	}
}