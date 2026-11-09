package executor

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// TestForwardDeepAI_InjectsLaneHeaders proves the two headers the upstream
// meters on reach the wire: the lane version and the auth header name. A bare
// OpenAI forward would send `Authorization` and drop the lane, which the
// upstream refuses with machine_required.
func TestForwardDeepAI_InjectsLaneHeaders(t *testing.T) {
	var gotHeaders http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{
		BaseURL:    upstream.URL + "/chat/completions",
		AuthHeader: "Api-Key",
		AuthScheme: "raw",
		StaticHeaders: map[string]string{
			"X-DeepAI-Code-Theos": "4",
		},
	}
	body := `{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`
	req := &Request{
		Ctx:       context.Background(),
		Client:    upstream.Client(),
		Config:    cfg,
		APIKey:    "test-key",
		Body:      []byte(body),
		ModelName: "glm-5.3-flash",
	}

	rec := httptest.NewRecorder()
	if err := ForwardDeepAI(rec, req); err != nil {
		t.Fatalf("ForwardDeepAI: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := gotHeaders.Get("X-DeepAI-Code-Theos"); got != "4" {
		t.Errorf("X-DeepAI-Code-Theos = %q, want 4", got)
	}
	if got := gotHeaders.Get("Api-Key"); got != "test-key" {
		t.Errorf("Api-Key = %q, want test-key", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "" {
		t.Errorf("Authorization must not be set for DeepAI, got %q", got)
	}
}

// TestApplyDeepAIHeaders_DefaultModel proves a bodiless model is filled with the
// lane's bundled default rather than left absent.
func TestApplyDeepAIHeaders_DefaultModel(t *testing.T) {
	cfg := &providers.ProviderConfig{BaseURL: "https://api.deepai.org/deepai_code/chat/completions"}
	req := &Request{Config: cfg, Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	applyDeepAIHeaders(req)

	var got map[string]any
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["model"] != DeepAIDefaultModel {
		t.Errorf("model = %v, want %s", got["model"], DeepAIDefaultModel)
	}
	if cfg.StaticHeaders["X-DeepAI-Code-Theos"] != "4" {
		t.Errorf("lane header not injected: %+v", cfg.StaticHeaders)
	}
}

// TestDeepAIModelsURL derives the catalogue path from the chat base.
func TestDeepAIModelsURL(t *testing.T) {
	cfg := &providers.ProviderConfig{BaseURL: "https://api.deepai.org/deepai_code/chat/completions"}
	got := DeepAIModelsURL(cfg)
	if got != "https://api.deepai.org/deepai_code/models" {
		t.Errorf("DeepAIModelsURL = %q", got)
	}
}

// TestParseMachineUUIDs mirrors the vendor's parser: a placeholder UUID reads as
// "no machine id" so the header is omitted rather than sent as a blank digest.
func TestParseMachineUUIDs(t *testing.T) {
	if got := parseSmbiosUUID("00000000-0000-0000-0000-000000000000"); got != "" {
		t.Errorf("placeholder UUID accepted: %q", got)
	}
	if got := parseSmbiosUUID("UUID\n4C4C4544-0032-4A10-8033-B3C04F4E4432\n"); got != "4c4c4544-0032-4a10-8033-b3c04f4e4432" {
		t.Errorf("real UUID parsed as %q", got)
	}
	ioreg := `  "IOPlatformUUID" = "A1B2C3D4-1111-2222-3333-444455556666"`
	if got := parseIOPlatformUUID(ioreg); got != "a1b2c3d4-1111-2222-3333-444455556666" {
		t.Errorf("ioreg UUID parsed as %q", got)
	}
	if sha256Hex("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Error("sha256Hex mismatch")
	}
}

// TestLinuxProductUUIDMatchesWindowsDigest is the point of the Linux lane: the
// kernel's product_uuid is the same SMBIOS UUID Windows' Win32_ComputerSystemProduct
// returns, so hashing it must land on the identical digest — the header a router
// sends equals the one the IDE would send for the same physical box.
func TestLinuxProductUUIDMatchesWindowsDigest(t *testing.T) {
	const board = "ec2f35eb-78e4-5590-b423-9ca713037288"

	if got := parseSmbiosUUID(board + "\n"); got != board {
		t.Fatalf("linux product_uuid parsed as %q, want %q", got, board)
	}

	// Whatever case the kernel or a PowerShell line reports, the digest is the
	// same one: the vendor lowercases before hashing on every platform.
	asWindows := parseSmbiosUUID("UUID\nEC2F35EB-78E4-5590-B423-9CA713037288\n")
	if asWindows != board {
		t.Fatalf("uppercase SMBIOS parsed as %q", asWindows)
	}
	if sha256Hex(parseSmbiosUUID(board)) != sha256Hex(asWindows) {
		t.Error("linux and windows digests differ for one board")
	}

	// The injected header must carry that digest when the host has an id.
	orig := deepAIMachineIDOnce
	deepAIMachineIDOnce = deepAIMachineID{value: sha256Hex(board), available: true}
	defer func() { deepAIMachineIDOnce = orig }()

	cfg := &providers.ProviderConfig{BaseURL: "https://api.deepai.org/deepai_code/chat/completions"}
	req := &Request{Config: cfg, Body: []byte(`{"model":"glm-5.3-flash","messages":[]}`)}
	applyDeepAIHeaders(req)
	if got := cfg.StaticHeaders["X-DeepAI-Code-Machine"]; got != sha256Hex(board) {
		t.Errorf("X-DeepAI-Code-Machine = %q, want %q", got, sha256Hex(board))
	}
}

// TestDetectRawMachineID_LinuxPath exercises the file seam so the Linux branch
// is covered even when the tests run elsewhere.
func TestDetectRawMachineID_LinuxPath(t *testing.T) {
	orig := readFileFn
	defer func() { readFileFn = orig }()
	readFileFn = func(string) ([]byte, error) {
		return []byte("ec2f35eb-78e4-5590-b423-9ca713037288\n"), nil
	}
	if runtime.GOOS != "linux" {
		t.Skip("linux-only branch")
	}
	if got := detectRawMachineID(); got != "ec2f35eb-78e4-5590-b423-9ca713037288" {
		t.Errorf("detectRawMachineID = %q", got)
	}
}

// TestDeepAIMachineHeaderValue_PerConnectionOverride proves a connection's own
// machine id wins over the host: that is what keeps two pooled accounts from
// sharing one per-machine allowance. The value is the raw UUID the operator's
// Windows box reports, hashed the vendor's way.
func TestDeepAIMachineHeaderValue_PerConnectionOverride(t *testing.T) {
	const winBox = "EC2F35EB-78E4-5590-B423-9CA713037288"

	got, ok := deepAIMachineHeaderValue("conn-1", map[string]any{DeepAIMachineIDKey: winBox})
	if !ok {
		t.Fatal("override not honoured")
	}
	if got != sha256Hex(strings.ToLower(winBox)) {
		t.Errorf("override digest = %q, want %q", got, sha256Hex(strings.ToLower(winBox)))
	}

	// Two connections with different ids must not collide.
	a, _ := deepAIMachineHeaderValue("conn-a", map[string]any{DeepAIMachineIDKey: "aaaaaaaa-0000-0000-0000-000000000001"})
	b, _ := deepAIMachineHeaderValue("conn-b", map[string]any{DeepAIMachineIDKey: "bbbbbbbb-0000-0000-0000-000000000002"})
	if a == b || a == "" {
		t.Error("distinct machine ids produced the same digest")
	}

	// A blank override falls back to the derived per-connection id (non-empty),
	// never to garbage and never to an empty header.
	if v, ok := deepAIMachineHeaderValue("conn-x", map[string]any{DeepAIMachineIDKey: "  "}); !ok || v == "" {
		t.Error("blank override should fall back to the derived id")
	}

	// And it reaches the wire through applyDeepAIHeaders.
	cfg := &providers.ProviderConfig{BaseURL: "https://api.deepai.org/deepai_code/chat/completions"}
	req := &Request{
		Config:   cfg,
		Body:     []byte(`{"model":"glm-5.3-flash","messages":[]}`),
		ConnData: map[string]any{DeepAIMachineIDKey: winBox},
	}
	applyDeepAIHeaders(req)
	if cfg.StaticHeaders["X-DeepAI-Code-Machine"] != got {
		t.Errorf("header = %q, want %q", cfg.StaticHeaders["X-DeepAI-Code-Machine"], got)
	}
}

// TestDeepAIDerivedMachineID_PerConnectionBuckets is the fix for the measured
// failure: two accounts sharing the router host's digest left the second one at
// granted=0 and turned every metered call into 403 "could not identify this
// computer". A distinct, stable id per connection gives each account its own
// daily bucket.
func TestDeepAIDerivedMachineID_PerConnectionBuckets(t *testing.T) {
	a := DeepAIDerivedMachineID("deepai-oauth-AAA")
	b := DeepAIDerivedMachineID("deepai-oauth-BBB")
	if a == b {
		t.Fatal("two connections derived the same machine id")
	}
	if DeepAIDerivedMachineID("deepai-oauth-AAA") != a {
		t.Error("derivation must be stable across calls (the grant is issued once per id)")
	}
	if len(a) != 64 || !hexOK(a) {
		t.Errorf("derived id must be a sha256 hex digest, got %q", a)
	}
	// Without a connection id the host digest still answers, so a direct call
	// keeps working when the caller could not supply one.
	if v, ok := deepAIMachineHeaderValue("", nil); ok != cachedDeepAIMachineID().available || (ok && v != cachedDeepAIMachineID().value) {
		t.Error("no-connection-id path must fall back to the host digest")
	}
}

func hexOK(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// TestForwardDeepAI_LaneRefusalSurfaces proves an upstream refusal is not
// swallowed: the auth_required body reaches the client unchanged.
func TestForwardDeepAI_LaneRefusalSurfaces(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"Sign in to DeepAI to use DeepAI Code.","type":"auth_required","code":"auth_required"}}`)
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{BaseURL: upstream.URL + "/chat/completions", AuthHeader: "Api-Key", AuthScheme: "raw"}
	req := &Request{
		Ctx:    context.Background(),
		Client: upstream.Client(),
		Config: cfg,
		APIKey: "bad",
		Body:   []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`),
	}
	rec := httptest.NewRecorder()
	err := ForwardDeepAI(rec, req)
	if err == nil {
		t.Fatalf("expected an error for a 403 upstream, got nil (status %d)", rec.Code)
	}
	if !strings.Contains(err.Error(), "auth_required") {
		t.Errorf("refusal not surfaced through the error: %v", err)
	}
}
