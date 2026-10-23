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

// Version constants. Bumped to the pair upstream ships with Gemini 4 Argon:
// the 'ide' flavor was answered HTTP 404 for that model family, and the CLI
// identity is the one the tiered Argon lanes accept (upstream
// decolua/9router#4934). The IDE version is kept separately because the MITM
// handlers and quota paths still emit it in their metadata block.
const (
	antigravityIDEVersion = "2.23.0"
	antigravityCLIVersion = "1.4.0"

	antigravityOS  = "windows"
	antigravityCPU = "amd64"

	// antigravityClientID is the `cl=` parameter the official CLI carries in its
	// User-Agent. The Cloud Code backend reads it as a client-build marker; an
	// identity without it is still answered, but the Argon lanes expect the
	// full shape.
	antigravityClientID = "1000494485"
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
//        auth_method=consumer)               (the standalone `agy` CLI)
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
