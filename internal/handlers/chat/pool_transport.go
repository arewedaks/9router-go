package chat

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
)

// poolTransport routes each request through the proxy pool's URLs in turn and
// fails over to the next URL when one cannot carry the request.
//
// A pool holds N proxies but the pool previously handed out one URL per request
// and built a client around only that URL. A dead entry therefore failed every
// request that happened to draw it — roughly 1 in N — even though the remaining
// entries were healthy. Failover belongs here, below the callers, because this
// is the only place that still holds the request body in a replayable form and
// has not yet written any response bytes.
type poolTransport struct {
	pool   *db.ProxyPool
	base   *http.Transport // template: cloned per attempt, never dialled directly
	cursor atomic.Uint64
}

// newPoolTransport builds the transport for one pool. base is cloned for every
// attempt so a failed proxy's half-open connections never leak into the next.
func newPoolTransport(pool *db.ProxyPool, base *http.Transport) *poolTransport {
	return &poolTransport{pool: pool, base: base.Clone()}
}

func (t *poolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	urls := t.pool.URLs
	if len(urls) == 0 {
		return nil, errors.New("proxy pool has no proxy URLs")
	}

	// Start at the pool's own round-robin position so load still spreads
	// across every entry, then walk the rest on failure.
	start := int(t.cursor.Add(1)-1) % len(urls)
	var lastErr error
	// attempted counts entries actually tried, and doubles as the rewind guard:
	// once anything has been sent the body may be half-consumed, so every later
	// attempt starts from a fresh copy. It is never reset inside the loop — a
	// proxy that answers 429 has still had its body written to it.
	attempted := 0
	for i := 0; i < len(urls); i++ {
		proxyURL := urls[(start+i)%len(urls)]
		// Skip an entry that recently refused a request. Without this the next
		// request simply redraws the same rate-limited proxy, which is the
		// symptom this whole path exists to remove.
		if db.ProxyCoolingDown(proxyURL) {
			continue
		}
		if attempted > 0 {
			if err := rewind(req); err != nil {
				return nil, err
			}
		}
		attempted++
		transport, err := t.transportFor(proxyURL)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := transport.RoundTrip(req)
		if err == nil {
			if !exhaustedProxyStatus(resp.StatusCode) {
				// A real answer from the upstream. Only now is the proxy proven
				// good, so one success clears an earlier cool-off.
				db.ClearProxyCooling(proxyURL)
				return resp, nil
			}
			// The proxy answered on its own behalf (rate limit, auth, dead
			// upstream). That is not the upstream's answer, so another entry may
			// still carry the request. The body is rewound at the top of the
			// next pass, exactly as it is for a dial failure.
			resp.Body.Close()
			parkProxy(proxyURL, resp.StatusCode)
			lastErr = fmt.Errorf("proxy %s returned %d", redactProxy(proxyURL), resp.StatusCode)
			log.Warn("proxy", "pool entry exhausted, trying next",
				"pool", t.pool.ID, "proxy", redactProxy(proxyURL), "status", resp.StatusCode)
			continue
		}
		lastErr = err
		// Only proxy-level failures are worth another entry. A request that
		// already reached the upstream must not be replayed: the body may be
		// half-consumed and a streaming response may already be in flight.
		if !canFailover(err) {
			return nil, err
		}
		db.MarkProxyCooling(proxyURL)
		db.SetProxyError(proxyURL, err.Error())
		log.Warn("proxy", "pool entry failed, trying next",
			"pool", t.pool.ID, "proxy", redactProxy(proxyURL), "error", err)
	}
	if attempted == 0 {
		// Every entry is cooling down. Serving through a parked proxy beats
		// failing outright: a rate limit usually still carries the request, so
		// the caller sees a degraded path rather than an outage.
		log.Warn("proxy", "all pool entries cooling down; using one anyway", "pool", t.pool.ID)
		proxyURL := urls[start]
		if transport, err := t.transportFor(proxyURL); err == nil {
			if resp, err := transport.RoundTrip(req); err == nil {
				return resp, nil
			}
		}
		return nil, errors.New("every proxy in the pool is cooling down after failures")
	}
	return nil, lastErr
}

// parkProxy records a refusal: the entry is skipped for proxyCooloff and the
// reason is kept so the dashboard can show it.
func parkProxy(proxyURL string, status int) {
	db.MarkProxyCooling(proxyURL)
	db.SetProxyError(proxyURL, fmt.Sprintf("HTTP %d from proxy", status))
}

// transportFor returns a transport bound to one proxy URL. The per-URL cache is
// what keeps connection reuse working across requests.
func (t *poolTransport) transportFor(proxyURL string) (*http.Transport, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}

	proxyClientsMu.Lock()
	cached, ok := poolProxyTransports[proxyURL]
	proxyClientsMu.Unlock()
	if ok {
		return cached, nil
	}

	transport := t.base.Clone()
	if dialer, ok := proxyDialer(parsed); ok {
		// SOCKS is negotiated by the dialer; setting Transport.Proxy too would
		// send the SOCKS CONNECT to the SOCKS port as HTTP.
		transport.Proxy = nil
		transport.DialContext = dialer
	} else {
		transport.Proxy = http.ProxyURL(parsed)
	}

	proxyClientsMu.Lock()
	defer proxyClientsMu.Unlock()
	if existing, ok := poolProxyTransports[proxyURL]; ok {
		return existing, nil
	}
	poolProxyTransports[proxyURL] = transport
	return transport, nil
}

// poolProxyTransports caches one transport per proxy URL, so connection reuse
// survives across requests and across pools.
var (
	poolProxyTransports = make(map[string]*http.Transport)
	poolProxyMu         sync.Mutex
)

// canFailover reports whether the failure means nothing reached the upstream,
// so another proxy may carry the same request. At the transport level a
// response is never reported as an error — only dial, tunnel and TLS failures
// are — so any error here is safe to retry once the body is rewound.
func canFailover(err error) bool {
	return !errors.Is(err, errBodyUnrewindable)
}

// proxyCooloff is how long a proxy is skipped after it refuses a request.
// Long enough that a rate-limited entry is not drawn again on the next few
// requests, short enough that a transient limit does not park the entry for
// the rest of the session. Declared as a var so tests need not sleep.
var proxyCooloff = 60 * time.Second

// exhaustedProxyStatus reports whether a response came from the proxy itself
// rather than from the upstream.
//
// A proxy that is rate-limited, out of credit or refusing the CONNECT answers
// on its own behalf, and http.Transport reports that as a perfectly successful
// round trip — so the old code handed it back as the upstream's answer and
// never tried the other entries. These statuses are what a spent proxy returns:
//
//   - 429: rate limit / quota on the proxy account.
//   - 407: proxy authentication required (expired credentials).
//   - 502, 503, 504: the proxy could not reach the upstream (dead or overloaded).
//
// Statuses the upstream itself can return (including 401 and 403, which belong
// to the API key) are deliberately absent: retrying those on another proxy
// would repeat a non-proxy failure and burn pool capacity doing it.
func exhaustedProxyStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusProxyAuthRequired,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// errBodyUnrewindable marks a request whose body cannot be resent, which makes
// a retry unsafe because the upstream would receive a truncated payload.
var errBodyUnrewindable = errors.New("request body cannot be rewound for retry")

// rewind replaces the body with a fresh copy for the next attempt.
func rewind(req *http.Request) error {
	if req.Body == nil {
		return nil
	}
	if req.GetBody == nil {
		return errBodyUnrewindable
	}
	body, err := req.GetBody()
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}

// redactProxy hides proxy credentials, which are part of the URL.
func redactProxy(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	return parsed.Host
}

// poolClient returns a client that rotates through every URL of pool and fails
// over when one is unreachable.
func (h *ChatHandler) poolClient(pool *db.ProxyPool) *http.Client {
	key := "pool:" + pool.ID
	proxyClientsMu.RLock()
	client, ok := proxyClients[key]
	proxyClientsMu.RUnlock()
	if ok {
		return client
	}

	base := http.DefaultTransport.(*http.Transport).Clone()
	tr := newPoolTransport(pool, base)

	proxyClientsMu.Lock()
	defer proxyClientsMu.Unlock()
	if client, ok := proxyClients[key]; ok {
		return client
	}
	client = &http.Client{Transport: tr, Timeout: h.Client.Timeout}
	proxyClients[key] = client
	return client
}
