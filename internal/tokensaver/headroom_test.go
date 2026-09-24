package tokensaver

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The bypass header is the only way a single request can opt out without
// changing a global setting, so the comparison has to be case-insensitive and
// must not trigger on any other value.
func TestBypassHeaderOnlyMatchesOff(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"off", true},
		{"OFF", true},
		{"Off", true},
		{" off ", true},
		{"on", false},
		{"", false},
		{"offline", false},
		{"false", false},
	} {
		ctx := WithBypass(context.Background(), tc.value)
		if got := BypassRequested(ctx); got != tc.want {
			t.Errorf("WithBypass(%q) bypassed = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// A nil context must not panic — the saver pipeline is also reached from paths
// that build their own context.
func TestBypassRequestedNilContext(t *testing.T) {
	if BypassRequested(nil) {
		t.Error("a nil context must not report a bypass")
	}
	if WithBypass(nil, "off") != nil {
		t.Error("WithBypass(nil, ...) must return nil rather than allocate a context")
	}
}

// The older Go dashboard wrote "light"/"medium"/"compact"; those must keep
// resolving to a real prompt instead of silently falling back to the default.
func TestNormalizeLevelMapsLegacyAliases(t *testing.T) {
	caveman := []struct {
		in   string
		want string
	}{
		{"lite", "lite"},
		{"light", "lite"},
		{"LITE", "lite"},
		{"full", "full"},
		{"medium", "full"},
		{"compact", "full"},
		{"ultra", "ultra"},
		{"", "full"},
		{"nonsense", "full"},
	}
	for _, tc := range caveman {
		if got := NormalizeLevel(tc.in, CavemanLevels); got != tc.want {
			t.Errorf("NormalizeLevel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ValidLevel is the settings validator: it must reject the legacy spellings
// (they are read-path aliases, not values the UI may write) and accept the
// three real levels.
func TestValidLevelRejectsAliases(t *testing.T) {
	for _, lvl := range CavemanLevels {
		if !ValidLevel(lvl, CavemanLevels) {
			t.Errorf("ValidLevel(%q) = false, want true", lvl)
		}
	}
	for _, lvl := range []string{"light", "medium", "compact", "", "  ", "ULTRA!"} {
		if ValidLevel(lvl, CavemanLevels) {
			t.Errorf("ValidLevel(%q) = true, want false", lvl)
		}
	}
}

// Each level must select a distinct prompt. Two levels collapsing onto one
// prompt would make the dropdown a lie.
func TestGetPromptSelectsDistinctLevels(t *testing.T) {
	seen := map[string]string{}
	for _, lvl := range CavemanLevels {
		p := GetCavemanPrompt(lvl)
		if p == "" {
			t.Fatalf("caveman level %q returned an empty prompt", lvl)
		}
		if prev, dup := seen[p]; dup {
			t.Errorf("caveman levels %q and %q share a prompt", prev, lvl)
		}
		seen[p] = lvl
	}
	seen = map[string]string{}
	for _, lvl := range PonytailLevels {
		p := GetPonytailPrompt(lvl)
		if p == "" {
			t.Fatalf("ponytail level %q returned an empty prompt", lvl)
		}
		if prev, dup := seen[p]; dup {
			t.Errorf("ponytail levels %q and %q share a prompt", prev, lvl)
		}
		seen[p] = lvl
	}
	// The legacy alias has to reach the same prompt as the level it maps to.
	if GetCavemanPrompt("light") != GetCavemanPrompt("lite") {
		t.Error(`GetCavemanPrompt("light") must equal the "lite" prompt`)
	}
	if GetPonytailPrompt("compact") != GetPonytailPrompt("full") {
		t.Error(`GetPonytailPrompt("compact") must equal the "full" prompt`)
	}
}

func TestHeadroomEndpointAppendsCompressPath(t *testing.T) {
	for _, tc := range []struct {
		base string
		want string
	}{
		{"http://localhost:8787", "http://localhost:8787/v1/compress"},
		{"http://localhost:8787/", "http://localhost:8787/v1/compress"},
		{"http://localhost:8787///", "http://localhost:8787/v1/compress"},
		{"  http://localhost:8787  ", "http://localhost:8787/v1/compress"},
		{"", ""},
	} {
		got := HeadroomOptions{BaseURL: tc.base}.Endpoint()
		if got != tc.want {
			t.Errorf("Endpoint(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}

// A working proxy returns the compressed conversation and its counters.
func TestCompressWithHeadroomReturnsCompressedMessages(t *testing.T) {
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"short"}],"tokens_before":900,"tokens_after":120,"tokens_saved":780}`))
	}))
	defer srv.Close()

	in := []any{map[string]any{"role": "user", "content": "very long conversation"}}
	out, stats, err := CompressWithHeadroom(context.Background(), srv.Client(), in,
		HeadroomOptions{BaseURL: srv.URL, Model: "gpt-4o", TimeoutMs: 2000})
	if err != nil {
		t.Fatalf("CompressWithHeadroom: %v", err)
	}
	if gotPath != "/v1/compress" {
		t.Errorf("path = %q, want /v1/compress", gotPath)
	}
	if len(out) != 1 {
		t.Fatalf("got %d messages, want 1", len(out))
	}
	if stats == nil || stats.TokensSaved != 780 {
		t.Errorf("stats = %+v, want tokens_saved=780", stats)
	}
	var sent struct {
		Messages []any  `json:"messages"`
		Model    string `json:"model"`
	}
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("proxy received unparseable body: %v", err)
	}
	if sent.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", sent.Model)
	}
	if len(sent.Messages) != 1 {
		t.Errorf("proxy received %d messages, want 1", len(sent.Messages))
	}
}

// compress_user_messages is opt-in and only appears when requested, because
// rewriting the operator's own turns is a bigger change than compressing tool
// output.
func TestCompressWithHeadroomSendsUserMessageFlagOnlyWhenAsked(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"messages":[{"role":"user","content":"x"}]}`))
	}))
	defer srv.Close()

	in := []any{map[string]any{"role": "user", "content": "x"}}

	if _, _, err := CompressWithHeadroom(context.Background(), srv.Client(), in,
		HeadroomOptions{BaseURL: srv.URL, Model: "m"}); err != nil {
		t.Fatalf("CompressWithHeadroom: %v", err)
	}
	var without map[string]jsontext.Value
	if err := json.Unmarshal(gotBody, &without); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := without["config"]; present {
		t.Error("config must be omitted when user-message compression is off")
	}

	if _, _, err := CompressWithHeadroom(context.Background(), srv.Client(), in,
		HeadroomOptions{BaseURL: srv.URL, Model: "m", CompressUserMessages: true}); err != nil {
		t.Fatalf("CompressWithHeadroom: %v", err)
	}
	var with struct {
		Config struct {
			CompressUserMessages bool `json:"compress_user_messages"`
		} `json:"config"`
	}
	if err := json.Unmarshal(gotBody, &with); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !with.Config.CompressUserMessages {
		t.Error("compress_user_messages must be true when requested")
	}
}

// Every failure mode is fail-open: the caller keeps its original body. A
// sidecar that is down, slow, or answering garbage must not fail a chat.
func TestCompressWithHeadroomFailsOpen(t *testing.T) {
	t.Run("no url", func(t *testing.T) {
		if _, _, err := CompressWithHeadroom(context.Background(), nil,
			[]any{map[string]any{"role": "user"}}, HeadroomOptions{}); err == nil {
			t.Error("expected an error for an empty URL")
		}
	})

	t.Run("no messages", func(t *testing.T) {
		if _, _, err := CompressWithHeadroom(context.Background(), nil, nil,
			HeadroomOptions{BaseURL: "http://localhost:8787"}); err == nil {
			t.Error("expected an error for an empty message list")
		}
	})

	t.Run("non-2xx", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		if _, _, err := CompressWithHeadroom(context.Background(), srv.Client(),
			[]any{map[string]any{"role": "user"}}, HeadroomOptions{BaseURL: srv.URL}); err == nil {
			t.Error("expected an error for HTTP 500")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"messages":`))
		}))
		defer srv.Close()
		if _, _, err := CompressWithHeadroom(context.Background(), srv.Client(),
			[]any{map[string]any{"role": "user"}}, HeadroomOptions{BaseURL: srv.URL}); err == nil {
			t.Error("expected an error for a truncated body")
		}
	})

	t.Run("empty messages", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"messages":[]}`))
		}))
		defer srv.Close()
		if _, _, err := CompressWithHeadroom(context.Background(), srv.Client(),
			[]any{map[string]any{"role": "user"}}, HeadroomOptions{BaseURL: srv.URL}); err == nil {
			t.Error("expected an error when the proxy returns no messages")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte(`{"messages":[{"role":"user"}]}`))
		}))
		defer srv.Close()
		start := time.Now()
		_, _, err := CompressWithHeadroom(context.Background(), srv.Client(),
			[]any{map[string]any{"role": "user"}},
			HeadroomOptions{BaseURL: srv.URL, TimeoutMs: 50})
		if err == nil {
			t.Error("expected a timeout error")
		}
		if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
			t.Errorf("timeout took %v; the configured deadline was not applied", elapsed)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		if _, _, err := CompressWithHeadroom(context.Background(), &http.Client{Timeout: time.Second},
			[]any{map[string]any{"role": "user"}},
			HeadroomOptions{BaseURL: "http://127.0.0.1:1", TimeoutMs: 200}); err == nil {
			t.Error("expected an error for an unreachable proxy")
		}
	})
}

// A default timeout must exist: an unset value must not mean "no deadline".
func TestHeadroomDefaultTimeoutApplied(t *testing.T) {
	opts := HeadroomOptions{}
	if got := opts.timeout(); got != DefaultHeadroomTimeoutMs*time.Millisecond {
		t.Errorf("timeout() = %v, want %v", got, DefaultHeadroomTimeoutMs*time.Millisecond)
	}
	if got := (HeadroomOptions{TimeoutMs: -5}).timeout(); got != DefaultHeadroomTimeoutMs*time.Millisecond {
		t.Errorf("a negative timeout must fall back to the default, got %v", got)
	}
}

// Responses-API input carrying tool/reasoning items must be skipped: the proxy
// only understands chat messages, and rewriting those items would corrupt the
// payload the provider expects.
func TestResponsesInputIsCompressible(t *testing.T) {
	plain := []any{map[string]any{"type": "message", "role": "user"}}
	if !ResponsesInputIsCompressible(plain) {
		t.Error("a message-only input must be compressible")
	}
	withTool := []any{
		map[string]any{"type": "message", "role": "user"},
		map[string]any{"type": "function_call", "name": "f"},
	}
	if ResponsesInputIsCompressible(withTool) {
		t.Error("input carrying a function_call must not be compressible")
	}
}

func TestMessageKeyDetectsShape(t *testing.T) {
	if k, ok := MessageKey(map[string]any{"messages": []any{}}); !ok || k != "messages" {
		t.Errorf("MessageKey(messages) = %q,%v", k, ok)
	}
	if k, ok := MessageKey(map[string]any{"input": []any{}}); !ok || k != "input" {
		t.Errorf("MessageKey(input) = %q,%v", k, ok)
	}
	if _, ok := MessageKey(map[string]any{"contents": []any{}}); ok {
		t.Error("an unknown shape must not report a key")
	}
}

func TestFormatHeadroomLog(t *testing.T) {
	if got := FormatHeadroomLog(nil); got != "" {
		t.Errorf("FormatHeadroomLog(nil) = %q, want empty", got)
	}
	got := FormatHeadroomLog(&HeadroomResponse{TokensBefore: 1000, TokensAfter: 400, TokensSaved: 600})
	if got == "" {
		t.Fatal("expected a rendered line")
	}
	for _, want := range []string{"delta=600", "before=1000", "after=400", "60.0%"} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q is missing %q", got, want)
		}
	}
}
