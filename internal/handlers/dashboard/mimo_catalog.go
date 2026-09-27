package dashboard

import (
	"strings"

	"9router/proxy/internal/providers"
)

// mimo_catalog.go — Import catalogue for the keyless MiMo Code Free provider
// (mimo-free, alias mmf).
//
// Why static: the free-ai surface (api.xiaomimimo.com/api/free-ai/*) has no
// /models route — every GET candidate answers 404 NOT_FOUND wrapped in a 400.
// The only model names Xiaomi publishes are the platform ones
// (models.dev "xiaomi" provider + the MiMo Code docs): mimo-v2.5-pro is the
// flagship, with v2.6 Pro/Flash and v2.5 tier ids beside it. This file lists
// those platform ids so Import offers rows that are, at worst, refused by the
// upstream with its own error — the same static-catalogue trade-off Freebuff
// already makes.
//
// NOTE: the free tier gates chat behind its own server-side auth (the
// bootstrap JWT of an anonymous fingerprint is accepted but every model id
// probed live answered "Unsupported model"), so these rows document what the
// platform serves, not a guaranteed free chat path. If Xiaomi opens the
// free-ai model list, replace this with the live fetcher.

// mimoFreeCatalog pairs a platform model id with a friendly label.
var mimoFreeCatalog = []struct {
	ID   string
	Name string
}{
	{"mimo-v2.5-pro", "MiMo V2.5 Pro"},
	{"mimo-v2.6-pro", "MiMo V2.6 Pro"},
	{"mimo-v2.6-flash", "MiMo V2.6 Flash"},
	{"mimo-v2.5", "MiMo V2.5"},
	{"mimo-v2-flash", "MiMo V2 Flash"},
	{"mimo-v2.5-tts", "MiMo V2.5 TTS"},
}

// mimoFreeStaticModels returns the catalogue as importable upstream models.
func mimoFreeStaticModels() []UpstreamModel {
	out := make([]UpstreamModel, 0, len(mimoFreeCatalog))
	for _, m := range mimoFreeCatalog {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out = append(out, UpstreamModel{ID: id, Name: m.Name})
	}
	sortUpstreamModels(out)
	return out
}

// isMimoFreeProvider reports whether a provider id (canonical or alias) is the
// keyless MiMo Code Free provider.
func isMimoFreeProvider(id string) bool {
	return providers.ResolveAlias(strings.TrimSpace(id)) == "mimo-free"
}
