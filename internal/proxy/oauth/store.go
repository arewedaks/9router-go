package oauth

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// RefreshStoredConnection refreshes an expiring OAuth connection exactly once,
// no matter how many goroutines ask for it at the same time.
//
// It is the single supported entry point for refreshing a stored connection.
// Both the chat path and the dashboard "Test Connection" probe call it, so a
// manual test can never race a live chat request for the same refresh token
// (which would trigger refresh_token_reused and brick a healthy account).
//
// Behaviour:
//
//   - The per-connection lock is taken for the whole "read → refresh → persist"
//     sequence.
//   - Expiry is re-checked INSIDE the lock. If another goroutine refreshed the
//     token while this caller waited, the fresh token is returned and no second
//     refresh is sent. This is what makes it safe under concurrency.
//   - force=true refreshes even when the token is not locally expired. The chat
//     path uses it after an upstream 401/403, where the token looked valid but
//     Google rejected it.
//
// Returns the (possibly unchanged) access token, the project ID, and whether a
// refresh actually happened. A nil error with refreshed=false means the token
// was still valid (or there is nothing to refresh), and the caller should use
// the returned token as-is.
func RefreshStoredConnection(ctx context.Context, repo *db.Repo, client *http.Client, connectionID string, force bool) (accessToken, projectID string, refreshed bool, err error) {
	if repo == nil || connectionID == "" {
		return "", "", false, fmt.Errorf("refresh: missing repo or connection id")
	}
	// The lock is acquired before any DB read so the "token is expired" decision
	// and the refresh that follows it cannot be interleaved by another caller.
	lockErr := WithRefreshLock(connectionID, func() error {
		accessToken, projectID, refreshed, err = refreshStoredConnectionLocked(ctx, repo, client, connectionID, force)
		return err
	})
	if lockErr != nil {
		return accessToken, projectID, refreshed, lockErr
	}
	return accessToken, projectID, refreshed, nil
}

// refreshStoredConnectionLocked is the body of RefreshStoredConnection. It MUST
// only be called with the per-connection lock already held.
func refreshStoredConnectionLocked(ctx context.Context, repo *db.Repo, client *http.Client, connectionID string, force bool) (string, string, bool, error) {
	database := repo.RawDB()
	row := database.QueryRow("SELECT provider, data FROM providerConnections WHERE id = ?", connectionID)

	var provider, rawData string
	if err := row.Scan(&provider, &rawData); err != nil {
		return "", "", false, fmt.Errorf("refresh: load connection %s: %w", connectionID, err)
	}

	var connMap map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &connMap); err != nil {
		return "", "", false, fmt.Errorf("refresh: parse connection %s: %w", connectionID, err)
	}

	oauthData := providers.ParseOAuthConnection(connMap)
	currentToken := ""
	projectID := ""
	if oauthData != nil {
		currentToken = oauthData.AccessToken
		projectID = oauthData.ProjectID
	}

	if oauthData == nil || oauthData.RefreshToken == "" {
		// API-key connections (and OAuth connections with no refresh token) have
		// nothing to refresh; report the current token unchanged.
		return currentToken, projectID, false, nil
	}

	// Re-check expiry *inside* the lock. If a concurrent caller already
	// refreshed this connection, the stored token is no longer expired and we
	// return it without a second exchange — the whole point of the lock.
	if !force && !oauthData.IsExpired() {
		return currentToken, projectID, false, nil
	}

	if client == nil {
		client = http.DefaultClient
	}

	result, refreshErr := runRefresher(ctx, client, provider, oauthData, currentToken)
	if refreshErr != nil {
		return currentToken, projectID, false, refreshErr
	}
	if result == nil || result.AccessToken == "" {
		return currentToken, projectID, false, fmt.Errorf("refresh: %s returned no access token", provider)
	}

	newProjectID := projectID
	if result.ProjectID != "" {
		newProjectID = result.ProjectID
	}

	if persistErr := persistRefreshResult(database, connectionID, rawData, result); persistErr != nil {
		// The token IS refreshed; only persistence failed. Surface it, but keep
		// the fresh token usable for the in-flight request.
		return result.AccessToken, newProjectID, true, persistErr
	}

	return result.AccessToken, newProjectID, true, nil
}

// runRefresher uses a provider-specific refresher when one is registered and
// otherwise falls back to the standard OAuth2 refresh configured for the
// provider. Mirrors the precedence the chat path already relied on.
func runRefresher(ctx context.Context, client *http.Client, provider string, data *providers.OAuthConnectionData, currentToken string) (*TokenResult, error) {
	if fn := Get(provider); fn != nil {
		return fn(ctx, &Params{
			Client:       client,
			Provider:     provider,
			RefreshToken: data.RefreshToken,
			AccessToken:  currentToken,
		})
	}

	cfg, ok := providers.KnownOAuthConfigs[provider]
	if !ok {
		return nil, fmt.Errorf("refresh: no OAuth configuration for provider %q", provider)
	}
	resp, err := providers.RefreshToken(cfg, data.RefreshToken)
	if err != nil {
		return nil, err
	}
	return &TokenResult{
		AccessToken: resp.AccessToken,
		ExpiresIn:   resp.ExpiresIn,
		Scope:       resp.Scope,
	}, nil
}

// persistRefreshResult merges a refresh result into the stored connection blob
// without dropping unrelated fields (model locks, provider-specific data, …).
func persistRefreshResult(database *sql.DB, connectionID, rawData string, result *TokenResult) error {
	var existing map[string]interface{}
	if err := json.Unmarshal([]byte(rawData), &existing); err != nil {
		existing = make(map[string]interface{})
	}
	update := BuildConnectionUpdate(result)
	if result.ProjectID != "" {
		update["projectId"] = result.ProjectID
	}
	for k, v := range update {
		existing[k] = v
	}
	merged, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("refresh: marshal merged connection: %w", err)
	}
	_, err = database.Exec(
		"UPDATE providerConnections SET data = ?, updatedAt = ? WHERE id = ?",
		string(merged), time.Now().UTC().Format(time.RFC3339), connectionID,
	)
	if err != nil {
		return fmt.Errorf("refresh: persist connection %s: %w", connectionID, err)
	}
	return nil
}
