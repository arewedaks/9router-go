package shared

import (
	"testing"
)

func TestNewTokenSaverConfig(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	if c.RTKEnabled() || c.CavemanEnabled() || c.PonytailEnabled() {
		t.Error("expected all false")
	}
	c2 := NewTokenSaverConfig(true, true, true)
	if !c2.RTKEnabled() || !c2.CavemanEnabled() || !c2.PonytailEnabled() {
		t.Error("expected all true")
	}
	c3 := NewTokenSaverConfig(true, false, true)
	if !c3.RTKEnabled() {
		t.Error("expected RTK true")
	}
	if c3.CavemanEnabled() {
		t.Error("expected Caveman false")
	}
	if !c3.PonytailEnabled() {
		t.Error("expected Ponytail true")
	}
}

func TestSetRTK(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	if c.RTKEnabled() {
		t.Error("expected initial false")
	}
	c.SetRTK(true)
	if !c.RTKEnabled() {
		t.Error("expected true after SetRTK(true)")
	}
	c.SetRTK(false)
	if c.RTKEnabled() {
		t.Error("expected false after SetRTK(false)")
	}
}

func TestSetCaveman(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetCaveman(true)
	if !c.CavemanEnabled() {
		t.Error("expected true after SetCaveman(true)")
	}
	c.SetCaveman(false)
	if c.CavemanEnabled() {
		t.Error("expected false after SetCaveman(false)")
	}
}

func TestSetPonytail(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetPonytail(true)
	if !c.PonytailEnabled() {
		t.Error("expected true after SetPonytail(true)")
	}
	c.SetPonytail(false)
	if c.PonytailEnabled() {
		t.Error("expected false after SetPonytail(false)")
	}
}

func TestSnapshot(t *testing.T) {
	c := NewTokenSaverConfig(true, false, true)
	rtk, caveman, ponytail := c.Snapshot()
	if rtk != true {
		t.Errorf("expected rtk=true, got %v", rtk)
	}
	if caveman != false {
		t.Errorf("expected caveman=false, got %v", caveman)
	}
	if ponytail != true {
		t.Errorf("expected ponytail=true, got %v", ponytail)
	}
}

func TestSetAll(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetAll(true, true, false)
	rtk, caveman, ponytail := c.Snapshot()
	if rtk != true || caveman != true || ponytail != false {
		t.Errorf("SetAll mismatch: got (%v,%v,%v), want (true,true,false)", rtk, caveman, ponytail)
	}
}

func TestInjectionGuardToggle(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	if !c.InjectionGuardEnabled() {
		t.Error("injection guard must be enabled by default")
	}
	c.SetInjectionGuard(false)
	if c.InjectionGuardEnabled() {
		t.Error("expected guard disabled after SetInjectionGuard(false)")
	}
	c.SetInjectionGuard(true)
	if !c.InjectionGuardEnabled() {
		t.Error("expected guard re-enabled after SetInjectionGuard(true)")
	}
}

// A database written by the older Go dashboard stores "light"/"medium" for
// caveman and "compact" for ponytail. The startup path feeds those straight into
// SetCaveman/SetPonytail, so they have to come out as a level the prompt lookup
// understands — otherwise the operator's saved style silently changes on upgrade.
func TestSetLevelsNormalizeLegacyAliases(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)

	c.SetCaveman(true, "medium")
	if got := c.CavemanLevel(); got != "full" {
		t.Errorf("SetCaveman(medium) stored %q, want full", got)
	}
	c.SetCaveman(true, "light")
	if got := c.CavemanLevel(); got != "lite" {
		t.Errorf("SetCaveman(light) stored %q, want lite", got)
	}
	c.SetPonytail(true, "compact")
	if got := c.PonytailLevel(); got != "full" {
		t.Errorf("SetPonytail(compact) stored %q, want full", got)
	}
	c.SetPonytail(true, "nonsense")
	if got := c.PonytailLevel(); got != "full" {
		t.Errorf("an unknown level stored %q, want the full default", got)
	}
}

// An empty level argument must leave the stored level alone: a toggle that only
// flips the switch must not reset the intensity the operator chose.
func TestSetLevelWithoutArgumentKeepsLevel(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetCaveman(true, "ultra")
	c.SetCaveman(false)
	if got := c.CavemanLevel(); got != "ultra" {
		t.Errorf("CavemanLevel = %q after a bare toggle, want ultra", got)
	}
}

// Headroom state must be readable without a URL configured, and a bare toggle
// must not blank the URL a script set.
func TestHeadroomConfigDefaultsAndPartialUpdates(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	if c.HeadroomEnabled() {
		t.Error("Headroom must default to disabled")
	}
	if got := c.HeadroomURL(); got != DefaultHeadroomURL {
		t.Errorf("HeadroomURL() = %q, want the default", got)
	}
	if got := c.HeadroomTimeoutMs(); got != 3000 {
		t.Errorf("HeadroomTimeoutMs() = %d, want 3000", got)
	}

	c.SetHeadroom(true, "http://example.test:9000", 4500)
	if !c.HeadroomEnabled() || c.HeadroomURL() != "http://example.test:9000" || c.HeadroomTimeoutMs() != 4500 {
		t.Fatalf("SetHeadroom did not apply: enabled=%v url=%q timeout=%d",
			c.HeadroomEnabled(), c.HeadroomURL(), c.HeadroomTimeoutMs())
	}

	c.SetHeadroom(false, "", 0)
	if c.HeadroomEnabled() {
		t.Error("SetHeadroom(false) did not disable")
	}
	if got := c.HeadroomURL(); got != "http://example.test:9000" {
		t.Errorf("a bare toggle reset the URL to %q", got)
	}
	if got := c.HeadroomTimeoutMs(); got != 4500 {
		t.Errorf("a bare toggle reset the timeout to %d", got)
	}
}
