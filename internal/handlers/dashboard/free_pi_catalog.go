package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// free-pi model import.
//
// free-pi has no OpenAI-style /models route. Its catalogue lives at
// GET /client-version, unauthenticated and account-independent, shaped
//
//	{"min":"0.6.18","latest":"0.6.18","model":"DeepSeek V4 Flash",
//	 "models":[{"id":"deepseek-v4-flash","name":"DeepSeek V4 Flash · 🔥 …"}]}
//
// which the generic registry fetcher cannot read (it expects a top-level
// {"data":[…]} list and the registry has no URL entry for this provider). Before
// this the Import button answered "Provider free-pi does not support models
// listing" and cached nothing.
//
// The `name` field carries marketing suffix noise ("· 🔥 Free Unlimited: limited
// time", "· ~20x usage"), so only the id is kept for the catalogue; the display
// name is relearned from the upstream list on each import and must not be baked
// into a stored model id.

// freePiCatalogURL is a var, not a const, so tests can point it at a local
// server (the convention every other catalogue fetcher in this package uses).
var freePiCatalogURL = providers.FreePiOrigin + "/client-version"

// freePiClientVersionResponse is the subset of GET /client-version this importer
// needs. min/latest are ignored here — the executor reads them at sign-in time.
type freePiClientVersionResponse struct {
	Models []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"models"`
}

// fetchFreePiModels reads the live catalogue and returns the model ids.
//
// The credential is not required by the endpoint, but a connection is: importing
// models for a provider the operator has not signed in to would offer lanes that
// cannot be called. The handler already enforces that before calling here.
func (h *Handler) fetchFreePiModels(data string, timeout time.Duration) (*ModelFetchResult, error) {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, freePiCatalogURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return &ModelFetchResult{
			Provider:  providers.FreePiProviderID,
			Models:    freePiSeedModels(),
			Supported: true,
			Error:     "free-pi catalogue returned HTTP " + resp.Status + "; using the seeded list",
		}, nil
	}

	var parsed freePiClientVersionResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return &ModelFetchResult{
			Provider:  providers.FreePiProviderID,
			Models:    freePiSeedModels(),
			Supported: true,
			Error:     "free-pi catalogue was not readable; using the seeded list",
		}, nil
	}

	seen := map[string]bool{}
	var ids []UpstreamModel
	for _, m := range parsed.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		// Only the id is kept: `name` carries marketing noise ("· 🔥 Free
		// Unlimited: limited time", "· ~20x usage") that must not become part of
		// the stored model identity.
		ids = append(ids, UpstreamModel{ID: id})
	}
	if len(ids) == 0 {
		// An empty catalogue is upstream saying "nothing here": serving the seed
		// list hides a real outage, so report what came back.
		return &ModelFetchResult{
			Provider:  providers.FreePiProviderID,
			Models:    freePiSeedModels(),
			Supported: true,
			Error:     "free-pi returned an empty catalogue; using the seeded list",
		}, nil
	}

	return &ModelFetchResult{
		Provider:  providers.FreePiProviderID,
		Models:    ids,
		Supported: true,
	}, nil
}

// freePiSeedModels is the compiled-in catalogue used when the live fetch cannot
// run, so Import never reports nothing for a provider that is otherwise usable.
func freePiSeedModels() []UpstreamModel {
	ids := providers.ProviderModels[providers.FreePiProviderID]
	out := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, UpstreamModel{ID: id})
	}
	return out
}