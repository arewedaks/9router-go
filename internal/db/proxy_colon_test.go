package db

import "testing"

// Proxy vendors export proxies as host:port:user:pass; the normalizer must
// rewire that colon shape into a credential-carrying URL, keep the plain
// host:port and scheme'd forms untouched, and refuse malformed inputs.
func TestNormalizeProxyURLColonExport(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"142.111.67.146:5611:eoyhwjll:nyge9dfz08fv", "http://eoyhwjll:nyge9dfz08fv@142.111.67.146:5611", true},
		{"1.2.3.4:8080", "http://1.2.3.4:8080", true}, // plain host:port unchanged
		{"http://u:p@1.2.3.4:8080", "http://u:p@1.2.3.4:8080", true}, // scheme'd untouched
		{"host:70000:a:b", "", false},                                // bad port
		{"a:b:c:d:e", "", false},                                     // five parts is not this shape
	}
	for _, c := range cases {
		got, ok := NormalizeProxyURL(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("NormalizeProxyURL(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
