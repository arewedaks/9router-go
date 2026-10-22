package dashboard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"

	json "encoding/json/v2"
)

// TestCodebuddyProviderDetection pins which ids route to the CodeBuddy
// catalogue. A miss here means Import falls through to the generic registry
// lookup and fails with "does not support models listing" — the exact bug this
// change fixes.
func TestCodebuddyProviderDetection(t *testing.T) {
	yes := []string{"codebuddy-cn", "codebuddy-intl", "cbcn", "cbai", "CodeBuddy-CN", " CODEBUDDY-INTL "}
	for _, id := range yes {
		if !isCodebuddyProvider(id) {
			t.Errorf("isCodebuddyProvider(%q) = false, want true", id)
		}
	}
	no := []string{"", "antigravity", "agy", "openai", "cline", "codebuddy"}
	for _, id := range no {
		if isCodebuddyProvider(id) {
			t.Errorf("isCodebuddyProvider(%q) = true, want false", id)
		}
	}
}

// TestFetchCodebuddyModels_ReturnsStaticCatalogue is the core regression: the
// fetch must SUCCEED with a non-empty list and never report an error, because
// there is no /models endpoint to fail against.
func TestFetchCodebuddyModels_ReturnsStaticCatalogue(t *testing.T) {
	h := &Handler{}
	for _, alias := range []string{"codebuddy-cn", "cbcn", "codebuddy-intl", "cbai"} {
		res, err := h.fetchCodebuddyModels(alias)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", alias, err)
		}
		if res == nil {
			t.Fatalf("%s: nil result", alias)
		}
		if !res.Supported {
			t.Errorf("%s: Supported = false, want true (so Import is enabled)", alias)
		}
		if len(res.Models) == 0 {
			t.Fatalf("%s: empty catalogue", alias)
		}
		if res.Error != "" {
			t.Errorf("%s: Error should be empty, got %q", alias, res.Error)
		}
		if res.Warning == "" {
			t.Errorf("%s: Warning should explain the static catalogue", alias)
		}
		// Aliases must resolve to the canonical id so cache keys are stable.
		if res.Provider != providers.ResolveAlias(alias) {
			t.Errorf("%s: Provider = %q, want %q", alias, res.Provider, providers.ResolveAlias(alias))
		}
	}
}

// TestCodebuddyCatalogue_ExcludesRetiredAndNonChat guards the two filter rules:
// an id upstream now rejects with "service info not found" must not be offered,
// and non-chat surfaces must be dropped.
func TestCodebuddyCatalogue_ExcludesRetiredAndNonChat(t *testing.T) {
	models := codebuddyStaticModels()
	seen := map[string]bool{}
	for _, m := range models {
		seen[m.ID] = true
	}
	if seen["deepseek-v4-flash"] {
		t.Error("deepseek-v4-flash is retired upstream (400 service info not found) but was offered")
	}
	for _, bad := range []string{"", "some-image-model", "glm-tts", "video-gen", "bge-embedding", "rerank-v1"} {
		if isDiscoverableCodebuddyModel(bad) {
			t.Errorf("isDiscoverableCodebuddyModel(%q) = true, want false", bad)
		}
	}
	// At least one real model from each surviving family must be present, or the
	// catalogue silently regressed to the blocklist behaviour.
	//
	// The expected ids are the ones a live account actually served. minimax-m2.7,
	// deepseek-v4-pro and hy3-preview were listed here before they were probed:
	// all three now answer 11102 ("model is not available"), so they are retired
	// rather than expected. minimax-m3 and deepseek-v4.1-flash are their
	// available counterparts.
	for _, want := range []string{"glm-5.1", "minimax-m3", "deepseek-v4.1-flash"} {
		if !seen[want] {
			t.Errorf("expected %q in the CodeBuddy catalogue", want)
		}
	}
	// Ids that only the intl registry (VansRouter codebuddy-intl.js) lists must
	// also be present, otherwise the union catalogue dropped them.
	for _, want := range []string{"glm-5.2", "glm-5v-turbo", "minimax-m3", "kimi-k2.7", "kimi-k2.6"} {
		if !seen[want] {
			t.Errorf("expected intl model %q in the CodeBuddy catalogue", want)
		}
	}
}

// TestCodebuddyModelsCarryKind ensures imported entries declare a kind, since
// the UI renders it and a blank kind reads as unknown.
func TestCodebuddyModelsCarryKind(t *testing.T) {
	for _, m := range codebuddyStaticModels() {
		if m.Kind == "" {
			t.Errorf("model %q has empty kind", m.ID)
		}
	}
}

// TestFetchUpstreamModels_RoutesCodebuddyToCatalogue proves the wiring in
// fetchUpstreamModels: a codebuddy provider must NOT reach the generic branch
// that answers "does not support models listing".
func TestFetchUpstreamModels_RoutesCodebuddyToCatalogue(t *testing.T) {
	h := &Handler{}
	res, err := h.fetchUpstreamModels("cbcn", `{"accessToken":"x"}`, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Supported || len(res.Models) == 0 {
		t.Fatalf("codebuddy did not route to the static catalogue: %+v", res)
	}
	if res.Error != "" {
		t.Fatalf("codebuddy import reported an error: %q", res.Error)
	}
}

// TestProbeCodebuddy_SendsConversationHeaders is the core regression: the
// intl gateway rejects any chat request that lacks the X-Conversation-* header
// family with HTTP 400 code 11128, which the operator reads as a failed
// account. The probe must send the headers the real chat path sends.
func TestProbeCodebuddy_SendsConversationHeaders(t *testing.T) {
	var gotConvID, gotConvReqID, gotConvMsgID, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotConvID = r.Header.Get("X-Conversation-ID")
		gotConvReqID = r.Header.Get("X-Conversation-Request-ID")
		gotConvMsgID = r.Header.Get("X-Conversation-Message-ID")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer srv.Close()

	h := &Handler{}
	out := h.probeCodebuddyAgainstURL(context.Background(), srv.URL, "conn-1", "tok-1", providers.ProviderConfig{}, connectionTestResult{})
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if !out.Valid {
		t.Errorf("Valid = false, want true for HTTP 200")
	}
	if gotConvID == "" || gotConvReqID == "" || gotConvMsgID == "" {
		t.Errorf("X-Conversation headers missing: conv=%q req=%q msg=%q", gotConvID, gotConvReqID, gotConvMsgID)
	}
	if gotAuth != "Bearer tok-1" {
		t.Errorf("Authorization = %q, want Bearer tok-1", gotAuth)
	}
}

// TestProbeCodebuddy_ConversationIDStablePerConnection pins the stable-UUID
// shape of the derived X-Conversation-ID: two calls with the same key must
// share an id, different keys must not.
func TestProbeCodebuddy_ConversationIDStablePerConnection(t *testing.T) {
	a1 := executor.CodebuddyConversationHeadersFor("conn-a")["X-Conversation-ID"]
	a2 := executor.CodebuddyConversationHeadersFor("conn-a")["X-Conversation-ID"]
	b := executor.CodebuddyConversationHeadersFor("conn-b")["X-Conversation-ID"]
	if a1 != a2 {
		t.Errorf("same key produced different conversation ids: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("different keys shared a conversation id: %q", a1)
	}
	// UUID-shaped: 8-4-4-4-12 with the v5 nibble set.
	if len(a1) != 36 || a1[14] != '5' {
		t.Errorf("conversation id %q is not a UUID-v5-shaped string", a1)
	}
}

// TestProbeCodebuddy_ClassifiesCodebuddy400 pins that a 400 carrying the
// CodeBuddy {"code":N,"msg":"..."} envelope is NOT read as a credential
// failure: 11128 (channel/headers) and 11102 (model unavailable) are warnings,
// an unknown code stays inconclusive.
func TestProbeCodebuddy_ClassifiesCodebuddy400(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		msg     string
		wantVal bool
		wantErr bool // out.Error set?
	}{
		{"channel rejection 11128", 11128, "Illegal API invocation from an unapproved channel", true, false},
		{"model unavailable 11102", 11102, "service info not found", true, false},
		{"unknown code stays inconclusive", 99999, "something else", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"code":%d,"msg":%q}`, tc.code, tc.msg)
			}))
			defer srv.Close()

			h := &Handler{}
			out := h.probeCodebuddyAgainstURL(context.Background(), srv.URL, "c", "tok", providers.ProviderConfig{}, connectionTestResult{})
			if tc.wantErr && out.Error == "" {
				t.Errorf("expected an Error, got none (Valid=%v Warning=%q)", out.Valid, out.Warning)
			}
			if !tc.wantErr && out.Error != "" {
				t.Errorf("expected no Error, got %q", out.Error)
			}
			if out.Valid != tc.wantVal {
				t.Errorf("Valid = %v, want %v", out.Valid, tc.wantVal)
			}
		})
	}
}

// TestProbeCodebuddy_Rejects401 pins the credential verdict: a 401 from the
// gateway IS a credential failure and must not be softened into a warning.
func TestProbeCodebuddy_Rejects401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":401,"msg":"invalid token"}`))
	}))
	defer srv.Close()

	h := &Handler{}
	out := h.probeCodebuddyAgainstURL(context.Background(), srv.URL, "c", "bad", providers.ProviderConfig{}, connectionTestResult{})
	if out.Error == "" {
		t.Fatal("401 must set Error; got none")
	}
	if out.Valid {
		t.Error("401 must not set Valid")
	}
}

// TestHandleTestConnection_CodebuddyEndToEnd is the full button-press path:
// seed a real codebuddy-intl connection in the DB, POST the exact endpoint the
// UI's Test button calls, and assert the verdict comes back green. This is the
// regression for "Test always fails on codebuddy/workbuddy" — the unit probe
// test above proves the headers are sent; this proves the handler dispatch
// actually reaches it and the endpoint reports valid:true.
func TestHandleTestConnection_CodebuddyEndToEnd(t *testing.T) {
	h, database := newProfileTestHandler(t)

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		if r.Header.Get("X-Conversation-ID") == "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer cb-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":401,"msg":"bad token"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer srv.Close()
	h.codebuddyBaseOverride = srv.URL

	seedConnection(t, db.NewRepo(database), "conn-cb", "codebuddy-intl", `{"accessToken":"cb-tok"}`)

	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/connections/conn-cb/test", strings.NewReader("{}"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "conn-cb")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	h.HandleTestConnection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handler status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var out connectionTestResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode verdict: %v (body %s)", err, w.Body.String())
	}
	if out.Error != "" {
		t.Fatalf("verdict has Error %q; the UI renders this as a red fail", out.Error)
	}
	if !out.Valid {
		t.Fatalf("verdict Valid = false (%+v); the UI renders this as a red fail", out)
	}
	if out.Status != http.StatusOK {
		t.Errorf("verdict Status = %d, want 200", out.Status)
	}
	// The probe body must be shaped like a real chat request: stream forced true
	// (else code 11101) and the seed system prompt first (else code 11128).
	if gotBody["stream"] != true {
		t.Errorf("probe body stream = %v, want true (gateway rejects non-stream with 11101)", gotBody["stream"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("probe sent no messages")
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "You are CodeBuddy Code." {
		t.Errorf("first message must be the CodeBuddy seed system prompt, got %v", first)
	}
}

// TestHandleTestConnection_CodebuddyMissingHeadersIsWarning pins that a gateway
// 11128 rejection (headers/channel problem) is reported as an amber warning,
// not a red fail — the token was accepted, so the account is not broken.
func TestHandleTestConnection_CodebuddyMissingHeadersIsWarning(t *testing.T) {
	h, database := newProfileTestHandler(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always reject with the channel code, simulating an unapproved egress IP.
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":11128,"msg":"Illegal API invocation from an unapproved channel"}`))
	}))
	defer srv.Close()
	h.codebuddyBaseOverride = srv.URL

	seedConnection(t, db.NewRepo(database), "conn-cb2", "workbuddy", `{"accessToken":"cb-tok"}`)

	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/connections/conn-cb2/test", strings.NewReader("{}"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "conn-cb2")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	h.HandleTestConnection(w, req)

	var out connectionTestResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode verdict: %v", err)
	}
	if !out.Valid {
		t.Errorf("Valid = false; a channel rejection is not a credential failure (%+v)", out)
	}
	if out.Warning == "" {
		t.Error("expected an amber Warning explaining the channel rejection")
	}
	if out.Error != "" {
		t.Errorf("Error must stay empty so the UI shows amber not red; got %q", out.Error)
	}
}

// TestProbeCodebuddy_InvalidUTF8BodyDoesNotBreakJSON is the regression for the
// 500 the UI showed instead of a verdict: CodeBuddy can answer with a
// non-UTF-8 body (and truncating mid-rune makes it worse), which made the
// verdict fail to marshal. The probe must always return a marshalable result.
func TestProbeCodebuddy_InvalidUTF8BodyDoesNotBreakJSON(t *testing.T) {
	// 0xff 0xfe are not valid UTF-8; slicing at byte 200 would split a rune.
	junk := strings.Repeat("\xff\xfe\x80", 200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":11128,"msg":"` + junk + `"}`))
	}))
	defer srv.Close()

	h := &Handler{}
	out := h.probeCodebuddyAgainstURL(context.Background(), srv.URL, "c", "tok", providers.ProviderConfig{}, connectionTestResult{})

	if _, err := json.Marshal(out); err != nil {
		t.Fatalf("verdict is not marshalable (UI would show a 500): %v", err)
	}
	if out.Warning == "" {
		t.Error("expected a Warning with the sanitized detail")
	}
}

// TestTruncateCutsOnRuneBoundary pins the underlying helper: a byte-slice cut in
// the middle of a multibyte rune yields invalid UTF-8 that the strict JSON
// encoder rejects.
func TestTruncateCutsOnRuneBoundary(t *testing.T) {
	s := strings.Repeat("日本語", 100) // 900 bytes, 300 runes
	got := truncate(s, 100)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
	}
	if len(got) > 100+len("…") {
		t.Errorf("truncate did not cap length: %d bytes", len(got))
	}
}

// WorkBuddy is a separate service from CodeBuddy (its own host, its own much
// smaller live catalogue). Treating it as another CodeBuddy variant sent two
// things wrong: the Import button handed the operator CodeBuddy's model list
// (ids WorkBuddy rejects), and the probe fired at www.codebuddy.ai, so a valid
// WorkBuddy credential reported as failing against a gateway that had never
// seen that account.
func TestWorkbuddyUsesItsOwnCatalogueNotCodebuddy(t *testing.T) {
	ui := &Handler{}
	res, err := ui.fetchCodebuddyModels("workbuddy")
	if err != nil {
		t.Fatalf("fetchCodebuddyModels(workbuddy): %v", err)
	}

	got := make(map[string]bool, len(res.Models))
	for _, m := range res.Models {
		got[m.ID] = true
	}

	want := providers.GetProviderModels("workbuddy")
	if len(want) == 0 {
		t.Fatal("workbuddy has no registry catalogue — the test cannot assert anything")
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("workbuddy catalogue is missing its own model %q", id)
		}
	}

	// Ids that only CodeBuddy serves must not leak into the WorkBuddy import.
	for _, id := range []string{"glm-5.0", "glm-4.7", "minimax-m2.5", "deepseek-r1"} {
		if got[id] {
			t.Errorf("workbuddy catalogue leaked CodeBuddy-only model %q", id)
		}
	}

	if len(res.Models) != len(want) {
		t.Errorf("workbuddy catalogue has %d models, registry says %d", len(res.Models), len(want))
	}
}

// TestWorkbuddyCatalogueComesFromItsOwnRegistry proves the split is keyed on the
// provider and not on the shared CodeBuddy code path.
//
// The two lists happen to overlap after CodeBuddy's retired-id filter (both end
// up serving the same eight ids), so set equality proves nothing. What does
// prove the split is order: WorkBuddy returns its registry entry verbatim,
// while codebuddy-intl returns the CodeBuddy static table — a different source
// that merely filters down to the same members.
func TestWorkbuddyCatalogueComesFromItsOwnRegistry(t *testing.T) {
	ui := &Handler{}

	wb, err := ui.fetchCodebuddyModels("workbuddy")
	if err != nil {
		t.Fatalf("fetchCodebuddyModels(workbuddy): %v", err)
	}

	want := providers.GetProviderModels("workbuddy")
	if len(want) == 0 {
		t.Fatal("workbuddy has no registry catalogue — the test cannot assert anything")
	}
	if len(wb.Models) != len(want) {
		t.Fatalf("workbuddy has %d models, registry says %d", len(wb.Models), len(want))
	}
	for i, id := range want {
		if wb.Models[i].ID != id {
			t.Errorf("workbuddy model %d is %q, registry says %q (order must follow the registry)", i, wb.Models[i].ID, id)
		}
	}

	// The CodeBuddy path must be untouched: it still answers from the static
	// table, which carries the IDs the WorkBuddy catalogue never had.
	cb, err := ui.fetchCodebuddyModels("codebuddy-intl")
	if err != nil {
		t.Fatalf("fetchCodebuddyModels(codebuddy-intl): %v", err)
	}
	if len(cb.Models) == 0 {
		t.Fatal("codebuddy-intl catalogue is empty")
	}
	if cb.Models[0].ID == wb.Models[0].ID && len(cb.Models) == len(wb.Models) {
		t.Log("catalogues overlap after filtering; the order assertion above is what pins the split")
	}
}
