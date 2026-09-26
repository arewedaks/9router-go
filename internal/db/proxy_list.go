package db

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ParseProxyList turns a pasted proxy list into normalised proxy URLs.
//
// Operators paste these straight out of a provider's export, so the input is
// messy by nature and every line a hand-written splitter gets wrong is a proxy
// that silently never routes:
//
//   - `host:port`                                  (no auth)
//   - `user:pass@host:port`                        (proxyscrape, most vendors)
//   - `http://user:pass@host:port`                 (already a URL)
//   - `socks5://host:port`                         (scheme honoured)
//   - blank lines, `#` comments, CRLF line endings (Windows/Telegram exports)
//
// Returns the URLs plus a count of lines that could not be parsed, so the
// caller can report "imported N, skipped M" instead of silently dropping input.
func ParseProxyList(raw string) (urls []string, skipped int) {
	// Normalise every line ending before splitting: an export downloaded via
	// Telegram ends in CRLF, and a trailing \r makes the URL unparseable —
	// curl reports the maddening "Unsupported proxy syntax" for it.
	normalised := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(raw)

	for _, line := range strings.Split(normalised, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Inline comments are common in hand-edited lists.
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
			if line == "" {
				continue
			}
		}
		u, ok := NormalizeProxyURL(line)
		if !ok {
			skipped++
			continue
		}
		urls = append(urls, u)
	}
	return urls, skipped
}

// NormalizeProxyURL accepts the shorthand vendors ship and returns a URL
// net/http can dial. The bool is false when the entry is not a usable proxy.
func NormalizeProxyURL(entry string) (string, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", false
	}

	// A bare host:port has no scheme, so url.Parse would read the host as a
	// scheme. Detect it and prefix http:// (the overwhelmingly common case for
	// a pasted list) while leaving explicit schemes alone.
	if !strings.Contains(entry, "://") {
		entry = "http://" + entry
	}

	u, err := url.Parse(entry)
	if err != nil {
		return "", false
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h", "socks4":
	default:
		return "", false
	}
	host := u.Hostname()
	if host == "" {
		return "", false
	}
	port := u.Port()
	if port == "" {
		return "", false
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", false
	}
	// Credentials must survive: a paid list authenticates per request, and
	// dropping them turns every proxy into a 407.
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		if user != "" && pass != "" {
			u.User = url.UserPassword(user, pass)
		}
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), true
}

// ProxyPoolDataFromList builds the stored pool payload for a list of proxies.
// A single URL keeps using ProxyURL so the row stays byte-compatible with what
// the Next.js dashboard writes; two or more go in the `urls` array that
// GetProxyPool already reads for round-robin rotation.
func ProxyPoolDataFromList(name string, urls []string, proxyType, noProxy string, strict bool) (ProxyPoolData, error) {
	if len(urls) == 0 {
		return ProxyPoolData{}, fmt.Errorf("proxy list is empty")
	}
	if proxyType == "" {
		proxyType = "http"
	}
	d := ProxyPoolData{
		Name:        name,
		Type:        proxyType,
		NoProxy:     noProxy,
		StrictProxy: strict,
		URLs:        urls,
	}
	if len(urls) == 1 {
		d.ProxyURL = urls[0]
		d.URLs = nil
	}
	return d, nil
}
