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
// Mirrors OmniRoute's antigravityClientProfile handling so operators can pick
// the identity that matches the credentials they imported.
type AntigravityClientProfile string

const (
	// AntigravityProfileIDE is the Antigravity IDE fingerprint (default).
	AntigravityProfileIDE AntigravityClientProfile = "ide"
	// AntigravityProfileCLI is the standalone Antigravity CLI fingerprint.
	AntigravityProfileCLI AntigravityClientProfile = "cli"
)

// ClientProfileKey is the connection-data key holding the selected profile.
const ClientProfileKey = "clientProfile"

// Version constants. The IDE version matches the value this fork already sent
// before profiles existed, so the default behaviour is byte-for-byte unchanged.
// The CLI version mirrors OmniRoute's embedded fallback (1.1.5).
const (
	antigravityIDEVersion = "2.11.0"
	antigravityCLIVersion = "1.1.5"

	antigravityOS  = "darwin"
	antigravityCPU = "arm64"
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

// ClientProfileFromData reads the profile out of a parsed connection-data map.
// It accepts the providerSpecificData wrapper used by OmniRoute and 9router as
// well as a top-level clientProfile key for flat connection blobs.
func ClientProfileFromData(data map[string]any) AntigravityClientProfile {
	if data == nil {
		return AntigravityProfileIDE
	}
	if psd, ok := data["providerSpecificData"].(map[string]any); ok {
		if v, present := psd[ClientProfileKey]; present {
			return NormalizeAntigravityClientProfile(v)
		}
	}
	if v, present := data[ClientProfileKey]; present {
		return NormalizeAntigravityClientProfile(v)
	}
	return AntigravityProfileIDE
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

// AntigravityUserAgent returns the User-Agent header for the given profile.
//
// ide → antigravity/ide/<ver> darwin/arm64
// cli → antigravity/cli/<ver> (aidev_client; os_type=darwin; arch=arm64; auth_method=consumer)
func AntigravityUserAgent(profile AntigravityClientProfile) string {
	if profile == AntigravityProfileCLI {
		return "antigravity/cli/" + antigravityCLIVersion +
			" (aidev_client; os_type=" + antigravityOS +
			"; arch=" + antigravityCPU +
			"; auth_method=consumer)"
	}
	return "antigravity/ide/" + antigravityIDEVersion + " " + antigravityOS + "/" + antigravityCPU
}

// AntigravityUserAgentForData is a convenience wrapper that resolves the
// profile from a connection-data map before building the header value.
func AntigravityUserAgentForData(data map[string]any) string {
	return AntigravityUserAgent(ClientProfileFromData(data))
}
