package dashboard

import (
	"io"
	"net/http"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// zedModelsURL is a seam for tests: the LLM API's model list.
var zedModelsURL = providers.ZedCloudURL + "/models"

// fetchZedModels imports the models a Zed account can use.
//
// The sign-in credential alone is not enough: cloud.zed.dev/models wants a bearer
// LLM token, minted from the account's organization (providers.ZedLLMToken).
// Sending the sign-in token there answers 401 — which a naive implementation
// reports as "the account may not have hosted models included".
func (h *Handler) fetchZedModels(providerID, data string, timeout time.Duration) *ModelFetchResult {
	out := &ModelFetchResult{Provider: providerID, Supported: true}

	var conn struct {
		AccessToken string `json:"accessToken"`
		UserID      string `json:"zedUserId"`
	}
	if err := json.Unmarshal([]byte(data), &conn); err != nil {
		out.Error = "could not read the connection: " + err.Error()
		return out
	}
	conn.AccessToken = strings.TrimSpace(conn.AccessToken)
	conn.UserID = strings.TrimSpace(conn.UserID)
	if conn.AccessToken == "" || conn.UserID == "" {
		out.Error = "Zed needs a signed-in account: use Sign in with Zed before importing models"
		return out
	}

	client := &http.Client{Timeout: timeout}
	list := func() ([]byte, int, error) {
		token, err := providers.ZedLLMToken(nil, client, conn.UserID, conn.AccessToken)
		if err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequest(http.MethodGet, zedModelsURL, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return body, resp.StatusCode, nil
	}

	body, status, err := list()
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if status == http.StatusUnauthorized {
		// The minted token aged out after the cache write: mint a fresh one once.
		providers.ZedInvalidateLLMToken(conn.UserID, conn.AccessToken)
		body, status, err = list()
		if err != nil {
			out.Error = err.Error()
			return out
		}
	}
	out.Status = status
	if status != http.StatusOK {
		out.Error = "Zed's model list answered HTTP " + http.StatusText(status)
		return out
	}

	var parsed struct {
		Models []struct {
			ID              string `json:"id"`
			DisplayName     string `json:"display_name"`
			MaxTokenCount   int    `json:"max_token_count"`
			MaxOutputTokens int    `json:"max_output_tokens"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		out.Error = "could not parse Zed's model list: " + err.Error()
		return out
	}
	for _, m := range parsed.Models {
		if m.ID == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out.Models = append(out.Models, UpstreamModel{
			ID:            m.ID,
			Name:          name,
			Kind:          "llm",
			ContextLength: m.MaxTokenCount,
			MaxOutput:     m.MaxOutputTokens,
		})
	}
	if len(out.Models) == 0 {
		out.Warning = "The account returned no hosted models."
	}
	return out
}
