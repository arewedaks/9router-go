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

// Cline / ClinePass model discovery.
//
// Cline has no OpenAI-style `/models` route, which is why the generic registry
// branch reported "Provider cline does not support models listing". It does,
// however, publish two live catalogues on the same host:
//
//	GET /api/v1/ai/cline/models               -> the full catalogue (~440 rows)
//	GET /api/v1/ai/cline/recommended-models   -> {recommended, free, clinePass, clineCloud}
//
// Both accept the WorkOS-prefixed bearer token and the Cline client-identity
// headers. This mirrors OmniRoute's open-sse/services/clinepassModels.ts, which
// splits the two namespaces deliberately:
//
//   - `cline`     = the full catalogue, MINUS the `cline-pass/*` subscription
//     namespace (those belong to ClinePass) and MINUS non-text-output models
//     (embedding/rerank rows that cannot serve a chat completion).
//   - `clinepass` = ONLY the `cline-pass/*` rows, taken from the recommended
//     endpoint's `clinePass` bucket.
//
// Keeping the namespaces disjoint is what stops Import from advertising a
// subscription-only model on the free Cline connection (which answers
// `403 ENTITLEMENT_ERROR`) or vice versa.

const (
	clineModelsURL            = "https://api.cline.bot/api/v1/ai/cline/models"
	clineRecommendedModelsURL = "https://api.cline.bot/api/v1/ai/cline/recommended-models"

	// clinePassModelPrefix is the ClinePass subscription namespace.
	clinePassModelPrefix = "cline-pass/"
)

// clineStaticFallbackModels is the offline catalogue, used ONLY when live
// discovery is unreachable so Import degrades to a useful list instead of
// failing. Ids are the documented free/recommended entries; ClinePass ids are
// deliberately absent because they are served by the sibling provider.
var clineStaticFallbackModels = []string{
	"cline-free/deepseek-v4.1-flash",
	"cline-free/muse-spark-1.3-contributor",
	"stealth/union-alpha",
	"z-ai/glm-5.3-flash",
	"openai/gpt-6-astra",
	"moonshotai/kimi-k3",
	"anthropic/claude-opus-5",
	"x-ai/grok-4.5",
	"deepseek/deepseek-v4-flash",
	"google/gemini-3.1-pro-preview",
}

// isClineProvider reports whether the provider id is the free Cline connection.
func isClineProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "cline", "cl":
		return true
	default:
		return false
	}
}

// isClinePassProvider reports whether the provider id is the ClinePass
// subscription connection. They share a host and auth contract, so they are
// resolved together and only differ in which model namespace they expose.
func isClinePassProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "clinepass", "cline-pass", "cp":
		return true
	default:
		return false
	}
}

// isClineFamily reports whether the provider belongs to either Cline namespace.
func isClineFamily(providerID string) bool {
	return isClineProvider(providerID) || isClinePassProvider(providerID)
}

// fetchClineModels resolves the catalogue for a Cline / ClinePass connection.
func (h *Handler) fetchClineModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	if canonical == "" || isClineFamily(providerID) {
		if isClinePassProvider(providerID) {
			canonical = "clinepass"
		} else {
			canonical = "cline"
		}
	}
	isPass := canonical == "clinepass"

	fallback := func(warning string) *ModelFetchResult {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    clineFallbackModels(isPass),
			Supported: true,
			Warning:   warning,
		}
	}

	token, _ := credentialFromData(data)
	token = strings.TrimSpace(token)
	if token == "" {
		return fallback("No Cline credential on this connection — using the built-in fallback catalogue."), nil
	}

	raw, status, err := h.getClineCatalog(endpointForCline(isPass), token, timeout)
	if err != nil {
		return fallback("Live Cline catalogue unreachable (" + err.Error() + ") — using the built-in fallback catalogue."), nil
	}
	if status != http.StatusOK {
		return fallback("Cline catalogue returned HTTP " + strconv.Itoa(status) + " — using the built-in fallback catalogue."), nil
	}

	models := parseClineCatalog(raw, isPass)
	if len(models) == 0 {
		return fallback("Cline catalogue contained no usable chat models — using the built-in fallback catalogue."), nil
	}

	sortUpstreamModels(models)
	return &ModelFetchResult{
		Provider:  canonical,
		Models:    models,
		Supported: true,
	}, nil
}

// endpointForCline picks the catalogue endpoint for the namespace. The full
// catalogue backs `cline`; ClinePass uses the recommended endpoint because its
// subscription bucket is only published there.
func endpointForCline(isPass bool) string {
	if isPass {
		return clineRecommendedModelsURL
	}
	return clineModelsURL
}

// getClineCatalog performs the authenticated catalogue GET with Cline's client
// identity headers. The token is normalized to the `workos:` form here so a
// connection saved before that normalization still imports correctly.
func (h *Handler) getClineCatalog(url, token string, timeout time.Duration) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", providers.ClineAuthHeader(token))
	req.Header.Set("Accept", "application/json")
	for k, v := range providers.ClineClientIdentityHeaders() {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// clineCatalogRow covers both endpoint shapes; the full catalogue and the
// recommended buckets share the same id/name/description fields.
type clineCatalogRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Architecture *struct {
		Modality         string   `json:"modality"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
}

type clineRecommendedPayload struct {
	Recommended []clineCatalogRow `json:"recommended"`
	Free        []clineCatalogRow `json:"free"`
	ClinePass   []clineCatalogRow `json:"clinePass"`
	ClineCloud  []clineCatalogRow `json:"clineCloud"`
}

// parseClineCatalog turns a catalogue body into upstream model rows.
//
//   - ClinePass keeps ONLY the `cline-pass/*` namespace.
//   - Cline keeps everything EXCEPT `cline-pass/*` and non-text-output models.
//
// A model is admitted on the full catalogue when it does not explicitly declare
// a non-text output modality. Rows that omit architecture entirely are kept, so
// an upstream schema change cannot silently empty the catalogue.
func parseClineCatalog(raw []byte, isPass bool) []UpstreamModel {
	var rows []clineCatalogRow

	if isPass {
		var payload clineRecommendedPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil
		}
		rows = payload.ClinePass
	} else {
		// The full catalogue may be a bare array or wrapped in {"data":[...]}.
		var wrapped struct {
			Data []clineCatalogRow `json:"data"`
		}
		if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Data) > 0 {
			rows = wrapped.Data
		} else if err := json.Unmarshal(raw, &rows); err != nil {
			return nil
		}
	}

	seen := make(map[string]bool, len(rows))
	out := make([]UpstreamModel, 0, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" || seen[id] {
			continue
		}
		if isPass {
			if !strings.HasPrefix(id, clinePassModelPrefix) {
				continue
			}
		} else {
			// ClinePass rows belong to the sibling provider.
			if strings.HasPrefix(id, clinePassModelPrefix) {
				continue
			}
			if !clineHasTextOutput(row) {
				continue
			}
		}
		seen[id] = true
		out = append(out, UpstreamModel{
			ID:     id,
			Name:   clineDisplayName(row),
			IsFree: providers.IsModelFreeBadge(clineProviderFor(isPass), freeCandidateFor(row)),
		})
	}
	return out
}

// clineProviderFor names the provider used for Free-badge lookup.
func clineProviderFor(isPass bool) string {
	if isPass {
		return "clinepass"
	}
	return "cline"
}

// clineDisplayName prefers the human-readable name, falling back to the id.
func clineDisplayName(row clineCatalogRow) string {
	if n := strings.TrimSpace(row.Name); n != "" {
		return n
	}
	return strings.TrimSpace(row.ID)
}

// clineHasTextOutput reports whether the row can serve a chat completion. Rows
// without architecture metadata are admitted (fail-open) so a schema change
// degrades to a longer list rather than an empty one.
func clineHasTextOutput(row clineCatalogRow) bool {
	if row.Architecture == nil {
		return true
	}
	if outs := row.Architecture.OutputModalities; len(outs) > 0 {
		for _, m := range outs {
			if strings.EqualFold(strings.TrimSpace(m), "text") {
				return true
			}
		}
		return false
	}
	// Legacy shape: "text+image->text". Only the output side matters.
	if modality := row.Architecture.Modality; modality != "" {
		if _, out, ok := strings.Cut(modality, "->"); ok {
			return strings.EqualFold(strings.TrimSpace(out), "text")
		}
	}
	return true
}

// freeCandidateFor builds the Free-badge candidate for a catalogue row. The id
// is passed as both ID and DisplayName because the badge rule inspects the
// display name for a "free" word too, and Cline namespaces its free models as
// `cline-free/...` — a signal that lives in the id, not the human name.
func freeCandidateFor(row clineCatalogRow) providers.FreeModelCandidate {
	id := strings.TrimSpace(row.ID)
	return providers.FreeModelCandidate{
		ID:          id,
		DisplayName: id,
	}
}

// clineFallbackModels returns the offline catalogue for the namespace as
// UpstreamModel rows.
func clineFallbackModels(isPass bool) []UpstreamModel {
	if isPass {
		// The fallback list is the free/recommended namespace; ClinePass has no
		// meaningful offline subset, so it resolves to an empty list and the
		// Warning explains why.
		return []UpstreamModel{}
	}
	out := make([]UpstreamModel, 0, len(clineStaticFallbackModels))
	for _, id := range clineStaticFallbackModels {
		out = append(out, UpstreamModel{
			ID:     id,
			Name:   id,
			IsFree: providers.IsModelFreeBadge("cline", providers.FreeModelCandidate{ID: id, DisplayName: id}),
		})
	}
	return out
}
