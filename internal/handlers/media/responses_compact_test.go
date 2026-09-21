package media

import (
	json "encoding/json/v2"
	"testing"
)

// The "_compact" marker is how a client asks for compaction, but it is not part
// of the Responses schema. It has to be removed before dispatch or the provider
// rejects the body.
func TestStripCompactMarkerRemovesMarker(t *testing.T) {
	in := `{"_compact":true,"model":"gpt-4","input":[{"role":"user","content":"hi"}]}`
	out := stripCompactMarker([]byte(in))

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, out)
	}
	if _, ok := m["_compact"]; ok {
		t.Errorf("_compact survived: %s", out)
	}
	// Everything else has to be preserved, because the Responses payload is what
	// the upstream actually needs.
	if m["model"] != "gpt-4" {
		t.Errorf("model was lost: %s", out)
	}
	if _, ok := m["input"]; !ok {
		t.Errorf("the input array was lost: %s", out)
	}
}

// A body without the marker must come back byte-identical. Rewriting every
// request would risk perturbing bodies that never asked for compaction.
func TestStripCompactMarkerLeavesOtherBodiesAlone(t *testing.T) {
	for _, in := range []string{
		`{"model":"gpt-4","input":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-4","_notcompact":true}`,
		``,
	} {
		if got := string(stripCompactMarker([]byte(in))); got != in {
			t.Errorf("body changed:\n in = %q\nout = %q", in, got)
		}
	}
}

// A malformed body must reach the upstream unchanged so the provider produces
// the real diagnostic, rather than this helper swallowing the request or
// replacing the error with a JSON parse failure of its own.
func TestStripCompactMarkerPassesMalformedBodyThrough(t *testing.T) {
	in := `{"_compact":true,"input":[`
	if got := string(stripCompactMarker([]byte(in))); got != in {
		t.Errorf("malformed body was rewritten:\n in = %q\nout = %q", in, got)
	}
}
