package paramfix

import (
	"encoding/json/v2"
	"strings"
	"testing"
)

// A request with no images at all must never be touched by the image repair,
// whatever the upstream says. This is the regression that would break every
// model at once, so it is pinned.
func TestTrimImagesNeverTouchesImageFreeRequest(t *testing.T) {
	bodies := []string{
		`{"model":"m","messages":[{"role":"user","content":"plain string"}]}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}],"tools":[{"type":"function"}]}`,
		`{"model":"m","messages":[{"role":"user","content":""}]}`,
		`{"model":"m","messages":[]}`,
		`{"model":"m"}`,
	}
	for _, body := range bodies {
		for _, limit := range []int{1, 2, 8, 100} {
			out, removed := TrimImagesToLimit([]byte(body), limit)
			if removed != 0 {
				t.Errorf("body %s (limit %d): removed %d images from an image-free request", body, limit, removed)
			}
			if string(out) != body {
				t.Errorf("body %s (limit %d): image-free request was rewritten to %s", body, limit, out)
			}
		}
	}
}

// The refusal the field repair handles must not be mistaken for an image limit,
// and vice versa. A wrong classification sends the wrong repair.
func TestRefusalKindsAreNotConfused(t *testing.T) {
	imageRefusal := []byte(`{"message":"This request carries 9 images; glm-5.3-flash takes at most 8 per request.","param":"messages"}`)
	fieldRefusal := []byte(`{"message":"Unrecognized request arguments supplied: store"}`)

	if _, ok := ImageLimitFromErrorBody(fieldRefusal); ok {
		t.Error("a field rejection must not be read as an image limit")
	}
	if f := UnknownArgumentFromErrorBody(imageRefusal); f != "" {
		t.Errorf("an image rejection must not name a droppable field, got %q", f)
	}
}

// A body already at or under the limit must come back byte-identical, because a
// rewritten body changes the request the caller gets billed for and answered.
func TestTrimImagesIsBytePreservingWhenUnderLimit(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"u"}}]}],"temperature":0.7,"unknown_field":{"a":[1,2,3]}}`)
	out, removed := TrimImagesToLimit(body, 8)
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if string(out) != string(body) {
		t.Error("a request under the limit must pass through byte-identical")
	}
	// And it must still be valid JSON with the unknown field intact.
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := m["unknown_field"]; !ok {
		t.Error("an unrelated field must survive")
	}
}

// A limit of zero or less is meaningless; it must not be read as "remove all".
func TestTrimImagesIgnoresNonPositiveLimit(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"u"}}]}]}`)
	for _, limit := range []int{0, -1} {
		out, removed := TrimImagesToLimit(body, limit)
		if removed != 0 || string(out) != string(body) {
			t.Errorf("limit %d must be ignored, got removed=%d", limit, removed)
		}
	}
}

// A refusal naming a message field must not be mistaken for a top-level one.
func TestUnknownArgumentIgnoresNestedLoc(t *testing.T) {
	nested := []byte(`{"type":"extra_forbidden","loc":["body","messages",0,"content"],"msg":"Extra inputs are not permitted"}`)
	if f := UnknownArgumentFromErrorBody(nested); f != "" {
		t.Errorf("a nested loc must not yield a droppable top-level field, got %q", f)
	}
	if strings.Contains(string(nested), "unrecognized") {
		t.Skip("fixture drift")
	}
}
