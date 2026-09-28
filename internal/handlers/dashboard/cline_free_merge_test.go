package dashboard

import "testing"

// The upstream catalogue omits the cline-free/* rows the free tier still
// serves (probed live: 458 rows, zero cline-free/*), so Import must merge
// the known free ids in — deduplicated, Free-badged, and untouched for
// ClinePass.
func TestMergeClineKnownFreeRows(t *testing.T) {
	live := []UpstreamModel{
		{ID: "anthropic/claude-sonnet-4.6", Name: "Claude Sonnet"},
		{ID: "cline-free/deepseek-v4.1-flash", Name: "already present"},
	}
	got := mergeClineKnownFreeRows(live, false)
	if len(got) != 3 {
		t.Fatalf("merged length = %d, want 3 (2 live + muse-spark, no duplicate deepseek)", len(got))
	}
	muse := false
	for _, m := range got {
		if m.ID == "cline-free/muse-spark-1.3-contributor" {
			muse = true
			if !m.IsFree {
				t.Error("known free row must carry the Free badge")
			}
		}
	}
	if !muse {
		t.Error("missing known free row muse-spark")
	}
	// ClinePass subscription namespace is untouched.
	if got2 := mergeClineKnownFreeRows(live, true); len(got2) != len(live) {
		t.Errorf("clinepass must be untouched: got %d rows, want %d", len(got2), len(live))
	}
}
