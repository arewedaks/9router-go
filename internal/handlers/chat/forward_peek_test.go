package chat

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPeekBareErrorEvent_PromotesFreeUsage(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached."}}`))
	ue, buffered := peekBareErrorEvent(body)
	if ue == nil {
		t.Fatal("expected upstream error, got nil")
	}
	if buffered != nil {
		t.Error("error path must not return a reader")
	}
	var upErr *upstreamError
	if !errors.As(ue, &upErr) {
		t.Fatalf("want upstreamError, got %T", ue)
	}
	if upErr.StatusCode != 429 {
		t.Errorf("status = %d, want 429", upErr.StatusCode)
	}
}
