package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/db"
)

// A combo's strategy is only real if it changes which upstream a live request
// reaches. These tests build a combo named "deepseek-model" whose two entries
// point at two different mock upstreams, then count the hits: order-only
// assertions cannot tell "the strategy ran" from "the strategy was ignored".

// comboTestRig stands up two mock upstreams and a combo pointing at both.
type comboTestRig struct {
	handler  *ChatHandler
	hitsA    int
	hitsB    int
	failA    bool // when true, upstream A answers 500 so fallback must try B
	mu       sync.Mutex
	upstream *httptest.Server
}

func (r *comboTestRig) count(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == "a" {
		r.hitsA++
	} else {
		r.hitsB++
	}
}

func (r *comboTestRig) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hitsA, r.hitsB
}

// newComboRig inserts two providers pointing at one mock upstream that tags
// each call by the path the provider uses, then stores the combo.
func newComboRig(t *testing.T, strategy string, entries []string) *comboTestRig {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)

	rig := &comboTestRig{}
	rig.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		kind := strings.TrimPrefix(req.URL.Path, "/")
		rig.count(kind)
		if kind == "a" && rig.failA {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"upstream A is down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"r","choices":[{"message":{"content":"served by ` + kind + `"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(rig.upstream.Close)

	for _, spec := range []struct{ id, provider, path string }{
		{"c-a", "deepseek", "a"},
		{"c-b", "openrouter", "b"},
	} {
		data, _ := json.Marshal(map[string]interface{}{
			"apiKey":  "sk-" + spec.provider,
			"baseUrl": rig.upstream.URL + "/" + spec.path,
		})
		if _, err := database.Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
			 VALUES (?, ?, 'apikey', ?, 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			spec.id, spec.provider, spec.provider, string(data)); err != nil {
			t.Fatalf("insert connection %s: %v", spec.id, err)
		}
	}

	models, _ := json.Marshal(entries)
	if _, err := database.Exec(
		`INSERT INTO combos (id, name, models, strategy, createdAt, updatedAt)
		 VALUES ('combo-1', 'deepseek-model', ?, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(models), strategy); err != nil {
		t.Fatalf("insert combo: %v", err)
	}

	rig.handler = NewChatHandler(db.NewRepo(database))
	return rig
}

// call sends one request through the real HTTP handler and returns the body.
func (r *comboTestRig) call(t *testing.T, userText string) string {
	t.Helper()
	body := `{"model":"deepseek-model","messages":[{"role":"user","content":"` + userText + `"}],"stream":false}`
	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.handler.HandleChatCompletions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// The bug this guards: a combo whose strategy never reaches the router would
// still "pass" any order-only unit test. Going through HandleChatCompletions
// proves the stored strategy selects the upstream.
func TestComboDeepseekModelRoundRobinRotatesUpstream(t *testing.T) {
	rig := newComboRig(t, "round-robin", []string{"deepseek/deepseek-chat", "openrouter/deepseek/deepseek-chat"})

	var served []string
	for i := 0; i < 4; i++ {
		out := rig.call(t, "turn")
		switch {
		case strings.Contains(out, "served by a"):
			served = append(served, "a")
		case strings.Contains(out, "served by b"):
			served = append(served, "b")
		default:
			t.Fatalf("turn %d: no upstream tag in %s", i, out)
		}
	}

	a, b := rig.counts()
	if a == 0 || b == 0 {
		t.Fatalf("round-robin must use both upstreams, got a=%d b=%d (served %v)", a, b, served)
	}
	// Two entries alternating: each upstream takes half the turns.
	if a != 2 || b != 2 {
		t.Errorf("4 turns over 2 entries should split 2/2, got a=%d b=%d (served %v)", a, b, served)
	}
	// And they must actually alternate, not clump.
	if len(served) == 4 && (served[0] == served[1] || served[2] == served[3]) {
		t.Errorf("round-robin should alternate, served %v", served)
	}
}

func TestComboDeepseekModelFallbackKeepsFirstUpstream(t *testing.T) {
	rig := newComboRig(t, "fallback", []string{"deepseek/deepseek-chat", "openrouter/deepseek/deepseek-chat"})

	for i := 0; i < 3; i++ {
		rig.call(t, "turn")
	}
	a, b := rig.counts()
	if a != 3 {
		t.Errorf("fallback must stay on the first entry, got a=%d", a)
	}
	if b != 0 {
		t.Errorf("fallback must not touch the second entry while the first works, got b=%d", b)
	}
}

// Fallback's whole point: when the first entry is down, the request still
// succeeds via the second.
func TestComboDeepseekModelFallbackSurvivesDeadFirstEntry(t *testing.T) {
	rig := newComboRig(t, "fallback", []string{"deepseek/deepseek-chat", "openrouter/deepseek/deepseek-chat"})
	rig.failA = true

	out := rig.call(t, "turn")
	if !strings.Contains(out, "served by b") {
		t.Fatalf("a dead first entry must fall through to the second, got: %s", out)
	}
	a, b := rig.counts()
	if a == 0 {
		t.Error("the first entry should have been tried before giving up on it")
	}
	if b != 1 {
		t.Errorf("exactly one fallback to B expected, got %d", b)
	}
}

// "capacity" is accepted by the API and stored, but the router must not treat
// it as an unknown strategy, and it must still route. This documents the
// current behaviour: capacity does not reorder, so it stays on the first entry.
func TestComboDeepseekModelCapacityStillRoutes(t *testing.T) {
	rig := newComboRig(t, "capacity", []string{"deepseek/deepseek-chat", "openrouter/deepseek/deepseek-chat"})

	out := rig.call(t, "turn")
	if !strings.Contains(out, "served by a") {
		t.Fatalf("capacity should route to the first entry, got: %s", out)
	}
}

// capacity is advertised in the picker as ">200K context only, then priority".
// Before it was implemented it silently behaved like fallback, so a combo set
// to capacity sent long prompts to whichever model happened to be listed first.
func TestComboDeepseekModelCapacityPrefersLargeContext(t *testing.T) {
	h := NewChatHandler(nil)

	// deepseek-chat is 131072 (< floor); a claude entry is 200000 (>= floor).
	models := []string{
		"deepseek/deepseek-chat",
		"claude/claude-sonnet-4-5",
		"deepseek/deepseek-reasoner",
	}

	got := h.ApplyComboStrategy("capacity", models, "deepseek-model", 1)
	if got[0] != "claude/claude-sonnet-4-5" {
		t.Errorf("capacity must promote the only >200K entry to the front, got %v", got)
	}
	// The small-context entries keep their configured order behind it.
	want := []string{
		"claude/claude-sonnet-4-5",
		"deepseek/deepseek-chat",
		"deepseek/deepseek-reasoner",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("capacity order: got %v, want %v", got, want)
	}
}

// Two entries that both clear the floor must keep their priority order: a
// capacity strategy is a partition, not a sort that reshuffles equals.
func TestComboDeepseekModelCapacityKeepsPriorityAmongLargeContext(t *testing.T) {
	h := NewChatHandler(nil)
	models := []string{
		"claude/claude-sonnet-4-5",
		"gemini/gemini-2.5-pro",
		"deepseek/deepseek-chat",
	}
	got := h.ApplyComboStrategy("capacity", models, "deepseek-model", 1)
	if !reflect.DeepEqual(got, models) {
		t.Errorf("capacity must preserve configured order among large-context entries, got %v", got)
	}
}
