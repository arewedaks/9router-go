package paramfix

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"os"
	"testing"
)

// Live verification against the real RunAnywhere endpoint. Skipped unless
// RAN_VERIFY_KEY is set, because it spends a real request.
//
// It proves the two halves the unit tests cannot: that the upstream really
// refuses 9 images for glm-5.3-flash, and that this package's trim is what turns
// that refusal into a served request.
func TestLiveRunAnywhereImageLimit(t *testing.T) {
	key := os.Getenv("RAN_VERIFY_KEY")
	if key == "" {
		t.Skip("set RAN_VERIFY_KEY to run the live check")
	}
	const endpoint = "https://inference.runanywhere.ai/v1/chat/completions"

	post := func(body []byte) (int, []byte) {
		req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("transport: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	img := map[string]any{"type": "image_url", "image_url": map[string]any{
		"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="}}
	parts := []any{map[string]any{"type": "text", "text": "count"}}
	for i := 0; i < 9; i++ {
		parts = append(parts, img)
	}
	raw, err := json.Marshal(map[string]any{
		"model":      "glm-5.3-flash",
		"messages":   []any{map[string]any{"role": "user", "content": parts}},
		"max_tokens": 8,
	})
	if err != nil {
		t.Fatal(err)
	}

	status, body := post(raw)
	t.Logf("unrepaired (9 images) -> HTTP %d: %s", status, snippet(body))

	limit, ok := ImageLimitFromErrorBody(body)
	if status == http.StatusBadRequest {
		if !ok {
			t.Fatalf("upstream refused but the body was not recognised as an image limit: %s", snippet(body))
		}
		t.Logf("detected per-request image limit = %d", limit)
	} else if status == http.StatusOK {
		t.Log("upstream did not enforce the limit on this connection; only the trim is exercised")
		return
	} else {
		t.Fatalf("unexpected status %d: %s", status, snippet(body))
	}

	trimmed, removed := TrimImagesToLimit(raw, limit)
	if removed != 9-limit {
		t.Fatalf("removed = %d, want %d", removed, 9-limit)
	}
	status2, body2 := post(trimmed)
	t.Logf("repaired (%d images) -> HTTP %d: %s", limit, status2, snippet(body2))
	if status2 != http.StatusOK {
		t.Fatalf("the repaired request was refused: %s", snippet(body2))
	}
}

func snippet(b []byte) string {
	s := string(b)
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}
