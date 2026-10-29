package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The OmniRush catalogue is only served to requests carrying the gateway's two
// identity headers, and it is the sole source of the real 400k/128k limits.
func TestFetchOmniRushModelsSendsIdentityHeaders(t *testing.T) {
	var gotUA, gotClient, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotClient = r.Header.Get("X-OmniRush-Client")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[
			{"id":"gpt-6-astra","display_name":"GPT 6 Astra","default":true,"limits":{"context":400000,"output":128000}},
			{"id":"gpt-6.1-sol","display_name":"GPT 6.1 Sol","limits":{"context":400000,"output":128000}}
		]}`)
	}))
	t.Cleanup(srv.Close)

	orig := omniRushModelsURL
	omniRushModelsURL = srv.URL
	t.Cleanup(func() { omniRushModelsURL = orig })

	h := &Handler{}
	res := h.fetchOmniRushModels("omnirush", `{"accessToken":"device-token"}`, srv.Client().Timeout)
	if res.Error != "" {
		t.Fatalf("fetch failed: %s", res.Error)
	}
	if gotUA != "omnirush" {
		t.Errorf("User-Agent = %q, want the bare product name", gotUA)
	}
	if gotClient == "" {
		t.Error("X-OmniRush-Client missing; the update gate reads it")
	}
	if gotAuth != "Bearer device-token" {
		t.Errorf("Authorization = %q, want the connection's device token", gotAuth)
	}
	if len(res.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(res.Models))
	}
	first := res.Models[0]
	if first.ID != "gpt-6-astra" || first.ContextLength != 400000 || first.MaxOutput != 128000 {
		t.Errorf("first model mapped as %+v", first)
	}
	if first.Name != "GPT 6 Astra" {
		t.Errorf("display name = %q", first.Name)
	}
}
