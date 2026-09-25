package svc

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The unit is only useful if systemd itself accepts it. Generating a unit that
// looks right but is rejected at boot is the exact failure this guards against.
func TestUnitPassesSystemdAnalyze(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not installed")
	}
	// Point at an executable that exists on the test machine: systemd-analyze
	// validates ExecStart existence too, and a fake path would fail the check
	// for a reason unrelated to the unit's syntax.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	spec := Spec{
		BinaryPath: exe,
		WorkingDir: workdir,
		Ports:      "20128",
		DataDir:    workdir,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "9router-go.service")
	if err := os.WriteFile(path, []byte(spec.UnitFile()), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("systemd-analyze", "verify", path).CombinedOutput()
	if err != nil {
		t.Fatalf("systemd rejected the generated unit: %v\n%s\n---\n%s", err, out, spec.UnitFile())
	}
}
