package db

import "testing"

// The pasted list this parser has to survive is a real vendor export: one proxy
// per line, `user:pass@host:port`, Windows line endings because it came through
// Telegram. A trailing \r makes the URL undialable and curl reports the
// misleading "Unsupported proxy syntax", so CRLF handling is the load-bearing
// part of this test, not a nicety.
func TestParseProxyListCRLFWithAuth(t *testing.T) {
	raw := "user1:pass1@1.2.3.4:3129\r\nuser2:pass2@5.6.7.8:8080\r\n"
	urls, skipped := ParseProxyList(raw)
	if skipped != 0 {
		t.Fatalf("skipped %d entries, want 0", skipped)
	}
	if len(urls) != 2 {
		t.Fatalf("got %d urls, want 2: %v", len(urls), urls)
	}
	want := []string{"http://user1:pass1@1.2.3.4:3129", "http://user2:pass2@5.6.7.8:8080"}
	for i, w := range want {
		if urls[i] != w {
			t.Errorf("url[%d] = %q, want %q", i, urls[i], w)
		}
	}
	// The failure mode this guards: a \r left on the end.
	for _, u := range urls {
		if containsCR(u) {
			t.Errorf("url %q still contains a carriage return", u)
		}
	}
}

func TestParseProxyListFormats(t *testing.T) {
	raw := `# comment line
1.2.3.4:8080
user:pass@1.2.3.4:3129
http://1.2.3.4:8000

socks5://1.2.3.4:1080
1.2.3.4:7000 # inline note
not-a-proxy
1.2.3.4:99999
`
	urls, skipped := ParseProxyList(raw)
	want := []string{
		"http://1.2.3.4:8080",
		"http://user:pass@1.2.3.4:3129",
		"http://1.2.3.4:8000",
		"socks5://1.2.3.4:1080",
		"http://1.2.3.4:7000",
	}
	if len(urls) != len(want) {
		t.Fatalf("got %d urls %v, want %d", len(urls), urls, len(want))
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Errorf("url[%d] = %q, want %q", i, urls[i], want[i])
		}
	}
	// "not-a-proxy" and the out-of-range port must be reported, not dropped
	// silently, so the caller can tell the operator something was rejected.
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
}

// Credentials must be preserved: a paid list authenticates per request, and
// stripping them turns every proxy into a 407.
func TestNormalizeProxyURLKeepsCredentials(t *testing.T) {
	got, ok := NormalizeProxyURL("wewlyxbn1n5s:m23xfcqrdnsq58l@209.50.175.80:3129")
	if !ok {
		t.Fatal("rejected a valid credentialed proxy")
	}
	if got != "http://wewlyxbn1n5s:m23xfcqrdnsq58l@209.50.175.80:3129" {
		t.Errorf("got %q", got)
	}
}

func TestProxyPoolDataFromListSplitsSingleFromMany(t *testing.T) {
	// One URL keeps the legacy single field so the row stays byte-compatible
	// with the Next.js dashboard; many go in the urls array GetProxyPool reads.
	one, err := ProxyPoolDataFromList("a", []string{"http://1.1.1.1:1"}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if one.ProxyURL == "" || len(one.URLs) != 0 {
		t.Errorf("single url: ProxyURL=%q urls=%v", one.ProxyURL, one.URLs)
	}

	many, err := ProxyPoolDataFromList("b", []string{"http://1.1.1.1:1", "http://2.2.2.2:2"}, "http", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(many.URLs) != 2 || many.ProxyURL != "" {
		t.Errorf("many urls: ProxyURL=%q urls=%v", many.ProxyURL, many.URLs)
	}

	if _, err := ProxyPoolDataFromList("c", nil, "", "", false); err == nil {
		t.Error("empty list must be an error")
	}
}

func containsCR(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' {
			return true
		}
	}
	return false
}
