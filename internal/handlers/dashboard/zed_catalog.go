package dashboard

import (
	"io"
	"net/http"
	"strings"
	"time"

	json "encoding/json/v2"
)

// zedModelsURL is a seam for tests. The endpoint answers 401 without a
// signed-in account, and is the only place Zed publishes its hosted models.
var zedModelsURL = "https://cloud.zed.dev/models"

// fetchZedModels imports the models a Zed account can use. Two details differ
// from a normal catalogue:
//
//   - the credential is `<user_id> <access_token>` with no bearer prefix, so both
//     halves must come from the connection the sign-in stored;
//   - the payload nests what the router needs under different names
//     (max_token_count / max_output_tokens) and carries a display name, which is
//     what the panel should show.
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

	req, err := http.NewRequest(http.MethodGet, zedModelsURL, nil)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	req.Header.Set("Authorization", conn.UserID+" "+conn.AccessToken)
	req.Header.Set("Accept", "application/json")

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
		out.Error = "Zed's model list answered HTTP " + http.StatusText(resp.StatusCode) +
			" — the account may not have hosted models included"
		return out
	}

	var parsed struct {
		Models []struct {
			ID               string `json:"id"`
			DisplayName      string `json:"display_name"`
			MaxTokenCount    int    `json:"max_token_count"`
			MaxOutputTokens  int    `json:"max_output_tokens"`
			SupportsTools    bool   `json:"supports_tools"`
			SupportsImages   bool   `json:"supports_images"`
			SupportsThinking bool   `json:"supports_thinking"`
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
