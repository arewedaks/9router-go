package paramfix

import (
	"bytes"
	"errors"
	"io"
	"net/http"
)

// UpstreamError is the minimal view of a non-200 upstream response this package
// needs to classify a rejection. Declared as an interface so paramfix stays free
// of the proxy package's concrete error type and both callers — the executor and
// the chat handler — can pass their own.
type UpstreamError interface {
	error
	Status() int
	ErrorBody() []byte
}

// Doer performs one upstream attempt with the given body.
type Doer func(body []byte) (*http.Response, error)

// Transport classifies the errors a Doer can return.
type Transport interface {
	// AsUpstream reports whether err is an upstream HTTP rejection, returning a
	// view of it. ok is false for transport-level failures.
	AsUpstream(err error) (UpstreamError, bool)
}

// MaxRepairs bounds how many fields a single request may have stripped. Each
// round removes exactly one field, so this is also the number of extra upstream
// calls the repair may cost. Three covers the realistic worst case (store +
// stream_options + a billing hint) without turning a broken request into an
// unbounded retry loop.
const MaxRepairs = 3

// RepairResult carries the final attempt back to the caller.
type RepairResult struct {
	Resp *http.Response
	Body []byte
	Err  error
	// Dropped lists the top-level fields removed, in the order they were. Empty
	// when the request went through untouched. Callers use it for logging.
	Dropped []string
}

// ForwardWithRepair sends the request, and when the upstream refuses it over a
// parameter the client did not need, removes that parameter and tries again.
//
// A strict OpenAI-compatible gateway rejects the whole request over a single
// unknown field, and that field is almost always one a client SDK added by
// default rather than something the caller asked for. Rules in Rules cannot
// cover these endpoints: a custom node's provider id is a generated UUID, so
// there is no name to write a rule against. Repairing from the response instead
// of predicting it covers every compatible node, including ones added later.
//
// Only fields on the safe-to-drop allowlist are touched. A rejection naming
// anything else — `messages`, `model`, a schema error — is returned to the
// caller untouched, because silently removing it would serve a different request
// than the one that was asked for.
func ForwardWithRepair(body []byte, doer Doer, transport Transport) RepairResult {
	var lastErr error
	var dropped []string

	for attempt := 0; attempt <= MaxRepairs; attempt++ {
		resp, err := doer(body)
		if err == nil {
			return RepairResult{Resp: resp, Body: body, Dropped: dropped}
		}
		lastErr = err

		uErr, ok := transport.AsUpstream(err)
		if !ok || uErr.Status() != http.StatusBadRequest {
			return RepairResult{Err: err, Body: body, Dropped: dropped}
		}

		field := UnknownArgumentFromErrorBody(uErr.ErrorBody())
		if field == "" || !IsSafeToDrop(field) {
			return RepairResult{Err: err, Body: body, Dropped: dropped}
		}
		repaired, changed := DropFields(body, field)
		if !changed {
			// The upstream named a field the request does not carry, so a retry
			// would send an identical request and get an identical answer.
			return RepairResult{Err: err, Body: body, Dropped: dropped}
		}
		body = repaired
		dropped = append(dropped, field)
	}

	return RepairResult{Err: lastErr, Body: body, Dropped: dropped}
}

// ReplayResponse rebuilds a response around an already-read body so callers can
// keep treating it as a live upstream response.
func ReplayResponse(resp *http.Response, body []byte) *http.Response {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return resp
}

// IsUpstream reports whether err wraps an *UpstreamError from the proxy package.
// Kept here as a helper so both call sites classify errors identically.
func IsUpstream(err error, target any) bool { return errors.As(err, target) }
