package dashboard

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// A refused tunnel must be reported with the proxy's own status line, because
// "407" and "could not connect" need different fixes.
func TestProbeProxySurfacesRefusal(t *testing.T) {
	fake := startFakeProxy(t, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")

	got := probeProxy(context.Background(), fake)
	if got == nil {
		t.Fatal("a 407 tunnel must not be reported as a success")
	}
	if !strings.Contains(got.Error(), "407") {
		t.Errorf("error = %v, want the proxy's status surfaced", got)
	}
}

// A proxy that accepts CONNECT is a pass. This is the case that separates a real
// proxy probe from a bare TCP dial: the socket opens in both, only this one
// proves the proxy forwards traffic.
func TestProbeProxyAcceptsSuccessfulTunnel(t *testing.T) {
	fake := startFakeProxy(t, "HTTP/1.1 200 Connection Established\r\n\r\n")
	if err := probeProxy(context.Background(), fake); err != nil {
		t.Fatalf("a 200 tunnel should pass, got %v", err)
	}
}

// A dead port must fail rather than hang, and the error must name that the
// proxy was unreachable (not that the target was).
func TestProbeProxyUnreachable(t *testing.T) {
	err := probeProxy(context.Background(), "http://127.0.0.1:9")
	if err == nil {
		t.Fatal("an unreachable proxy must fail")
	}
	if !strings.Contains(err.Error(), "cannot reach proxy") {
		t.Errorf("error = %v, want it to blame the proxy", err)
	}
}

// Garbage input must be rejected before any dial is attempted.
func TestProbeProxyRejectsInvalidURL(t *testing.T) {
	if err := probeProxy(context.Background(), "not a proxy at all"); err == nil {
		t.Fatal("invalid input must be rejected")
	}
}

// Credentials in the URL must reach the CONNECT request as Proxy-Authorization,
// otherwise an authenticated proxy reports 407 for a pool that is correct.
func TestProbeProxySendsProxyAuthorization(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 2048)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := conn.Read(buf)
		got <- string(buf[:n])
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	}()

	addr := fmt.Sprintf("http://user:secret@%s", ln.Addr().String())
	if err := probeProxy(context.Background(), addr); err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	req := <-got
	if !strings.Contains(req, "Proxy-Authorization: Basic ") {
		t.Errorf("request did not carry credentials:\n%s", req)
	}
	// base64("user:secret")
	if !strings.Contains(req, "dXNlcjpzZWNyZXQ=") {
		t.Errorf("credentials not encoded as expected:\n%s", req)
	}
}

// startFakeProxy runs a TCP listener that answers the first line of any CONNECT
// with the given status line, then closes. It stands in for a real proxy so the
// probe's parsing is tested without touching the network.
func startFakeProxy(t *testing.T, statusLine string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte(statusLine))
			}(conn)
		}
	}()
	return fmt.Sprintf("http://%s", ln.Addr().String())
}
