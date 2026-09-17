package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/log"
)

func TestConsoleLogsHandlerGetAndClear(t *testing.T) {
	log.ClearConsoleLogs()
	log.Info("testtag", "sample console log line for test")

	// Test GET
	req := httptest.NewRequest("GET", "/api/translator/console-logs", nil)
	w := httptest.NewRecorder()
	HandleConsoleLogsGet(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Success bool     `json:"success"`
		Logs    []string `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success: true")
	}
	if len(resp.Logs) == 0 {
		t.Fatal("expected at least 1 log line")
	}

	found := false
	for _, l := range resp.Logs {
		if strings.Contains(l, "sample console log line for test") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find test log line, got: %v", resp.Logs)
	}

	// Test DELETE
	delReq := httptest.NewRequest("DELETE", "/api/translator/console-logs", nil)
	delW := httptest.NewRecorder()
	HandleConsoleLogsDelete(delW, delReq)

	if delW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", delW.Code)
	}

	// Verify cleared
	reqAfter := httptest.NewRequest("GET", "/api/translator/console-logs", nil)
	wAfter := httptest.NewRecorder()
	HandleConsoleLogsGet(wAfter, reqAfter)

	var respAfter struct {
		Success bool     `json:"success"`
		Logs    []string `json:"logs"`
	}
	json.Unmarshal(wAfter.Body.Bytes(), &respAfter)
	if len(respAfter.Logs) != 0 {
		t.Fatalf("expected 0 logs after clear, got %d", len(respAfter.Logs))
	}
}
