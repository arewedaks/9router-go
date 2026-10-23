package executor

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

// Perch refuses a hosted model call unless the request carries a turn ticket,
// answering 403 perch_surface_required. Measured 2026-10-23: the body alone is
// not enough — stripping attribution, clientSurface, lane, runId and resend all
// still answered 403, while minting a ticket and echoing it back (with the
// ticket's runId in the body) answered 200. This test pins the handshake so a
// later refactor cannot drop the header again.
func TestPerchForwardSendsTurnTicket(t *testing.T) {
	var (
		gotTicket string
		gotRunID  string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/perch-terminal/turn-ticket", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("ticket endpoint must be POST, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer tok" {
			t.Errorf("ticket mint must carry the bearer token, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"ticket":"TICKET-1","runId":"tkt-cli-42-deadbeef","surface":"cli","enforced":false}`))
	})
	mux.HandleFunc("/api/perch-terminal/model-call", func(w http.ResponseWriter, r *http.Request) {
		gotTicket = r.Header.Get(perchTurnTicketHdr)
		body, _ := io.ReadAll(r.Body)
		var parsed struct {
			RunID string `json:"runId"`
		}
		_ = json.Unmarshal(body, &parsed)
		gotRunID = parsed.RunID
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"text":"hi","finishReason":"stop"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := providers.KnownProviders["perch"]
	cfg.BaseURL = srv.URL + "/api/perch-terminal/model-call"

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: srv.Client(),
		Config: &cfg,
		APIKey: "tok",
		Body:   []byte(`{"model":"deepseek-flash-starter","messages":[{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardPerch(rec, req); err != nil {
		t.Fatalf("ForwardPerch: %v", err)
	}

	if gotTicket != "TICKET-1" {
		t.Errorf("model call must echo the minted ticket, got %q", gotTicket)
	}
	if gotRunID != "tkt-cli-42-deadbeef" {
		t.Errorf("body runId must match the ticket's runId, got %q", gotRunID)
	}
}

// A dead ticket endpoint must not fail the call locally: the request still goes
// out so the upstream error names the real problem.
func TestPerchForwardSurvivesTicketFailure(t *testing.T) {
	var sawHeader bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/perch-terminal/turn-ticket", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/api/perch-terminal/model-call", func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get(perchTurnTicketHdr) != ""
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"text":"hi","finishReason":"stop"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := providers.KnownProviders["perch"]
	cfg.BaseURL = srv.URL + "/api/perch-terminal/model-call"

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: srv.Client(),
		Config: &cfg,
		APIKey: "tok",
		Body:   []byte(`{"model":"deepseek-flash-starter","messages":[{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardPerch(rec, req); err != nil {
		t.Fatalf("a failed ticket mint must not abort the call: %v", err)
	}
	if sawHeader {
		t.Error("no ticket header may be sent when none was minted")
	}
}
