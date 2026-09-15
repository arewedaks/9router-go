package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"

	"9router/proxy/internal/handlerutil"
)

// HandleGetSettings handles GET /api/settings.
// Reads raw settings data map.
func (h *DashboardHandler) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	raw, err := h.Repo.GetSettingsRaw()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if raw == nil {
		raw = make(map[string]any)
	}
	handlerutil.WriteJSON(w, http.StatusOK, raw)
}

// HandleUpdateSettings handles PUT /api/settings.
// Calls UpdateSettingsRaw with incoming JSON updates.
func (h *DashboardHandler) HandleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var updates map[string]any
	if err := json.Unmarshal(body, &updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.Repo.UpdateSettingsRaw(updates); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	updated, err := h.Repo.GetSettingsRaw()
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, updated)
}
