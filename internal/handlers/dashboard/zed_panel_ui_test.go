package dashboard

import (
	"os"
	"strings"
	"testing"
)

// The dashboard is one embedded HTML file, so wiring the Zed panel is a string
// contract: the panel entry, the markup and the three handlers must all be
// present or the provider silently falls back to the generic paste form.
func TestProviderDetailExposesZedSignInPanel(t *testing.T) {
	raw, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatalf("read ui: %v", err)
	}
	ui := string(raw)
	for _, want := range []string{
		`{ ids: ["zed", "zd"], render: p => zedOAuthPanelHtml(p) }`,
		"function zedOAuthPanelHtml(providerId)",
		"startZedOAuth(",
		"finishZedOAuth(",
		"/api/oauth/zed/status?session=",
		`body: JSON.stringify({ session: zedOAuthCtx.session, callback })`,
		`let zedOAuthCtx = null;`,
	} {
		if !strings.Contains(ui, want) {
			t.Errorf("ui/index.html is missing %q", want)
		}
	}
}
