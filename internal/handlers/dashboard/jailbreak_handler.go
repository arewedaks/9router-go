package dashboard

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// Jailbreak management: one agents.md-style prompt document applied globally,
// plus an on/off toggle per provider. A provider with the toggle off never
// receives the prompt — injection is opt-in per provider, default off.

func (h *Handler) HandleGetJailbreak(w http.ResponseWriter, r *http.Request) {
	doc, err := h.repo.GetJailbreakDoc()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, doc)
}

const maxJailbreakBodyBytes = 64 * 1024

func (h *Handler) HandlePutJailbreak(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJailbreakBodyBytes+1))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var doc db.JailbreakDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.repo.SaveJailbreakDoc(&doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) HandleListJailbreakProviders(w http.ResponseWriter, r *http.Request) {
	toggles, err := h.repo.ListJailbreakProviders()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, toggles)
}

func (h *Handler) HandleSetJailbreakProvider(w http.ResponseWriter, r *http.Request) {
	providerID := chi.URLParam(r, "id")
	if providerID == "" || len(providerID) > 200 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid provider id")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.repo.SetJailbreakProvider(providerID, req.Enabled); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": req.Enabled})
}
