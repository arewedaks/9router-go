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
