package dashboard

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// Cloudflare Workers AI keys its catalogue under an account id and has no
// OpenAI-style /models route. Probed live with a valid token:
//
//	GET /client/v4/accounts/{id}/ai/v1/models      -> 405
//	    {"success":false,"errors":[{"code":7001,
//	     "message":"GET not supported for requested URI."}]}
//	GET /client/v4/accounts/{id}/ai/models/search  -> 200
//
// VansRouter never reaches the working route: it derives the listing URL from
// the registry baseUrl by stripping /chat/completions and appending /models
// (open-sse/providers/schema.js, deriveValidateUrl), which yields the 405 URL
// above. So Import fails there too, and the fork used to answer "Provider
// cloudflare-ai does not support models listing".
//
// The working route paginates (per_page max 100; 308 models on the account
// probed here), so this walks the pages instead of losing everything past the
// first 100.

const (
	// Cloudflare clamps per_page and stops paginating once the page size is too
	// large: per_page=100 returns only 65 rows and page=2 then comes back empty,
	// so a walk asking for 100 silently loses models past the first page.
	// per_page=50 paginates correctly (verified live).
	cloudflarePageSize = 50
	// Bound the walk so a pagination bug cannot spin forever. The largest real
	// account observed exposes 65 models, which is two pages at this size.
	cloudflareMaxPages = 50
)

// cloudflareAPIBase is a var so tests can point it at an httptest server.
var cloudflareAPIBase = "https://api.cloudflare.com/client/v4/accounts"

// isCloudflareProvider reports whether the id resolves to Workers AI. Both the
// canonical id and its "cf" alias are accepted, matching the other isX helpers.
func isCloudflareProvider(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "cloudflare-ai", "cf", "cloudflare":
		return true
	}
	return false
}

// cloudflareAccountID pulls the account id out of a connection's JSON blob.
// It lives under providerSpecificData.accountId; the legacy flat spelling is
// accepted too because older connection rows stored it at the top level.
func cloudflareAccountID(data string) string {
	if strings.TrimSpace(data) == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return ""
	}
	if psd, ok := m["providerSpecificData"].(map[string]any); ok {
		for _, k := range []string{"accountId", "account_id", "accountID"} {
			if v, ok := psd[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	for _, k := range []string{"accountId", "account_id", "accountID"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// cloudflareSearchResponse is the /ai/models/search envelope.
type cloudflareSearchResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Task        struct {
			Name string `json:"name"`
		} `json:"task"`
	} `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		PerPage    int `json:"per_page"`
		Count      int `json:"count"`
		TotalCount int `json:"total_count"`
	} `json:"result_info"`
}

// cloudflareKindFromTask maps Cloudflare's task label onto the dashboard's
// model kind. Anything unrecognised stays "llm": Import is primarily about chat
// models, and a wrong non-llm kind would hide a usable model from the chat path.
func cloudflareKindFromTask(task string) string {
	switch task {
	case "Text Generation", "Text-to-Text", "Text Generation (Instruct)":
		return "llm"
	case "Text Embeddings":
		return "embedding"
	case "Text-to-Image", "Image-to-Image":
		return "image"
	case "Automatic Speech Recognition", "Text-to-Speech":
		return "audio"
	case "Text Classification", "Summarization", "Translation":
		return "llm"
	}
	return "llm"
}

// fetchCloudflareModels lists Workers AI models for a connection, walking the
// paginated /ai/models/search route.
func (h *Handler) fetchCloudflareModels(providerID, data string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	if canonical == "" || isCloudflareProvider(providerID) {
		canonical = "cloudflare-ai"
	}

	token, _ := credentialFromData(data)
	accountID := cloudflareAccountID(data)

	// Without an account id the route cannot be built at all: it is part of the
	// path, unlike every other provider where only the host matters.
	if accountID == "" {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    cloudflareStaticFallbackModels(),
			Supported: true,
			Warning:   "No Account ID on this connection — using the built-in catalogue. Add the Account ID to import the live list.",
		}, nil
	}
	if strings.TrimSpace(token) == "" {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    cloudflareStaticFallbackModels(),
			Supported: true,
			Warning:   "No API token on this connection — using the built-in catalogue.",
		}, nil
	}

	client := &http.Client{Timeout: timeout}
	seen := make(map[string]bool)
	var models []UpstreamModel

	for page := 1; page <= cloudflareMaxPages; page++ {
		q := url.Values{}
		q.Set("per_page", fmt.Sprintf("%d", cloudflarePageSize))
		q.Set("page", fmt.Sprintf("%d", page))
		endpoint := fmt.Sprintf("%s/%s/ai/models/search?%s", cloudflareAPIBase, url.PathEscape(accountID), q.Encode())

		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return &ModelFetchResult{
				Provider:  canonical,
				Models:    cloudflareStaticFallbackModels(),
				Supported: true,
				Warning:   "Live Workers AI catalogue unreachable (" + err.Error() + ") — using the built-in catalogue.",
			}, nil
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			// A bad token or wrong account id is the common case; fall back
			// rather than making Import fail with an opaque status.
			return &ModelFetchResult{
				Provider:  canonical,
				Models:    cloudflareStaticFallbackModels(),
				Supported: true,
				Warning: fmt.Sprintf("Workers AI returned HTTP %d — using the built-in catalogue. Check the API token and Account ID.",
					resp.StatusCode),
			}, nil
		}
		if readErr != nil {
			return nil, readErr
		}

		var parsed cloudflareSearchResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("parse cloudflare models: %w", err)
		}
		if !parsed.Success {
			msg := "unknown error"
			if len(parsed.Errors) > 0 {
				msg = parsed.Errors[0].Message
			}
			return &ModelFetchResult{
				Provider:  canonical,
				Models:    cloudflareStaticFallbackModels(),
				Supported: true,
				Warning:   "Workers AI rejected the request (" + msg + ") — using the built-in catalogue.",
			}, nil
		}

		for _, m := range parsed.Result {
			name := strings.TrimSpace(m.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			models = append(models, UpstreamModel{
				ID:   name,
				Name: name,
				Kind: cloudflareKindFromTask(m.Task.Name),
			})
		}

		// Stop on the first empty page. Testing len(Result)==0 rather than
		// comparing against per_page keeps this correct if Cloudflare clamps the
		// page size again.
		if len(parsed.Result) == 0 {
			break
		}
	}

	if len(models) == 0 {
		return &ModelFetchResult{
			Provider:  canonical,
			Models:    cloudflareStaticFallbackModels(),
			Supported: true,
			Warning:   "Workers AI returned no models — using the built-in catalogue.",
		}, nil
	}

	result := &ModelFetchResult{
		Provider:  canonical,
		Models:    models,
		Supported: true,
	}
	// Note: result_info.total_count is Cloudflare's whole public catalogue (308
	// on the account probed here), not what this account may call — paging to the
	// end yields 65 models while total_count still reports 308. It is therefore
	// deliberately ignored instead of driving a misleading shortfall warning.
	return result, nil
}

// cloudflareStaticFallbackModels mirrors the registry list so Import still
// returns something usable when the live call cannot be made.
func cloudflareStaticFallbackModels() []UpstreamModel {
	ids := providers.GetProviderModels("cloudflare-ai")
	out := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, UpstreamModel{ID: id, Name: id, Kind: inferModelKind(id)})
	}
	return out
}
