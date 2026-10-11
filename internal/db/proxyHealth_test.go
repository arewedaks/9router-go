package db

import (
	"testing"
	"time"
)

// A proxy that refused a request must read as cooling, and must stop reading
// that way once its window elapses — otherwise a transient limit parks the
// entry forever and the pool quietly shrinks.
func TestProxyCoolingWindow(t *testing.T) {
	ResetProxyCooloffs()
	const dead = "http://127.0.0.1:9"

	if ProxyCoolingDown(dead) {
		t.Fatal("a proxy that has not failed must not report cooling")
	}

	// Shrink the window before parking, not after: the deadline is computed when
	// the proxy is marked, so changing the duration later cannot affect it.
	orig := ProxyCooloff
	ProxyCooloff = time.Millisecond
	defer func() { ProxyCooloff = orig }()

	MarkProxyCooling(dead)
	if !ProxyCoolingDown(dead) {
		t.Fatal("a parked proxy must report cooling")
	}

	time.Sleep(5 * time.Millisecond)
	if ProxyCoolingDown(dead) {
		t.Fatal("cooling must expire once the window elapses")
	}
}

// A success after a failure must clear the cool-off immediately: waiting out a
// transient limit would leave a healthy proxy unused.
func TestClearProxyCoolingRestoresEntry(t *testing.T) {
	ResetProxyCooloffs()
	const url = "http://127.0.0.1:9"

	MarkProxyCooling(url)
	ClearProxyCooling(url)
	if ProxyCoolingDown(url) {
		t.Fatal("a proven-good proxy must not stay parked")
	}
}

// The snapshot drives the dashboard, so it must report every URL it was given
// and attach each reason to the right entry.
func TestProxyHealthSnapshotReportsPerURL(t *testing.T) {
	ResetProxyCooloffs()
	const bad = "http://127.0.0.1:9"
	const good = "http://127.0.0.1:10"

	MarkProxyCooling(bad)
	SetProxyError(bad, "HTTP 429 from proxy")

	got := ProxyHealthSnapshot([]string{bad, good})
	if len(got) != 2 {
		t.Fatalf("snapshot entries = %d, want one per URL", len(got))
	}
	if !got[0].Cooling || got[0].LastError != "HTTP 429 from proxy" {
		t.Errorf("entry 0 = %+v, want cooling with the 429 reason", got[0])
	}
	if got[0].Until == "" {
		t.Error("a cooling entry must report when it becomes usable again")
	}
	if got[1].Cooling {
		t.Errorf("entry 1 = %+v, want healthy", got[1])
	}
}
