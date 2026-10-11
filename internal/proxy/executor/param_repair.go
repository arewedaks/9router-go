package executor

import (
	"errors"
	"net/http"

	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/paramfix"
)

// upstreamTransport adapts the proxy package's error type to the interface
// paramfix uses to classify a rejection, keeping paramfix free of a dependency
// on the proxy package.
type upstreamTransport struct{}

func (upstreamTransport) AsUpstream(err error) (paramfix.UpstreamError, bool) {
	var uErr *proxy.UpstreamError
	if errors.As(err, &uErr) {
		return uErr, true
	}
	return nil, false
}

// forwardWithParamRepair sends the request, retrying once per parameter the
// upstream rejected.
//
// A strict OpenAI-compatible gateway rejects the whole request over a single
// unknown field, and that field is almost always one a client SDK added by
// default rather than something the caller asked for. The rule table cannot
// cover these endpoints: a custom node's provider id is a generated UUID, so
// there is no name to write a rule against. Repairing from the response instead
// of predicting it covers every compatible node, including ones added later.
//
// The classification and repair live in paramfix so this path and the chat
// handler's OpenAI-compatible path behave identically.
func forwardWithParamRepair(req *Request) repairResult {
	result := paramfix.ForwardWithRepair(req.Body, func(b []byte) (*http.Response, error) {
		return proxy.ForwardOpenAI(req.Ctx, req.Client, req.Config, req.APIKey, b, req.IsStream)
	}, upstreamTransport{})
	req.Body = result.Body
	return repairResult{resp: result.Resp, body: result.Body, err: result.Err}
}

// repairResult carries the final attempt back to the caller.
type repairResult struct {
	resp *http.Response
	body []byte
	err  error
}
