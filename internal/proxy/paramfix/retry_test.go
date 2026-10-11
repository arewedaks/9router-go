package paramfix

import (
	"encoding/json/v2"
	"testing"
)

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, raw)
	}
	return got
}

// The two error shapes seen in production. Upstage answers with the first,
// Mistral's pydantic validator with the second; both must yield the field name.
func TestUnknownArgumentFromErrorBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "upstage unrecognized arguments",
			body: `{"message":"Unrecognized request arguments supplied: store","type":"invalid_request_error","param":null,"code":null}`,
			want: "store",
		},
		{
			name: "mistral extra_forbidden",
			body: `{"type":"extra_forbidden","loc":["body","store"],"msg":"Extra inputs are not permitted","input":false}`,
			want: "store",
		},
		{
			name: "singular argument",
			body: `{"message":"Unrecognized request argument supplied: stream_options"}`,
			want: "stream_options",
		},
		{
			name: "dotted field name",
			body: `{"type":"extra_forbidden","loc":["body","response_format.type"]}`,
			want: "response_format.type",
		},
		{
			name: "a different 400 is not a parameter rejection",
			body: `{"error":{"message":"context length exceeded","type":"invalid_request_error"}}`,
			want: "",
		},
		{
			name: "a nested message field is not a top-level one",
			body: `{"type":"extra_forbidden","loc":["body","messages",0,"reasoning_content"]}`,
			want: "",
		},
		{name: "empty body", body: "", want: ""},
		{name: "html error page", body: "<html>502 Bad Gateway</html>", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnknownArgumentFromErrorBody([]byte(tc.body)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDropFields(t *testing.T) {
	in := []byte(`{"model":"solar-pro4","store":false,"stream":true,"messages":[]}`)

	out, changed := DropFields(in, "store")
	if !changed {
		t.Fatal("dropping a present field must report a change")
	}
	got := decode(t, out)
	if _, present := got["store"]; present {
		t.Error("store must be gone")
	}
	if got["stream"] != true || got["model"] != "solar-pro4" {
		t.Errorf("unrelated fields must survive: %v", got)
	}

	// A field the request does not carry must not report a change, or the retry
	// loop would resend an identical request.
	if _, changed := DropFields(in, "metadata"); changed {
		t.Error("dropping an absent field must report no change")
	}
	// An unparseable body is not ours to rewrite.
	if out, changed := DropFields([]byte("not json"), "store"); changed {
		t.Errorf("unparseable body must pass through, got %s", out)
	}
}

// The allowlist is what keeps the auto-retry from serving a different request
// than the caller asked for. `messages` and `model` being rejected is a schema
// error, not a stray optional parameter.
func TestIsSafeToDropAllowlist(t *testing.T) {
	for _, f := range []string{"store", "stream_options", "service_tier", "STORE"} {
		if !IsSafeToDrop(f) {
			t.Errorf("%q must be droppable", f)
		}
	}
	for _, f := range []string{"messages", "model", "tools", "input", "temperature", "max_tokens", ""} {
		if IsSafeToDrop(f) {
			t.Errorf("%q must never be dropped automatically", f)
		}
	}
}
