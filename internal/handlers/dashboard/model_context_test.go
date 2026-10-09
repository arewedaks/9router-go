package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A model the catalog has never listed must report no context window. The
// resolution path behind GetModelTokenLimitsFor has a name-pattern fallback
// that maps every unrecognised id to a single default (128K); that is fine for
// routing, where a rough cap beats none, but it must not reach the UI — a
// "128K" label on a custom model is a fabricated limit. Zero renders no label,
// which is the honest answer.
func TestUnknownModelReportsNoContextWindow(t *testing.T) {
	_, repo, r := setupTestDashboard(t)
	if err := repo.AddCachedModel("antigravity", "totally-made-up-model-xyz", "llm", ""); err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/dashboard/auth/login",
		strings.NewReader(`{"password":"123456"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.RemoteAddr = "127.0.0.1:1234"
	r.ServeHTTP(login, loginReq)
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", login.Code, login.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/providers/antigravity", nil)
	for _, c := range login.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("provider detail = %d: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Models []struct {
			ContextWindow int `json:"contextWindow"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) == 0 {
		t.Fatal("expected the cached model to be listed")
	}
	if got.Models[0].ContextWindow != 0 {
		t.Errorf("unknown model must report no context window, got %d", got.Models[0].ContextWindow)
	}
}
