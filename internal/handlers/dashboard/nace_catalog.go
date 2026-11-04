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
					var list struct {
						Data []struct {
							ID string `json:"id"`
						} `json:"data"`
					}
					if json.Unmarshal(raw, &list) == nil && len(list.Data) > 0 {
						var models []UpstreamModel
						for _, m := range list.Data {
							id := strings.TrimSpace(m.ID)
							if id != "" {
								models = append(models, UpstreamModel{
									ID:   id,
									Name: naceModelLabel(id),
									Kind: "llm",
								})
							}
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

func naceModelLabel(id string) string {
	switch id {
	case "drex-max":
		return "Drex Max (Multimodal)"
	case "drex-max-v1.1":
		return "Drex Max v1.1 (Multimodal)"
	case "drex-max-finance":
		return "Drex Max Finance (Multimodal)"
	case "drex-v2.0":
		return "Drex v2.0"
	case "drex-latest":
		return "Drex Latest"
	default:
		return id
	}
}
