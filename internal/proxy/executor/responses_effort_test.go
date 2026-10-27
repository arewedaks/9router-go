package executor

import "testing"

// "max" is a real reasoning level on GPT-5.6 / GPT-6; clamping it to "xhigh"
// silently downgraded every request that asked for it. Other models reject
// "max", so they keep the old mapping.
func TestResponsesEffortKeepsMaxForGPT56And6(t *testing.T) {
	cases := []struct {
		model, in, want string
	}{
		{"gpt-5.6-luna", "max", "max"},
		{"gpt-5.6-sol", "max", "max"},
		{"gpt-6-sol", "max", "max"},
		{"gpt-6-astra", "max", "max"},
		{"gpt-6", "max", "max"},
		{"gpt-5.3-codex", "max", "xhigh"},
		{"claude-sonnet-4.5", "max", "xhigh"},
		{"deepseek-v4", "max", "xhigh"},
		{"gpt-5.6-luna", "high", "high"},
		{"gpt-6-sol", "low", "low"},
		{"gpt-5.6-luna", "xhigh", "xhigh"},
	}
	for _, tc := range cases {
		if got := responsesEffort(tc.in, tc.model); got != tc.want {
			t.Errorf("responsesEffort(%q, %q) = %q, want %q", tc.in, tc.model, got, tc.want)
		}
	}
}
