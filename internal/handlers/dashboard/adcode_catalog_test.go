package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ADCode's gateway has no /models route (404); the vendor client reads
// /v1/ai/tagflow/config. Import must read the same source, otherwise a connected
// account caches zero models and /v1/models advertises none of them.
func TestFetchAdCodeModelsReadsTheVendorConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua == "" || ua == "Go-http-client/1.1" {
			t.Errorf("catalogue fetched with the default Go agent (%q); Cloudflare answers 403 1010", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"available":true,"defaultModel":"tagflow-code-27b","models":[
			{"id":"tagflow-code-27b","name":"Tagflow Code 27B","contextWindow":262144,"maxOutput":50000,"creditRate":1,"paid":false},
			{"id":"heabsy-qwen38","name":"Qwen3.8 27B","contextWindow":131072,"maxOutput":50000,"creditRate":0.2,"paid":true}
		],"credits":{"weeklyCredits":20000}}`)
	}))
	t.Cleanup(srv.Close)

	orig := adcodeConfigURL
	adcodeConfigURL = srv.URL
	t.Cleanup(func() { adcodeConfigURL = orig })

	h := &Handler{}
	res := h.fetchAdCodeModels("adcode", srv.Client().Timeout)
	if res.Error != "" {
		t.Fatalf("fetch failed: %s", res.Error)
	}
	if !res.Supported {
		t.Fatal("import must report Supported so the dashboard offers it")
	}
	if len(res.Models) != 2 {
		t.Fatalf("got %d models, want 2", len(res.Models))
	}
	first := res.Models[0]
	if first.ID != "tagflow-code-27b" || first.ContextLength != 262144 || first.MaxOutput != 50000 {
		t.Errorf("free model mapped as %+v", first)
	}
	second := res.Models[1]
	if second.ID != "heabsy-qwen38" || second.ContextLength != 131072 {
		t.Errorf("paid model mapped as %+v", second)
	}
	// The plan is visible in the name because the dashboard flattens both tiers
	// into one table and a free-plan account cannot use the paid ids.
	if second.Name == "Qwen3.8 27B" {
		t.Errorf("paid model name %q does not mark the plan", second.Name)
	}
}
