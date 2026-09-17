package executor

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

// TestCodebuddySeedsRequiredSystemPrompt pins the fix for
// `400 code 11128 "Illegal API invocation from an unapproved channel"`.
//
// The gateway fingerprints the calling client on the shape of `messages`: the
// first message has to be the system prompt `You are CodeBuddy Code.`. An agent
// CLI sends its own system prompt (or none) and a bare-string user content, so
// every request through the router was refused. Confirmed against the live
// gateway, which answers `{"code":11128,"msg":"first message is not system
// prompt"}` for the old shape and 200 for this one.
func TestCodebuddySeedsRequiredSystemPrompt(t *testing.T) {
	in := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"You are a helpful assistant."},{"role":"user","content":"hello"}]}`)
	out, err := transformCodebuddyBody(in)
	if err != nil {
		t.Fatalf("transformCodebuddyBody: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msgs, _ := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3 (seed + caller's two)", len(msgs))
	}

	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "You are CodeBuddy Code." {
		t.Errorf("first message = %v, want the seeded CodeBuddy system prompt", first)
	}
	// The caller's own system prompt must survive behind the seed: dropping it
	// would silently lose agent instructions.
	second, _ := msgs[1].(map[string]any)
	if second["role"] != "system" || second["content"] != "You are a helpful assistant." {
		t.Errorf("second message = %v, want the caller's system prompt preserved", second)
	}
	// stream is forced because the gateway rejects non-stream (code 11101).
	if got["stream"] != true {
		t.Errorf("stream = %v, want forced true", got["stream"])
	}
}

// TestCodebuddyUserContentBecomesTypedBlocks pins the second half of the client
// fingerprint: user content has to be a typed block array, not a bare string.
func TestCodebuddyUserContentBecomesTypedBlocks(t *testing.T) {
	in := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	out, err := transformCodebuddyBody(in)
	if err != nil {
		t.Fatalf("transformCodebuddyBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := got["messages"].([]any)
	// seed + three caller messages
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}

	check := func(idx int, wantText string) {
		m, _ := msgs[idx].(map[string]any)
		blocks, ok := m["content"].([]any)
		if !ok {
			t.Fatalf("message %d content = %T, want typed block array", idx, m["content"])
		}
		b, _ := blocks[0].(map[string]any)
		if b["type"] != "text" || b["text"] != wantText {
			t.Errorf("message %d block = %v, want text block %q", idx, b, wantText)
		}
	}
	check(1, "hello")
	check(3, "again")

	// Assistant content is left alone — only user turns are typed on the wire by
	// the official client, and rewriting it risks breaking tool-call round trips.
	assistant, _ := msgs[2].(map[string]any)
	if assistant["content"] != "hi" {
		t.Errorf("assistant content = %v, want untouched string", assistant["content"])
	}
}

// TestCodebuddySeedIsNotDuplicated keeps the seed idempotent: a client that
// already ships the prompt must not end up with two copies, which the gateway
// would read as a second, different client.
func TestCodebuddySeedIsNotDuplicated(t *testing.T) {
	in := []byte(`{"model":"glm-5.2","messages":[{"role":"system","content":"You are CodeBuddy Code."},{"role":"user","content":"hi"}]}`)
	out, err := transformCodebuddyBody(in)
	if err != nil {
		t.Fatalf("transformCodebuddyBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := got["messages"].([]any)

	seedCount := 0
	for _, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m["content"] == "You are CodeBuddy Code." {
			seedCount++
		}
	}
	if seedCount != 1 {
		t.Errorf("seed prompt appears %d times, want exactly 1", seedCount)
	}
}

// TestCodebuddyReasoningSummary mirrors the CLI: an explicit reasoning_effort
// makes the gateway surface reasoning, and "none"/"off" is dropped because the
// gateway has no such level.
func TestCodebuddyReasoningSummary(t *testing.T) {
	for _, tc := range []struct {
		eff         string
		wantSummary any
	}{
		{"high", "auto"},
		{"none", nil},
		{"off", nil},
	} {
		in := []byte(`{"model":"glm-5.2","reasoning_effort":"` + tc.eff + `","messages":[{"role":"user","content":"hi"}]}`)
		out, err := transformCodebuddyBody(in)
		if err != nil {
			t.Fatalf("eff=%s: %v", tc.eff, err)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("eff=%s unmarshal: %v", tc.eff, err)
		}
		if got["reasoning_summary"] != tc.wantSummary {
			t.Errorf("eff=%s: reasoning_summary = %v, want %v", tc.eff, got["reasoning_summary"], tc.wantSummary)
		}
		if _, present := got["reasoning_effort"]; present && tc.eff != "high" {
			t.Errorf("eff=%s: reasoning_effort should be removed, got %v", tc.eff, got["reasoning_effort"])
		}
	}
}

// TestCodebuddyEmptyMessagesStillSeeded guards the degenerate call: a request
// with no messages at all must still carry the seed, or it is refused.
func TestCodebuddyEmptyMessagesStillSeeded(t *testing.T) {
	out, err := transformCodebuddyBody([]byte(`{"model":"glm-5.2","messages":[]}`))
	if err != nil {
		t.Fatalf("transformCodebuddyBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want the seed alone", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["content"] != "You are CodeBuddy Code." {
		t.Errorf("first message = %v, want the seed", first)
	}
}

// TestCodebuddySanitizesCompetitorPromptFingerprint verifies that Claude Code's
// official system prompt fingerprint ("official CLI for Claude") is sanitized so
// the gateway doesn't reject it with 11128.
func TestCodebuddySanitizesCompetitorPromptFingerprint(t *testing.T) {
	in := []byte(`{"model":"glm-5.2","messages":[
		{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude.\nHere are instructions..."},
		{"role":"user","content":"hello"}
	]}`)
	out, err := transformCodebuddyBody(in)
	if err != nil {
		t.Fatalf("transformCodebuddyBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3", len(msgs))
	}
	second, _ := msgs[1].(map[string]any)
	secContent, _ := second["content"].(string)
	if claudeCodeBlockedPromptRegex.MatchString(secContent) {
		t.Errorf("second message still contains blocked fingerprint: %q", secContent)
	}
	wantSub := "Anthropic's CLI for Claude"
	if !strings.Contains(secContent, wantSub) {
		t.Errorf("second message missing sanitized substring %q: %q", wantSub, secContent)
	}
}

