package executor

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// ForwardDeepAI forwards DeepAI Code — the lane the vendor's desktop IDE uses.
//
// The upstream is OpenAI-compatible, but it meters and gates calls on two
// headers the IDE always sends and a bare curl does not:
//
//	X-DeepAI-Code-Theos: <lane version>  default "4". Absent, a metered call is
//	                                     refused with `machine_required`.
//	X-DeepAI-Code-Machine: sha256(machine-id)
//	                                     the per-machine daily allowance is
//	                                     bucketed by this; see deepai_machine.go.
//
// Auth is `Api-Key: <key>` — not Authorization, not a bearer. Verified live: a
// keyless POST to https://api.deepai.org/deepai_code/chat/completions answers
// {"error":{"message":"Sign in to DeepAI to use DeepAI Code.",
//
//	"type":"auth_required","code":"auth_required"}}.
//
// This executor is a thin decorator over ForwardOpenAI: it injects the lane and
// machine headers (and a default model, since the lane's bundled default is
// glm-5.3-flash) then hands off, so streaming, tool calls and response
// translation stay on the one already-tested path.
func ForwardDeepAI(w http.ResponseWriter, req *Request) error {
	applyDeepAIHeaders(req)
	return ForwardOpenAI(w, req)
}

// DeepAIDefaultModel is the managed default the IDE ships
// (BUNDLED_DEEPAI_MODELS in the vendor bundle).
const DeepAIDefaultModel = "glm-5.3-flash"

// deepAILaneVersion is the billing lane this build speaks; the vendor pins it as
// `X-DeepAI-Code-Theos: "4"` and refuses anything older with machine_required.
const deepAILaneVersion = "4"

// applyDeepAIHeaders injects the lane/machine headers and a default model. It
// never fails the request: a missing machine id is normal on a host that exposes
// none and the upstream accepts that, and a body it cannot parse is handed to the
// generic path so the real error surfaces instead of being masked here.
func applyDeepAIHeaders(req *Request) {
	if req.Config == nil {
		return
	}
	if req.Config.StaticHeaders == nil {
		req.Config.StaticHeaders = map[string]string{}
	}
	if _, ok := req.Config.StaticHeaders["X-DeepAI-Code-Theos"]; !ok {
		req.Config.StaticHeaders["X-DeepAI-Code-Theos"] = deepAILaneVersion
	}
	if _, ok := req.Config.StaticHeaders["X-DeepAI-Code-Machine"]; !ok {
		if value, ok := deepAIMachineHeaderValue(req.ConnData); ok {
			req.Config.StaticHeaders["X-DeepAI-Code-Machine"] = value
		}
	}
	if len(req.Body) == 0 {
		return
	}
	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return
	}
	if _, ok := body["model"]; !ok {
		body["model"] = DeepAIDefaultModel
		if b, err := json.Marshal(body); err == nil {
			req.Body = b
		}
	}
}

// DeepAIMachineIDKey is the per-connection override for the machine id, read
// from the connection's providerSpecificData.
//
// The upstream buckets its daily allowance per machine, so a pool that fronts
// several DeepAI accounts under one host id would make them share (and starve on)
// a single allowance. Setting this on a connection gives that account its own
// bucket — the value is the raw machine id, hashed exactly as the vendor hashes
// its own (sha256 of the lowercased string), so putting the UUID the operator's
// Windows box reports reproduces what the IDE would send there.
const DeepAIMachineIDKey = "deepaiMachineId"

// deepAIMachineHeaderValue returns the header value: the connection's own machine
// id when one is configured, else the local host's digest, else not ok.
func deepAIMachineHeaderValue(connData map[string]any) (string, bool) {
	if connData != nil {
		if raw, ok := connData[DeepAIMachineIDKey].(string); ok {
			if raw = strings.TrimSpace(raw); raw != "" {
				return sha256Hex(strings.ToLower(raw)), true
			}
		}
	}
	if id := cachedDeepAIMachineID(); id.available {
		return id.value, true
	}
	return "", false
}

// DeepAIModelsURL is the lane's model catalogue endpoint: the sibling /models of
// the chat route (https://api.deepai.org/deepai_code/models).
func DeepAIModelsURL(cfg *providers.ProviderConfig) string {
	base := "https://api.deepai.org/deepai_code"
	if cfg != nil && cfg.BaseURL != "" {
		if u, err := url.Parse(cfg.BaseURL); err == nil {
			base = u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, "/chat/completions")
		}
	}
	return base + "/models"
}

// DeepAIUsesSystemone reports whether a request names a model the lane serves
// through its own decision endpoint. Reserved for a future shape: today the lane
// is chat-only, so this is the single place to branch if that changes.
func DeepAIUsesSystemone(string) bool { return false }

// deepAIModelsHeaders are the headers every lane request needs, with the machine
// digest when a connection override or this host provides one.
func deepAIModelsHeaders(apiKey string, connData map[string]any) map[string]string {
	h := map[string]string{
		"Content-Type":        "application/json",
		"Accept":              "application/json",
		"X-DeepAI-Code-Theos": deepAILaneVersion,
	}
	if value, ok := deepAIMachineHeaderValue(connData); ok {
		h["X-DeepAI-Code-Machine"] = value
	}
	if apiKey != "" {
		h["Api-Key"] = apiKey
	}
	return h
}

// fetchDeepAIModels lists the lane catalogue. The listing is behind the same
// auth as a call (403 without a key), so a keyless probe cannot populate it; the
// function exists so an operator-supplied key can, and so the fetcher wiring has
// one implementation to call.
func fetchDeepAIModels(ctx context.Context, client *http.Client, cfg *providers.ProviderConfig, apiKey string) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	r, err := proxy.DoRequest(ctx, client, "POST", DeepAIModelsURL(cfg), deepAIModelsHeaders(apiKey, nil), []byte("{}"))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return nil, fmt.Errorf("deepai models: status %d: %s", r.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		out := make([]string, 0, len(parsed.Data))
		for _, m := range parsed.Data {
			if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		return out, nil
	}
	return nil, nil
}
