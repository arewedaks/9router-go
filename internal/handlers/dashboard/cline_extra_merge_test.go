package dashboard

import "testing"

// The upstream listing omits ids the gateway still serves (probed live:
// cline-free/* rows answer 200, pixel-canary answers 402 vs 404 for a bogus
// id), so Import must merge the known rows in — deduplicated, Free-badged
// where free, and untouched for ClinePass.
func TestMergeClineExtraCatalogRows(t *testing.T) {
	live := []UpstreamModel{
		{ID: "anthropic/claude-sonnet-4.6", Name: "Claude Sonnet"},
		{ID: "cline-free/deepseek-v4.1-flash", Name: "already present"},
	}
	got := mergeClineExtraCatalogRows(live, false)
	if len(got) != 5 {
		t.Fatalf("merged length = %d, want 5 (2 live + muse-spark + 2 pixel-canary, no duplicate deepseek)", len(got))
	}
	seenFree, seenCanary := false, false
	for _, m := range got {
		if m.ID == "cline-free/muse-spark-1.3-contributor" && m.IsFree {
			seenFree = true
		}
		if m.ID == "google/pixel-canary" {
			seenCanary = true
			if m.IsFree {
				t.Error("pixel-canary is a paid model and must not carry the Free badge")
			}
		}
	}
	if !seenFree {
		t.Error("missing known free row muse-spark (Free-badged)")
	}
	if !seenCanary {
		t.Error("missing pixel-canary row")
	}
	// ClinePass subscription namespace is untouched.
	if got2 := mergeClineExtraCatalogRows(live, true); len(got2) != len(live) {
		t.Errorf("clinepass must be untouched: got %d rows, want %d", len(got2), len(live))
	}
}
