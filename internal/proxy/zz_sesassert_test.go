package proxy

import (
	"encoding/hex"
	"testing"
	"time"
)

// Zen rejects a ses_ whose 12 hex prefix is not ~(ms*4096+n) with 403.
// The prefix carries only the low 48 bits, so compare inside that space
// (same arithmetic as upstream PR #4791's test).
const mask48 = uint64(1)<<48 - 1

func decodedUnits(t *testing.T, id string) uint64 {
	t.Helper()
	raw, err := hex.DecodeString(id[4:16])
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	var v uint64
	for _, b := range raw {
		v = v<<8 | uint64(b)
	}
	return mask48 - v
}

func TestSessionIDEncodesRecentTimestamp(t *testing.T) {
	nowUnits := (uint64(time.Now().UnixMilli()) * 0x1000) & mask48
	check := func(label, id string) {
		t.Helper()
		diffMs := int64(decodedUnits(t, id)-nowUnits) / 0x1000
		if diffMs > 60_000 || diffMs < -60_000 {
			t.Fatalf("%s: %s decodes %d ms away from now", label, id, diffMs)
		}
	}

	check("generated", GenerateOpenCodeSessionID())

	a := TranslateOpenCodeSessionID("downstream-abc", "claude")
	check("translated", a)
	if TranslateOpenCodeSessionID("downstream-abc", "claude") != a {
		t.Fatal("not stable per downstream session")
	}
	if TranslateOpenCodeSessionID("downstream-abc", "codex") == a {
		t.Fatal("same session, different tool must differ")
	}
	if TranslateOpenCodeSessionID("downstream-xyz", "claude") == a {
		t.Fatal("distinct sessions collided")
	}
	if TranslateOpenCodeSessionID(a, "claude") != a {
		t.Fatal("already-valid id not passed through")
	}
}
