package db

import (
	"sync"
	"time"
)

// ProxyCooloff is how long a proxy is skipped after it refuses a request. Long
// enough that a rate-limited entry is not drawn again on the next few requests,
// short enough that a transient limit does not park it for the whole session.
var ProxyCooloff = 60 * time.Second

// proxyCooloffs records, per proxy URL, the instant it becomes usable again.
// It is process memory on purpose: a restart should forget a transient limit,
// and the alternative (a schema column) would need a write on every failure.
var (
	proxyCooloffsMu sync.Mutex
	proxyCooloffs   = map[string]time.Time{}
)

// coolingDown reports whether a proxy is still inside its cool-off window.
func ProxyCoolingDown(proxyURL string) bool {
	proxyCooloffsMu.Lock()
	defer proxyCooloffsMu.Unlock()
	until, ok := proxyCooloffs[proxyURL]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(proxyCooloffs, proxyURL)
		return false
	}
	return true
}

// markCooling records that a proxy refused a request and should be skipped.
func MarkProxyCooling(proxyURL string) {
	proxyCooloffsMu.Lock()
	defer proxyCooloffsMu.Unlock()
	proxyCooloffs[proxyURL] = time.Now().Add(ProxyCooloff)
}

// clearCooling marks a proxy healthy again, so a single success undoes an
// earlier cool-off rather than waiting it out.
func ClearProxyCooling(proxyURL string) {
	proxyCooloffsMu.Lock()
	defer proxyCooloffsMu.Unlock()
	delete(proxyCooloffs, proxyURL)
}

// resetProxyCooloffs drops all cool-off state. Only tests need this: the map is
// process-global and keyed by URL, so a dead entry parked by one test would be
// skipped by the next one that reuses the same address.
func ResetProxyCooloffs() {
	proxyCooloffsMu.Lock()
	defer proxyCooloffsMu.Unlock()
	proxyLastErrorMu.Lock()
	defer proxyLastErrorMu.Unlock()
	proxyCooloffs = map[string]time.Time{}
	proxyLastError = map[string]string{}
}

// ProxyHealth is a snapshot of one proxy URL's current routing state, for the
// dashboard. It reports what the router actually believes, not a stored guess.
type ProxyHealth struct {
	URL       string `json:"url"`
	Cooling   bool   `json:"cooling"`
	Until     string `json:"until,omitempty"`
	LastError string `json:"lastError,omitempty"`
}

// ProxyLastErrorMu guards proxyLastError. why a proxy was parked, so the dashboard can say
// "429" instead of only "unavailable".
var (
	proxyLastErrorMu sync.Mutex
	proxyLastError   = map[string]string{}
)

// ProxyHealthSnapshot returns the routing state of every URL in the pool.
func ProxyHealthSnapshot(urls []string) []ProxyHealth {
	out := make([]ProxyHealth, 0, len(urls))
	now := time.Now()

	proxyCooloffsMu.Lock()
	defer proxyCooloffsMu.Unlock()
	proxyLastErrorMu.Lock()
	defer proxyLastErrorMu.Unlock()

	for _, u := range urls {
		h := ProxyHealth{URL: u}
		if until, ok := proxyCooloffs[u]; ok {
			if now.Before(until) {
				h.Cooling = true
				h.Until = until.UTC().Format(time.RFC3339)
				h.LastError = proxyLastError[u]
			} else {
				delete(proxyCooloffs, u)
				delete(proxyLastError, u)
			}
		}
		out = append(out, h)
	}
	return out
}

// SetProxyError records why a proxy was parked, so the dashboard can show the
// reason instead of only that the entry is unavailable.
func SetProxyError(proxyURL, msg string) {
	proxyLastErrorMu.Lock()
	defer proxyLastErrorMu.Unlock()
	proxyLastError[proxyURL] = msg
}
