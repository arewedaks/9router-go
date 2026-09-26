package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// maxProxyListBytes bounds a pasted proxy list. A few hundred vendors' exports
// are a couple of hundred KB at most; anything far larger is a mistake (or an
// attempt to make the server hold a huge string in memory) and is rejected
// before it is parsed.
const maxProxyListBytes = 2 << 20 // 2 MiB

// proxyImportPayload is the body of POST /proxy-pools.
//
// `list` is the raw pasted text, one proxy per line, so the browser does not
// have to parse anything and every format detail (CRLF, comments, scheme-less
// entries) is handled in one place on the server.
type proxyImportPayload struct {
	Name       string   `json:"name"`
	List       string   `json:"list"`
	URLs       []string `json:"urls,omitempty"`
	Type       string   `json:"type,omitempty"`
	NoProxy    string   `json:"noProxy,omitempty"`
	Strict     bool     `json:"strictProxy,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Strategy   string   `json:"rotateStrategy,omitempty"`
	ActivateIt bool     `json:"assign,omitempty"`
}

// HandleCreateProxyPool imports a proxy list and, optionally, attaches the new
// pool to a provider in the same request.
//
// Both steps are one endpoint because they are useless apart: a pool nobody
// targets never routes anything, and a provider strategy pointing at a pool
// that does not exist resolves to "" (a silent fall back to the host IP, not an
// error). Doing them together means the UI cannot half-apply the change.
func (h *Handler) HandleCreateProxyPool(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyListBytes+1))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	if len(body) > maxProxyListBytes {
		handlerutil.WriteJSONError(w, http.StatusRequestEntityTooLarge, "Proxy list is too large (limit 2 MiB)")
		return
	}

	var payload proxyImportPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	// Accept either the raw pasted text or an already-split array, so a caller
	// that has the entries in hand need not re-join them.
	entries := payload.URLs
	skipped := 0
	if len(entries) == 0 {
		if strings.TrimSpace(payload.List) == "" {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "Proxy list is empty")
			return
		}
		entries, skipped = db.ParseProxyList(payload.List)
	} else {
		normalised := make([]string, 0, len(entries))
		for _, e := range entries {
			if u, ok := db.NormalizeProxyURL(e); ok {
				normalised = append(normalised, u)
			} else {
				skipped++
			}
		}
		entries = normalised
	}
	if len(entries) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "No usable proxy entries found (expected user:pass@host:port or host:port)")
		return
	}

	name := strings.TrimSpace(payload.Name)
	if name == "" {
		name = "Imported proxies"
	}

	poolData, err := db.ProxyPoolDataFromList(name, entries, payload.Type, payload.NoProxy, payload.Strict)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	pool, err := h.repo.InsertProxyPool(poolData)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := map[string]any{
		"pool":     pool,
		"imported": len(entries),
		"skipped":  skipped,
	}

	// Attaching a pool to a provider is what makes it take effect, so do it in
	// the same call and report the provider it was wired to.
	if provider := strings.TrimSpace(payload.Provider); provider != "" {
		poolID, _ := pool["id"].(string)
		strat, err := h.repo.GetSettings()
		existing := db.ProviderStrategy{}
		if err == nil && strat != nil {
			existing = strat.ProviderStrategies[provider]
		}
		existing.ProxyPoolID = poolID
		if s := strings.TrimSpace(payload.Strategy); s != "" {
			existing.RotateStrategy = s
		}
		if existing.RotateStrategy == "" {
			existing.RotateStrategy = "none"
		}
		if err := h.repo.SetProviderStrategy(provider, existing); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp["provider"] = provider
		resp["proxyPoolId"] = poolID
	}

	handlerutil.WriteJSON(w, http.StatusCreated, resp)
}
