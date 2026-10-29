package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	json "encoding/json/v2"
)

// OmniRush (omnirush.ai) is a coding CLI whose "included AI" is a vendor
// gateway. It is not OpenAI-chat compatible: the only model route is the
// Responses API, streaming only. Facts below were measured against the live
// gateway on 2026-10-29:
//
//	GET  {origin}/device/me            200 with a device bearer
//	GET  {base}/v1/models              200 OpenAI-style list (gpt-6-astra, …)
//	POST {base}/v1/responses           200 SSE; 400 "Stream must be set to true"
//	POST {base}/v1/chat/completions    404 unsupported_model_endpoint
//
// Two headers decide whether the gateway answers at all: the User-Agent must be
// the bare product name (a versioned "omnirush/2.7.1" is refused as a lookalike
// with 403 client_not_allowed) and X-OmniRush-Client must carry cli/<version> so
// the update gate sees a supported release.
const (
	// OmniRushOrigin is the account origin the device endpoints hang off.
	OmniRushOrigin = "https://omnirush.ai/omnirush"

	// OmniRushUserAgent is deliberately version-less.
	OmniRushUserAgent = "omnirush"

	// OmniRushDefaultClientVersion is the CLI release reported to the update
	// gate. The installed 2.7.0 is below the minimum, so the gate would answer
	// 426 for it; a current release is reported instead.
	OmniRushDefaultClientVersion = "2.7.1"
)

// OmniRushBase is the origin the device endpoints hang off. A variable so tests
// can point it at httptest.
var OmniRushBase = OmniRushOrigin

// OmniRushClientVersion is env-overridable: when OmniRush raises its minimum the
// value moves without a rebuild.
func OmniRushClientVersion() string {
	if v := strings.TrimSpace(os.Getenv("OMNIRUSH_CLIENT_VERSION")); v != "" {
		return v
	}
	return OmniRushDefaultClientVersion
}

// OmniRushHeaders are the identity headers every gateway call carries.
func OmniRushHeaders() map[string]string {
	return map[string]string{
		"User-Agent":        OmniRushUserAgent,
		"X-OmniRush-Client": "cli/" + OmniRushClientVersion(),
	}
}

// OmniRushDeviceCode is the pending authorization a sign-in starts with.
type OmniRushDeviceCode struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                int
	ExpiresIn               int
}

// OmniRushAuthorize starts the device sign-in the CLI itself uses.
func OmniRushAuthorize(ctx context.Context, client *http.Client, deviceName, platform string) (OmniRushDeviceCode, error) {
	if deviceName == "" {
		deviceName = "9router"
	}
	if platform == "" {
		platform = "linux"
	}
	body, err := json.Marshal(map[string]any{"device_name": deviceName, "platform": platform})
	if err != nil {
		return OmniRushDeviceCode{}, err
	}
	raw, status, err := omniRushPost(ctx, client, OmniRushBase+"/device/authorize", body)
	if err != nil {
		return OmniRushDeviceCode{}, err
	}
	if status != http.StatusOK {
		return OmniRushDeviceCode{}, fmt.Errorf("omnirush: authorize HTTP %d: %s", status, omniRushDetail(raw))
	}
	var parsed struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return OmniRushDeviceCode{}, fmt.Errorf("omnirush: decode authorize: %w", err)
	}
	if parsed.DeviceCode == "" {
		return OmniRushDeviceCode{}, fmt.Errorf("omnirush: authorize returned no device_code")
	}
	if parsed.Interval <= 0 {
		parsed.Interval = 5
	}
	if parsed.ExpiresIn <= 0 {
		parsed.ExpiresIn = 600
	}
	return OmniRushDeviceCode{
		DeviceCode:              parsed.DeviceCode,
		UserCode:                parsed.UserCode,
		VerificationURI:         parsed.VerificationURI,
		VerificationURIComplete: parsed.VerificationURIComplete,
		Interval:                parsed.Interval,
		ExpiresIn:               parsed.ExpiresIn,
	}, nil
}

// OmniRushTokenResult is one poll of /device/token.
type OmniRushTokenResult struct {
	Pending      bool
	SlowDown     bool
	AccessToken  string
	RefreshToken string
	GatewayURL   string
	Detail       string
}

// OmniRushPollToken asks once whether the operator approved the device.
func OmniRushPollToken(ctx context.Context, client *http.Client, deviceCode string) (OmniRushTokenResult, error) {
	body, err := json.Marshal(map[string]any{"device_code": deviceCode})
	if err != nil {
		return OmniRushTokenResult{}, err
	}
	raw, status, err := omniRushPost(ctx, client, OmniRushBase+"/device/token", body)
	if err != nil {
		return OmniRushTokenResult{}, err
	}
	detail := omniRushDetail(raw)
	switch {
	case status == http.StatusOK:
		var parsed struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			GatewayURL   string `json:"gateway_url"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return OmniRushTokenResult{}, fmt.Errorf("omnirush: decode token: %w", err)
		}
		if parsed.AccessToken == "" || parsed.RefreshToken == "" {
			return OmniRushTokenResult{}, fmt.Errorf("omnirush: token response missing access/refresh token")
		}
		return OmniRushTokenResult{
			AccessToken:  parsed.AccessToken,
			RefreshToken: parsed.RefreshToken,
			GatewayURL:   parsed.GatewayURL,
		}, nil
	case status == http.StatusPreconditionRequired || detail == "authorization_pending":
		return OmniRushTokenResult{Pending: true, Detail: detail}, nil
	case status == http.StatusTooManyRequests || detail == "slow_down":
		return OmniRushTokenResult{SlowDown: true, Detail: detail}, nil
	default:
		return OmniRushTokenResult{Detail: detail}, fmt.Errorf("omnirush: token HTTP %d: %s", status, detail)
	}
}

// OmniRushRefresh rotates the device tokens. The gateway invalidates the
// presented refresh token, so the new pair must be stored.
func OmniRushRefresh(ctx context.Context, client *http.Client, refreshToken string) (accessToken, newRefresh string, expiresIn int, err error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return "", "", 0, fmt.Errorf("omnirush: empty refresh token")
	}
	body, err := json.Marshal(map[string]any{"refresh_token": refreshToken})
	if err != nil {
		return "", "", 0, err
	}
	raw, status, err := omniRushPost(ctx, client, OmniRushBase+"/device/refresh", body)
	if err != nil {
		return "", "", 0, err
	}
	if status != http.StatusOK {
		return "", "", 0, fmt.Errorf("omnirush: refresh HTTP %d: %s", status, omniRushDetail(raw))
	}
	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", 0, fmt.Errorf("omnirush: decode refresh: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", "", 0, fmt.Errorf("omnirush: refresh returned no access token")
	}
	return parsed.AccessToken, parsed.RefreshToken, parsed.ExpiresIn, nil
}

// omniRushPost performs a JSON POST with the gateway identity headers.
func omniRushPost(ctx context.Context, client *http.Client, url string, body []byte) ([]byte, int, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range OmniRushHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	return raw, resp.StatusCode, nil
}

// omniRushDetail pulls `detail` out of an error body.
func omniRushDetail(raw []byte) string {
	var parsed struct {
		Detail string `json:"detail"`
		Error  struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if parsed.Detail != "" {
			return parsed.Detail
		}
		if parsed.Error.Code != "" {
			return parsed.Error.Code
		}
		if parsed.Error.Message != "" {
			return parsed.Error.Message
		}
	}
	return strings.TrimSpace(string(raw))
}
