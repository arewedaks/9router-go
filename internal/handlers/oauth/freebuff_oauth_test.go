package oauth

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withFreebuffBase points the handler at a test server for the duration of the
// test. Mirrors withCodebuddyURLs.
func withFreebuffBase(t *testing.T, base string) {
	t.Helper()
	prev := freebuffLoginBase
	freebuffLoginBase = base
	t.Cleanup(func() { freebuffLoginBase = prev })
}

// The login-code call must POST the fingerprint to the login host and surface
// the returned loginUrl + fingerprint hash unchanged.
func TestHandleFreebuffAuthorize_ReturnsLoginURL(t *testing.T) {
	var gotPath, gotMethod, gotBody, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotUA = r.Header.Get("User-Agent")
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		_, _ = w.Write([]byte(`{"fingerprintId":"FP1","fingerprintHash":"HASH1","loginUrl":"https://freebuff.com/login?auth_code=ABC","expiresAt":0}`))
	}))
	defer srv.Close()
	withFreebuffBase(t, srv.URL)

	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleFreebuffAuthorize(rec, httptest.NewRequest("GET", "/api/oauth/freebuff/authorize", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotPath != freebuffLoginCodePath {
		t.Errorf("path = %q, want %q", gotPath, freebuffLoginCodePath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotUA != freebuffCLIVersion {
		t.Errorf("User-Agent = %q, want %q", gotUA, freebuffCLIVersion)
	}
	if !strings.Contains(gotBody, "fingerprintId") {
		t.Errorf("body %q does not carry a fingerprintId", gotBody)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["authUrl"] != "https://freebuff.com/login?auth_code=ABC" {
		t.Errorf("authUrl = %v", resp["authUrl"])
	}
	if resp["fingerprintHash"] != "HASH1" {
		t.Errorf("fingerprintHash = %v", resp["fingerprintHash"])
	}
	// A zero server expiresAt must be clamped to a usable deadline, never left
	// at 0 (which would make the dashboard poll a stale flow forever).
	if exp, _ := resp["expiresAt"].(float64); exp <= 0 {
		t.Errorf("expiresAt = %v, want a positive clamped deadline", resp["expiresAt"])
	}
}

// A status response without a user token means "still waiting" and must NOT be
// treated as an error — the dashboard polls on this.
func TestHandleFreebuffExchange_PendingThenSuccess(t *testing.T) {
	var call int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Authentication failed"}`))
			return
		}
		_, _ = w.Write([]byte(`{"user":{"id":"U1","email":"a@b.c","name":"Ada","authToken":"AT1","fingerprintId":"FP1"}}`))
	}))
	defer srv.Close()
	withFreebuffBase(t, srv.URL)

	h := &OAuthHandler{Repo: newDuplicateTestRepo(t)}

	poll := func() (int, map[string]any) {
		rec := httptest.NewRecorder()
		body := `{"fingerprintId":"FP1","fingerprintHash":"HASH1","expiresAt":123}`
		req := httptest.NewRequest("POST", "/api/oauth/freebuff/exchange", strings.NewReader(body))
		h.HandleFreebuffExchange(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, out := poll()
	if code != http.StatusOK {
		t.Fatalf("pending poll status = %d, want 200", code)
	}
	if out["pending"] != true {
		t.Errorf("pending poll = %v, want pending:true", out)
	}

	code, out = poll()
	if code != http.StatusOK {
		t.Fatalf("success poll status = %d, want 200: %v", code, out)
	}
	if out["success"] != true {
		t.Fatalf("success poll = %v, want success:true", out)
	}
	conn, _ := out["connection"].(map[string]any)
	if conn == nil || conn["provider"] != "freebuff" {
		t.Errorf("connection = %v, want provider freebuff", out["connection"])
	}
}

// A poll missing the fingerprint pair cannot be answered by upstream, so it must
// fail fast with 400 rather than issuing a doomed request.
func TestHandleFreebuffExchange_MissingFingerprint(t *testing.T) {
	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/oauth/freebuff/exchange", strings.NewReader(`{}`))
	h.HandleFreebuffExchange(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
