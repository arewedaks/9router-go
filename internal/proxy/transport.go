package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

// NewFallbackTransport wraps base so a direct dial that fails as a transport
// error is retried through the environment's proxy settings.
//
// Free-pi's auth and ad calls run from a server that may sit behind a sandbox or
// corporate egress proxy, where a direct dial produces only an opaque transport
// error. http.DefaultTransport already honours HTTPS_PROXY; this keeps that
// while still preferring a direct connection, so a no-proxy deployment pays
// nothing. Only transport failures are retried — an HTTP response, whatever its
// status, is upstream's answer and must not be replayed against a second path.
func NewFallbackTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &fallbackTransport{base: base, proxied: &http.Transport{Proxy: http.ProxyFromEnvironment}}
}

type fallbackTransport struct {
	base    http.RoundTripper
	proxied *http.Transport
}

func (t *fallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	return t.proxied.RoundTrip(req)
}

// ShouldBypassNoProxy checks whether targetURL matches any pattern in noProxy.
func ShouldBypassNoProxy(targetURL, noProxy string) bool {
	noProxy = strings.TrimSpace(noProxy)
	if noProxy == "" {
		return false
	}
	if noProxy == "*" {
		return true
	}

	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		host = strings.ToLower(targetURL)
	}

	patterns := strings.Split(noProxy, ",")
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if strings.HasPrefix(p, ".") {
			if strings.HasSuffix(host, p) || host == p[1:] {
				return true
			}
		}
		if host == p || strings.HasSuffix(host, "."+p) {
			return true
		}
	}
	return false
}

// BuildEdgeRelayHeaders formats headers for Vercel, Cloudflare, and Deno edge relays.
func BuildEdgeRelayHeaders(targetURL string, existingHeaders map[string]string) map[string]string {
	headers := make(map[string]string, len(existingHeaders)+2)
	for k, v := range existingHeaders {
		headers[k] = v
	}

	parsed, err := url.Parse(targetURL)
	if err == nil {
		headers["x-relay-target"] = parsed.Scheme + "://" + parsed.Host
		path := parsed.Path
		if path == "" {
			path = "/"
		}
		if parsed.RawQuery != "" {
			path += "?" + parsed.RawQuery
		}
		headers["x-relay-path"] = path
	}
	return headers
}
