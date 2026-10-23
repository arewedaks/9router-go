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
	// The IDE app identifies as plain antigravity/<version>. The old
	// "antigravity/ide/<ver> darwin/arm64" shape is what the Code Assist backend
	// answers HTTP 404 to for the Gemini 4 Argon tiered models.
	const want = "antigravity/2.23.0"
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

// The dropdown has to change the wire, or it is a control that does nothing.
// Guard both halves: the two profiles must differ, and neither may resurrect
// the retired ide/ flavor the Argon lanes reject.
func TestAntigravityProfilesDifferAndAvoidRetiredFlavor(t *testing.T) {
	ide, cli := AntigravityUserAgent(AntigravityProfileIDE), AntigravityUserAgent(AntigravityProfileCLI)
	if ide == cli {
		t.Errorf("IDE and CLI must differ, both resolved to %q — the profile dropdown would be inert", ide)
	}
	for name, ua := range map[string]string{"ide": ide, "cli": cli} {
		if strings.Contains(ua, "antigravity/ide/") {
			t.Errorf("%s UA %q uses the retired ide/ flavor", name, ua)
		}
		if !strings.HasPrefix(ua, "antigravity/") {
			t.Errorf("%s UA %q does not start with antigravity/", name, ua)
		}
	}
	if !strings.Contains(ide, "2.23.0") {
		t.Errorf("IDE UA %q must carry the current 2.23.0 version", ide)
	}
	if !strings.Contains(cli, "cli/1.4.0") || !strings.Contains(cli, "cl=1000494485") {
		t.Errorf("CLI UA %q is missing the 1.4.0 identity or the cl= marker", cli)
	}
}
