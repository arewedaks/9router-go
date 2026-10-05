package chat

import (
	"errors"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

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
	for i := 0; i < len(urls); i++ {
		proxyURL := urls[(start+i)%len(urls)]
		if i > 0 {
			// The previous attempt may have consumed part of the body before
			// failing, so every retry starts from a fresh copy.
			if err := rewind(req); err != nil {
				return nil, err
			}
		}
		transport, err := t.transportFor(proxyURL)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := transport.RoundTrip(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// Only proxy-level failures are worth another entry. A request that
		// already reached the upstream must not be replayed: the body may be
		// half-consumed and a streaming response may already be in flight.
		if !canFailover(err) {
			return nil, err
		}
		log.Warn("proxy", "pool entry failed, trying next",
			"pool", t.pool.ID, "proxy", redactProxy(proxyURL), "error", err)
	}
	return nil, lastErr
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
