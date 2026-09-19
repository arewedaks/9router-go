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
