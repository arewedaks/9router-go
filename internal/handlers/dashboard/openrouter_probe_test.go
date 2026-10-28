package dashboard

import (
	"strings"
	"testing"
)

// A valid key with a drained wallet is a valid key. The chip must stay green —
// the old chat probe answered 402 for openai/gpt-4o-mini and turned the whole
// row red while the same key served :free models with 200.
func TestOpenrouterBalanceNote(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantText string // substring, "" means no note
	}{
		{
			name:     "free tier, never purchased",
			body:     `{"data":{"is_free_tier":true,"limit":null,"limit_remaining":null,"usage":0.239150728}}`,
			wantText: "free tier",
		},
		{
			name:     "limit spent",
			body:     `{"data":{"is_free_tier":false,"limit":10,"limit_remaining":0,"usage":10}}`,
			wantText: "limit is spent",
		},
		{
			name:     "funded account, nothing to say",
			body:     `{"data":{"is_free_tier":false,"limit":null,"limit_remaining":null,"usage":1.5}}`,
			wantText: "",
		},
		{
			name:     "credit remaining",
			body:     `{"data":{"is_free_tier":false,"limit":10,"limit_remaining":4.5,"usage":5.5}}`,
			wantText: "",
		},
		{name: "not json", body: "not json", wantText: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openrouterBalanceNote(tc.body)
			if tc.wantText == "" {
				if got != "" {
					t.Errorf("note = %q, want none", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantText) {
				t.Errorf("note = %q, want it to mention %q", got, tc.wantText)
			}
		})
	}
}

// The note must never arrive as a warning: warning paints the chip amber, which
// is the exact bug being fixed.
func TestOpenrouterNoteDoesNotDowngradeChip(t *testing.T) {
	res := connectionTestResult{Valid: true}
	res.Note = openrouterBalanceNote(`{"data":{"is_free_tier":true,"limit":null,"usage":0.24}}`)
	if res.Note == "" {
		t.Fatal("expected a note for a free-tier key")
	}
	if res.Warning != "" {
		t.Errorf("warning = %q, want empty so the chip stays green", res.Warning)
	}
	if !res.Valid {
		t.Error("Valid was cleared")
	}
}
