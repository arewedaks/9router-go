package executor

import "testing"

// Compaction is addressed by appending a path segment to the provider's
// Responses URL, mirroring the reference implementation's buildUrl. The Codex
// base already ends in /responses, so the result must not gain a second one.
func TestCodexCompactURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
		want string
	}{
		{"codex base", "https://chatgpt.com/backend-api/codex/responses", "https://chatgpt.com/backend-api/codex/responses/compact"},
		{"trailing slash", "https://chatgpt.com/backend-api/codex/responses/", "https://chatgpt.com/backend-api/codex/responses/compact"},
		{"already compact", "https://chatgpt.com/backend-api/codex/responses/compact", "https://chatgpt.com/backend-api/codex/responses/compact"},
		{"no responses segment", "https://example.test/v1", "https://example.test/v1/compact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexCompactURL(tc.base); got != tc.want {
				t.Errorf("codexCompactURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

// Applying the transform twice must not daisy-chain suffixes, since a retry path
// may run the request through the same code again.
func TestCodexCompactURLIsIdempotent(t *testing.T) {
	base := "https://chatgpt.com/backend-api/codex/responses"
	once := codexCompactURL(base)
	if twice := codexCompactURL(once); twice != once {
		t.Errorf("second application changed the URL: %q -> %q", once, twice)
	}
}
