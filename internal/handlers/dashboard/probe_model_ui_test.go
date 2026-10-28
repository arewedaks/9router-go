package dashboard

import (
	"os"
	"strings"
	"testing"
)

// The test-model field has to be reachable from the provider page: an override
// the operator cannot edit is the same as no override. The dashboard ships as a
// single embedded file, so this pins the wiring the browser needs.
func TestProviderDetailExposesEditableTestModel(t *testing.T) {
	raw, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatalf("read ui/index.html: %v", err)
	}
	html := string(raw)

	for _, needle := range []string{
		"function testModelControl(d, strat)", // renders the input next to account rotation
		`class="acct-test-model"`,             // the field itself
		"function setTestModel(provider, value)",
		"{ testModel: model }",          // saved through the settings endpoint
		"${testModelControl(d, strat)}", // actually placed in the detail panel
		"d.probeModelDefault",           // placeholder shows what "empty" resolves to
	} {
		if !strings.Contains(html, needle) {
			t.Errorf("ui/index.html is missing %q", needle)
		}
	}
}
