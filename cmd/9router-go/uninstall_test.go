package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A self-update leaves <binary>.old behind and a manual install leaves
// <binary>.bak-<hhmm>; both must go with the binary itself.
func TestStaleBinariesPicksRotationLeftovers(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "9router-go")
	for _, name := range []string{"9router-go", "9router-go.old", "9router-go.bak-1202", "unrelated"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := staleBinaries(exe)
	want := map[string]bool{
		exe:               true,
		exe + ".old":      true,
		exe + ".bak-1202": true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d entries", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected target %s", p)
		}
	}
}
