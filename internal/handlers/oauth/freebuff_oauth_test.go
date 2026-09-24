package oauth

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	if gotUA != freebuffUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, freebuffUserAgent)
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

// TestFreebuffHandshakeTargetsCodebuffHost guards the login host.
//
// freebuff.com is a separate consumer product: it answers the cli/code RPC with
// 200 and a real loginUrl, so a flow pointed there looks healthy end to end, but
// its OAuth application differs from the CLI's and it has no /api/v1 — the
// auth_code therefore belongs to an account on the other service and this host's
// status RPC never binds it, leaving the dashboard polling until it times out.
// The released CLI (codebuff 1.0.688) uses https://www.codebuff.com.
func TestFreebuffHandshakeTargetsCodebuffHost(t *testing.T) {
	if freebuffLoginHost != "https://www.codebuff.com" {
		t.Errorf("login host = %q, want https://www.codebuff.com", freebuffLoginHost)
	}
	if strings.Contains(freebuffLoginHost, "//freebuff.com") {
		t.Error("freebuff.com cannot bind a CLI auth_code; it is a different service")
	}

	const serverExpiry = int64(1790264896378)
	var gotFingerprint string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		var body struct {
			FingerprintID string `json:"fingerprintId"`
		}
		_ = json.Unmarshal(buf[:n], &body)
		gotFingerprint = body.FingerprintID
		_, _ = w.Write([]byte(`{"fingerprintId":"FP1","fingerprintHash":"HASH1","loginUrl":"https://www.codebuff.com/login?auth_code=ABC","expiresAt":1790264896378}`))
	}))
	defer srv.Close()
	withFreebuffBase(t, srv.URL)

	h := NewOAuthHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleFreebuffAuthorize(rec, httptest.NewRequest("GET", "/api/oauth/freebuff/authorize", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// The fingerprint must carry the shape the released client sends, since the
	// backend's acceptance of a bare random string is undocumented leniency.
	if !strings.HasPrefix(gotFingerprint, "codebuff-cli-") {
		t.Errorf("fingerprintId = %q, want the codebuff-cli- prefix", gotFingerprint)
	}

	var resp struct {
		ExpiresAt     int64 `json:"expiresAt"`
		PollTimeoutMs int64 `json:"pollTimeoutMs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The server's expiry must be forwarded, not replaced by the poll deadline:
	// the status RPC is asked about the code the server issued.
	if resp.ExpiresAt != serverExpiry {
		t.Errorf("expiresAt = %d, want the server value %d", resp.ExpiresAt, serverExpiry)
	}
	// The poll bound is reported separately so the dashboard can stop early
	// instead of hammering the status endpoint for an hour.
	if resp.PollTimeoutMs != int64(freebuffLoginTimeout/time.Millisecond) {
		t.Errorf("pollTimeoutMs = %d, want %d", resp.PollTimeoutMs, freebuffLoginTimeout/time.Millisecond)
	}
}
