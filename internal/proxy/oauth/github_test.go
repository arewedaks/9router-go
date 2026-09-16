package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRefreshGitHubUsesTokenSchemeAndDerivesCopilotToken(t *testing.T) {
	var gotAuth, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		exp := time.Now().Add(30 * time.Minute).Unix()
		_, _ = w.Write([]byte(`{"token":"copilot_abc","expires_at":` + itoaTest(exp) + `}`))
	}))
	defer srv.Close()

	// The refresher targets the const URL; swap it for the test server via the
	// package-level indirection so no real network call happens.
	old := githubCopilotTokenURLForTest
	githubCopilotTokenURLForTest = srv.URL
	defer func() { githubCopilotTokenURLForTest = old }()

	res, err := RefreshGitHub(context.Background(), &Params{
		AccessToken: "gho_github_token",
		Client:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("RefreshGitHub: %v", err)
	}
	if res.AccessToken != "gho_github_token" {
		t.Errorf("AccessToken = %q, want the GitHub token preserved (overwriting it with the Copilot token would break the next refresh)", res.AccessToken)
	}
	if res.RefreshToken != "gho_github_token" {
		t.Errorf("RefreshToken = %q, want the GitHub token preserved", res.RefreshToken)
	}
	// The derived Copilot bearer must travel under providerSpecificData so the
	// catalogue/chat readers that prefer it over accessToken pick up the new one.
	ct, _ := res.ProviderSpecificData["copilotToken"].(string)
	if ct != "copilot_abc" {
		t.Errorf("copilotToken = %q, want copilot_abc", ct)
	}
	// The scheme must be `token`, not `Bearer` — the internal endpoint rejects
	// Bearer and that asymmetry is the reason this is a custom refresher.
	if !strings.HasPrefix(gotAuth, "token ") {
		t.Errorf("Authorization = %q, want `token ...`", gotAuth)
	}
	if gotUA != "GithubCopilot/1.0" {
		t.Errorf("User-Agent = %q, want GithubCopilot/1.0", gotUA)
	}
	// ~30 minutes remaining should round into a positive expires_in.
	if res.ExpiresIn <= 0 || res.ExpiresIn > 31*60 {
		t.Errorf("ExpiresIn = %d, want a positive value near 1800", res.ExpiresIn)
	}
}

func TestRefreshGitHubRequiresGitHubToken(t *testing.T) {
	_, err := RefreshGitHub(context.Background(), &Params{})
	if err == nil {
		t.Fatal("expected an error when no GitHub access token is available")
	}
	if !strings.Contains(err.Error(), "no GitHub access token") {
		t.Errorf("error = %v, want a 'no GitHub access token' message", err)
	}
}

func TestRefreshGitHubAcceptsRefreshTokenFallback(t *testing.T) {
	// Connections may store the long-lived GitHub token under either field;
	// the refresher must accept both so a restored connection still works.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"copilot_from_refresh"}`))
	}))
	defer srv.Close()

	old := githubCopilotTokenURLForTest
	githubCopilotTokenURLForTest = srv.URL
	defer func() { githubCopilotTokenURLForTest = old }()

	res, err := RefreshGitHub(context.Background(), &Params{RefreshToken: "gho_via_refresh", Client: srv.Client()})
	if err != nil {
		t.Fatalf("RefreshGitHub: %v", err)
	}
	if res.AccessToken != "gho_via_refresh" {
		t.Errorf("AccessToken = %q, want the GitHub token preserved", res.AccessToken)
	}
	if ct, _ := res.ProviderSpecificData["copilotToken"].(string); ct != "copilot_from_refresh" {
		t.Errorf("copilotToken = %q, want copilot_from_refresh", ct)
	}
}

func TestRefreshGitHubHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	old := githubCopilotTokenURLForTest
	githubCopilotTokenURLForTest = srv.URL
	defer func() { githubCopilotTokenURLForTest = old }()

	if _, err := RefreshGitHub(context.Background(), &Params{AccessToken: "bad", Client: srv.Client()}); err == nil {
		t.Fatal("expected an error on HTTP 401")
	}
}

func itoaTest(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
