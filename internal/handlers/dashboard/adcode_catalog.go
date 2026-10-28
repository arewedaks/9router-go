package dashboard

import (
	"io"
	"net/http"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// adcodeConfig is the slice of GET /v1/ai/tagflow/config the importer needs.
// The endpoint is public (no credential), which is why import works before any
// account is connected.
type adcodeConfig struct {
	DefaultModel string `json:"defaultModel"`
	Models       []struct {
		ID            string  `json:"id"`
		Name          string  `json:"name"`
		ProviderName  string  `json:"providerName"`
		ContextWindow int     `json:"contextWindow"`
		MaxOutput     int     `json:"maxOutput"`
		CreditRate    float64 `json:"creditRate"`
		Paid          bool    `json:"paid"`
	} `json:"models"`
}

// adcodeConfigURL is a seam for tests: the real value is the vendor endpoint.
var adcodeConfigURL = providers.AdCodeModelConfigURL

// fetchAdCodeModels imports ADCode's catalogue. The gateway has no /models
// route (404), and the vendor's own client reads this config endpoint instead,
// so the router must read the same source or import caches nothing.
func (h *Handler) fetchAdCodeModels(providerID string, timeout time.Duration) *ModelFetchResult {
	canonical := providers.ResolveAlias(providerID)
	out := &ModelFetchResult{Provider: canonical, Supported: true}

	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, adcodeConfigURL, nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	// Cloudflare answers Go's default agent with 403 "error code: 1010" here.
	req.Header.Set("User-Agent", providers.AdCodeUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	out.Status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		out.Error = "catalogue listing answered HTTP " + http.StatusText(resp.StatusCode)
		return out
	}

	var cfg adcodeConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		out.Error = "could not parse the catalogue: " + err.Error()
		return out
	}
	for _, m := range cfg.Models {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if m.Paid {
			// The plan matters to the operator: a free-plan account cannot use the
			// paid ids, and the dashboard lists one flat model table.
			name = name + " (paid)"
		}
		out.Models = append(out.Models, UpstreamModel{
			ID:            m.ID,
			Name:          name,
			Kind:          "llm",
			ContextLength: m.ContextWindow,
			MaxOutput:     m.MaxOutput,
		})
	}
	if len(out.Models) == 0 {
		out.Warning = "The catalogue answered 200 with no models; the gateway may be between configurations."
	}
	return out
}
