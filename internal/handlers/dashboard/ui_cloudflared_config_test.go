package dashboard

import (
	"strings"
	"testing"
)

// The Cloudflare card's config generator must emit a file the operator can
// paste as-is. Two placeholder bugs made that untrue:
//
//  1. hostname was hardcoded to router.example.com even with a domain saved,
//     so the "ready to paste" block always needed hand-editing.
//  2. --db-path named a path that did not exist on a real install, which
//     starts the process on a fresh, empty database -- data that looks lost.
//
// Both are silent failures, so they are pinned here rather than left to the
// next reader of the generator.
func TestCloudflaredConfigUsesRealDomainAndOmittedDBPath(t *testing.T) {
	ui := readEmbeddedUI(t)

	// The source of the generator line is:  "  - hostname: " + host + "\n"
	// so a hardcoded placeholder shows up as a literal string in that slot.
	if strings.Contains(ui, "\"  - hostname: router.example.com") {
		t.Error("the cloudflared hostname is hardcoded: it must be derived from " +
			"the saved publicBaseURL so the generated file needs no editing")
	}
	if !strings.Contains(ui, "\"  - hostname: \" + host + \"\\n\"") {
		t.Error("the hostname line is not built from the resolved host variable")
	}
	if !strings.Contains(ui, "proxyCardPublicBaseURL") {
		t.Error("the generator does not read the saved public base URL, so it " +
			"cannot emit the operator's real hostname")
	}
	if strings.Contains(ui, "--db-path /var/lib/9router/data.sqlite") {
		t.Error("--db-path is hardcoded to a path a real install does not use; " +
			"a copy-paste would start the process on an empty database")
	}
	if !strings.Contains(ui, `const dbFlag = proxyCardDBPath ? " --db-path " + proxyCardDBPath : "";`) {
		t.Error("the database flag is not derived from the configured path, so " +
			"the emitted command can point at the wrong database")
	}
}

// The generated block used to instruct TRUST_PROXY=true / AUTH_COOKIE_SECURE=true
// while the card's own preset button told the operator those switches are stored
// in settings and apply immediately. Contradictory guidance on the same screen.
func TestCloudflaredConfigDoesNotContradictProxySwitches(t *testing.T) {
	ui := readEmbeddedUI(t)

	if strings.Contains(ui, "TRUST_PROXY=true \\\\\\n") {
		t.Error("the generated config still passes TRUST_PROXY as an environment " +
			"variable, contradicting the preset switch on the same card")
	}
	if strings.Contains(ui, "AUTH_COOKIE_SECURE=true \\\\\\n") {
		t.Error("the generated config still passes AUTH_COOKIE_SECURE as an " +
			"environment variable, contradicting the preset switch")
	}
	// HOST is the one variable that genuinely cannot be changed at runtime, so
	// the generated command must keep it.
	if !strings.Contains(ui, "HOST=127.0.0.1") {
		t.Error("the generated command dropped HOST=127.0.0.1, so the socket " +
			"would stay exposed and Cloudflare could be bypassed")
	}
}

// The settings payload feeds the generator. Without dbPath the UI has to guess,
// and a guess is what produced the empty-database bug.
func TestSettingsPayloadExposesDBPath(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	if _, ok := getSettings(t, r)["dbPath"]; !ok {
		t.Error("the settings response does not expose dbPath, so the config " +
			"generator has to guess the database path")
	}
}
