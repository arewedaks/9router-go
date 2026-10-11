package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An error the caller cannot act on is worse than no message: it sends whoever
// reads it hunting for a cause the message already knew and withheld. That is
// exactly how a body over the size cap presented as a bare 400 and cost hours.
// These tests pin the reason into the response.

func postChat(t *testing.T, h *ChatHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.HandleChatCompletions(rec, req)
	return rec
}

// The decoder knows the offset and the offending field; the caller must get both.
func TestInvalidJSONReportsTheCause(t *testing.T) {
	rec := postChat(t, &ChatHandler{}, `{"model":"m","bad":}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	got := rec.Body.String()
	if !strings.Contains(got, "invalid JSON body:") {
		t.Errorf("the response must name the cause, got %s", got)
	}
	// The decoder's detail is the actionable part: it says where parsing stopped.
	if !strings.Contains(got, "offset") && !strings.Contains(got, "bad") {
		t.Errorf("the response must carry the decoder's detail, got %s", got)
	}
}

// "missing model" alone leaves the caller guessing whether the field was absent,
// misspelled, or empty. The message has to say what to do.
func TestMissingModelSaysWhatIsMissing(t *testing.T) {
	rec := postChat(t, &ChatHandler{}, `{"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, "model") || !strings.Contains(got, "name") {
		t.Errorf("the response must tell the caller a model is required, got %s", got)
	}
}

// A different failure must produce a different message, not the same catch-all.
// Shared wording is what made the size cap look like a malformed request.
func TestDistinctFailuresProduceDistinctMessages(t *testing.T) {
	badJSON := postChat(t, &ChatHandler{}, `{`).Body.String()
	noModel := postChat(t, &ChatHandler{}, `{"messages":[]}`).Body.String()
	if badJSON == noModel {
		t.Error("a malformed body and a missing model must not report the same thing")
	}
}
