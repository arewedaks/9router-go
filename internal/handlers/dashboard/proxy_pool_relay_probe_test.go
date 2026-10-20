package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeEdgeRelayVsProxy(t *testing.T) {
	// A Worker-shaped relay: answers 400 on a bare GET, refuses CONNECT.
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusBadRequest) // what the real Worker answers
	}))
	defer relay.Close()

	ctx := context.Background()

	// Probed as a forward proxy (the old behaviour) it must fail.
	if err := probeProxyTyped(ctx, relay.URL, "http"); err == nil {
		t.Error("forward-proxy probe should reject a relay that refuses CONNECT")
	} else {
		t.Logf("as proxy : %v", err)
	}

	// Probed as the edge relay it is, it must pass.
	if err := probeProxyTyped(ctx, relay.URL, "cloudflare"); err != nil {
		t.Errorf("edge-relay probe should accept a reachable Worker, got %v", err)
	} else {
		t.Log("as relay : ok")
	}

	// A Worker that 5xxes is broken and must be reported.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer broken.Close()
	if err := probeProxyTyped(ctx, broken.URL, "cloudflare"); err == nil {
		t.Error("edge-relay probe should reject a 5xx Worker")
	}
}
