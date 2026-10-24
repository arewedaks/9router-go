package providers

import (
	"strings"
	"testing"
)

// TestAntigravityUserAgentShape pins the two identities to the shapes the
// official clients send. A stale or invented version is the whole risk here:
// the backend keys its model catalogue on this header, so the strings must stay
// in the documented forms.
func TestAntigravityUserAgentShape(t *testing.T) {
	ide := AntigravityUserAgent(AntigravityProfileIDE)
	if !strings.HasPrefix(ide, "antigravity/") {
		t.Errorf("ide UA %q lacks the antigravity/ prefix", ide)
	}
	if strings.HasPrefix(ide, "antigravity/ide/") {
		t.Errorf("ide UA %q uses the old antigravity/ide/<v> flavor, which the backend 404s", ide)
	}
	// Bare `antigravity/<version>`: exactly one slash after the scheme-less host.
	if got := strings.Count(ide, "/"); got != 1 {
		t.Errorf("ide UA %q has %d slashes, want 1 (antigravity/<version>)", ide, got)
	}
	if !strings.HasPrefix(ide, "antigravity/"+AntigravityUserAgentIDEEdition()) {
		t.Errorf("ide UA %q does not carry the shared version constant", ide)
	}

	cli := AntigravityUserAgent(AntigravityProfileCLI)
	for _, want := range []string{
		"antigravity/cli/", "aidev_client", "os_type=", "arch=", "cl=", "auth_method=consumer",
	} {
		if !strings.Contains(cli, want) {
			t.Errorf("cli UA %q is missing %q", cli, want)
		}
	}
	if ide == cli {
		t.Error("ide and cli profiles emit the same UA — the setting would be a no-op")
	}
}

// TestAntigravityVersionIsCurrent guards against the exact drift this file was
// fixed for: two call sites quoting different builds of the same binary. If a
// version is bumped, bump the constant — never inline a literal elsewhere.
func TestAntigravityVersionIsCurrent(t *testing.T) {
	// Verified against Google's release feed (2026-10-23):
	//   antigravity 2.0 app 2.26.0 / CLI agy 1.4.6
	if antigravityIDEVersion != "2.26.0" {
		t.Errorf("IDE version = %q, want 2.26.0", antigravityIDEVersion)
	}
	if antigravityCLIVersion != "1.4.6" {
		t.Errorf("CLI version = %q, want 1.4.6", antigravityCLIVersion)
	}
}

// TestNormalizeClientProfile covers the legacy aliases so stored connections
// keep resolving.
func TestNormalizeClientProfile(t *testing.T) {
	for in, want := range map[string]AntigravityClientProfile{
		"ide": AntigravityProfileIDE, "IDE": AntigravityProfileIDE,
		"cli": AntigravityProfileCLI, "harness": AntigravityProfileCLI,
		"sdk": AntigravityProfileCLI, "": AntigravityProfileIDE,
		"garbage": AntigravityProfileIDE,
	} {
		if got := NormalizeAntigravityClientProfile(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
