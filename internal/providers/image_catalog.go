package providers

import "strings"

// imageModelMarkers identify catalogue entries that generate images rather than
// chat text. Kept as markers rather than an explicit list so a new image model
// from any provider is picked up without a second edit.
var imageModelMarkers = []string{
	"image", "imagen", "dall-e", "flux", "sd3", "stable-diffusion", "stable-image",
	"sdwebui", "sd35", "gen4", "seedance", "recraft", "venice-sd",
}

// IsImageModelID reports whether a model id from ProviderModels generates images.
func IsImageModelID(modelID string) bool {
	m := strings.ToLower(modelID)
	for _, marker := range imageModelMarkers {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// DefaultImageModel returns the model id to advertise for a provider whose
// catalogue carries no recognisable image entry, so /v1/models/image never
// hands out an id that resolution rejects.
func DefaultImageModel(provider string) string {
	if models := ProviderModels[provider]; len(models) > 0 {
		for _, m := range models {
			if IsImageModelID(m) {
				return m
			}
		}
		return models[0]
	}
	return "default"
}

// ProviderAliasFor resolves a provider id to the alias its catalogue is keyed
// by (xai -> xai, fal-ai -> fal-ai, ...). Falls back to the id itself.
func ProviderAliasFor(provider string) string {
	if _, ok := ProviderModels[provider]; ok {
		return provider
	}
	return GetProviderAlias(provider)
}
