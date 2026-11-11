package providers

import (
	"strings"
)

// AntigravityClientProfile selects which official client identity 9router
// presents to Google's Cloud Code / Antigravity backend.
//
// Google ships two clients that share the same OAuth client id and the same
// Code Assist backend but advertise different User-Agent fingerprints:
//
//   - "ide" — the Antigravity IDE desktop app (default, historical behaviour).
//   - "cli" — the standalone Antigravity CLI (`agy`).
//
// The profile is a provider-wide setting (SettingsData.AntigravityClientProfile):
// it names the client we imitate, which is the same for every account on the
// provider. It deliberately does not live on the connection, so accounts cannot
// drift onto different identities.
type AntigravityClientProfile string

const (
	// AntigravityProfileIDE is the Antigravity IDE fingerprint (default).
	AntigravityProfileIDE AntigravityClientProfile = "ide"
	// AntigravityProfileCLI is the standalone Antigravity CLI fingerprint.
	AntigravityProfileCLI AntigravityClientProfile = "cli"
)

// Version constants for the two clients we imitate. Both are kept here, in one
// place, because a second copy is how the same binary ends up presenting two
// different identities — see the note on AntigravityUserAgent.
//
// Google ships THREE products on separate update feeds, all keyed by
// `antigravity-*-auto-updater-974169037036.us-central1.run.app/releases`:
//
//	antigravity-hub : 2.31.0  — the desktop app (productName "Antigravity")
//	antigravity-ide : 2.6.0   — the standalone IDE build
//	antigravity-cli : 1.5.5   — the `agy` CLI
//
// The IDE profile below describes the *desktop* client. Its own bundle spawns
// resources/bin/language_server with `--override_ide_version <app.getVersion()>`
// and `--override_user_agent_name antigravity`, i.e. `antigravity/<appVersion>`;
// the desktop app reports 2.31.0 (read from the shipped package.json in
// Antigravity-x64/resources/app.asar, 2026-11-11).
//
// Both desktop flavours answer the same 23-model catalogue, so the version is
// not what gates models — the client class is. Measured live against
// cloudcode-pa with the same token:
//
//	ide (any 2.x) : 23 models
//	cli (1.5.5)   : 27 models  (+gemini-4-argon-low/medium/high,
//	                            +gemini-4-carbon-disabled)
//
// The CLI numbers were captured from a real `agy` 1.5.5 run, not inferred:
//
//	User-Agent: antigravity/cli/1.5.5 (aidev_client; os_type=linux;
//	            arch=amd64; cl=1017970541; auth_method=consumer)
//
// A lying `cl=` is the risk here: Cloud Code reads it as a client-build marker,
// and a build id that does not match the announced version is exactly the sort
// of inconsistency that gets an account flagged (this file previously pinned
// CLI 1.4.6 while the installed binary was 1.5.5).
const (
	antigravityIDEVersion = "2.31.0"
	antigravityCLIVersion = "1.5.5"

	antigravityOS  = "linux"
	antigravityCPU = "amd64"

	// antigravityClientID is the `cl=` parameter the official CLI carries in its
	// User-Agent. The Cloud Code backend reads it as a client-build marker; an
	// identity without it is still answered, but the Argon lanes expect the
	// full shape.
	antigravityClientID = "1017970541"
)

// NormalizeAntigravityClientProfile coerces an arbitrary stored value into a
// known profile. Unknown or absent values fall back to "ide".
//
// The legacy synthetic names "harness"/"sdk" predate the official CLI profile
// and are still readable so previously stored connections keep working; new
// writes never produce them.
func NormalizeAntigravityClientProfile(value any) AntigravityClientProfile {
	s, ok := value.(string)
	if !ok {
		return AntigravityProfileIDE
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "cli", "harness", "sdk":
		return AntigravityProfileCLI
	case "ide":
		return AntigravityProfileIDE
	default:
		return AntigravityProfileIDE
	}
}

// IsAntigravityClientProfile reports whether v is an accepted profile value
// (including legacy aliases) after normalization. Used to validate API input.
func IsAntigravityClientProfile(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ide", "cli", "harness", "sdk":
		return true
	default:
		return false
	}
}

// String returns the stored form of the profile. Quota callers carry it as a
// plain string so the tracker package does not have to import providers.
func (p AntigravityClientProfile) String() string { return string(p) }

// AntigravityUserAgent returns the User-Agent header for the given profile.
//
// ide → antigravity/<ver>                    (the IDE app's own shape)
// cli → antigravity/cli/<ver> (aidev_client; os_type=…; arch=…; cl=…;
//
//	auth_method=consumer)               (the standalone `agy` CLI)
//
// The two are genuinely different clients and stay different strings: the
// profile setting would be a no-op otherwise. What changed is the ide shape.
// It used to be `antigravity/ide/<ver> darwin/arm64` — a flavor the Code Assist
// backend answers HTTP 404 to for the Gemini 4 Argon tiered models. The IDE app
// itself identifies as plain `antigravity/<version>` (see the MITM handler,
// which sends exactly that when it rewrites the metadata block), so the ide
// profile now emits the real IDE shape instead of being folded into the CLI.
func AntigravityUserAgent(profile AntigravityClientProfile) string {
	if profile == AntigravityProfileCLI {
		return "antigravity/cli/" + antigravityCLIVersion +
			" (aidev_client; os_type=" + antigravityOS +
			"; arch=" + antigravityCPU +
			"; cl=" + antigravityClientID +
			"; auth_method=consumer)"
	}
	return "antigravity/" + antigravityIDEVersion
}

// AntigravityUserAgentIDEEdition returns just the version component of the IDE
// User-Agent, for callers that build the header themselves: the MITM handler
// writes `antigravity/<version>` and the matching metadata block.
func AntigravityUserAgentIDEEdition() string { return antigravityIDEVersion }

// AntigravityPlatformOS returns the os_type component the CLI fingerprint
// advertises. Exported so the lcaMetadata builder can derive the Cloud Code
// platform enum from the *client identity we present* rather than from the host
// the router happens to run on.
func AntigravityPlatformOS() string { return antigravityOS }

// AntigravityPlatformCPU returns the arch component of the CLI identity.
func AntigravityPlatformCPU() string { return antigravityCPU }

// AntigravityPlatformEnum maps the client identity we present to the
// Google Cloud Code ClientMetadata.Platform enum used by the loadCodeAssist and
// onboardUser RPCs.
//
//	1 PLATFORM_DARWIN_AMD64     2 PLATFORM_DARWIN_ARM64
//	3 PLATFORM_LINUX_AMD64      4 PLATFORM_LINUX_ARM64
//	5 PLATFORM_WINDOWS_AMD64    0 PLATFORM_UNSPECIFIED
//
// Derived from the emulated identity, NOT from runtime.GOOS/GOARCH. The enum
// describes the *client*, and the client we imitate announces itself in the
// User-Agent (`os_type=…; arch=…`). Deriving it from the host would be a
// contradiction the moment the two disagree — e.g. a linux/amd64 server
// announcing `platform=DARWIN_ARM64` while its own UA says `os_type=linux`,
// which is exactly the inconsistency anti-abuse scoring looks for.
//
// Upstream (luqman-v1/9router-go 5445d055) made this host-derived and called the
// resulting mismatch a trigger. It is not: the server ignores the field
// entirely (verified — byte-identical loadCodeAssist responses for platform 2,
// 3, 5, 999, and omitted), and the real `agy` client sends no `platform` at all.
// Keeping the value consistent with our own UA is the only thing that matters.
func AntigravityPlatformEnum() int {
	switch antigravityOS {
	case "darwin":
		if antigravityCPU == "arm64" {
			return 2
		}
		return 1
	case "linux":
		if antigravityCPU == "arm64" {
			return 4
		}
		return 3
	case "windows":
		return 5
	}
	return 0
}
