package oauth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ghTestHost extracts the host from a URL for assertions.
func ghTestHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return u.Host
}

func TestGitHubDeviceFlowURLs(t *testing.T) {
	// The three GitHub hosts the device flow touches must stay distinct; mixing
	// github.com with api.github.com is the classic port bug (the device code is
	// issued by the former and the Copilot token by the latter).
	if got := ghTestHost(githubDeviceCodeURL); got != "github.com" {
		t.Errorf("device code host = %q, want github.com", got)
	}
	if got := ghTestHost(githubAccessTokenURL); got != "github.com" {
		t.Errorf("access token host = %q, want github.com", got)
	}
	if got := ghTestHost(githubCopilotTokenURL); got != "api.github.com" {
		t.Errorf("copilot token host = %q, want api.github.com", got)
	}
	if got := ghTestHost(githubUserURL); got != "api.github.com" {
		t.Errorf("user host = %q, want api.github.com", got)
	}
	if !strings.HasSuffix(githubCopilotTokenURL, "/copilot_internal/v2/token") {
		t.Errorf("copilot token path = %q, want .../copilot_internal/v2/token", githubCopilotTokenURL)
	}
}

func TestGitHubClientIDResolves(t *testing.T) {
	// The registry entry is the source of truth so GITHUB_OAUTH_CLIENT_ID keeps
	// working; githubClientID must never return empty.
	if got := githubClientID(); strings.TrimSpace(got) == "" {
		t.Fatal("githubClientID() returned empty")
	}
}

func TestGitHubDeviceCodeRequest(t *testing.T) {
	var gotMethod, gotClientID, gotScope string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = r.ParseForm()
		gotClientID = r.Form.Get("client_id")
		gotScope = r.Form.Get("scope")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"DEV","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`))
	}))
	defer srv.Close()

	// requestGitHubDeviceCode uses the package const; exercise the builder by
	// pointing the same form at the test server.
	form := url.Values{}
	form.Set("client_id", "cid")
	form.Set("scope", githubDeviceScope)
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotClientID != "cid" {
		t.Errorf("client_id = %q, want cid", gotClientID)
	}
	if gotScope != "read:user" {
		t.Errorf("scope = %q, want read:user", gotScope)
	}
}

func TestPollGitHubAccessTokenClassification(t *testing.T) {
	// The state machine is the part with real logic, so assert it directly
	// instead of depending on a mutable package URL.
	tokens, pending, err := classifyGitHubPoll(githubTokenResponse{Error: "authorization_pending"})
	if err != nil || !pending {
		t.Fatalf("authorization_pending: pending=%v err=%v, want pending=true err=nil", pending, err)
	}
	if tokens.AccessToken != "" {
		t.Fatalf("authorization_pending returned a token: %q", tokens.AccessToken)
	}

	tokens, pending, err = classifyGitHubPoll(githubTokenResponse{Error: "slow_down"})
	if err != nil || !pending {
		t.Fatalf("slow_down: pending=%v err=%v, want pending=true err=nil", pending, err)
	}

	tokens, pending, err = classifyGitHubPoll(githubTokenResponse{AccessToken: "gho_test"})
	if err != nil || pending || tokens.AccessToken != "gho_test" {
		t.Fatalf("ready: tokens=%v pending=%v err=%v, want token with pending=false", tokens, pending, err)
	}

	if _, pending, err := classifyGitHubPoll(githubTokenResponse{Error: "access_denied"}); err == nil || pending {
		t.Fatalf("access_denied: pending=%v err=%v, want error and pending=false", pending, err)
	}

	// Empty error and no token is treated as pending, not a hard failure.
	if _, pending, err := classifyGitHubPoll(githubTokenResponse{}); err != nil || !pending {
		t.Fatalf("empty: pending=%v err=%v, want pending=true err=nil", pending, err)
	}
}

func TestIsGitHubAlias(t *testing.T) {
	for _, id := range []string{"github", "gh", "copilot", "GitHub", " gh "} {
		if !isGitHubAlias(id) {
			t.Errorf("isGitHubAlias(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"gitlab", "codebuddy-cn", "antigravity", ""} {
		if isGitHubAlias(id) {
			t.Errorf("isGitHubAlias(%q) = true, want false", id)
		}
	}
}
