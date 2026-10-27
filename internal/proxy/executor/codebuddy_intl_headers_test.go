package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"9router/proxy/internal/providers"
)

// Pins the codebuddy-intl fix end to end: the executor, not just the header
// builder, must put the X-Conversation-* family on the wire. The gateway answers
// without it with HTTP 400 code 11128 "request illegal".
func TestCodebuddyIntlExecutorSendsConversationHeaders(t *testing.T) {
	uuidRE := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	req := &Request{
		Ctx:    context.Background(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL, NoAuth: true},
		Client: srv.Client(),
		APIKey: "test-key",
		Body:   []byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`),
	}
	if err := ForwardCodebuddyIntl(httptest.NewRecorder(), req); err != nil {
		t.Fatalf("executor error = %v", err)
	}
	if got == nil {
		t.Fatal("upstream never received a request")
	}
	for _, name := range []string{"X-Conversation-ID", "X-Conversation-Request-ID", "X-Conversation-Message-ID"} {
		if v := got.Get(name); !uuidRE.MatchString(v) {
			t.Errorf("%s = %q, want a UUID", name, v)
		}
	}
}
