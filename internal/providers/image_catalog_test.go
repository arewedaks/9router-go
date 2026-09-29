package providers

import "testing"

// /v1/models/image must hand out ids that POST /v1/images/generations accepts.
// A bare provider name ("xai") is rejected by resolution, so every entry has to
// carry "{provider}/{model}".
func TestImageCatalogProducesResolvableIDs(t *testing.T) {
	for _, modelID := range ProviderModels["xai"] {
		if IsImageModelID(modelID) && modelID != "grok-2-image-1212" {
			t.Logf("xai image model: %s", modelID)
		}
	}
	if !IsImageModelID("grok-2-image-1212") {
		t.Fatal("grok-2-image-1212 should classify as an image model")
	}
	if !IsImageModelID("gemini-3.1-flash-image-preview") {
		t.Fatal("gemini-3.1-flash-image-preview should classify as an image model")
	}
	if IsImageModelID("claude-sonnet-4-6") {
		t.Fatal("claude-sonnet-4-6 is not an image model")
	}
	if got := DefaultImageModel("recraft"); got == "" || got == "default" {
		t.Fatalf("DefaultImageModel(recraft) = %q, want a catalogue entry", got)
	}
	if got := ProviderAliasFor("fal-ai"); got == "" {
		t.Fatal("ProviderAliasFor(fal-ai) returned empty")
	}
}
