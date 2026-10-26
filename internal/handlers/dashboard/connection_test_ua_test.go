package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Nous (and any provider behind the same Cloudflare WAF) answers Go's default
// "Go-http-client/1.1" with a 403 HTML challenge, which the probe path reads
// as a bad credential. Every probe request must carry probeUserAgent.
func TestProbeRequestsSendNonDefaultUserAgent(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"data":{}}`))
	}))
	defer srv.Close()

	h := &Handler{}
	body := []byte(`{"model":"x","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`)
	if _, err := h.doJSONTest(t.Context(), srv.Client(), srv.URL+"/v1/chat/completions", body, "k", "llm"); err != nil {
		t.Fatalf("doJSONTest: %v", err)
	}

	// Connection probes build their own requests; assert the helper they call.
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	setProbeUserAgent(req)
	got = append(got, req.Header.Get("User-Agent"))

	for _, ua := range got {
		if ua == "" || ua == "Go-http-client/1.1" {
			t.Fatalf("probe sent default/empty User-Agent %q", ua)
		}
	}

	// A provider fingerprint must not be overwritten.
	own, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	own.Header.Set("User-Agent", "Windsurf/1.0")
	setProbeUserAgent(own)
	if own.Header.Get("User-Agent") != "Windsurf/1.0" {
		t.Fatalf("setProbeUserAgent clobbered provider UA: %q", own.Header.Get("User-Agent"))
	}
}

// The nous probe model must exist on Nous' free tier; a withdrawn model makes a
// healthy account look broken with a 404.
func TestNousProbeModelIsCurrentFreeModel(t *testing.T) {
	if got := genericProbeModel("nous"); got != "meituan/longcat-2.5-preview:free" {
		t.Fatalf("nous probe model = %q, want meituan/longcat-2.5-preview:free", got)
	}
}
