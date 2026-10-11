package paramfix

import (
	"encoding/json/v2"
	"testing"
)

// The exact refusal Wally/RunAnywhere returns for glm-5.3-flash. Note `param` is
// `messages`: the images live inside it, which is why the field-drop repair must
// not touch this case.
const wallyImageLimitBody = `{"message":"This request carries 9 images; glm-5.3-flash takes at most 8 per request.","type":"invalid_request_error","code":"bad_request","param":"messages"}`

func TestImageLimitFromErrorBody(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		limit int
		ok    bool
	}{
		{name: "wally glm-5.3-flash", body: wallyImageLimitBody, limit: 8, ok: true},
		{
			name:  "different model and limit",
			body:  `{"message":"This request carries 5 images; some-model takes at most 2 images per request."}`,
			limit: 2, ok: true,
		},
		{
			name:  "singular image",
			body:  `{"message":"This request carries 3 images; m takes at most 1 image per request."}`,
			limit: 1, ok: true,
		},
		{name: "not an image limit", body: `{"message":"Unrecognized request arguments supplied: store"}`, ok: false},
		{name: "context length", body: `{"error":{"message":"context length exceeded"}}`, ok: false},
		{name: "empty", body: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, ok := ImageLimitFromErrorBody([]byte(tc.body))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && limit != tc.limit {
				t.Errorf("limit = %d, want %d", limit, tc.limit)
			}
		})
	}
}

func imgPart(url string) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

// textPart builds a text part so the test can tell messages apart.
func textPart(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

// countImagesIn counts images across the messages of a full request body.
func countImagesIn(t *testing.T, body []byte) int {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	return countImages(m["messages"].([]any))
}

// countParts counts images within one message's content array.
func countParts(parts []any) int {
	n := 0
	for _, p := range parts {
		if isImagePart(p) {
			n++
		}
	}
	return n
}

// The newest images are the ones the caller is asking about, so the oldest are
// removed first.
func TestTrimImagesDropsOldestFirst(t *testing.T) {
	body := map[string]any{
		"model": "glm-5.3-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{textPart("old"), imgPart("old-a"), imgPart("old-b")}},
			map[string]any{"role": "user", "content": []any{textPart("new"), imgPart("new-a"), imgPart("new-b")}},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	out, removed := TrimImagesToLimit(raw, 2)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if n := countImagesIn(t, out); n != 2 {
		t.Fatalf("remaining images = %d, want 2", n)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	msgs := m["messages"].([]any)
	// The last message must keep both its images; the first must keep none.
	lastParts := msgs[1].(map[string]any)["content"].([]any)
	if n := countParts(lastParts); n != 2 {
		t.Errorf("newest message must keep both images, got %d", n)
	}
	firstParts := msgs[0].(map[string]any)["content"].([]any)
	if n := countParts(firstParts); n != 0 {
		t.Errorf("oldest message must lose its images, got %d", n)
	}
	// The text that was there must survive.
	if len(firstParts) != 1 || firstParts[0].(map[string]any)["text"] != "old" {
		t.Errorf("text part must survive image removal, got %v", firstParts)
	}
}

// A body at or under the limit is not ours to touch.
func TestTrimImagesNoOpUnderLimit(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"u"}}]}]}`)
	out, removed := TrimImagesToLimit(body, 8)
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	if string(out) != string(body) {
		t.Error("a body under the limit must pass through byte-identical")
	}
}

// A message whose parts are all images must not become empty: an empty content
// array is rejected by most gateways, turning a repairable 400 into a worse one.
// Removing every image from a message inserts a placeholder instead.
func TestTrimImagesLeavesPlaceholderInEmptiedMessage(t *testing.T) {
	body := []byte(`{"model":"m","messages":[
		{"role":"user","content":[{"type":"image_url","image_url":{"url":"a"}},{"type":"image_url","image_url":{"url":"b"}}]},
		{"role":"user","content":[{"type":"image_url","image_url":{"url":"c"}}]}]}`)
	out, removed := TrimImagesToLimit(body, 1)
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	msgs := m["messages"].([]any)

	// The first message lost both images, so it must carry the placeholder.
	first := msgs[0].(map[string]any)["content"].([]any)
	if len(first) != 1 {
		t.Fatalf("emptied message must carry exactly one placeholder, got %d parts", len(first))
	}
	if isImagePart(first[0]) {
		t.Error("the placeholder must be text, not an image")
	}

	// The newest message keeps its image.
	last := msgs[1].(map[string]any)["content"].([]any)
	if len(last) != 1 || !isImagePart(last[0]) {
		t.Errorf("newest message must keep its image, got %v", last)
	}
}

// Gemini-style inlineData is counted too, so a mixed request cannot slip past.
func TestCountImagesRecognisesProviderSpellings(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"a"}},
		{"type":"input_image","image_url":"b"},
		{"inlineData":{"mimeType":"image/png","data":"c"}},
		{"type":"text","text":"not an image"}]}]}`)
	if n := countImagesIn(t, body); n != 3 {
		t.Errorf("counted %d images, want 3", n)
	}
}

// An unrecognised shape passes through rather than being rewritten.
func TestTrimImagesLeavesUnknownShapesAlone(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"model":"m"}`,
		`{"model":"m","messages":"a string"}`,
		`{"model":"m","messages":[{"role":"user","content":"plain text"}]}`,
	} {
		out, removed := TrimImagesToLimit([]byte(body), 1)
		if removed != 0 {
			t.Errorf("body %q: removed = %d, want 0", body, removed)
		}
		if string(out) != body {
			t.Errorf("body %q must pass through unchanged", body)
		}
	}
}
