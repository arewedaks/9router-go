package oauth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// cbTestHost extracts the host from a URL for assertions.
func cbTestHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return u.Host
}

func TestCodebuddyConfigFor(t *testing.T) {
	cases := []struct {
		in           string
		wantID       string
		wantOK       bool
		wantHost     string
		wantPlatform string
		wantUA       string
	}{
		{"codebuddy-cn", "codebuddy-cn", true, "copilot.tencent.com", "CLI", "CLI/2.63.2 CodeBuddy/2.63.2"},
		{"CODEBUDDY-CN", "codebuddy-cn", true, "copilot.tencent.com", "CLI", "CLI/2.63.2 CodeBuddy/2.63.2"},
		{"cbcn", "codebuddy-cn", true, "copilot.tencent.com", "CLI", "CLI/2.63.2 CodeBuddy/2.63.2"},
		{"codebuddy-intl", "codebuddy-intl", true, "www.codebuddy.ai", "ide", "IDE/2.63.2 CodeBuddy/2.63.2"},
		{"cbai", "codebuddy-intl", true, "www.codebuddy.ai", "ide", "IDE/2.63.2 CodeBuddy/2.63.2"},
		{" codebuddy-intl ", "codebuddy-intl", true, "www.codebuddy.ai", "ide", "IDE/2.63.2 CodeBuddy/2.63.2"},
		{"workbuddy", "workbuddy", true, "www.workbuddy.ai", "CLI", "CLI/2.63.2 WorkBuddy/2.63.2"},
		{"wb", "workbuddy", true, "www.workbuddy.ai", "CLI", "CLI/2.63.2 WorkBuddy/2.63.2"},
		{" WORKBUDDY ", "workbuddy", true, "www.workbuddy.ai", "CLI", "CLI/2.63.2 WorkBuddy/2.63.2"},
		{"codebuddy", "", false, "", "", ""},
		{"antigravity", "", false, "", "", ""},
		{"", "", false, "", "", ""},
	}
	for _, c := range cases {
		cfg, ok := codebuddyConfigFor(c.in)
		if ok != c.wantOK {
			t.Errorf("codebuddyConfigFor(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if cfg.Provider != c.wantID {
			t.Errorf("codebuddyConfigFor(%q).Provider = %q, want %q", c.in, cfg.Provider, c.wantID)
		}
		// The intl region MUST NOT point at the Tencent host: a token issued by
		// one region is rejected by the other.
		if got := cbTestHost(cfg.BaseURL); got != c.wantHost {
			t.Errorf("codebuddyConfigFor(%q) host = %q, want %q", c.in, got, c.wantHost)
		}
		if cfg.Domain != c.wantHost {
			t.Errorf("codebuddyConfigFor(%q).Domain = %q, want %q", c.in, cfg.Domain, c.wantHost)
		}
		if cfg.Platform != c.wantPlatform {
			t.Errorf("codebuddyConfigFor(%q).Platform = %q, want %q", c.in, cfg.Platform, c.wantPlatform)
		}
		if cfg.UserAgent != c.wantUA {
			t.Errorf("codebuddyConfigFor(%q).UserAgent = %q, want %q", c.in, cfg.UserAgent, c.wantUA)
		}
		if ok != isCodebuddyProviderID(c.in) {
			t.Errorf("isCodebuddyProviderID(%q) disagrees with codebuddyConfigFor", c.in)
		}
	}
}

// The state endpoint reads `platform` from the query string; VansRouter sends
// `{}` as the body. Assert the query param is present, the body is empty, the
// region headers are right, and the envelope parses into (state, authUrl).
func TestRequestCodebuddyState_SendsPlatformQueryParam(t *testing.T) {
	var gotPath, gotQuery, gotBody, gotDomain, gotIDEType, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("platform")
		gotBody = readAll(t, r)
		gotDomain = r.Header.Get("X-Domain")
		gotIDEType = r.Header.Get("X-IDE-Type")
		gotUA = r.Header.Get("User-Agent")
		if r.Header.Get("X-No-Authorization") != "true" {
			t.Errorf("X-No-Authorization = %q, want true", r.Header.Get("X-No-Authorization"))
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"state":"STATE-123","authUrl":"https://example.test/approve"}}`))
	}))
	defer srv.Close()

	withCodebuddyURLs(t, srv.URL)
	cfg, _ := codebuddyConfigFor("codebuddy-cn")

	state, authURL, err := requestCodebuddyState(cfg)
	if err != nil {
		t.Fatalf("requestCodebuddyState: %v", err)
	}
	if state != "STATE-123" {
		t.Errorf("state = %q, want STATE-123", state)
	}
	if authURL != "https://example.test/approve" {
		t.Errorf("authURL = %q", authURL)
	}
	if gotQuery != "CLI" {
		t.Errorf("platform query param = %q, want CLI", gotQuery)
	}
	if gotPath != "/v2/plugin/auth/state" {
		t.Errorf("path = %q", gotPath)
	}
	if strings.TrimSpace(gotBody) != "{}" {
		t.Errorf("body = %q, want empty {} (platform goes in the query)", gotBody)
	}
	if gotDomain != "copilot.tencent.com" {
		t.Errorf("X-Domain = %q", gotDomain)
	}
	if gotIDEType != "CLI" {
		t.Errorf("X-IDE-Type = %q, want CLI", gotIDEType)
	}
	if gotUA != "CLI/2.63.2 CodeBuddy/2.63.2" {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

// The intl region must hit the .ai host with platform=ide and the IDE identity,
// not the Tencent/CLI defaults.
func TestRequestCodebuddyState_IntlRegion(t *testing.T) {
	var gotQuery, gotDomain, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("platform")
		gotDomain = r.Header.Get("X-Domain")
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"code":0,"data":{"state":"S","authUrl":"https://example.test/i"}}`))
	}))
	defer srv.Close()

	cfg, _ := codebuddyConfigFor("codebuddy-intl")
	cfg.StateURL = srv.URL + "/v2/plugin/auth/state"

	if _, _, err := requestCodebuddyState(cfg); err != nil {
		t.Fatalf("requestCodebuddyState(intl): %v", err)
	}
	if gotQuery != "ide" {
		t.Errorf("intl platform = %q, want ide", gotQuery)
	}
	if gotDomain != "www.codebuddy.ai" {
		t.Errorf("intl X-Domain = %q, want www.codebuddy.ai", gotDomain)
	}
	if gotUA != "IDE/2.63.2 CodeBuddy/2.63.2" {
		t.Errorf("intl UA = %q", gotUA)
	}
}

func TestRequestCodebuddyState_ErrorsOnNonZeroCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":40001,"msg":"platform is empty"}`))
	}))
	defer srv.Close()
	withCodebuddyURLs(t, srv.URL)
	cfg, _ := codebuddyConfigFor("codebuddy-cn")

	if _, _, err := requestCodebuddyState(cfg); err == nil {
		t.Fatal("expected error when upstream code != 0")
	} else if !strings.Contains(err.Error(), "platform is empty") {
		t.Errorf("error %q does not surface upstream msg", err)
	}
}

// code 11217 (RetryFetchToken) must be reported as *pending*, not a failure, and
// the poll must be a GET with state in the query string (not a POST body).
func TestPollCodebuddyToken_PendingAndSuccess(t *testing.T) {
	var method, stateQuery, gotDomain string
	var call int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		method = r.Method
		stateQuery = r.URL.Query().Get("state")
		gotDomain = r.Header.Get("X-Domain")
		if call == 1 {
			_, _ = w.Write([]byte(`{"code":11217,"msg":"RetryFetchToken"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"AT","refreshToken":"RT","expiresIn":3600,"email":"a@b.c"}}`))
	}))
	defer srv.Close()
	withCodebuddyURLs(t, srv.URL)
	cfg, _ := codebuddyConfigFor("codebuddy-cn")

	if _, pending, err := pollCodebuddyToken("S1", cfg); err != nil {
		t.Fatalf("first poll error: %v", err)
	} else if !pending {
		t.Fatal("code 11217 should be pending")
	}

	tokens, pending, err := pollCodebuddyToken("S1", cfg)
	if err != nil {
		t.Fatalf("second poll error: %v", err)
	}
	if pending {
		t.Fatal("code 0 with token should not be pending")
	}
	if tokens.Data.AccessToken != "AT" || tokens.Data.RefreshToken != "RT" {
		t.Errorf("tokens = %+v", tokens.Data)
	}
	if method != http.MethodGet {
		t.Errorf("poll method = %q, want GET", method)
	}
	if stateQuery != "S1" {
		t.Errorf("state query = %q, want S1", stateQuery)
	}
	if gotDomain != "copilot.tencent.com" {
		t.Errorf("X-Domain = %q", gotDomain)
	}
}

func TestPollCodebuddyToken_HardErrorOnUnknownCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":50000,"msg":"boom"}`))
	}))
	defer srv.Close()
	withCodebuddyURLs(t, srv.URL)
	cfg, _ := codebuddyConfigFor("codebuddy-cn")

	if _, _, err := pollCodebuddyToken("S", cfg); err == nil {
		t.Fatal("expected hard error for unknown code")
	}
}

func TestTruncateForOAuth(t *testing.T) {
	if got := truncateForOAuth("  hello  ", 10); got != "hello" {
		t.Errorf("trim = %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := truncateForOAuth(long, 100); len([]rune(got)) != 101 {
		t.Errorf("truncated rune length = %d, want 101", len([]rune(got)))
	}
	// Multi-byte safety: must not split a rune.
	id := "héllo wörld"
	if got := truncateForOAuth(id, 3); !strings.HasSuffix(got, "…") {
		t.Errorf("multi-byte truncate = %q", got)
	}
}

// --- helpers ---

func readAll(t *testing.T, r *http.Request) string {
	t.Helper()
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 256)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return string(buf)
}

// withCodebuddyURLs points the CN region endpoints at a test server for the
// duration of the test. codebuddyConfigFor reads the package vars, so tests
// that resolve a config after calling this see the stubbed URLs.
func withCodebuddyURLs(t *testing.T, base string) {
	t.Helper()
	origState, origToken := codebuddyCNStateURL, codebuddyCNTokenURL
	codebuddyCNStateURL = base + "/v2/plugin/auth/state"
	codebuddyCNTokenURL = base + "/v2/plugin/auth/token"
	t.Cleanup(func() {
		codebuddyCNStateURL = origState
		codebuddyCNTokenURL = origToken
	})
}
