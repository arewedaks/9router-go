package dashboard

// Kiro model discovery.
//
// Kiro publishes the models an account can actually call through the CodeWhisperer
// control-plane call `AmazonCodeWhispererService.ListAvailableModels`. That is not
// an OpenAI-style GET /models route, so the generic fetcher cannot reach it — and
// the static registry list cannot either: it drifts from what the account is
// entitled to.
//
// Measured against a live free-tier account: the endpoint returned 9 models, four
// of which (`auto`, `claude-sonnet-4`, `minimax-m2.1`, `minimax-m2.5`) are absent
// from the static list, while nine static entries (Opus 5, Sonnet 5, the GPT-5.6
// family) were not offered on that tier. Importing the live list is therefore the
// difference between seeing the models you can use and seeing a list that is
// partly wrong in both directions.

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

const (
	kiroControlPlaneHost = "https://codewhisperer.us-east-1.amazonaws.com"
	kiroListModelsTarget = "AmazonCodeWhispererService.ListAvailableModels"
	// Kiro's control plane speaks the AWS JSON 1.0 protocol, which is why the
	// target rides in a header and the body is a bare JSON document.
	kiroContentType = "application/x-amz-json-1.0"
)

// isKiroProvider reports whether a provider id (canonical or alias) addresses Kiro.
func isKiroProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "kiro", "kr":
		return true
	default:
		return false
	}
}

// kiroModel is one entry from the control-plane response.
type kiroModel struct {
	ModelID   string `json:"modelId"`
	ModelName string `json:"modelName"`
	// RateMultiplier is the credit cost per request; surfaced in the description
	// because it is the number that decides which model is worth using.
	RateMultiplier float64 `json:"rateMultiplier"`
	Description    string  `json:"description"`
}

// fetchKiroModels lists the account's models, falling back to the static
// registry catalogue when the control plane is unreachable or the token is not
// usable for it.
func (h *Handler) fetchKiroModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	token, _ := credentialFromData(data)
	if strings.TrimSpace(token) == "" {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    kiroStaticModels(canonical),
			Supported: true,
			Warning:   "OAuth token unavailable — using the local Kiro catalogue.",
		}, nil
	}

	models, err := h.requestKiroModels(token, timeout)
	if err != nil || len(models) == 0 {
		reason := "the control plane returned no models"
		if err != nil {
			reason = err.Error()
		}
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    kiroStaticModels(canonical),
			Supported: true,
			Warning:   fmt.Sprintf("Live Kiro discovery unavailable — using the local catalogue. (%s)", truncate(reason, 120)),
		}, nil
	}

	return &ModelFetchResult{
		Provider:  canonical,
		Models:    models,
		Supported: true,
	}, nil
}

// requestKiroModels performs the control-plane call and normalises the response.
func (h *Handler) requestKiroModels(token string, timeout time.Duration) ([]UpstreamModel, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// The origin field is what the Kiro IDE sends; the endpoint rejects the
	// request as improperly formed without a recognisable one.
	body := []byte(`{"origin":"AI_EDITOR"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kiroControlPlaneHost+"/", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", kiroContentType)
	req.Header.Set("X-Amz-Target", kiroListModelsTarget)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 120))
	}

	return normalizeKiroModels(raw), nil
}

// normalizeKiroModels converts the control-plane payload into the shared shape.
// A response that does not parse yields no models, which sends the caller to the
// static fallback rather than surfacing an empty list as success.
func normalizeKiroModels(raw []byte) []UpstreamModel {
	var payload struct {
		Models       []kiroModel `json:"models"`
		DefaultModel kiroModel   `json:"defaultModel"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	out := make([]UpstreamModel, 0, len(payload.Models))

	add := func(m kiroModel) {
		id := strings.TrimSpace(m.ModelID)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		name := strings.TrimSpace(m.ModelName)
		if name == "" {
			name = humanizeModelID(id)
		}
		out = append(out, UpstreamModel{
			ID:   id,
			Name: name,
			Kind: inferModelKind(id),
		})
	}

	// The default model is part of the account's catalogue even when it is also
	// listed under `models`, so it is added first and de-duplicated.
	add(payload.DefaultModel)
	for _, m := range payload.Models {
		add(m)
	}
	return out
}

// kiroStaticModels returns the registry catalogue as a fallback, so Import still
// produces a usable list when the control plane cannot be reached.
func kiroStaticModels(canonical string) []UpstreamModel {
	ids := providers.ProviderModels[canonical]
	if len(ids) == 0 {
		ids = providers.ProviderModels["kiro"]
	}
	out := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, UpstreamModel{
			ID:   id,
			Name: humanizeModelID(id),
			Kind: inferModelKind(id),
		})
	}
	return out
}
