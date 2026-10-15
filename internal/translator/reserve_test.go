package translator

import (
	json "encoding/json/v2"
	"strconv"
	"testing"
)

// Regression: a tiered model thinks by default, so a small caller max_tokens
// used to be spent entirely on hidden reasoning and came back as
// finishReason=MAX_TOKENS with no text. Observed on the wire before the fix:
// max_tokens=100 -> 0 completion tokens, while omitting max_tokens -> 37.
func TestThinkingReservation(t *testing.T) {
	for _, mt := range []int{50, 100, 256, 512, 4096, 100000} {
		body := `{"model":"gemini-3.8-flash","max_tokens":` + strconv.Itoa(mt) +
			`,"messages":[{"role":"user","content":"hi"}]}`
		gem, err := TranslateOpenAIToGemini([]byte(body))
		if err != nil {
			t.Fatalf("max_tokens=%d: %v", mt, err)
		}
		var m map[string]any
		if err := json.Unmarshal(gem, &m); err != nil {
			t.Fatal(err)
		}
		gc, _ := m["generationConfig"].(map[string]any)
		tc, has := gc["thinkingConfig"].(map[string]any)
		if !has {
			t.Errorf("max_tokens=%d: no thinkingConfig, thinking would eat the budget (gc=%v)", mt, gc)
			continue
		}
		budget, _ := tc["thinkingBudget"].(float64)
		maxOut, _ := gc["maxOutputTokens"].(float64)
		if budget <= 0 {
			t.Errorf("max_tokens=%d: thinkingBudget=%.0f must be positive", mt, budget)
		}
		if budget >= maxOut {
			t.Errorf("max_tokens=%d: thinkingBudget=%.0f must stay below maxOutputTokens=%.0f (PR #3981)", mt, budget, maxOut)
		}
		if maxOut-budget < minAnswerTokens {
			t.Errorf("max_tokens=%d: only %.0f tokens left for the answer, want >= %d", mt, maxOut-budget, minAnswerTokens)
		}
		t.Logf("max_tokens=%-6d -> maxOutputTokens=%-7.0f thinkingBudget=%-6.0f answerRoom=%.0f", mt, maxOut, budget, maxOut-budget)
	}
}

// The caller's request struct must not be mutated by the reservation logic.
func TestThinkingReservationDoesNotMutateCaller(t *testing.T) {
	mt := 100
	req := &OpenAIRequest{Model: "gemini-3.8-flash", MaxTokens: &mt}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TranslateOpenAIToGemini(body); err != nil {
		t.Fatal(err)
	}
	if mt != 100 {
		t.Errorf("caller MaxTokens mutated: got %d, want 100", mt)
	}
}

// Antigravity serves its claude-* models by translating Gemini's thinkingBudget
// into Anthropic's thinking.enabled.budget_tokens, which rejects anything below
// 1024. A max_tokens=1024 request used to reserve 512 and 400 on every
// claude-opus-4-6-thinking call:
//
//	HTTP 400: thinking.enabled.budget_tokens: Input should be greater than or equal to 1024
//
// Three paths reach the wire, and all three must clear the floor: the default
// reservation, an explicit thinking_budget, and a Claude-style thinking block.
func TestThinkingBudgetFloorForClaudeModels(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"default reservation", `{"model":"claude-opus-4-6-thinking","max_tokens":1024,` +
			`"messages":[{"role":"user","content":"hi"}]}`},
		{"explicit thinking_budget", `{"model":"claude-opus-4-6-thinking","thinking_budget":100,` +
			`"max_tokens":2048,"messages":[{"role":"user","content":"hi"}]}`},
		{"claude thinking block", `{"model":"claude-opus-4-6-thinking",` +
			`"thinking":{"type":"enabled","budget_tokens":100},"max_tokens":2048,` +
			`"messages":[{"role":"user","content":"hi"}]}`},
	} {
		gem, err := TranslateOpenAIToGemini([]byte(tc.body))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(gem, &m); err != nil {
			t.Fatal(err)
		}
		gc, _ := m["generationConfig"].(map[string]any)
		tc2, _ := gc["thinkingConfig"].(map[string]any)
		budget, _ := tc2["thinkingBudget"].(float64)
		if budget < 1024 {
			t.Errorf("%s: thinkingBudget=%.0f is below Anthropic's 1024 floor; "+
				"claude-* Antigravity models 400 on this", tc.name, budget)
		}
		maxOut, _ := gc["maxOutputTokens"].(float64)
		if budget >= maxOut {
			t.Errorf("%s: thinkingBudget=%.0f must stay below maxOutputTokens=%.0f",
				tc.name, budget, maxOut)
		}
	}
}
