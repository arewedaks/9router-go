package chat

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"9router/proxy/internal/constants"
)

// Live probe untuk jalur MiMo free-ai (discovery, bukan CI): hanya jalan
// dengan MIMO_LIVE=1. Memakai jalur kode asli — getMimoJWT + header set
// identik MimoFreeChat — supaya jawaban upstream mencerminkan produksi.
// Berguna saat Xiaomi mengubah allowlist model atau membuka /models route.
func TestMimoLiveProbe(t *testing.T) {
	if os.Getenv("MIMO_LIVE") != "1" {
		t.Skip("live probe only with MIMO_LIVE=1")
	}
	jwt, err := getMimoJWT()
	if err != nil {
		t.Fatalf("getMimoJWT: %v", err)
	}
	t.Logf("jwt ok: %d chars", len(jwt))
	client := &http.Client{Timeout: 60 * time.Second}
	for _, m := range []string{"mimo-v2.5-pro", "mimo-v2.6-flash"} {
		body := []byte(`{"model":"` + m + `","messages":[{"role":"system","content":"` + mimoSystemMarker + `"},{"role":"user","content":"Say PONG"}],"stream":false}`)
		req, err := http.NewRequestWithContext(context.Background(), "POST", mimoChatURL, bytes.NewReader(injectMimoMarker(body)))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
		req.Header.Set(constants.HeaderAuthorization, constants.AuthPrefixBearer+jwt)
		req.Header.Set("X-Mimo-Source", "mimocode-cli-free")
		req.Header.Set("x-session-affinity", getMimoSessionID())
		req.Header.Set(constants.HeaderUserAgent, "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
		resp, err := client.Do(req)
		if err != nil {
			t.Logf("model=%s → transport: %v", m, err)
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		resp.Body.Close()
		t.Logf("model=%s → HTTP %d | %s", m, resp.StatusCode, string(b))
	}
}
