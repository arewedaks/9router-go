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
	// CLI verified against the live `agy` 1.5.5 client (2026-11-11) — the UA it
	// actually sends is
	//   antigravity/cli/1.5.5 (aidev_client; os_type=linux; arch=amd64;
	//                          cl=1017970541; auth_method=consumer)
	// IDE verified against the antigravity-hub update feed and the shipped
	// desktop bundle: 2.31.0 (Antigravity-x64/resources/app.asar package.json),
	// which the app passes to its language server as --override_ide_version.
	if antigravityCLIVersion != "1.5.5" {
		t.Errorf("CLI version = %q, want 1.5.5", antigravityCLIVersion)
	}
	if antigravityIDEVersion != "2.31.0" {
		t.Errorf("IDE version = %q, want 2.31.0 (antigravity-hub desktop app)", antigravityIDEVersion)
	}
	// The build marker must track the announced version. A `cl=` left behind
	// from an older build is the inconsistency this guard exists to catch.
	if antigravityClientID != "1017970541" {
		t.Errorf("CLI build marker cl = %q, want 1017970541 (agy 1.5.5)", antigravityClientID)
	}
	// os_type/arch describe the identities we emulate; agy ships per-platform,
	// but one stable pair is what keeps a single account from looking like it
	// roams between machines.
	if antigravityOS != "linux" || antigravityCPU != "amd64" {
		t.Errorf("platform = %s/%s, want linux/amd64", antigravityOS, antigravityCPU)
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

// TestAntigravityPlatformMatchesUserAgent pins the invariant that matters for
// anti-abuse: the metadata platform enum must describe the same client the
// User-Agent announces. A mismatch between the two is the contradiction that
// gets an account flagged, so the enum is derived from the UA constants rather
// than from the router's host.
func TestAntigravityPlatformMatchesUserAgent(t *testing.T) {
	ua := AntigravityUserAgent(AntigravityProfileCLI)
	enum := AntigravityPlatformEnum()

	want := map[string]int{"darwin/amd64": 1, "darwin/arm64": 2, "linux/amd64": 3, "linux/arm64": 4, "windows/amd64": 5}
	key := AntigravityPlatformOS() + "/" + AntigravityPlatformCPU()
	if got, ok := want[key]; ok && enum != got {
		t.Errorf("platform enum = %d for identity %s, want %d", enum, key, got)
	}
	// The UA must actually carry the identity the enum is derived from.
	if !strings.Contains(ua, "os_type="+AntigravityPlatformOS()) || !strings.Contains(ua, "arch="+AntigravityPlatformCPU()) {
		t.Errorf("UA %q does not announce the identity the platform enum is derived from", ua)
	}
	if enum == 0 {
		t.Errorf("platform enum is UNSPECIFIED for the configured identity %s", key)
	}
}
