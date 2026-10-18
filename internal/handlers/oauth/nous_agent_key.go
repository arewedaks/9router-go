package oauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	nousAgentKeyURL = "https://portal.nousresearch.com/api/oauth/agent-key"
	// Nous caps the minted key's life; 7200s is what the Hermes CLI asks for and
	// the longest value the Portal accepts.
	nousAgentKeyTTL = 7200
)

// mintNousAgentKey exchanges a Portal OAuth access token for an inference key.
//
// Returns the `sk-...` key and its lifetime in seconds. The key is what the
// OpenAI-compatible inference host accepts as a bearer, so it is stored as the
// connection's apiKey.
func mintNousAgentKey(client *http.Client, accessToken string) (string, int, error) {
	return mintNousAgentKeyFrom(client, nousAgentKeyURL, accessToken)
}

// mintNousAgentKeyFrom is the testable body of mintNousAgentKey.
func mintNousAgentKeyFrom(client *http.Client, url, accessToken string) (string, int, error) {
	body, err := json.Marshal(map[string]any{"min_ttl_seconds": nousAgentKeyTTL})
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("agent-key endpoint answered HTTP %d: %s", resp.StatusCode, truncateForOAuth(string(raw), 200))
	}

	var parsed struct {
		APIKey    string `json:"api_key"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", 0, errors.New("agent-key response is not valid JSON")
	}
	if parsed.APIKey == "" {
		return "", 0, errors.New("agent-key response carried no api_key")
	}
	ttl := parsed.ExpiresIn
	if ttl <= 0 {
		ttl = nousAgentKeyTTL
	}
	return parsed.APIKey, ttl, nil
}

// scopedHTTPClient is the bounded client used for the minting call. It matches
// the timeout doRequest applies to every other outbound OAuth call.
func scopedHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}
