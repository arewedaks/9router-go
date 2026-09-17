package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateProviderNodeOpenAI pins the OpenAI-compatible create flow: the id
// encodes the api type, the prefix lands in the data blob (which is what routing
// reads), and the base URL default is applied when omitted.
func TestCreateProviderNodeOpenAI(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	w := postJSON(t, r, "/api/dashboard/provider-nodes", `{
		"name":"My Gateway","prefix":"mvg","type":"openai-compatible","apiType":"chat"
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Node struct {
			ID   string  `json:"id"`
			Type *string `json:"type"`
			Name *string `json:"name"`
			Data string  `json:"data"`
		} `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.Node.ID, "openai-compatible-chat-") {
		t.Fatalf("id = %q, want openai-compatible-chat-<uuid>", resp.Node.ID)
	}
	if strings.Count(resp.Node.ID, "-") < 5 {
		t.Fatalf("id %q does not look generated", resp.Node.ID)
	}
	var blob struct {
		Prefix  string `json:"prefix"`
		APIType string `json:"apiType"`
		BaseURL string `json:"baseUrl"`
		Name    string `json:"nodeName"`
	}
	if err := json.Unmarshal([]byte(resp.Node.Data), &blob); err != nil {
		t.Fatalf("decode blob: %v", err)
	}
	if blob.Prefix != "mvg" {
		t.Fatalf("blob prefix = %q, want mvg", blob.Prefix)
	}
	if blob.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("blob baseUrl = %q, want the OpenAI default", blob.BaseURL)
	}
	if blob.APIType != "chat" {
		t.Fatalf("blob apiType = %q, want chat", blob.APIType)
	}

	// The node must be resolvable by prefix, which is how the router finds it.
	node, data, err := h.repo.GetProviderNodeByPrefix("mvg")
	if err != nil || node == nil {
		t.Fatalf("GetProviderNodeByPrefix(mvg) = %v, %v", node, err)
	}
	if data.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("resolved baseUrl = %q", data.BaseURL)
	}
}

// TestCreateProviderNodeAnthropicOmitsAPIType pins that an Anthropic node is not
// given an apiType and does not get the openai- id prefix.
func TestCreateProviderNodeAnthropicOmitsAPIType(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	w := postJSON(t, r, "/api/dashboard/provider-nodes", `{
		"name":"Claude Proxy","prefix":"clp","type":"anthropic-compatible","apiType":"chat"
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Node struct {
			ID   string `json:"id"`
			Data string `json:"data"`
		} `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.Node.ID, "anthropic-compatible-") {
		t.Fatalf("id = %q, want anthropic-compatible-<uuid>", resp.Node.ID)
	}
	if strings.Contains(resp.Node.ID, "chat") {
		t.Fatalf("anthropic id carries an apiType: %q", resp.Node.ID)
	}
	if strings.Contains(resp.Node.Data, `"apiType"`) {
		t.Fatalf("anthropic blob must not persist apiType: %s", resp.Node.Data)
	}
}

// TestCreateProviderNodeSanitizesBaseURL pins that pasting the full chat URL does
// not produce a doubled path, and that the /messages suffix is stripped for
// Anthropic. Both are upstream behaviours an operator hits constantly.
func TestCreateProviderNodeSanitizesBaseURL(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	cases := []struct {
		name, body, want string
	}{
		{
			"openai chat suffix",
			`{"name":"a","prefix":"p1","type":"openai-compatible","baseUrl":"https://x.dev/v1/chat/completions"}`,
			"https://x.dev/v1",
		},
		{
			"openai trailing slash",
			`{"name":"b","prefix":"p2","type":"openai-compatible","baseUrl":"https://x.dev/v1/"}`,
			"https://x.dev/v1",
		},
		{
			"anthropic messages suffix",
			`{"name":"c","prefix":"p3","type":"anthropic-compatible","baseUrl":"https://a.dev/v1/messages?beta=true"}`,
			"https://a.dev/v1",
		},
	}
	for _, tc := range cases {
		w := postJSON(t, r, "/api/dashboard/provider-nodes", tc.body)
		if w.Code != http.StatusCreated {
			t.Fatalf("(%s) status = %d, body %s", tc.name, w.Code, w.Body.String())
		}
		var resp struct {
			Node struct {
				Data string `json:"data"`
			} `json:"node"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("(%s) decode: %v", tc.name, err)
		}
		var blob struct {
			BaseURL string `json:"baseUrl"`
		}
		if err := json.Unmarshal([]byte(resp.Node.Data), &blob); err != nil {
			t.Fatalf("(%s) decode blob: %v", tc.name, err)
		}
		if blob.BaseURL != tc.want {
			t.Errorf("(%s) baseUrl = %q, want %q", tc.name, blob.BaseURL, tc.want)
		}
	}
}

// TestCreateProviderNodeRejectsDuplicatePrefix pins that a prefix collision is a
// 409, not a silent second node: a prefix routes to exactly one node. It also
// pins that the message names the existing provider by its display name, never
// by its generated id — the id is an internal key the operator must not see.
func TestCreateProviderNodeRejectsDuplicatePrefix(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	first := `{"name":"BAI","prefix":"dup","type":"openai-compatible"}`
	if w := postJSON(t, r, "/api/dashboard/provider-nodes", first); w.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, body %s", w.Code, w.Body.String())
	}

	second := `{"name":"second","prefix":"dup","type":"openai-compatible"}`
	w := postJSON(t, r, "/api/dashboard/provider-nodes", second)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate prefix status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "dup") {
		t.Fatalf("error does not name the clashing prefix: %s", body)
	}
	if !strings.Contains(body, "BAI") {
		t.Fatalf("error should name the existing provider: %s", body)
	}
	if strings.Contains(body, "openai-compatible-") {
		t.Fatalf("error leaks the generated node id: %s", body)
	}
}

// TestCreateProviderNodeValidation covers the malformed inputs a form can send.
func TestCreateProviderNodeValidation(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	cases := []struct {
		name, body string
		wantStatus int
	}{
		{"missing name", `{"prefix":"p","type":"openai-compatible"}`, http.StatusBadRequest},
		{"missing prefix", `{"name":"n","type":"openai-compatible"}`, http.StatusBadRequest},
		{"bad type", `{"name":"n","prefix":"p","type":"nonsense"}`, http.StatusBadRequest},
		{"bad url", `{"name":"n","prefix":"p","type":"openai-compatible","baseUrl":"ftp://x"}`, http.StatusBadRequest},
		{"credentials in url", `{"name":"n","prefix":"p","type":"openai-compatible","baseUrl":"https://u:p@x.dev/v1"}`, http.StatusBadRequest},
		{"invalid json", `{`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		w := postJSON(t, r, "/api/dashboard/provider-nodes", tc.body)
		if w.Code != tc.wantStatus {
			t.Errorf("(%s) status = %d, want %d (body %s)", tc.name, w.Code, tc.wantStatus, w.Body.String())
		}
	}
}

// TestValidateProviderNodeProbesModelsEndpoint pins the happy path of the
// pre-save probe: it hits the endpoint's /models with the key and reports valid.
func TestValidateProviderNodeProbesModelsEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()

	_, _, r := setupTestDashboard(t)
	w := postJSON(t, r, "/api/dashboard/provider-nodes/validate",
		`{"baseUrl":"`+upstream.URL+`/v1","apiKey":"sk-test","type":"openai-compatible"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var res struct {
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Valid {
		t.Fatalf("valid = false, body %s", w.Body.String())
	}
	if gotPath != "/v1/models" {
		t.Fatalf("probed %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
}

// TestValidateProviderNodeUnauthorizedShortCircuits pins that a 401 is reported
// as an auth problem and does NOT fall through to the chat probe — retrying with
// the same key cannot help.
func TestValidateProviderNodeUnauthorizedShortCircuits(t *testing.T) {
	var chatHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			chatHits++
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	_, _, r := setupTestDashboard(t)
	w := postJSON(t, r, "/api/dashboard/provider-nodes/validate",
		`{"baseUrl":"`+upstream.URL+`/v1","apiKey":"bad","modelId":"gpt-4o","type":"openai-compatible"}`)

	var res struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected invalid, body %s", w.Body.String())
	}
	if !strings.Contains(res.Error, "unauthorized") {
		t.Fatalf("error = %q, want an auth message", res.Error)
	}
	if chatHits != 0 {
		t.Fatalf("chat fallback was probed %d times despite 401", chatHits)
	}
}

// TestValidateProviderNodeFallsBackToChat pins the documented fallback: when
// /models is absent (404) but a model id was supplied, a 1-token completion is
// tried and its success marks the endpoint valid (method "chat").
func TestValidateProviderNodeFallsBackToChat(t *testing.T) {
	var chatBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			chatBody = string(buf)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound) // no /models
	}))
	defer upstream.Close()

	_, _, r := setupTestDashboard(t)
	w := postJSON(t, r, "/api/dashboard/provider-nodes/validate",
		`{"baseUrl":"`+upstream.URL+`/v1","apiKey":"sk","modelId":"my-model","type":"openai-compatible"}`)

	var res struct {
		Valid  bool   `json:"valid"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.Valid {
		t.Fatalf("valid = false, body %s", w.Body.String())
	}
	if res.Method != "chat" {
		t.Fatalf("method = %q, want chat", res.Method)
	}
	if !strings.Contains(chatBody, "my-model") {
		t.Fatalf("chat probe did not send the model id: %s", chatBody)
	}
}

// TestValidateProviderNodeConnectionRefusedMentionsDocker pins the actionable
// error text: a refused connection on localhost is usually a Docker/network
// mistake, so the message must say so instead of echoing a raw Go error.
func TestValidateProviderNodeConnectionRefusedMentionsDocker(t *testing.T) {
	// Port 1 is reserved and nothing listens there.
	_, _, r := setupTestDashboard(t)
	w := postJSON(t, r, "/api/dashboard/provider-nodes/validate",
		`{"baseUrl":"http://127.0.0.1:1/v1","apiKey":"sk","type":"openai-compatible"}`)

	var res struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Valid {
		t.Fatal("expected invalid")
	}
	if !strings.Contains(strings.ToLower(res.Error), "refused") &&
		!strings.Contains(strings.ToLower(res.Error), "reachable") {
		t.Fatalf("error = %q, want a network hint", res.Error)
	}
}

// TestValidateProviderNodeRequiresKeyAndURL pins that the probe does not even try
// without the two inputs it needs.
func TestValidateProviderNodeRequiresKeyAndURL(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	for _, body := range []string{
		`{"apiKey":"sk","type":"openai-compatible"}`,
		`{"baseUrl":"https://x.dev/v1","type":"openai-compatible"}`,
	} {
		w := postJSON(t, r, "/api/dashboard/provider-nodes/validate", body)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
		var res struct {
			Valid bool `json:"valid"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if res.Valid {
			t.Fatalf("expected invalid for %s", body)
		}
	}
}

// TestProviderDetailExposesCompatibleNode pins the compatible-node card payload:
// the detail page must carry the endpoint the node actually calls (base URL,
// protocol in words, path) so the operator can see it after creation — upstream
// renders these on CompatibleNodeCard, where they are otherwise invisible.
func TestProviderDetailExposesCompatibleNode(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1","nodeName":"Atria AI"}`)
	seedStatusConnection(t, h, "at-1", nodeID, 1, "", nil)

	req := httptest.NewRequest("GET", "/api/dashboard/providers/"+nodeID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var d struct {
		DisplayName string `json:"displayName"`
		Node        *struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Prefix   string `json:"prefix"`
			BaseURL  string `json:"baseUrl"`
			APIType  string `json:"apiType"`
			APILabel string `json:"apiLabel"`
			APIPath  string `json:"apiPath"`
		} `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Node == nil {
		t.Fatal("node payload missing for a compatible endpoint")
	}
	if d.Node.BaseURL != "https://api.atria-asi.ai/v1" {
		t.Fatalf("baseUrl = %q", d.Node.BaseURL)
	}
	// The endpoint the card prints is baseUrl + "/" + apiPath.
	if d.Node.APIPath != "chat/completions" {
		t.Fatalf("apiPath = %q, want chat/completions (no leading slash)", d.Node.APIPath)
	}
	if d.Node.APILabel != "Chat Completions" {
		t.Fatalf("apiLabel = %q", d.Node.APILabel)
	}
	if d.Node.Prefix != "atri" {
		t.Fatalf("prefix = %q", d.Node.Prefix)
	}
	// The display name must never be the generated id.
	if d.DisplayName != "Atria AI" {
		t.Fatalf("displayName = %q, want Atria AI", d.DisplayName)
	}
}

// TestProviderDetailNodeAPIForAnthropicAndCC pins the two Anthropic variants: both
// speak the Messages API, but the Claude Code variant keeps its own chat path and
// is flagged so the UI can show its warning.
func TestProviderDetailNodeAPIForAnthropicAndCC(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const plainID = "anthropic-compatible-11111111-2222-3333-4444-555555555555"
	seedNode(t, h, plainID, "anthropic-compatible", "Plain Anthropic",
		`{"prefix":"panth","baseUrl":"https://proxy.example.com/v1"}`)
	seedStatusConnection(t, h, "pa-1", plainID, 1, "", nil)

	const ccID = "anthropic-compatible-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	seedNode(t, h, ccID, "anthropic-compatible", "CC Gateway",
		`{"prefix":"ccgw","baseUrl":"https://cc.example.com","compatMode":"cc"}`)
	seedStatusConnection(t, h, "cc-1", ccID, 1, "", nil)

	type nodeView struct {
		APILabel   string `json:"apiLabel"`
		APIPath    string `json:"apiPath"`
		CompatMode string `json:"compatMode"`
	}
	fetch := func(id string) nodeView {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/dashboard/providers/"+id, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
		}
		var d struct {
			Node *nodeView `json:"node"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if d.Node == nil {
			t.Fatalf("node missing for %s", id)
		}
		return *d.Node
	}

	plain := fetch(plainID)
	if plain.APILabel != "Messages API" {
		t.Fatalf("plain apiLabel = %q, want Messages API", plain.APILabel)
	}
	if plain.APIPath != "messages" {
		t.Fatalf("plain apiPath = %q, want messages", plain.APIPath)
	}
	if plain.CompatMode != "" {
		t.Fatalf("plain compatMode = %q, want empty", plain.CompatMode)
	}

	cc := fetch(ccID)
	if cc.APILabel != "Messages API" {
		t.Fatalf("cc apiLabel = %q, want Messages API", cc.APILabel)
	}
	if cc.APIPath != "v1/messages?beta=true" {
		t.Fatalf("cc apiPath = %q, want v1/messages?beta=true", cc.APIPath)
	}
	if cc.CompatMode != "cc" {
		t.Fatalf("cc compatMode = %q, want cc", cc.CompatMode)
	}
}

// TestUpdateProviderNodeRenamesAndRepoints pins the edit flow: name, prefix and
// base URL change, the id stays put (connections point at it), and a pasted
// endpoint suffix is stripped just as it is on create.
func TestUpdateProviderNodeRenamesAndRepoints(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Old Name",
		`{"prefix":"old","apiType":"chat","baseUrl":"https://old.example.com/v1"}`)
	seedStatusConnection(t, h, "c-1", nodeID, 1, "", nil)

	body := `{"name":"New Name","prefix":"newp","baseUrl":"https://new.example.com/v1/chat/completions","apiType":"chat"}`
	req := httptest.NewRequest("PATCH", "/api/dashboard/provider-nodes/"+nodeID, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}

	node, data, err := h.repo.GetProviderNodeByID(nodeID)
	if err != nil || node == nil {
		t.Fatalf("node gone: %v", err)
	}
	if node.Name == nil || *node.Name != "New Name" {
		t.Fatalf("name not updated: %v", node.Name)
	}
	if data.Prefix != "newp" {
		t.Fatalf("prefix = %q, want newp", data.Prefix)
	}
	// The pasted "/chat/completions" must be stripped so the executor does not
	// build ".../chat/completions/chat/completions".
	if data.BaseURL != "https://new.example.com/v1" {
		t.Fatalf("baseUrl = %q, want the suffix stripped", data.BaseURL)
	}

	// The connection must survive an edit: only the node metadata changed.
	if n, err := h.repo.CountProviderNodeConnections(nodeID); err != nil || n != 1 {
		t.Fatalf("connections = %d (err %v), want 1", n, err)
	}
}

// TestUpdateProviderNodeRejectsDuplicatePrefix pins the collision guard on edit:
// two nodes sharing a prefix would make "<prefix>/<model>" ambiguous. The message
// must name the other provider, never leak its generated id.
func TestUpdateProviderNodeRejectsDuplicatePrefix(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const aID = "openai-compatible-chat-11111111-2222-3333-4444-555555555555"
	seedNode(t, h, aID, "openai-compatible", "BAI",
		`{"prefix":"bai","apiType":"chat","baseUrl":"https://api.b.ai/v1"}`)

	const bID = "openai-compatible-chat-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	seedNode(t, h, bID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`)

	body := `{"name":"Atria AI","prefix":"bai","baseUrl":"https://api.atria-asi.ai/v1","apiType":"chat"}`
	req := httptest.NewRequest("PATCH", "/api/dashboard/provider-nodes/"+bID, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "BAI") {
		t.Fatalf("message should name the other provider: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "openai-compatible-") {
		t.Fatalf("message leaks a generated node id: %s", w.Body.String())
	}
}

// TestUpdateProviderNodeKeepsOwnPrefix confirms the collision guard excludes the
// row being edited: saving without changing the prefix must not conflict with
// itself.
func TestUpdateProviderNodeKeepsOwnPrefix(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`)

	body := `{"name":"Atria AI Renamed","prefix":"atri","baseUrl":"https://api.atria-asi.ai/v1","apiType":"chat"}`
	req := httptest.NewRequest("PATCH", "/api/dashboard/provider-nodes/"+nodeID, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
}

// TestDeleteProviderNodeRefusesWhenConnectionsRemain pins the safety rail: a node
// still referenced by accounts is refused with 409 and a count, so the operator
// does not silently lose every account by deleting the endpoint.
func TestDeleteProviderNodeRefusesWhenConnectionsRemain(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`)
	seedStatusConnection(t, h, "at-1", nodeID, 1, "", nil)

	req := httptest.NewRequest("DELETE", "/api/dashboard/provider-nodes/"+nodeID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body.String())
	}
	// The node must survive a refused delete.
	if node, _, err := h.repo.GetProviderNodeByID(nodeID); err != nil || node == nil {
		t.Fatalf("node should still exist: %v", err)
	}
}

// TestDeleteProviderNodeCascade pins the explicit cascade: with ?cascade=1 both
// the node and its connections go, and the response reports how many.
func TestDeleteProviderNodeCascade(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`)
	seedStatusConnection(t, h, "at-1", nodeID, 1, "", nil)
	seedStatusConnection(t, h, "at-2", nodeID, 1, "", nil)

	req := httptest.NewRequest("DELETE", "/api/dashboard/provider-nodes/"+nodeID+"?cascade=1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if node, _, err := h.repo.GetProviderNodeByID(nodeID); err != nil || node != nil {
		t.Fatalf("node should be gone: %v", err)
	}
	if n, err := h.repo.CountProviderNodeConnections(nodeID); err != nil || n != 0 {
		t.Fatalf("connections = %d (err %v), want 0", n, err)
	}
}

// TestDeleteProviderNodeWithoutConnections pins the happy path: an unused node
// deletes without needing the cascade flag.
func TestDeleteProviderNodeWithoutConnections(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	const nodeID = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	seedNode(t, h, nodeID, "openai-compatible", "Atria AI",
		`{"prefix":"atri","apiType":"chat","baseUrl":"https://api.atria-asi.ai/v1"}`)

	req := httptest.NewRequest("DELETE", "/api/dashboard/provider-nodes/"+nodeID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if node, _, err := h.repo.GetProviderNodeByID(nodeID); err != nil || node != nil {
		t.Fatalf("node should be gone: %v", err)
	}
}

// TestCreateProviderNodeStoresCompatMode pins that the Claude Code variant is
// persisted: without it the node resolves to the plain Messages path and its
// warning would never render.
func TestCreateProviderNodeStoresCompatMode(t *testing.T) {
	h, _, r := setupTestDashboard(t)

	w := postJSON(t, r, "/api/dashboard/provider-nodes", `{
		"name":"CC Gateway","prefix":"ccgw","type":"anthropic-compatible",
		"compatMode":"cc","baseUrl":"https://cc.example.com"
	}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, data, err := h.repo.GetProviderNodeByID(resp.Node.ID)
	if err != nil || data == nil {
		t.Fatalf("node not readable: %v", err)
	}
	if data.CompatMode != "cc" {
		t.Fatalf("compatMode = %q, want cc", data.CompatMode)
	}
}
