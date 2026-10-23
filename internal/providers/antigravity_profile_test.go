package providers

import (
	"strings"
	"testing"
)

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
	// The IDE profile emits the CLI identity: upstream unified them after the
	// 'ide' flavor was answered HTTP 404 for Gemini 4 Argon tiered models.
	const want = "antigravity/cli/1.4.0 (aidev_client; os_type=windows; arch=amd64; cl=1000494485; auth_method=consumer)"
	if got := AntigravityUserAgent(AntigravityProfileIDE); got != want {
		t.Errorf("IDE UA = %q, want %q", got, want)
	}
}

func TestAntigravityUserAgent_CLI(t *testing.T) {
	const want = "antigravity/cli/1.4.0 (aidev_client; os_type=windows; arch=amd64; cl=1000494485; auth_method=consumer)"
	if got := AntigravityUserAgent(AntigravityProfileCLI); got != want {
		t.Errorf("CLI UA = %q, want %q", got, want)
	}
}

// An unknown profile string must behave exactly like the IDE default so
// existing connections keep working unchanged.
func TestAntigravityUserAgent_UnknownFallsBackToIDE(t *testing.T) {
	if got, want := AntigravityUserAgent(AntigravityClientProfile("bogus")), AntigravityUserAgent(AntigravityProfileIDE); got != want {
		t.Errorf("unknown profile UA = %q, want the IDE default %q", got, want)
	}
}

// Both profiles must resolve to one identity. A split would mean the operator's
// profile setting silently changes which models the account can reach.
func TestAntigravityProfilesShareOneIdentity(t *testing.T) {
	ide, cli := AntigravityUserAgent(AntigravityProfileIDE), AntigravityUserAgent(AntigravityProfileCLI)
	if ide != cli {
		t.Errorf("IDE UA %q and CLI UA %q must be identical", ide, cli)
	}
	for _, want := range []string{"antigravity/cli/1.4.0", "cl=1000494485", "auth_method=consumer"} {
		if !strings.Contains(cli, want) {
			t.Errorf("UA %q is missing %q", cli, want)
		}
	}
	if strings.Contains(cli, "antigravity/ide/") {
		t.Error("the ide/ flavor is refused by the Argon lanes and must not be emitted")
	}
}
