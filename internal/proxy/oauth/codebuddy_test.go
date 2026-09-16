package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// CodeBuddy's refresh is not OAuth2: it POSTs an empty `{}` body and carries the
// refresh token in the X-Refresh-Token header (plus X-Auth-Refresh-Source:
// plugin), mirroring the official client. Assert the wire format so a future
// refactor back to the shared StandardRefresher is caught.
func TestRefreshCodebuddy_PostsRefreshTokenHeader(t *testing.T) {
	var gotMethod, gotContentType, gotRefreshHeader, gotSource, gotUserAgent, gotDomain, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotRefreshHeader = r.Header.Get("X-Refresh-Token")
		gotSource = r.Header.Get("X-Auth-Refresh-Source")
		gotUserAgent = r.Header.Get("User-Agent")
		gotDomain = r.Header.Get("X-Domain")
		gotBody = cbReadBody(t, r)
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"NEW","refreshToken":"RT2","expiresIn":7200}}`))
	}))
	defer srv.Close()

	restore := stubCodebuddyRefreshURL(t, "codebuddy-cn", srv.URL)
	defer restore()

	res, err := RefreshCodebuddy(context.Background(), &Params{
		Provider:     "codebuddy-cn",
		RefreshToken: "OLD-RT",
		Client:       srv.Client(),
	})
	if err != nil {
		t.Fatalf("RefreshCodebuddy: %v", err)
	}
	if res.AccessToken != "NEW" {
		t.Errorf("AccessToken = %q, want NEW", res.AccessToken)
	}
	if res.RefreshToken != "RT2" {
		t.Errorf("RefreshToken = %q, want RT2", res.RefreshToken)
	}
	if res.ExpiresIn != 7200 {
		t.Errorf("ExpiresIn = %d, want 7200", res.ExpiresIn)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotRefreshHeader != "OLD-RT" {
		t.Errorf("X-Refresh-Token = %q, want OLD-RT (token must be a header, not a body field)", gotRefreshHeader)
	}
	if gotSource != "plugin" {
		t.Errorf("X-Auth-Refresh-Source = %q, want plugin", gotSource)
	}
	if strings.TrimSpace(gotBody) != "{}" {
		t.Errorf("body = %q, want empty {}", gotBody)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotUserAgent != "CLI/2.63.2 CodeBuddy/2.63.2" {
		t.Errorf("User-Agent = %q", gotUserAgent)
	}
	if gotDomain != "copilot.tencent.com" {
		t.Errorf("X-Domain = %q", gotDomain)
	}
}

// The intl region must target www.codebuddy.ai with the IDE identity.
func TestRefreshCodebuddy_IntlRegionHostAndIdentity(t *testing.T) {
	var gotDomain, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDomain = r.Header.Get("X-Domain")
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"A"}}`))
	}))
	defer srv.Close()

	restore := stubCodebuddyRefreshURL(t, "codebuddy-intl", srv.URL)
	defer restore()

	if _, err := RefreshCodebuddy(context.Background(), &Params{Provider: "codebuddy-intl", RefreshToken: "RT", Client: srv.Client()}); err != nil {
		t.Fatalf("RefreshCodebuddy(intl): %v", err)
	}
	if gotDomain != "www.codebuddy.ai" {
		t.Errorf("intl X-Domain = %q, want www.codebuddy.ai", gotDomain)
	}
	if gotUA != "IDE/2.63.2 CodeBuddy/2.63.2" {
		t.Errorf("intl UA = %q", gotUA)
	}
}

func TestRefreshCodebuddy_DefaultsExpiryWhenOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"A"}}`))
	}))
	defer srv.Close()
	restore := stubCodebuddyRefreshURL(t, "codebuddy-cn", srv.URL)
	defer restore()

	res, err := RefreshCodebuddy(context.Background(), &Params{Provider: "codebuddy-cn", RefreshToken: "RT", Client: srv.Client()})
	if err != nil {
		t.Fatalf("RefreshCodebuddy: %v", err)
	}
	if res.ExpiresIn != 86400 {
		t.Errorf("ExpiresIn = %d, want 86400 default (a 0 would force a refresh every call)", res.ExpiresIn)
	}
	// When upstream omits a new refresh token, the old one must be preserved so
	// the account does not lose its ability to refresh again.
	if res.RefreshToken != "RT" {
		t.Errorf("RefreshToken = %q, want the original RT preserved", res.RefreshToken)
	}
}

func TestRefreshCodebuddy_RequiresRefreshToken(t *testing.T) {
	if _, err := RefreshCodebuddy(context.Background(), &Params{Provider: "codebuddy-cn"}); err == nil {
		t.Fatal("expected error when refresh token is missing")
	}
}

func TestRefreshCodebuddy_ErrorsOnEmptyAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer srv.Close()
	restore := stubCodebuddyRefreshURL(t, "codebuddy-cn", srv.URL)
	defer restore()

	if _, err := RefreshCodebuddy(context.Background(), &Params{Provider: "codebuddy-cn", RefreshToken: "RT", Client: srv.Client()}); err == nil {
		t.Fatal("expected error when response has no access token")
	}
}

// Both CodeBuddy variants must resolve to a registered refresher, otherwise an
// expired account silently falls back to "no OAuth refresher".
func TestCodebuddyRefreshersRegistered(t *testing.T) {
	for _, p := range []string{"codebuddy-cn", "codebuddy-intl"} {
		if Get(p) == nil {
			t.Errorf("no refresher registered for %s", p)
		}
	}
}

// The two regions must point at different hosts — the assertion that would have
// caught the original bug where intl refreshed against copilot.tencent.com.
func TestCodebuddyRegionHostsDiffer(t *testing.T) {
	cn := codebuddyRegions["codebuddy-cn"]
	intl := codebuddyRegions["codebuddy-intl"]
	if codebuddyHostOf(cn.RefreshURL) == codebuddyHostOf(intl.RefreshURL) {
		t.Fatalf("both regions refresh against %q; intl must use www.codebuddy.ai", codebuddyHostOf(cn.RefreshURL))
	}
	if codebuddyHostOf(intl.RefreshURL) != "www.codebuddy.ai" {
		t.Errorf("intl refresh host = %q, want www.codebuddy.ai", codebuddyHostOf(intl.RefreshURL))
	}
	if codebuddyHostOf(cn.RefreshURL) != "copilot.tencent.com" {
		t.Errorf("cn refresh host = %q, want copilot.tencent.com", codebuddyHostOf(cn.RefreshURL))
	}
}

func cbReadBody(t *testing.T, r *http.Request) string {
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

// stubCodebuddyRefreshURL overrides the KnownOAuthConfigs token URL for one
// provider (RefreshCodebuddy prefers it when present) and returns a restore fn.
func stubCodebuddyRefreshURL(t *testing.T, provider, base string) func() {
	t.Helper()
	cfg, ok := providers.KnownOAuthConfigs[provider]
	if !ok {
		t.Fatalf("no KnownOAuthConfigs entry for %s", provider)
	}
	prev := cfg.TokenURL
	cfg.TokenURL = base
	providers.KnownOAuthConfigs[provider] = cfg
	return func() {
		cfg.TokenURL = prev
		providers.KnownOAuthConfigs[provider] = cfg
	}
}
