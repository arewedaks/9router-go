package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The cap must be large enough for the requests this proxy serves. A vision or
// agent harness sends screenshots inline as base64, and a single PNG can be
// several megabytes, so the old 10MB ceiling refused realistic multi-image
// turns — and surfaced as a bare 400 before the cause was logged.
func TestMaxBodySizeHoldsAMultiImageTurn(t *testing.T) {
	if DefaultMaxBodySize < 32*1024*1024 {
		t.Fatalf("DefaultMaxBodySize = %d bytes, too small for inline images", DefaultMaxBodySize)
	}
}

// A body under the cap must pass through untouched, so the limit protects against
// oversized requests without altering normal ones.
func TestMaxBodyAllowsBodyUnderLimit(t *testing.T) {
	const payload = `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	var got string
	h := MaxBody(DefaultMaxBodySize)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(payload)))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got != payload {
		t.Errorf("body = %q, want %q", got, payload)
	}
}

// A body over the cap must be refused, and the error must be recognisable so the
// handler can answer 413 instead of a misleading 400.
func TestMaxBodyRefusesBodyOverLimit(t *testing.T) {
	h := MaxBody(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		var maxErr *http.MaxBytesError
		if !errors.As(err, &maxErr) {
			t.Errorf("expected *http.MaxBytesError, got %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(strings.Repeat("x", 2048))))
}
