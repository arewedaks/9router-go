package executor

import (
	"encoding/json"
	"testing"
)

// muse-spark rejects max_output_tokens < 16 with a 400, so a caller asking for
// a very short answer ("say OK") got a hard failure from a model that would
// have served it. The clamp must raise it, not forward the rejection.
func TestNormalizeMuseSparkClampsMinOutputTokens(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any
	}{
		{"below floor (number)", 5, minMuseSparkOutputTokens},
		{"below floor (string)", "5", minMuseSparkOutputTokens},
		{"exactly floor", 16, 16},
		{"above floor untouched", 64, 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"model":             "muse-spark-1.2-contributor-free",
				"max_output_tokens": tc.in,
				"input":             []any{},
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := normalizeMuseSparkResponsesBody(body, "muse-spark-1.2-contributor-free")
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			// NumericTokenLimit returns an int; compare through the same helper
			// so the assertion is about the value, not its JSON type.
			n, ok := numericTokenLimit(got["max_output_tokens"])
			if !ok {
				t.Fatalf("max_output_tokens missing or unreadable: %v", got["max_output_tokens"])
			}
			want, _ := numericTokenLimit(tc.want)
			if n != want {
				t.Errorf("max_output_tokens = %d, want %d", n, want)
			}
		})
	}
}

// A request with no cap must not gain one: absent is not the same as "too small".
func TestNormalizeMuseSparkLeavesAbsentCapAlone(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.2-contributor-free","input":[]}`)
	out, err := normalizeMuseSparkResponsesBody(body, "muse-spark-1.2-contributor-free")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["max_output_tokens"]; present {
		t.Error("an absent cap must stay absent, not be invented")
	}
}
