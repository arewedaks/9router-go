package providers

import "testing"

func TestNormalizeAntigravityClientProfile(t *testing.T) {
	cases := []struct {
		in   any
		want AntigravityClientProfile
	}{
		{"ide", AntigravityProfileIDE},
		{"IDE", AntigravityProfileIDE},
		{"  ide  ", AntigravityProfileIDE},
		{"cli", AntigravityProfileCLI},
		{"CLI", AntigravityProfileCLI},
		{"harness", AntigravityProfileCLI}, // legacy alias
		{"sdk", AntigravityProfileCLI},     // legacy alias
		{"", AntigravityProfileIDE},
		{"garbage", AntigravityProfileIDE},
		{nil, AntigravityProfileIDE},
		{42, AntigravityProfileIDE},
		{true, AntigravityProfileIDE},
	}
	for _, c := range cases {
		if got := NormalizeAntigravityClientProfile(c.in); got != c.want {
			t.Errorf("NormalizeAntigravityClientProfile(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClientProfileFromData_NilAndEmpty(t *testing.T) {
	if got := ClientProfileFromData(nil); got != AntigravityProfileIDE {
		t.Errorf("nil data: got %q, want ide", got)
	}
	if got := ClientProfileFromData(map[string]any{}); got != AntigravityProfileIDE {
		t.Errorf("empty data: got %q, want ide", got)
	}
}

func TestClientProfileFromData_TopLevel(t *testing.T) {
	data := map[string]any{"clientProfile": "cli"}
	if got := ClientProfileFromData(data); got != AntigravityProfileCLI {
		t.Errorf("top-level cli: got %q, want cli", got)
	}
}

func TestClientProfileFromData_ProviderSpecificData(t *testing.T) {
	data := map[string]any{
		"providerSpecificData": map[string]any{"clientProfile": "cli"},
	}
	if got := ClientProfileFromData(data); got != AntigravityProfileCLI {
		t.Errorf("nested cli: got %q, want cli", got)
	}
}

// providerSpecificData must win over a stray top-level key, matching OmniRoute
// where the wrapper is the source of truth.
func TestClientProfileFromData_NestedWinsOverTopLevel(t *testing.T) {
	data := map[string]any{
		"clientProfile":        "ide",
		"providerSpecificData": map[string]any{"clientProfile": "cli"},
	}
	if got := ClientProfileFromData(data); got != AntigravityProfileCLI {
		t.Errorf("nested should win: got %q, want cli", got)
	}
}

func TestIsAntigravityClientProfile(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{"ide", true},
		{"cli", true},
		{"harness", true},
		{"sdk", true},
		{"IDE", true},
		{"garbage", false},
		{"", false},
		{nil, false},
		{42, false},
	}
	for _, c := range cases {
		if got := IsAntigravityClientProfile(c.in); got != c.want {
			t.Errorf("IsAntigravityClientProfile(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAntigravityUserAgent_IDE(t *testing.T) {
	const want = "antigravity/ide/2.11.0 darwin/arm64"
	if got := AntigravityUserAgent(AntigravityProfileIDE); got != want {
		t.Errorf("IDE UA = %q, want %q", got, want)
	}
}

func TestAntigravityUserAgent_CLI(t *testing.T) {
	const want = "antigravity/cli/1.1.5 (aidev_client; os_type=darwin; arch=arm64; auth_method=consumer)"
	if got := AntigravityUserAgent(AntigravityProfileCLI); got != want {
		t.Errorf("CLI UA = %q, want %q", got, want)
	}
}

// An unknown profile string must behave exactly like the historical IDE
// default so existing connections keep working unchanged.
func TestAntigravityUserAgent_UnknownFallsBackToIDE(t *testing.T) {
	const want = "antigravity/ide/2.11.0 darwin/arm64"
	if got := AntigravityUserAgent(AntigravityClientProfile("bogus")); got != want {
		t.Errorf("unknown profile UA = %q, want %q", got, want)
	}
}

func TestAntigravityUserAgentForData(t *testing.T) {
	if got, want := AntigravityUserAgentForData(nil), AntigravityUserAgent(AntigravityProfileIDE); got != want {
		t.Errorf("nil data UA = %q, want %q", got, want)
	}
	data := map[string]any{"clientProfile": "cli"}
	if got, want := AntigravityUserAgentForData(data), AntigravityUserAgent(AntigravityProfileCLI); got != want {
		t.Errorf("cli data UA = %q, want %q", got, want)
	}
}
