package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// forwardOpenCodeRotating wraps a ForwardOpencode request with session rotation.
// When the free-tier gate reports FreeUsageLimitError, it expires the stable session
// that just failed and installs a freshly minted one via the install hook, then retries.
func forwardOpenCodeRotating(ctx context.Context, send func() (*http.Response, error),
	sendDirect func() (*http.Response, error), rotate func()) (*http.Response, error) {

	resp, err := send()
	if err == nil {
		return resp, nil
	}

	// Check for FreeUsageLimitError in the error
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) || ue.StatusCode != http.StatusTooManyRequests {
		return nil, err
	}

	bodyStr := string(ue.Body)
	if !strings.Contains(bodyStr, "FreeUsageLimitError") {
		return nil, err
	}

	// Rotate session: expire old, install fresh
	if rotate != nil {
		rotate()
	}

	// Retry with direct path (no relay)
	resp2, err2 := sendDirect()
	if err2 != nil {
		return nil, err2
	}

	return resp2, nil
}

// forwardOpencodeRequestDirect forwards an OpenCode request using plain HTTP client
// (bypasses stealth TLS when needed).
func forwardOpencodeRequestDirect(ctx context.Context, cfg *providers.ProviderConfig, apiKey string, body []byte, isStream bool) (*http.Response, error) {
	headers := map[string]string{}
	if !cfg.NoAuth {
		headers["Authorization"] = "Bearer " + apiKey
	}
	for k, v := range cfg.StaticHeaders {
		headers[k] = v
	}
	if isStream {
		headers["Accept"] = "text/event-stream"
	} else {
		headers["Accept"] = "*/*"
	}
	return proxy.DoRequest(ctx, nil, "POST", cfg.BaseURL, headers, body)
}
