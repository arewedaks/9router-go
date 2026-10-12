package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The toggle must survive a reload. Model ids contain "/" and ":"
// ("tkh/mimo-v2.6-flash:free"), and an id passed through the URL path came
// back still-encoded — the write answered 200 under a key that the read path
// never matched, so the switch looked on and then reverted. The id travels in
// the body now; this pins that it lands verbatim.
func TestJailbreakToggleStoresModelIDVerbatim(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	const modelID = "tkh/mimo-v2.6-flash:free"
	payload, _ := json.Marshal(map[string]any{"id": modelID, "enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// Exactly the id the request asked for, no percent-escapes anywhere.
	on, err := repo.JailbreakModelEnabled(modelID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !on {
		t.Fatal("toggle did not persist under the id the client sent")
	}
	list, err := repo.ListJailbreakModels()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !list[modelID] {
		t.Fatalf("list = %v, want the verbatim id", list)
	}
	for id := range list {
		if id != modelID {
			t.Fatalf("stored key %q is not the verbatim id", id)
		}
	}

	// And off removes it again, so the next reload reads off.
	payload, _ = json.Marshal(map[string]any{"id": modelID, "enabled": false})
	req = httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT off status = %d, want 200", rec.Code)
	}
	on, _ = repo.JailbreakModelEnabled(modelID)
	if on {
		t.Fatal("toggle stayed on after being switched off")
	}
}

// An empty id must be refused rather than writing a key nobody can match.
func TestJailbreakToggleRejectsEmptyID(t *testing.T) {
	_, _, r := setupTestDashboard(t)
	payload, _ := json.Marshal(map[string]any{"id": "", "enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
