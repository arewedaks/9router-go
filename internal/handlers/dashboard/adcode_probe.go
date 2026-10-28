package dashboard

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// adCodeUsageURL is a seam for tests; the real value is the vendor endpoint.
var adCodeUsageURL = providers.AdCodeUsageURL

// probeAdCodeConnection validates an ADCode account against the vendor's own
// allowance endpoint. A chat probe would judge the account by whichever model is
// picked, and the free lane's upstream model goes down on the vendor's side
// (502 "included AI provider refused the request") while the credential is
// perfectly healthy — the same trap Cline had.
//
// GET /v1/ai/tagflow/usage answers 200 for a signed-in account and
// 401 {"error":{"code":"missing-token"}} for a dead one, costs no credits, and
// names the plan in its body.
func (h *Handler) probeAdCodeConnection(ctx context.Context, raw map[string]any, out connectionTestResult) connectionTestResult {
	refresh := connString(raw, "refreshToken", "apiKey", "accessToken", "token")
	if refresh == "" {
		out.Error = "No ADCode refresh token on this connection (looked for refreshToken / apiKey)."
		return out
	}

	client := h.httpClientForTest()
	ctx, cancel := context.WithTimeout(ctx, connectionTestTimeout())
	defer cancel()

	token, err := providers.AdCodeIDToken(ctx, client, refresh)
	if err != nil {
		out.Error = "Could not exchange the ADCode refresh token: " + err.Error()
		return out
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adCodeUsageURL, nil)
	if err != nil {
		out.Error = "Could not build the probe request: " + err.Error()
		return out
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range providers.AdCodeStaticHeaders() {
		req.Header.Set(k, v)
	}
	setProbeUserAgent(req)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			out.Error = "Test timed out."
			return out
		}
		out.Error = "Probe request failed: " + err.Error()
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, connectionTestMaxBody))
	text := string(body)
	out.Status = resp.StatusCode

	switch {
	case resp.StatusCode == http.StatusOK:
		out.Valid = true
		out.TestedAt = time.Now().UTC().Format(time.RFC3339)
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		// A revoked refresh token fails at the exchange above; a 401 here means
		// the minted token was not accepted (banned account, wrong project).
		out.Error = "ADCode refused the token (HTTP " + strconv.Itoa(resp.StatusCode) + "): " +
			truncate(sanitizeUpstreamText(text), 240)
	default:
		out.Error = "Probe answered HTTP " + strconv.Itoa(resp.StatusCode) + ": " +
			truncate(sanitizeUpstreamText(text), 240)
	}
	if out.Error == "" && strings.Contains(text, `"blocked":true`) {
		out.Warning = "Account is blocked for included AI."
	}
	return out
}
