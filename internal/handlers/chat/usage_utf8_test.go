package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"9router/proxy/internal/constants"
)

// The dashboard's request history is written by marshalling the extracted
// messages. encoding/json/v2 rejects invalid UTF-8 on marshal where v1 silently
// substituted U+FFFD, so a single corrupt message used to fail the whole write
// with `invalid UTF-8 within "/request/messages/N/content"` and drop the record.
func TestExtractRequestMessagesSurvivesCutRune(t *testing.T) {
	// A message whose 500th byte lands inside a 4-byte rune. Under a byte-offset
	// cut this leaves a truncated sequence, which json/v2 refuses to marshal.
	body := []byte(`{"messages":[{"role":"user","content":"` +
		strings.Repeat("a", constants.MaxMessageContentLen-1) + `🎉 tail"}]}`)

	msgs := extractRequestMessages(body)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	content := msgs[0]["content"]
	if !utf8.ValidString(content) {
		t.Fatalf("truncation produced invalid UTF-8: %q", content)
	}

	// This is the exact marshalling the usage logger does. It must not fail.
	if _, err := json.Marshal(map[string]any{
		"request": map[string]any{"messages": msgs},
	}); err != nil {
		t.Fatalf("marshalling request detail failed: %v", err)
	}
}

// A client can send raw invalid UTF-8 inside a JSON string. Unmarshal rejects the
// body outright, so extraction yields nothing rather than a record that fails on
// the way back out. This pins that the two failure modes stay distinct: a
// malformed BODY means no history row, while a malformed SLICE of a valid body
// must still produce a row.
func TestExtractRequestMessagesRejectsRawInvalidBytes(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"halo ` + "\xff\xfe" + ` dunia"}]}`)

	if msgs := extractRequestMessages(body); msgs != nil {
		t.Fatalf("expected no messages from a body json/v2 rejects, got %v", msgs)
	}

	// The escaped form of the same text is valid JSON, and must survive intact.
	escaped := []byte(`{"messages":[{"role":"user","content":"halo \ufffd dunia"}]}`)
	msgs := extractRequestMessages(escaped)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if !utf8.ValidString(msgs[0]["content"]) {
		t.Fatalf("escaped content not valid UTF-8: %q", msgs[0]["content"])
	}
}

// The byte budget is still honoured, so a long message cannot inflate the row.
func TestTruncateUTF8RespectsLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		limit int
	}{
		{"ascii over limit", strings.Repeat("x", 100), 50},
		{"runes over limit", strings.Repeat("🎉", 100), 50},
		{"mixed", strings.Repeat("a🎉", 100), 51},
		{"under limit", "short", 50},
		{"exactly at limit", strings.Repeat("y", 50), 50},
	} {
		got := truncateUTF8(tc.in, tc.limit)
		if !utf8.ValidString(got) {
			t.Errorf("%s: invalid UTF-8 result %q", tc.name, got)
		}
		if len(got) > tc.limit+3 { // +3 for the "..." suffix
			t.Errorf("%s: result %d bytes exceeds limit %d (+3)", tc.name, len(got), tc.limit)
		}
		if len(tc.in) <= tc.limit && got != tc.in {
			t.Errorf("%s: short input was modified: %q -> %q", tc.name, tc.in, got)
		}
		if len(tc.in) > tc.limit && !strings.HasSuffix(got, "...") {
			t.Errorf("%s: truncated result missing ellipsis: %q", tc.name, got)
		}
	}
}
