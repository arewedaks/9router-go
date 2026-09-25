package dashboard

import (
	"strings"
	"testing"
)

// The Add Provider picker (the "+ Add Provider" modal in the top toolbar)
// lists only providers that can be activated by choosing them: OAuth, Free
// Tier, API Key, and Web Cookie.
//
// "custom" (OpenAI-compatible and Anthropic-compatible) must NOT be offered in
// this picker. A compatible endpoint is a node created with a base URL, prefix,
// and chat path through the dedicated "+ OpenAI Compatible" and "+ Anthropic
// Compatible" buttons on the page (openCompatModal). Offering them in the
// picker was confusing because picking one navigated to the dummy provider
// detail rather than creating an endpoint.
func TestAddProviderPickerExcludesCompatibleEndpoints(t *testing.T) {
	body := readEmbeddedUI(t)

	start := strings.Index(body, "const PICKER_GROUPS = [")
	if start == -1 {
		t.Fatal("PICKER_GROUPS definition not found")
	}
	end := strings.Index(body[start:], "];")
	if end == -1 {
		t.Fatal("PICKER_GROUPS closing not found")
	}
	block := body[start : start+end]

	if strings.Contains(block, `key: "custom"`) {
		t.Error("PICKER_GROUPS still includes the 'custom' category; compatible endpoints " +
			"must be added via the dedicated buttons on the page, not the picker")
	}

	// The legitimate categories must still be present.
	for _, key := range []string{`key: "oauth"`, `key: "freeTier"`, `key: "apikey"`, `key: "webCookie"`} {
		if !strings.Contains(block, key) {
			t.Errorf("missing expected picker category: %s", key)
		}
	}
}
