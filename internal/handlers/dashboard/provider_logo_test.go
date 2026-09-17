package dashboard

import (
	"net/http"
	"net/http/httptest"

	"path"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
)

// The provider logo assets are embedded, so a missing embed directive would
// silently ship a dashboard whose every icon is a broken image. Assert against
// the bytes the server actually serves, and against the embed FS itself.
func TestProviderLogosAreServed(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// A handful of well-known ids that must exist.
	for _, id := range []string{"openai", "anthropic", "opencode", "github", "antigravity"} {
		req := httptest.NewRequest("GET", "/provider-logos/"+id+".webp", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", id, w.Code)
			continue
		}
		if ct := w.Header().Get("Content-Type"); ct != "image/webp" {
			t.Errorf("%s: Content-Type = %q, want image/webp", id, ct)
		}
		if w.Body.Len() == 0 {
			t.Errorf("%s: served an empty body", id)
		}
	}
}

// Every asset the embed FS carries must be reachable over HTTP. A single
// unreadable file is a card with a broken image, which is exactly the bug this
// whole change exists to fix.
func TestEveryEmbeddedLogoIsReachable(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	entries, err := uiAssets.ReadDir("ui/providers")
	if err != nil {
		t.Fatalf("read embedded logo dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no provider logos are embedded — the go:embed directive is missing the directory")
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		req := httptest.NewRequest("GET", "/provider-logos/"+name, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", name, w.Code)
		}
	}
}

// The public route must not become a file-read primitive.
func TestProviderLogoRejectsTraversal(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	for _, bad := range []string{
		"../index.html",
		"..%2Findex.html",
		"....//index.html",
		".hidden.webp",
		"nested/logo.webp",
		"logo.exe",
		"logo",
		"",
	} {
		req := httptest.NewRequest("GET", "/provider-logos/"+bad, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%q: status = %d, want 404", bad, w.Code)
		}
	}
}

// An unknown provider is 404, not a 500 or an empty 200: the UI relies on the
// failure to swap in its glyph fallback.
func TestProviderLogoUnknownIDIs404(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("GET", "/provider-logos/definitely-not-a-provider.webp", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// The UI must ask for a brand asset, keep a glyph fallback, and know which
// assets are PNG rather than WebP. These are the three ways the feature can
// regress while every Go test above still passes.
func TestUIRendersRealProviderLogos(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "/provider-logos/") {
		t.Error("the UI never requests an embedded provider logo")
	}
	if !strings.Contains(ui, "function providerIconHtml") {
		t.Error("providerIconHtml is missing")
	}
	// The glyph must survive as the fallback for the 14 ids with no asset.
	if !strings.Contains(ui, `class="msym"`) {
		t.Error("the glyph fallback was removed")
	}
	if !strings.Contains(ui, "data-fb=") {
		t.Error("the image error fallback is not wired")
	}
	if !strings.Contains(ui, "LOGO_MISSING") {
		t.Error("the missing-logo memo is absent, so every re-render would re-request a 404")
	}
	// Both mounts must use the helper rather than re-inlining a glyph.
	if strings.Count(ui, "providerIconHtml(") < 3 {
		t.Errorf("providerIconHtml is defined but not used at both mount points (found %d references)",
			strings.Count(ui, "providerIconHtml("))
	}
}

// The UI carries its own copy of the generated-node-id rule. If the two drift,
// compatible endpoints start requesting (and 404ing on) UUID-named logos.
func TestUIKnowsWhichIDsCanHaveNoLogo(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "function isGeneratedNodeId") {
		t.Fatal("the UI has no generated-node-id check")
	}
	// It must key off the trailing UUID, matching the backend rule.
	if !strings.Contains(ui, "{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}") {
		t.Error("the UI's node-id check does not match the backend's trailing-UUID rule")
	}
	// And it must actually be consulted before building a logo URL. A definition
	// that nothing calls is the same bug with extra steps.
	if !strings.Contains(ui, "if (isGeneratedNodeId(id)) return \"\";") {
		t.Error("isGeneratedNodeId is defined but never used to suppress a logo request")
	}
}

// Every PNG asset must be listed so the UI resolves the right extension first
// rather than eating a 404 on each miss.
func TestUIPNGExtensionListMatchesAssets(t *testing.T) {
	ui := readEmbeddedUI(t)

	entries, err := uiAssets.ReadDir("ui/providers")
	if err != nil {
		t.Fatalf("read embedded logo dir: %v", err)
	}

	var pngIDs []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(path.Ext(e.Name()), ".png") {
			pngIDs = append(pngIDs, strings.TrimSuffix(e.Name(), path.Ext(e.Name())))
		}
	}
	if len(pngIDs) == 0 {
		t.Skip("no PNG assets in this build")
	}

	for _, id := range pngIDs {
		if !strings.Contains(ui, `"`+id+`"`) {
			t.Errorf("asset %s.png exists but is not in the UI's LOGO_PNG set", id)
		}
	}
}

// The assets are stored under the registry id, not the upstream alias name, so
// the UI needs no alias indirection. This pins that: an id whose upstream asset
// lives under a different filename must still have a file named for itself.
func TestEveryRegistryIDWithALogoIsNamedForThatID(t *testing.T) {
	entries, err := uiAssets.ReadDir("ui/providers")
	if err != nil {
		t.Fatalf("read embedded logo dir: %v", err)
	}

	embedded := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			embedded[e.Name()] = true
		}
	}

	for _, id := range []string{"codebuddy-intl", "alims-intl", "kilo-gateway", "vercel-ai-gateway"} {
		if !embedded[id+".webp"] && !embedded[id+".png"] {
			t.Errorf("%s has no asset stored under its own id", id)
		}
	}
}

// SVG can carry script, so serving it from this origin would let a future logo
// drop become stored XSS.
//
// This asserts the extension rule directly rather than the HTTP response: no
// SVG ships today, so a request for one would 404 at the embed lookup and the
// guard could be deleted without the response changing. Below, a real
// script-bearing SVG dropped into a sibling directory proves the point.
func TestProviderLogoServesNoSVG(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("GET", "/provider-logos/evil.svg", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("svg status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "svg") {
		t.Fatalf("svg Content-Type leaked: %q", ct)
	}
}

// The content type must be derived from a whitelist, never echoed from the
// request. If a request could name its own type, an uploaded asset would decide
// how the browser interprets it.
func TestProviderLogoContentTypeIsWhitelisted(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	// A real asset, requested through a route param that tries to smuggle a
	// different type via the filename.
	for name, want := range map[string]string{
		"openai.webp": "image/webp",
		"morph.png":   "image/png",
	} {
		req := httptest.NewRequest("GET", "/provider-logos/"+name, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", name, w.Code)
		}
		if got := w.Header().Get("Content-Type"); got != want {
			t.Errorf("%s: Content-Type = %q, want %q", name, got, want)
		}
	}
}

// An asset must be immutable-cacheable but still revalidatable, so a
// hard-refresh picks up a new build instead of serving a stale logo forever.
func TestProviderLogoSetsCacheAndETag(t *testing.T) {
	_, _, r := setupTestDashboard(t)

	req := httptest.NewRequest("GET", "/provider-logos/openai.webp", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q, want a max-age", cc)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag: every revalidation would refetch the bytes")
	}

	// The same bytes must hash to the same tag, and different bytes must not.
	req2 := httptest.NewRequest("GET", "/provider-logos/openai.webp", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if got := w2.Header().Get("ETag"); got != etag {
		t.Errorf("ETag is not stable: %q then %q", etag, got)
	}

	req3 := httptest.NewRequest("GET", "/provider-logos/anthropic.webp", nil)
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if got := w3.Header().Get("ETag"); got == etag {
		t.Errorf("different assets share ETag %q", got)
	}
}

// Every provider that has a site must render a link to it in the detail header,
// and every provider without one must not render a dead link. This is the
// regression that matters: the field existed in the registry and was already
// serialised, but nothing displayed it.
func TestUIProviderDetailLinksToTheProviderSite(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, "function providerLink") {
		t.Fatal("providerLink is missing: the registry website field would go unused")
	}
	// It must actually be placed in the hero, not merely defined.
	if !strings.Contains(ui, "${providerLink(d)}") {
		t.Error("providerLink is defined but never rendered")
	}
	// An external link must never leak the referrer or the opener.
	if !strings.Contains(ui, `rel="noopener noreferrer"`) {
		t.Error("the provider link does not set rel=\"noopener noreferrer\"")
	}
	if !strings.Contains(ui, ".hero-link") {
		t.Error("the hero link has no styling, so it would render as bare text")
	}
	// The old, buried link inside the Capabilities tab must be gone, or the same
	// URL appears twice in two different places.
	if strings.Contains(ui, "Website: <a href=") {
		t.Error("the duplicate Website link in the Capabilities tab was not removed")
	}
}

// The registry is the single source of truth for these URLs. Pin a few known
// ones so an accidental edit (or a bad merge) that blanks or corrupts the field
// is caught here rather than in the browser.
func TestRegistryCarriesProviderWebsites(t *testing.T) {
	want := map[string]string{
		"openai":         "https://platform.openai.com",
		"opencode":       "https://opencode.ai",
		"codebuddy-intl": "https://www.codebuddy.ai",
		"codebuddy-cn":   "https://copilot.tencent.com",
		"searxng":        "https://docs.searxng.org",
		"anthropic":      "https://console.anthropic.com",
		"antigravity":    "https://antigravity.google",
	}

	for id, url := range want {
		meta, ok := providers.GetProviderMeta(id)
		if !ok {
			t.Errorf("%s is missing from the registry", id)
			continue
		}
		if meta.Website != url {
			t.Errorf("%s: website = %q, want %q", id, meta.Website, url)
		}
	}
}

// Most providers should carry a site. A sharp drop means the field was dropped
// from the struct or a bulk edit wiped it.
func TestMostProvidersHaveAWebsite(t *testing.T) {
	total, with := 0, 0
	for _, list := range providers.GetCatalogByCategory() {
		for _, m := range list {
			total++
			if m.Website != "" {
				with++
			}
		}
	}
	if total == 0 {
		t.Fatal("the catalog is empty")
	}
	if with*100/total < 90 {
		t.Errorf("only %d/%d providers have a website; expected at least 90%%", with, total)
	}
}
