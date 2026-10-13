package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The toggle must survive a reload. Model ids contain "/" and ":"
// ("tkh/mimo-v2.6-flash:free"), and an id passed through the URL path came
// back still-encoded — the write answered 200 under a key that the read path
// never matched, so the switch looked on and then reverted. The id travels in
// the body now; this pins that it lands verbatim.
func TestJailbreakToggleStoresModelIDVerbatim(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	const modelID = "tkh/mimo-v2.6-flash:free"
	payload, _ := json.Marshal(map[string]any{"id": modelID, "enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// Exactly the id the request asked for, no percent-escapes anywhere.
	on, err := repo.JailbreakModelEnabled(modelID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !on {
		t.Fatal("toggle did not persist under the id the client sent")
	}
	list, err := repo.ListJailbreakModels()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !list[modelID] {
		t.Fatalf("list = %v, want the verbatim id", list)
	}
	for id := range list {
		if id != modelID {
			t.Fatalf("stored key %q is not the verbatim id", id)
		}
	}

	// And off removes it again, so the next reload reads off.
	payload, _ = json.Marshal(map[string]any{"id": modelID, "enabled": false})
	req = httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT off status = %d, want 200", rec.Code)
	}
	on, _ = repo.JailbreakModelEnabled(modelID)
	if on {
		t.Fatal("toggle stayed on after being switched off")
	}
}

// An empty id must be refused rather than writing a key nobody can match.
func TestJailbreakToggleRejectsEmptyID(t *testing.T) {
	_, _, r := setupTestDashboard(t)
	payload, _ := json.Marshal(map[string]any{"id": "", "enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak/models", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The prompt is usually an uploaded .md file, so the document has to carry the
// file's name alongside its text, and the body ceiling has to admit a real
// markdown document rather than truncate it at a typed-line size.
func TestJailbreakDocStoresUploadedMarkdownFile(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	// ~120 KB of markdown: comfortably past the old 64 KB body cap.
	big := "# Agents\n\n" + strings.Repeat("- rule line with `code` and **bold**\n", 3000)
	payload, _ := json.Marshal(map[string]any{
		"prompt":       big,
		"geminiSafety": true,
		"source":       "agents.md",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// GET is what the page reloads from: everything must come back intact.
	req = httptest.NewRequest(http.MethodGet, "/api/dashboard/jailbreak", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if got["source"] != "agents.md" {
		t.Errorf("source = %v, want agents.md", got["source"])
	}
	if got["geminiSafety"] != true {
		t.Errorf("geminiSafety = %v, want true", got["geminiSafety"])
	}
	prompt, _ := got["prompt"].(string)
	if len(prompt) != len(big) {
		t.Errorf("prompt came back %d bytes, sent %d — truncated", len(prompt), len(big))
	}

	doc, err := repo.GetJailbreakDoc()
	if err != nil {
		t.Fatalf("repo read: %v", err)
	}
	if doc.Source != "agents.md" || doc.Prompt != big {
		t.Errorf("stored doc mismatch: source=%q len(prompt)=%d", doc.Source, len(doc.Prompt))
	}
}

func TestJailbreakPresetCRUDRoutes(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// Create preset via PUT /api/dashboard/jailbreak/presets
	payload, _ := json.Marshal(map[string]any{
		"id":           "expert-coder",
		"name":         "Expert Coder",
		"prompt":       "You are an expert coder.",
		"geminiSafety": true,
		"source":       "expert.md",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak/presets", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT preset status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// List presets GET /api/dashboard/jailbreak/presets
	req = httptest.NewRequest(http.MethodGet, "/api/dashboard/jailbreak/presets", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET presets status = %d, want 200", rec.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode presets: %v", err)
	}
	found := false
	for _, p := range list {
		if p["id"] == "expert-coder" {
			found = true
			if p["name"] != "Expert Coder" || p["prompt"] != "You are an expert coder." {
				t.Fatalf("preset content mismatch: %+v", p)
			}
		}
	}
	if !found {
		t.Fatalf("created preset not in list: %+v", list)
	}

	// Bind model to preset
	bindPayload, _ := json.Marshal(map[string]any{
		"id":       "anthropic/claude-3-7-sonnet",
		"presetId": "expert-coder",
	})
	req = httptest.NewRequest(http.MethodPut, "/api/dashboard/jailbreak/models", bytes.NewReader(bindPayload))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT model bind status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// GET /api/dashboard/jailbreak/models
	req = httptest.NewRequest(http.MethodGet, "/api/dashboard/jailbreak/models", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET models status = %d, want 200", rec.Code)
	}
	var modelConfigs map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &modelConfigs); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	cfg := modelConfigs["anthropic/claude-3-7-sonnet"]
	if cfg["enabled"] != true || cfg["presetId"] != "expert-coder" {
		t.Fatalf("model config mismatch: %+v", cfg)
	}

	// Delete preset DELETE /api/dashboard/jailbreak/presets/{id}
	req = httptest.NewRequest(http.MethodDelete, "/api/dashboard/jailbreak/presets/expert-coder", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE preset status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestJailbreakModelRowMobileTwoLines(t *testing.T) {
	ui := readEmbeddedUI(t)
	for _, need := range []string{
		`jb-model-row`,
		`jb-model-label`,
		`jb-model-select`,
		`.jb-model-row`,
		`.jb-model-select`,
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("jailbreak model row missing %s", need)
		}
	}
}
