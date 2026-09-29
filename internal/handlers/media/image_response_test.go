package media

import (
	"encoding/base64"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/handlers/chat"
)

// The 9router-image skill downloads an image with ?response_format=binary.
// Before this, /v1/images/generations always answered JSON, so --output out.png
// wrote a JSON document to disk.
func TestWriteImageResponseBinary(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	upstream, _ := json.Marshal(map[string]any{
		"created": 1,
		"data":    []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(png)}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations?response_format=binary", nil)
	rec := httptest.NewRecorder()
	writeImageResponse(rec, req, upstream, []byte(`{"prompt":"a cat"}`), &chat.ModelInfo{}, "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q, want image/png", ct)
	}
	if got := rec.Body.Bytes(); string(got) != string(png) {
		t.Fatalf("body = %x, want raw PNG %x", got, png)
	}
}

// Without the flag the endpoint keeps answering OpenAI-shaped JSON.
func TestWriteImageResponseJSONByDefault(t *testing.T) {
	upstream, _ := json.Marshal(map[string]any{
		"created": 1,
		"data":    []map[string]any{{"b64_json": "AAAA"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	rec := httptest.NewRecorder()
	writeImageResponse(rec, req, upstream, []byte(`{"prompt":"a cat"}`), &chat.ModelInfo{}, "application/json")

	if body := rec.Body.String(); !strings.Contains(body, `"b64_json"`) {
		t.Fatalf("body = %s, want OpenAI JSON with b64_json", body)
	}
}

// A raw Gemini envelope (what Antigravity returns) is normalised before being
// written, so the client never sees an undocumented shape.
func TestWriteImageResponseNormalisesGemini(t *testing.T) {
	gemini, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content": map[string]any{
				"parts": []map[string]any{{"inlineData": map[string]any{"data": "QUJD"}}},
			},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	rec := httptest.NewRecorder()
	writeImageResponse(rec, req, gemini, []byte(`{"prompt":"a cat"}`), &chat.ModelInfo{}, "application/json")

	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(out.Data) == 0 || out.Data[0].B64JSON != "QUJD" {
		t.Fatalf("data = %+v, want b64_json QUJD", out.Data)
	}
}

// The Antigravity envelope nests the Gemini payload: the inline image lives at
// {"response":{"candidates":[{"content":{"parts":[{"inlineData":...}]}}]}}.
// Normalisation has to unwrap that level, otherwise the client receives the
// undocumented envelope instead of OpenAI JSON.
func TestWriteImageResponseUnwrapsNestedEnvelope(t *testing.T) {
	nested, _ := json.Marshal(map[string]any{
		"response": map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"parts": []map[string]any{{"inlineData": map[string]any{"data": "QUJD"}}},
				},
			}},
		},
		"traceId": "abc",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	rec := httptest.NewRecorder()
	writeImageResponse(rec, req, nested, []byte(`{"prompt":"a cat"}`), &chat.ModelInfo{}, "application/json")

	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(out.Data) == 0 || out.Data[0].B64JSON != "QUJD" {
		t.Fatalf("data = %+v, want b64_json QUJD", out.Data)
	}
}

// The caller's size must survive to the provider: Antigravity encodes the
// aspect ratio as a suffix on the model name, so forwarding the model
// unchanged silently renders everything 1:1.
func TestModelWithSizeSuffix(t *testing.T) {
	cases := []struct{ model, size, want string }{
		{"gemini-3.1-flash-image", "1792x1024", "gemini-3.1-flash-image-16x9"},
		{"gemini-3.1-flash-image", "1024x1024", "gemini-3.1-flash-image-1x1"},
		{"gemini-3.1-flash-image", "", "gemini-3.1-flash-image"},
		{"gemini-3.1-flash-image", "999x999", "gemini-3.1-flash-image"},
	}
	for _, c := range cases {
		if got := modelWithSizeSuffix(c.model, c.size); got != c.want {
			t.Errorf("modelWithSizeSuffix(%q,%q) = %q, want %q", c.model, c.size, got, c.want)
		}
	}
}

func TestNormalizeImageResponse_StabilityAI(t *testing.T) {
	raw := []byte(`{"image": "AQIDBA=="}`)
	out := normalizeImageResponse(raw, "test")
	var res struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res.Data) == 0 || res.Data[0].B64 != "AQIDBA==" {
		t.Fatalf("unexpected normalized output: %s", string(out))
	}
}

func TestNormalizeImageResponse_SDWebUI(t *testing.T) {
	raw := []byte(`{"images": ["AQIDBA==", "BQYHCA=="]}`)
	out := normalizeImageResponse(raw, "test")
	var res struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res.Data) != 2 || res.Data[1].B64 != "BQYHCA==" {
		t.Fatalf("unexpected normalized output: %s", string(out))
	}
}

func TestNormalizeImageResponse_FalAI(t *testing.T) {
	raw := []byte(`{"images": [{"url": "https://fal.media/files/image.png"}]}`)
	out := normalizeImageResponse(raw, "test")
	var res struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res.Data) == 0 || res.Data[0].URL != "https://fal.media/files/image.png" {
		t.Fatalf("unexpected normalized output: %s", string(out))
	}
}

func TestNormalizeImageResponse_BFL(t *testing.T) {
	raw := []byte(`{"result": {"sample": "https://bfl.ai/sample.png"}}`)
	out := normalizeImageResponse(raw, "test")
	var res struct {
		Data []struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &res); err != nil || len(res.Data) == 0 || res.Data[0].URL != "https://bfl.ai/sample.png" {
		t.Fatalf("unexpected normalized output: %s", string(out))
	}
}
