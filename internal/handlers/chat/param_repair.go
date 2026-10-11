package chat

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"

	"9router/proxy/internal/log"
	internalproxy "9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/paramfix"
	"9router/proxy/internal/providers"
)

// upstreamTransport adapts the proxy package's error type to the interface
// paramfix uses to classify a rejection, keeping paramfix free of a dependency
// on the proxy package.
type upstreamTransport struct{}

func (upstreamTransport) AsUpstream(err error) (paramfix.UpstreamError, bool) {
	var uErr *internalproxy.UpstreamError
	if errors.As(err, &uErr) {
		return uErr, true
	}
	return nil, false
}

// forwardWithParamRepair sends the request upstream on the OpenAI-compatible
// path, retrying once per rejected parameter.
//
// This is the path every custom compatible node takes (the provider id is
// "openai-compatible-chat-<uuid>"), and those endpoints are exactly the ones
// paramfix.Rules cannot name. Repairing from the upstream's own rejection is
// what makes them survive strict validators such as Upstage's.
func (h *ChatHandler) forwardWithParamRepair(
	ctx context.Context,
	cfg *providers.ProviderConfig,
	provider string,
	apiKey string,
	body []byte,
	isStream bool,
) (*http.Response, error) {
	// Same corrections the executor applies, so a rule reaches this path too.
	body = paramfix.Apply(provider, modelFromBody(body), body)

	// A client speaking the Anthropic dialect can be pointed at this endpoint
	// instead of /v1/messages, and the translator does not run here. Converting
	// the blocks up front is what keeps such a request from reaching an
	// OpenAI-compatible upstream in a shape it refuses.
	if converted, n := paramfix.ConvertAnthropicImageBlocks(body); n > 0 {
		body = converted
		log.Info("paramfix", "converted anthropic image blocks", "provider", provider, "count", n)
	}

	result := paramfix.ForwardWithRepair(body, func(b []byte) (*http.Response, error) {
		return internalproxy.ForwardOpenAI(ctx, h.Client, cfg, apiKey, b, isStream)
	}, upstreamTransport{})

	if result.Err == nil && (len(result.Dropped) > 0 || result.ImagesTrimmed > 0) {
		log.Info("paramfix", "repaired rejected request",
			"provider", provider, "fields", result.Dropped, "images_trimmed", result.ImagesTrimmed)
	}
	return result.Resp, result.Err
}

// modelFromBody reads the model name paramfix scopes model-specific rules by.
// A body that cannot be read simply scopes nothing, which is the same behaviour
// as an unknown model.
func modelFromBody(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	return m.Model
}
