package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// ForwardFreePi sends an [OI] Chat Completions request to free-pi
// (api.freepi.ai).
//
// The body is forwarded byte-identical: free-pi performs no request-shape
// checks and has no TLS fingerprint gate (hungkevin/free-pi-cli-proxy), so there
// is nothing to rewrite and nothing to cloak. What it does require is two
// per-request headers that proxy.Forward[OI] cannot express, because it only
// knows the registry's static map:
//
//	x-session-id      stable per ACCOUNT (see free_pi_session.go)
//	x-client-version  the server's own `latest`, or the compiled-in floor
//
// It also must NOT be registered on the default passthrough: executor.Default()
// never inspects resp.StatusCode and jsonResponse always writes 200, which would
// turn a 409 `concurrent_session` into a successful turn carrying an error body.
// This executor checks the status itself and returns a real *proxy.UpstreamError,
// exactly as ForwardFreebuff does.
func ForwardFreePi(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	if req.Config == nil {
		return errors.New("free-pi: no provider config")
	}
	token := req.APIKey
	if token == "" {
		return errors.New("free-pi: access token is required")
	}

	// A dead credential is refused before the request goes out. free-pi has no
	// refresh token, so this is terminal and the honest answer is
	// "re-authenticate", not a 401 discovered after a wasted round trip.
	if freePiTokenExpired(token, time.Now()) {
		return freePiExpiredTokenError()
	}

	state := newFreePiSessionState(token, req.ConnData)

	// Claim the account for this process. Failing to acquire is not an error:
	// it only means a sibling holds reset rights, so this request must not
	// reset on a 409. The deferred release stops a panic or early return from
	// stranding the claim until its TTL.
	if freePiAcquireLease(req.Leases, state.AccountKey) {
		defer freePiReleaseLease(req.Leases, state.AccountKey)
	}

	resp, err := freePiDoChat(ctx, client, req, state)
	if err != nil {
		// proxy.DoRequest returns any non-200 as an *UpstreamError, so the
		// status is on the error rather than the response. That is also the
		// reason this provider cannot ride executor.Default(): there the same
		// status would never be inspected and a 409 would reach the client as a
		// successful turn carrying an error body.
		var ue *proxy.UpstreamError
		if !errors.As(err, &ue) || ue.StatusCode != http.StatusConflict {
			return err
		}

		code, _ := freePiErrorCode(ue.Body)
		retried, retryErr := freePiResetAndRetry(ctx, client, req, state, code)
		if retryErr != nil {
			return retryErr
		}
		if retried == nil {
			// Reset suppressed (sibling in flight, cooldown, opt-out) or the
			// reset itself failed: report the real conflict, unchanged, rather
			// than a synthetic error the client cannot act on.
			return ue
		}
		resp = retried
	}

	forwardUpstreamResponseHeaders(w, resp.Header)

	if req.IsStream {
		// Inspect the head of the body before it is wrapped for streaming.
		//
		// The ad gate answers a stream request with a plain JSON completion
		// object, not SSE frames, and it must be caught here: once
		// execSSEStream commits text/event-stream there is no way to report a
		// failure, and the gate's instruction would be streamed to the client as
		// the model's own output. The peek reads only the first bytes and hands
		// them back, so a real stream passes through untouched.
		if gate, restored := freePiPeekAdGate(resp.Body); gate != "" {
			return freePiAdGateError(gate)
		} else if restored != nil {
			resp.Body = restored
		}
		stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "free-pi")
		defer func() { _ = stallReader.Close() }()
		return execSSEStream(w, stallReader, req)
	}

	defer func() { _ = resp.Body.Close() }()

	// The ad gate arrives as a 200 whose body is the gate's instruction, so it
	// has to be recognised here — before jsonResponse hands it to the client as
	// if the model had said it. Reading the body is safe: the transport buffers
	// it anyway for this non-streaming path, and the JSON is re-emitted from the
	// bytes we already hold.
	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return readErr
	}
	if gate := freePiAdGateBody(raw); gate != "" {
		log.Warn("free-pi", "upstream paused the session pending an ad impression",
			"account", state.AccountKey,
			"remedy", "open the official free-pi CLI so an ad can load, then retry")
		return freePiAdGateError(gate)
	}
	return jsonResponseBody(ctx, w, raw, req.TranslateResp, req.ResponseBuf)
}

// freePiDoChat performs one upstream attempt with the free-pi header set.
func freePiDoChat(ctx context.Context, client *http.Client, req *Request, state freePiSessionState) (*http.Response, error) {
	// A fresh map per attempt: the registry's StaticHeaders are shared across
	// every request in the process, so the two per-account headers must never
	// be written into that map.
	headers := map[string]string{
		"Authorization":    "Bearer " + req.APIKey,
		"Content-Type":     "application/json",
		"x-session-id":     state.SessionID,
		"x-client-version": state.ClientVersion,
	}
	if req.IsStream {
		headers["Accept"] = "text/event-stream"
	}
	for k, v := range req.Config.StaticHeaders {
		headers[k] = v
	}
	return proxy.DoRequest(ctx, client, http.MethodPost, req.Config.BaseURL, headers, req.Body)
}

// freePiResetAndRetry handles a 409: it decides whether an automatic reset is
// permitted, performs it at most once, and retries once.
//
// Returns (nil, nil) when the reset was suppressed or failed, leaving the caller
// to report the original 409; a non-nil error means the retry itself blew up.
func freePiResetAndRetry(ctx context.Context, client *http.Client, req *Request, state freePiSessionState, code string) (*http.Response, error) {
	if !state.AutoReset {
		log.Debug("free-pi", "auto-reset disabled for this connection; returning 409", "account", state.AccountKey)
		return nil, nil
	}

	if !freePiResetPermitted(req.Leases, state.AccountKey, code) {
		return nil, nil
	}

	if err := freePiResetSession(ctx, client, req.Config, req.APIKey); err != nil {
		log.Warn("free-pi", "session reset failed", "account", state.AccountKey, "error", err)
		return nil, nil
	}

	resp, err := freePiDoChat(ctx, client, req, state)
	if err != nil {
		return nil, fmt.Errorf("free-pi upstream retry: %w", err)
	}
	return resp, nil
}

// freePiResetPermitted is the reset gate: lease ownership, then cooldown.
//
// The lease check and the cooldown check are separate on purpose. The lease
// answers "is a sibling driving this account right now?"; the cooldown answers
// "did we already try this recently?". Both must pass, and the lease mutex is
// released before the network call so a slow reset cannot block the stripe.
func freePiResetPermitted(store LeaseStore, accountKey, code string) bool {
	mu := freePiStripe(accountKey)
	mu.Lock()
	mayReset := freePiMayReset(store, accountKey)
	mu.Unlock()

	if !mayReset {
		log.Warn("free-pi", "409 while a sibling holds the session lease; not resetting",
			"account", accountKey, "code", code)
		return false
	}

	if !freePiResetAllowed(accountKey, time.Now()) {
		log.Warn("free-pi", "409 inside the reset cooldown; not resetting again",
			"account", accountKey, "code", code)
		return false
	}
	return true
}

// freePiResetSession calls POST /session/reset, which releases the caller's
// OTHER sessions — the same operation free-pi-cli exposes as
// /close-other-session.
//
// `released:false` is success: it means there was nothing to release, which is
// exactly the outcome the caller wanted.
func freePiResetSession(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, token string) error {
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	if cfg != nil {
		for k, v := range cfg.StaticHeaders {
			headers[k] = v
		}
	}
	resp, err := proxy.DoRequest(ctx, client, http.MethodPost, freePiOrigin(cfg)+"/session/reset", headers, nil)
	if err != nil {
		return fmt.Errorf("reset request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
	}
	return nil
}

// freePiOrigin derives the API root from the configured chat endpoint, so an
// operator pointing the provider at a stub or a mirror moves the sibling routes
// (/session/reset, /ads/*, /client-version) with it instead of silently reaching
// production.
func freePiOrigin(cfg *providers.ProviderConfig) string {
	base := ""
	if cfg != nil {
		base = cfg.BaseURL
	}
	if base == "" {
		return providers.FreePiOrigin
	}
	for _, suffix := range []string{"/v1/chat/completions", "/chat/completions", "/v1"} {
		if strings.HasSuffix(base, suffix) {
			return strings.TrimSuffix(base, suffix)
		}
	}
	return strings.TrimRight(base, "/")
}

// freePiErrorCode pulls `code` and `message` out of the upstream error
// envelope. Callers branch on the code only: the message text is server-owned
// and changes between deploys (the CLI's own error-notice extension makes the
// same point).
func freePiErrorCode(body []byte) (code, message string) {
	if len(body) == 0 {
		return "", ""
	}
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", ""
	}
	code = env.Code
	if code == "" {
		code = env.Error.Code
	}
	message = env.Message
	if message == "" {
		message = env.Error.Message
	}
	return code, message
}

// freePiExpiredTokenError reports a locally-detected dead credential using the
// same envelope shape as upstream, so the fallback layer and the dashboard both
// read it without a special case.
func freePiExpiredTokenError() error {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    FreePiTokenExpiredCode,
			"message": "free-pi credential has expired; free-pi has no refresh token, so sign in again to get a new one",
			"type":    FreePiTokenExpiredCode,
		},
	})
	// 401 so the existing auth-failure classification applies, with the body's
	// code steering it to the long cooldown rule rather than the 2-minute one.
	return &proxy.UpstreamError{StatusCode: http.StatusUnauthorized, Body: body}
}

// freePiPeekAdGate inspects the first bytes of an upstream body for the ad gate
// without consuming them.
//
// It returns the gate text when the body IS the gate, and otherwise the same
// bytes wrapped back into a ReadCloser so the caller can stream them unchanged.
// Only the gate's own shape is inspected: a body that does not start with a
// JSON object is returned immediately, because a real SSE frame starts with
// "data:".
func freePiPeekAdGate(body io.ReadCloser) (string, io.ReadCloser) {
	buffered := bufio.NewReader(body)
	head, err := buffered.Peek(2048)
	if err != nil && len(head) == 0 {
		// Too short to be a completion; give the caller its reader back.
		return "", &readCloserPair{Reader: buffered, Closer: body}
	}
	trimmed := bytes.TrimSpace(head)
	isSSE := bytes.HasPrefix(trimmed, []byte("data:"))
	// A real SSE frame is streamed straight through; only a body that could be
	// the gate (a JSON completion object, or SSE chunks whose text mentions the
	// gate) is worth reading out in full. The gate's streaming form is three
	// small frames, so this read is bounded and cheap.
	if !isSSE && (len(trimmed) == 0 || trimmed[0] != '{') {
		return "", &readCloserPair{Reader: buffered, Closer: body}
	}
	if !bytes.Contains(head, []byte("free-pi is free because it shows ads")) {
		// The overwhelming majority of streams fail this on the first 2 KiB and
		// are returned untouched, so the extra read only happens for a body that
		// already looks like the gate.
		return "", &readCloserPair{Reader: buffered, Closer: body}
	}
	raw, _ := io.ReadAll(io.LimitReader(buffered, 1<<20))
	if gate := freePiAdGateBody(raw); gate != "" {
		return gate, nil
	}
	if gate := freePiAdGateStreamBody(raw); gate != "" {
		return gate, nil
	}
	return "", &readCloserPair{Reader: io.MultiReader(bytes.NewReader(raw), buffered), Closer: body}
}

// readCloserPair re-attaches the original Closer to a reader that buffered part
// of the body, so replacing resp.Body does not leak the upstream handle.
type readCloserPair struct {
	io.Reader
	io.Closer
}

// freePiAdGateStreamBody scans an SSE-framed body for the same gate.
//
// Upstream answers a streaming request with the gate as ordinary chat chunks:
// one `data:` frame carrying the instruction as `delta.content`, then a final
// frame with finish_reason "stop" and usage all zeroes. There is no error frame
// to branch on, so the same two signals are required as for the JSON shape —
// the gate's opening phrase AND a billed-token count of zero — and additionally
// that the whole body contains no non-zero usage, which is what separates it
// from a model that happens to discuss free-pi's ads.
func freePiAdGateStreamBody(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	text := string(raw)
	if !providers.FreePiIsAdGate(text) {
		return ""
	}
	// Any frame that bills a token means the model really answered.
	for _, m := range reUsageTotals.FindAllStringSubmatch(text, -1) {
		for _, v := range m[1:] {
			if v != "" && v != "0" {
				return ""
			}
		}
	}
	if !strings.Contains(text, `"usage"`) {
		// No usage frame at all: unverifiable, so leave it alone rather than
		// risk converting a real answer into an error.
		return ""
	}
	// Report the instruction itself, taken from the delta that carried it.
	if m := reGateDeltaContent.FindStringSubmatch(text); m != nil {
		if s := unquoteJSONString(m[1]); s != "" {
			return s
		}
	}
	return providers.FreePiAdGateMessages[0]
}

var (
	reUsageTotals = regexp.MustCompile(`"prompt_tokens":(\d+),"completion_tokens":(\d+),"total_tokens":(\d+)`)
	// The gate delta, matched loosely: the phrase is server-owned prose, so the
	// capture runs to the closing quote of that JSON string.
	reGateDeltaContent = regexp.MustCompile(`"content":"((?:[^"\\]|\\.)*free-pi is free because it shows ads(?:[^"\\]|\\.)*)"`)
)

// unquoteJSONString decodes one JSON string body (no surrounding quotes).
func unquoteJSONString(src string) string {
	var out string
	if err := json.Unmarshal([]byte(`"`+src+`"`), &out); err != nil {
		return ""
	}
	return out
}

// freePiAdGateBody returns the gate's own instruction text when an upstream
// response is the ad gate rather than a completion, or "" for a real answer.
//
// The gate is NOT an error status: upstream answers HTTP 200 with
// finish_reason "stop" and zero usage, and the instruction arrives as the
// assistant message. A caller that only inspects the status renders that
// instruction to the user as if the model had produced it, which is what the pi
// harness in front of this gateway was doing.
//
// Detection is therefore content-based and narrow: a non-streaming body whose
// assistant content opens with the gate's stable first clause AND whose usage is
// all zeroes. Requiring both keeps a model that happens to discuss free-pi's ads
// from being mistaken for the gate — only the gate itself answers with no tokens
// billed.
func freePiAdGateBody(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return ""
	}
	content := parsed.Choices[0].Message.Content
	if !providers.FreePiIsAdGate(content) {
		return ""
	}
	if parsed.Usage.PromptTokens != 0 || parsed.Usage.CompletionTokens != 0 || parsed.Usage.TotalTokens != 0 {
		// Billed tokens mean the model actually answered; the phrase was its own
		// output, not a gate.
		return ""
	}
	return content
}

// freePiAdGateError turns the gate into a failed request the fallback layer and
// the client can act on, instead of a 200 whose body reads like an answer.
// 403 matches the documented ad-gate status so the existing classification
// applies; the body carries the provider's own wording verbatim, because it
// names the remedy (open the official CLI) that this gateway cannot perform on
// the operator's behalf.
func freePiAdGateError(message string) error {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    "ad_gate",
			"message": message,
			"type":    "ad_gate",
		},
	})
	return &proxy.UpstreamError{StatusCode: http.StatusForbidden, Body: body}
}
