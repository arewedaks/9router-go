package dashboard

import (
	"io"
	"net/http"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// omniRushModelsURL is a seam for tests.
var omniRushModelsURL = providers.OmniRushOrigin + "/v1/models"

// fetchOmniRushModels imports the gateway's catalogue. The listing needs both
// identity headers — a bare Authorization is refused with 403 client_not_allowed
// — and it is the only place the real context/output numbers are published.
func (h *Handler) fetchOmniRushModels(providerID, data string, timeout time.Duration) *ModelFetchResult {
	canonical := providers.ResolveAlias(providerID)
	out := &ModelFetchResult{Provider: canonical, Supported: true}

	req, err := http.NewRequest(http.MethodGet, omniRushModelsURL, nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	// The credential is the device access token stored by the sign-in.
	token, _ := credentialFromData(data)
	for k, v := range providers.OmniRushHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out.Status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		out.Error = "OmniRush catalogue answered HTTP " + http.StatusText(resp.StatusCode)
		return out
	}

	var parsed struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Limits      struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		out.Error = "could not parse the catalogue: " + err.Error()
		return out
	}
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		out.Models = append(out.Models, UpstreamModel{
			ID:            m.ID,
			Name:          m.DisplayName,
			Kind:          "llm",
			ContextLength: m.Limits.Context,
			MaxOutput:     m.Limits.Output,
		})
	}
	if len(out.Models) == 0 {
		out.Warning = "The catalogue answered 200 with no models."
	}
	return out
}
