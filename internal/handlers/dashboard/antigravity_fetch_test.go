package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// liveFixture is a trimmed copy of a real /v1internal:fetchAvailableModels
// response captured from a working Antigravity account. It deliberately mixes
// callable chat models with non-chat and internal entries the importer must
// drop.
const liveFixture = `{
  "models": {
    "gemini-3.8-flash-tiered": {"displayName": "Gemini 3.8 Flash (Tiered)"},
    "gemini-3.8-flash": {"displayName": "Gemini 3.8 Flash"},
    "gemini-3.7-flash-tiered": {"displayName": "Gemini 3.7 Flash (Tiered)"},
    "gemini-3.1-flash-lite": {"displayName": "Gemini 3.1 Flash Lite"},
    "claude-sonnet-4-6": {"displayName": "Claude Sonnet 4.6 (Thinking)"},
    "claude-opus-4-6-thinking": {"displayName": "Claude Opus 4.6 (Thinking)"},
    "gemini-pro-agent": {"displayName": "Gemini 3.1 Pro (High)"},
    "gemini-3.1-pro-low": {"displayName": "Gemini 3.1 Pro (Low)"},
    "gpt-oss-120b-medium": {"displayName": "GPT-OSS 120B (Medium)"},
    "gemini-3.1-flash-image": {"displayName": "Gemini 3.1 Flash Image"},
    "tab_flash_lite_preview": {"displayName": ""},
    "tab_jump_flash_lite_preview": {"displayName": ""},
    "chat_20706": {"displayName": ""},
    "chat_23310": {"displayName": ""},
    "gemini-2.5-flash": {"displayName": "Gemini 3.5 Flash Lite"},
    "gemini-3.6-flash-high": {"displayName": "Gemini 3.6 Flash (High)"},
    "gemini-3.1-flash-tts-preview": {"displayName": "TTS"}
  }
}`

// TestIsDiscoverableAntigravityModel covers the allow/deny matrix.
func TestIsDiscoverableAntigravityModel(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"gemini-3.8-flash", true},
		{"gemini-3.8-flash-tiered", true},
		{"claude-sonnet-4-6", true},
		{"claude-opus-4-6-thinking", true},
		{"gemini-pro-agent", true},
		{"gpt-oss-120b-medium", true},
		// Non-chat surfaces.
		{"gemini-3.1-flash-image", false},
		{"gemini-3-pro-image-preview", false},
		{"gemini-3.1-flash-tts-preview", false},
		{"text-embedding-004", false},
		{"veo-3", false},
		{"some-video-model", false},
		// Internal slots.
		{"chat_20706", false},
		{"tab_flash_lite_preview", false},
		// Retired.
		{"gemini-2.5-flash", false},
		{"gemini-2.5-pro", false},
		{"gemini-3.6-flash-high", false},
		{"gemini-3.5-flash-low", false},
		// Empty.
		{"", false},
	}
	for _, c := range cases {
		if got := providers.IsDiscoverableAntigravityModel(c.id); got != c.want {
			t.Errorf("providers.IsDiscoverableAntigravityModel(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// TestNormalizeAntigravityModels_FiltersLiveFixture verifies the live payload
// yields only the user-callable chat models.
func TestNormalizeAntigravityModels_FiltersLiveFixture(t *testing.T) {
	models := normalizeAntigravityModels([]byte(liveFixture))
	got := map[string]bool{}
	for _, m := range models {
		got[m.ID] = true
	}

	wantPresent := []string{
		"gemini-3.8-flash-tiered", "gemini-3.8-flash", "gemini-3.7-flash-tiered",
		"gemini-3.1-flash-lite", "claude-sonnet-4-6", "claude-opus-4-6-thinking",
		"gemini-pro-agent", "gemini-3.1-pro-low", "gpt-oss-120b-medium",
	}
	for _, id := range wantPresent {
		if !got[id] {
			t.Errorf("expected %q in normalised models, missing", id)
		}
	}

	wantAbsent := []string{
		"gemini-3.1-flash-image", "tab_flash_lite_preview", "tab_jump_flash_lite_preview",
		"chat_20706", "chat_23310", "gemini-2.5-flash", "gemini-3.6-flash-high",
		"gemini-3.1-flash-tts-preview",
	}
	for _, id := range wantAbsent {
		if got[id] {
			t.Errorf("did not expect %q in normalised models", id)
		}
	}

	// Results must be sorted by id and populated with display labels.
	for i := 1; i < len(models); i++ {
		if models[i].ID < models[i-1].ID {
			t.Fatalf("models not sorted: %q before %q", models[i-1].ID, models[i].ID)
		}
	}
	found := false
	for _, m := range models {
		if m.ID == "claude-sonnet-4-6" {
			found = true
			if m.Name != "Claude Sonnet 4.6 (Thinking)" {
				t.Errorf("unexpected label for claude-sonnet-4-6: %q", m.Name)
			}
		}
	}
	if !found {
		t.Error("claude-sonnet-4-6 missing from normalised output")
	}
}

// TestNormalizeAntigravityModels_ArrayShape verifies the alternate array
// envelope is tolerated.
func TestNormalizeAntigravityModels_ArrayShape(t *testing.T) {
	fixture := `{"models":[{"id":"gemini-3.8-flash","displayName":"Gemini 3.8 Flash"},
	                        {"name":"claude-sonnet-4-6","displayName":"Claude Sonnet 4.6"},
	                        {"id":"tab_x","displayName":"x"},
	                        {"id":"gemini-3.1-flash-image","displayName":"img"}]}`
	models := normalizeAntigravityModels([]byte(fixture))
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(models), models)
	}
}

// TestNormalizeAntigravityModels_Malformed verifies bad JSON yields nil rather
// than panicking.
func TestNormalizeAntigravityModels_Malformed(t *testing.T) {
	if got := normalizeAntigravityModels([]byte(`{not json`)); got != nil {
		t.Fatalf("expected nil for malformed input, got %+v", got)
	}
}

// TestAntigravityStaticModels_AllDiscoverable verifies the fallback catalogue
// contains no entry the live filter would reject.
func TestAntigravityStaticModels_AllDiscoverable(t *testing.T) {
	models := antigravityStaticModels()
	if len(models) == 0 {
		t.Fatal("static catalogue is empty")
	}
	for _, m := range models {
		if !providers.IsDiscoverableAntigravityModel(m.ID) {
			t.Errorf("static catalogue contains non-discoverable id %q", m.ID)
		}
		if m.Name == "" {
			t.Errorf("static model %q has an empty label", m.ID)
		}
		if m.Kind != "llm" {
			t.Errorf("static model %q has kind %q, want llm", m.ID, m.Kind)
		}
	}
}

// TestFetchAntigravityModels_LiveHTTP drives the discovery path against a stub
// server, verifying the POST headers and catalogue extraction.
func TestFetchAntigravityModels_LiveHTTP(t *testing.T) {
	var gotAuth, gotUA, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotMethod = r.Method
		_, _ = w.Write([]byte(liveFixture))
	}))
	defer srv.Close()

	h, _ := newProfileTestHandler(t)
	// Point the discovery hosts at the stub for the duration of the test.
	origHosts := antigravityDiscoveryHosts
	antigravityDiscoveryHosts = []string{srv.URL}
	defer func() { antigravityDiscoveryHosts = origHosts }()

	data := `{"accessToken":"ya29.test-token","projectId":"p1"}`
	res, err := h.fetchAntigravityModels("antigravity", data, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ya29.") {
		t.Errorf("expected Bearer token, got %q", gotAuth)
	}
	if !strings.Contains(gotUA, "antigravity/ide/") || !strings.Contains(gotUA, "darwin/arm64") {
		t.Errorf("unexpected User-Agent %q", gotUA)
	}
	if len(res.Models) != 9 {
		t.Fatalf("expected 9 models, got %d: %+v", len(res.Models), res.Models)
	}
	if !res.Supported {
		t.Error("result should be marked supported")
	}
}

// TestFetchAntigravityModels_CLIProfileUsesCLIUserAgent verifies discovery
// honours the provider-wide client profile, so a catalogue is fetched with the
// same fingerprint chat will use.
func TestFetchAntigravityModels_CLIProfileUsesCLIUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(liveFixture))
	}))
	defer srv.Close()

	origHosts := antigravityDiscoveryHosts
	antigravityDiscoveryHosts = []string{srv.URL}
	defer func() { antigravityDiscoveryHosts = origHosts }()

	h, _ := newProfileTestHandler(t)
	if err := h.repo.SetAntigravityClientProfile("cli"); err != nil {
		t.Fatalf("set profile: %v", err)
	}

	data := `{"accessToken":"ya29.test-token"}`
	if _, err := h.fetchAntigravityModels("antigravity", data, 5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(gotUA, "antigravity/cli/") {
		t.Errorf("expected CLI User-Agent, got %q", gotUA)
	}
	if !strings.Contains(gotUA, "auth_method=consumer") {
		t.Errorf("CLI User-Agent should carry auth_method=consumer, got %q", gotUA)
	}
}

// TestFetchAntigravityModels_NoTokenFallsBack verifies a missing token yields
// the local catalogue with a warning instead of an error.
func TestFetchAntigravityModels_NoTokenFallsBack(t *testing.T) {
	h, _ := newProfileTestHandler(t)
	res, err := h.fetchAntigravityModels("antigravity", `{}`, 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Supported || len(res.Models) == 0 {
		t.Fatalf("expected local catalogue, got %+v", res)
	}
	if res.Warning == "" {
		t.Error("expected a warning when the token is unavailable")
	}
}

// TestFetchAntigravityModels_HostFallback verifies a failing first host falls
// through to the next one.
func TestFetchAntigravityModels_HostFallback(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html>404</html>"))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(liveFixture))
	}))
	defer good.Close()

	origHosts := antigravityDiscoveryHosts
	antigravityDiscoveryHosts = []string{bad.URL, good.URL}
	defer func() { antigravityDiscoveryHosts = origHosts }()

	h, _ := newProfileTestHandler(t)
	res, err := h.fetchAntigravityModels("antigravity", `{"accessToken":"t"}`, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Models) != 9 {
		t.Fatalf("expected fallback host to yield 9 models, got %d", len(res.Models))
	}
}

// TestFetchAntigravityModels_AllHostsDownFallsBack verifies total failure still
// returns the local catalogue (never an error) so Import never dead-ends.
func TestFetchAntigravityModels_AllHostsDownFallsBack(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer bad.Close()

	origHosts := antigravityDiscoveryHosts
	antigravityDiscoveryHosts = []string{bad.URL}
	defer func() { antigravityDiscoveryHosts = origHosts }()

	h, _ := newProfileTestHandler(t)
	res, err := h.fetchAntigravityModels("antigravity", `{"accessToken":"t"}`, 3*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Models) == 0 || !res.Supported {
		t.Fatalf("expected local catalogue fallback, got %+v", res)
	}
	if res.Warning == "" {
		t.Error("expected a warning when all hosts fail")
	}
}

// TestIsAntigravityProvider covers canonical id and alias forms.
func TestIsAntigravityProvider(t *testing.T) {
	for _, id := range []string{"antigravity", "Antigravity", "agy", "AGY"} {
		if !isAntigravityProvider(id) {
			t.Errorf("isAntigravityProvider(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"ag", "gemini", "openai", ""} {
		if isAntigravityProvider(id) {
			t.Errorf("isAntigravityProvider(%q) = true, want false", id)
		}
	}
}

// TestHumanizeModelID verifies raw slugs become readable labels.
func TestHumanizeModelID(t *testing.T) {
	cases := map[string]string{
		"gemini-3.8-flash-tiered": "Gemini 3.8 Flash (Tiered)",
		"gemini-3.7-flash-high":   "Gemini 3.7 Flash (High)",
		"claude-sonnet-4-6":       "Claude Sonnet 4 6",
		"gpt-oss-120b-medium":     "Gpt Oss 120b (Medium)",
	}
	for in, want := range cases {
		if got := humanizeModelID(in); got != want {
			t.Errorf("humanizeModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAntigravityFriendlyName verifies upstream labels win, curated labels are
// used next, and raw slugs are humanised last.
func TestAntigravityFriendlyName(t *testing.T) {
	// Upstream label wins.
	if got := antigravityFriendlyName("claude-sonnet-4-6", "Claude Sonnet 4.6 (Thinking)"); got != "Claude Sonnet 4.6 (Thinking)" {
		t.Errorf("upstream label should win, got %q", got)
	}
	// Upstream empty -> curated catalogue.
	if got := antigravityFriendlyName("gemini-3.8-flash-high", ""); got != "Gemini 3.8 Flash (High)" {
		t.Errorf("curated label expected, got %q", got)
	}
	// Upstream echoes the id -> curated catalogue.
	if got := antigravityFriendlyName("gemini-3.7-flash-tiered", "gemini-3.7-flash-tiered"); got != "Gemini 3.7 Flash (Tiered)" {
		t.Errorf("should ignore echoed id and use curated label, got %q", got)
	}
	// Unknown id -> humanised.
	if got := antigravityFriendlyName("brand-new-model-preview", ""); got == "brand-new-model-preview" || got == "" {
		t.Errorf("expected humanised label, got %q", got)
	}
}
