package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ADCode (Tagflow AI) is the desktop editor's own "included AI" gateway. It is
// not a plain API-key service: the bearer is a Firebase ID token that lives for
// an hour, minted from the refresh token the editor stores after sign-in. The
// token exchange lives in the executor (internal/proxy/executor/adcode.go);
// this file only pins the facts the registry needs.
//
// Measured 2026-10-28 against https://adcode.bluethenics.com:
//
//	GET  /v1/ai/tagflow/config           200, public, lists the models + plans
//	GET  /v1/ai/tagflow/usage            200 with a bearer, 401 {missing-token} without
//	POST /v1/ai/tagflow/chat/completions 200 OpenAI-shaped, streams SSE
//
// Two things are mandatory beyond the bearer:
//   - a browser-shaped User-Agent, otherwise Cloudflare answers 403 "error code: 1010"
//   - the x-adcode-* headers the desktop client sends; persona is "coding-assistant"
//     for the chat lane (the editor sends "inline-assistant"/"agent" elsewhere).
const (
	// AdCodeAPIOrigin is the backend host. ADCODE_AD_SERVER overrides it in the
	// vendor client, so the same variable is honoured here for parity.
	AdCodeAPIOrigin = "https://adcode.bluethenics.com"

	// AdCodeTagflowBase is the OpenAI-compatible base the editor points its own
	// provider at (`baseUrl: ${apiBaseUrl()}/ai/tagflow`).
	AdCodeTagflowBase = AdCodeAPIOrigin + "/v1/ai/tagflow"

	// AdCodeModelConfigURL is the public catalogue: model ids with their
	// context window, output ceiling and whether the free plan serves them.
	AdCodeModelConfigURL = AdCodeTagflowBase + "/config"

	// AdCodeUsageURL reports the account's allowance (daily requests/tokens,
	// weekly credits). 401 without a bearer, so it is only useful per connection.
	AdCodeUsageURL = AdCodeTagflowBase + "/usage"

	// AdCodeFirebaseAPIKey is the web API key baked into the desktop client. It
	// identifies the Firebase project, not an account: the refresh-token exchange
	// refuses a token from another project.
	AdCodeFirebaseAPIKey = "AIzaSyATZzX5Fw2HjR34VxB3UQWTmljprA5uXqE"

	// AdCodeUserAgent mirrors the desktop client's Electron UA. Cloudflare
	// rejects Go's default agent on this host.
	AdCodeUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) ADCode/2.4.4 Chrome/128.0.0.0 Electron/32.0.0 Safari/537.36"
)

// AdCodeStaticHeaders are the headers every call to the gateway carries. The
// persona is the chat lane's ("coding-assistant"); the editor sends
// "inline-assistant" for completions and a role id for agents, which is also
// why the source header is not decorative — the gateway scores it.
func AdCodeStaticHeaders() map[string]string {
	return map[string]string{
		"User-Agent":          AdCodeUserAgent,
		"x-adcode-ai-source":  "chat",
		"x-adcode-ai-persona": "coding-assistant",
		"x-adcode-platform":   runtime.GOOS,
	}
}

// AdCodeTokenURL is a seam for tests; the real value is Firebase's exchange.
var AdCodeTokenURL = "https://securetoken.googleapis.com/v1/token"

type adCodeToken struct {
	token  string
	expiry time.Time
}

var (
	adCodeMu    sync.Mutex
	adCodeCache = map[string]adCodeToken{}
)

// AdCodeIDToken turns the account's refresh token into the one-hour ID token the
// gateway requires. Results are cached per refresh token until two minutes before
// expiry, so a burst of requests performs one exchange.
//
// The error names the upstream code (INVALID_REFRESH_TOKEN) and never the
// credential itself.
func AdCodeIDToken(ctx context.Context, client *http.Client, refreshToken string) (string, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return "", fmt.Errorf("adcode: empty refresh token")
	}
	sum := sha256.Sum256([]byte(refreshToken))
	key := hex.EncodeToString(sum[:8])

	adCodeMu.Lock()
	if cached, ok := adCodeCache[key]; ok && time.Now().Before(cached.expiry) {
		adCodeMu.Unlock()
		return cached.token, nil
	}
	adCodeMu.Unlock()

	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	endpoint := AdCodeTokenURL + "?key=" + url.QueryEscape(AdCodeFirebaseAPIKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", AdCodeUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("adcode: securetoken HTTP %d: %s", resp.StatusCode, adCodeErrorCode(body))
	}

	var parsed struct {
		IDToken   string `json:"id_token"`
		ExpiresIn any    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("adcode: decode securetoken: %w", err)
	}
	if parsed.IDToken == "" {
		return "", fmt.Errorf("adcode: securetoken returned no id_token")
	}

	ttl := 55 * time.Minute
	switch v := parsed.ExpiresIn.(type) {
	case string:
		if secs, err := time.ParseDuration(v + "s"); err == nil && secs > 0 {
			ttl = secs
		}
	case float64:
		if v > 0 {
			ttl = time.Duration(v) * time.Second
		}
	}
	adCodeMu.Lock()
	adCodeCache[key] = adCodeToken{token: parsed.IDToken, expiry: time.Now().Add(ttl - 2*time.Minute)}
	adCodeMu.Unlock()
	return parsed.IDToken, nil
}

// ResetAdCodeTokenCache drops cached ID tokens. Tests use it; production never
// needs to, because entries expire on their own.
func ResetAdCodeTokenCache() {
	adCodeMu.Lock()
	adCodeCache = map[string]adCodeToken{}
	adCodeMu.Unlock()
}

// adCodeErrorCode pulls the machine-readable code out of a securetoken error
// body without echoing values.
func adCodeErrorCode(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Error.Message != "" {
			return parsed.Error.Message
		}
		if parsed.Error.Status != "" {
			return parsed.Error.Status
		}
	}
	return "unrecognised error body"
}
