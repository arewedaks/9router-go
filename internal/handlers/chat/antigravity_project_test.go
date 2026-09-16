package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestFetchAntigravityProjectID_outcomes pins the classification that drives the
// refresh/no-cache decisions: pid found, token rejected, project definitively
// missing vs transient rate limit.
func TestFetchAntigravityProjectID_outcomes(t *testing.T) {
	tests := []struct {
		name        string
		loadAssist  int    // status code for loadCodeAssist
		loadBody    string // body for loadCodeAssist
		onboard     int    // status code for onboardUser
		onboardBody string // body for onboardUser
		wantPID     string
		wantAuth    bool
		wantNoProj  bool
	}{
		{
			name:       "project found",
			loadAssist: 200,
			loadBody:   `{"cloudaicompanionProject":{"id":"proj-123","name":"x"}}`,
			wantPID:    "proj-123",
		},
		{
			name:       "token rejected 401",
			loadAssist: 401,
			wantAuth:   true,
		},
		{
			name:       "token rejected 403",
			loadAssist: 403,
			wantAuth:   true,
		},
		{
			name:        "empty project confirmed",
			loadAssist:  200,
			loadBody:    `{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`,
			onboard:     200,
			onboardBody: `{"done":true,"response":{"cloudaicompanionProject":{}}}`,
			wantNoProj:  true,
		},
		{
			// loadCodeAssist already said "no project for this token" (200, tiers
			// only) — an onboardUser 429 afterwards doesn't change that verdict,
			// so it's still cached as no-project.
			name:       "onboard rate-limited after clean load",
			loadAssist: 200,
			loadBody:   `{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`,
			onboard:    429,
			wantNoProj: true,
			wantAuth:   false,
		},
		{
			name:       "concurrent connection refused is transient",
			loadAssist: 503,
			wantNoProj: false,
		},
	}

	oldDelay := antigravityProbeDelay
	antigravityProbeDelay = time.Millisecond
	defer func() { antigravityProbeDelay = oldDelay }()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "loadCodeAssist") {
					w.WriteHeader(tc.loadAssist)
					w.Write([]byte(tc.loadBody))
					return
				}
				w.WriteHeader(tc.onboard)
				w.Write([]byte(tc.onboardBody))
			}))
			defer srv.Close()

			oldL, oldO := loadCodeAssistURL, onboardUserURL
			loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
			defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

			pid, auth, noProj := fetchAntigravityProjectID(context.Background(), srv.Client(), "test-token", "")
			if pid != tc.wantPID {
				t.Errorf("pid = %q, want %q", pid, tc.wantPID)
			}
			if auth != tc.wantAuth {
				t.Errorf("authFailed = %v, want %v", auth, tc.wantAuth)
			}
			if noProj != tc.wantNoProj {
				t.Errorf("noProject = %v, want %v", noProj, tc.wantNoProj)
			}
		})
	}
}

func TestProjectNoCache(t *testing.T) {
	projectNoCache.Delete("test-conn")
	if projectProbeCached("test-conn") {
		t.Fatal("fresh cache should not report cached")
	}
	cacheProjectMissing("test-conn")
	if !projectProbeCached("test-conn") {
		t.Fatal("should be cached after cacheProjectMissing")
	}
	projectNoCache.Store("test-conn", int64(time.Now().Add(-time.Second).Unix()))
	if projectProbeCached("test-conn") {
		t.Fatal("expired cache entry should not report cached")
	}
}

// TestDiscoveryUserAgent pins the regression that broke account onboarding:
// Google omits cloudaicompanionProject when the discovery RPCs identify as the
// generic API client instead of a real Antigravity client. Callers that know the
// connection profile must be able to override the UA, and the default must stay
// the legacy value so non-Antigravity callers are unaffected.
func TestDiscoveryUserAgent(t *testing.T) {
	if got := discoveryUserAgent(""); got != antigravityDiscoveryUserAgent {
		t.Errorf("empty override = %q, want legacy %q", got, antigravityDiscoveryUserAgent)
	}
	if got := discoveryUserAgent("   "); got != antigravityDiscoveryUserAgent {
		t.Errorf("whitespace override = %q, want legacy %q", got, antigravityDiscoveryUserAgent)
	}
	const cli = "antigravity/cli/1.1.5 (aidev_client; os_type=darwin; arch=arm64; auth_method=consumer)"
	if got := discoveryUserAgent(cli); got != cli {
		t.Errorf("cli override = %q, want %q", got, cli)
	}
}

// TestFetchAntigravityProjectID_SendsUserAgent asserts the connection's UA (IDE
// or CLI) is actually put on the wire for both onboarding RPCs.
func TestFetchAntigravityProjectID_SendsUserAgent(t *testing.T) {
	const wantUA = "antigravity/ide/2.11.0 darwin/arm64"

	var gotLCA, gotOnboard string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "loadCodeAssist") {
			gotLCA = r.Header.Get("User-Agent")
			// Force the onboarding path by returning tiers without a project.
			w.WriteHeader(200)
			w.Write([]byte(`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`))
			return
		}
		gotOnboard = r.Header.Get("User-Agent")
		w.WriteHeader(200)
		w.Write([]byte(`{"done":true,"response":{"cloudaicompanionProject":{"id":"proj-ua"}}}`))
	}))
	defer srv.Close()

	oldL, oldO := loadCodeAssistURL, onboardUserURL
	loadCodeAssistURL, onboardUserURL = srv.URL+"/loadCodeAssist", srv.URL+"/onboardUser"
	defer func() { loadCodeAssistURL, onboardUserURL = oldL, oldO }()

	oldDelay := antigravityProbeDelay
	antigravityProbeDelay = time.Millisecond
	defer func() { antigravityProbeDelay = oldDelay }()

	pid, _, _ := fetchAntigravityProjectID(context.Background(), srv.Client(), "tok", wantUA)
	if pid != "proj-ua" {
		t.Fatalf("pid = %q, want proj-ua", pid)
	}
	if gotLCA != wantUA {
		t.Errorf("loadCodeAssist User-Agent = %q, want %q", gotLCA, wantUA)
	}
	if gotOnboard != wantUA {
		t.Errorf("onboardUser User-Agent = %q, want %q", gotOnboard, wantUA)
	}
}
