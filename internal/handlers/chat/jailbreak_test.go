package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

// applyJailbreak is the injection boundary for every chat request. These tests
// pin the gate that matters: a model id with the toggle off (or no document)
// passes through untouched, and an armed model gets the prompt before the
// original content — in both wire formats — with the context flag set so the
// Gemini path can add safetySettings. Keys are the ids /v1/models serves,
// exactly as the client sends them: prefixes and suffixes are part of the key.

func newTestRepo(t *testing.T) *db.Repo {
	t.Helper()
	database, err := db.OpenDatabase(filepath.Join(t.TempDir(), "jb.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db.EnsureSchema(database); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return db.NewRepo(database)
}

// A model whose toggle is off (or no repo at all) must leave the body
// byte-identical. This is the default posture: injection never happens
// because a model exists, only because it was switched on.
func TestModelOffPassesThroughUntouched(t *testing.T) {
	h := &ChatHandler{} // Repo nil: nothing configured anywhere
	body := map[string]any{
		"model":    "wal/glm-5.3-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	before, _ := json.Marshal(body)
	ctx := h.applyJailbreak(context.Background(), body, "wal/glm-5.3-flash")
	if translator.GeminiSafetyRequested(ctx) {
		t.Error("off model must not set the safety flag")
	}
	after, _ := json.Marshal(body)
	if string(before) != string(after) {
		t.Errorf("body was modified while model off: %s -> %s", before, after)
	}
}

func TestModelOnWithDocInjectsPrompt(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.SaveJailbreakDoc(&db.JailbreakDoc{Prompt: "CUSTOM RULES", GeminiSafety: true}); err != nil {
		t.Fatalf("save doc: %v", err)
	}
	const armed = "wal/glm-5.3-flash"
	if err := repo.SetJailbreakModel(armed, true); err != nil {
		t.Fatalf("enable model: %v", err)
	}

	h := &ChatHandler{Repo: repo}
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "be helpful"},
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	ctx := h.applyJailbreak(context.Background(), body, armed)
	if !translator.GeminiSafetyRequested(ctx) {
		t.Error("doc.geminiSafety must set the context flag")
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("message count = %d, want 3 (prompt + original system + user)", len(msgs))
	}
	first, _ := json.Marshal(msgs[0])
	var got map[string]any
	_ = json.Unmarshal(first, &got)
	if got["role"] != "system" || got["content"] != "CUSTOM RULES" {
		t.Errorf("first message = %s, want the prompt as system", first)
	}
	second, _ := json.Marshal(msgs[1])
	_ = json.Unmarshal(second, &got)
	if got["content"] != "be helpful" {
		t.Errorf("original system message damaged: %s", second)
	}

	// A nearby but different model id — no bleed, no prefix matching.
	other := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	before, _ := json.Marshal(other)
	h.applyJailbreak(context.Background(), other, "wal/glm-5.3-flash-turbo")
	after, _ := json.Marshal(other)
	if string(before) != string(after) {
		t.Errorf("unrelated model was injected: %s", after)
	}
}

// An armed model with an empty document must still not inject: the document
// is the content, the toggle only arms it.
func TestModelOnWithEmptyDocInjectsNothing(t *testing.T) {
	repo := newTestRepo(t)
	if err := repo.SetJailbreakModel("wal/glm-5.3-flash", true); err != nil {
		t.Fatalf("enable model: %v", err)
	}
	h := &ChatHandler{Repo: repo}
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	before, _ := json.Marshal(body)
	h.applyJailbreak(context.Background(), body, "wal/glm-5.3-flash")
	after, _ := json.Marshal(body)
	if string(before) != string(after) {
		t.Errorf("empty doc was injected: %s -> %s", before, after)
	}
}

func TestSystemPromptPrependedToClaudeStringSystem(t *testing.T) {
	body := map[string]any{"system": "be concise"}
	prependSystemPrompt(body, "CUSTOM RULES")
	if got := body["system"].(string); got != "CUSTOM RULES\n\nbe concise" {
		t.Errorf("system = %q, want prompt prepended", got)
	}
}

func TestSystemPromptPrependedToClaudeBlockSystem(t *testing.T) {
	orig := []any{map[string]any{"type": "text", "text": "be concise"}}
	body := map[string]any{"system": orig}
	prependSystemPrompt(body, "CUSTOM RULES")
	blocks := body["system"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	first, _ := json.Marshal(blocks[0])
	var got map[string]any
	_ = json.Unmarshal(first, &got)
	if got["type"] != "text" || got["text"] != "CUSTOM RULES" {
		t.Errorf("first block = %s, want the prompt", first)
	}
	second, _ := json.Marshal(blocks[1])
	var origBack map[string]any
	_ = json.Unmarshal(second, &origBack)
	if origBack["text"] != "be concise" {
		t.Errorf("original block damaged: %s", second)
	}
}

func TestGeminiSafetyContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if translator.GeminiSafetyRequested(ctx) {
		t.Error("default must be off")
	}
	ctx = translator.WithGeminiSafety(ctx, true)
	if !translator.GeminiSafetyRequested(ctx) {
		t.Error("flag lost in round trip")
	}
}

func TestGeminiSafetySettingsInjectedAndPreserved(t *testing.T) {
	src := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	out, err := translator.ApplyGeminiSafetySettings(src)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ss, ok := got["safetySettings"].([]any)
	if !ok || len(ss) != 4 {
		t.Fatalf("safetySettings = %v, want 4 categories", got["safetySettings"])
	}
	if got["contents"] == nil {
		t.Error("original contents lost")
	}

	// A body that already carries safetySettings is not second-guessed.
	withOwn := []byte(`{"safetySettings":[{"category":"X","threshold":"BLOCK_ONLY"}],"contents":[]}`)
	out2, err := translator.ApplyGeminiSafetySettings(withOwn)
	if err != nil {
		t.Fatalf("apply second: %v", err)
	}
	if string(out2) != string(withOwn) {
		t.Error("existing safetySettings must be returned untouched")
	}
}
