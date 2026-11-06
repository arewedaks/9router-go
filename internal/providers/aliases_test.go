package providers

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A canonical id with several aliases must resolve to the same primary alias on
// every process, and that alias must not collide with the provider's own model
// ids.
//
// ProviderToAliasMap is built by ranging a map, so before the pinned table the
// winner was random per process: nace advertised itself as nc/, drex/ or nace/
// across three restarts of the same binary. The drex/ case was worse than
// cosmetic — it rendered "drex/drex-v2.0".
func TestGetProviderAliasIsDeterministic(t *testing.T) {
	if got := GetProviderAlias("nace"); got != "nc" {
		t.Fatalf("GetProviderAlias(nace) = %q, want nc (the alias must not be drex: it collides with drex-* model ids)", got)
	}
	// The alias must still resolve back to the provider.
	if got := ResolveAlias(GetProviderAlias("nace")); got != "nace" {
		t.Errorf("ResolveAlias(GetProviderAlias(nace)) = %q, want nace", got)
	}
}

// The real regression: run the resolution in fresh processes and require the
// same answer every time. A single in-process check cannot catch a randomised
// map iteration — the build that served nc/ and the one that served drex/ both
// passed it.
func TestGetProviderAliasStableAcrossProcesses(t *testing.T) {
	if os.Getenv("ALIAS_CHILD") == "1" {
		os.Stdout.WriteString("ALIAS=" + GetProviderAlias("nace") + "\n")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate test binary: %v", err)
	}
	var saw []string
	for i := 0; i < 8; i++ {
		cmd := exec.Command(exe, "-test.run=TestGetProviderAliasStableAcrossProcesses")
		cmd.Env = append(os.Environ(), "ALIAS_CHILD=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("child %d: %v (%s)", i, err, out)
		}
		var got string
		for _, line := range strings.Split(string(out), "\n") {
			if after, ok := strings.CutPrefix(line, "ALIAS="); ok {
				got = strings.TrimSpace(after)
			}
		}
		if got == "" {
			t.Fatalf("child %d produced no ALIAS line: %q", i, out)
		}
		saw = append(saw, got)
	}
	for _, got := range saw {
		if got != "nc" {
			t.Fatalf("child processes returned %v, want nc every time", saw)
		}
	}
	t.Logf("8 fresh processes all resolved nace -> %s", saw[0])
}