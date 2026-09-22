package oauth

import (
	"net"
	"net/http"
	"strings"
)

// loopbackRedirectURI builds the callback URL for an installed-app OAuth client
// using the port this server is actually reachable on.
//
// The port has to come from the request, not a constant: the operator may run
// the gateway on any port, and a redirect URI naming the wrong one sends the
// browser to a dead port where the authorization code is lost. Google will not
// accept an arbitrary URI either — the client is registered for loopback, and
// any 127.0.0.1/localhost port is allowed — so echoing the request's own port is
// both correct and permitted.
//
// The Host header is used when it carries a port. Without one (a reverse proxy
// on :80/:443, or a Host header stripped upstream) the configured fallback is
// returned, because guessing would be worse than the documented default.
func loopbackRedirectURI(r *http.Request, path string) string {
	if r != nil {
		if host := hostWithoutZone(r.Host); host != "" {
			if _, port, err := net.SplitHostPort(host); err == nil && port != "" {
				// A proxy in front may report the public port; that is still the
				// port the browser must reach, which is what matters here.
				return "http://" + host + path
			}
		}
	}
	return fallbackRedirectURI(path)
}

// hostWithoutZone strips an IPv6 zone index, which is not valid in a URL.
func hostWithoutZone(host string) string {
	if i := strings.LastIndex(host, "%"); i >= 0 {
		if j := strings.Index(host, "]"); j > i {
			return host[:i] + host[j:]
		}
	}
	return host
}

// fallbackRedirectURI is the documented default used when the request carries no
// usable port. Kept as one place so the three providers stay consistent.
func fallbackRedirectURI(path string) string {
	return "http://localhost:" + defaultListenPort + path
}

// defaultListenPort matches config.DefaultPort. Duplicated as a constant so this
// package does not import config (which would pull the CLI dependency in).
const defaultListenPort = "20128"
