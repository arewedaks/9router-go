package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// HandleSetClientProfile updates the Antigravity client profile (ide|cli) for a
// single connection. The profile is stored inside the connection's data blob
// under providerSpecificData.clientProfile, mirroring OmniRoute so a connection
// imported from either dashboard keeps working.
//
// POST /api/dashboard/connections/{id}/client-profile
//
//	{"profile":"ide"|"cli"}
func (h *Handler) HandleSetClientProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var payload struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if !providers.IsAntigravityClientProfile(payload.Profile) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, `profile must be "ide" or "cli"`)
		return
	}
	profile := providers.NormalizeAntigravityClientProfile(payload.Profile)

	conn, err := h.repo.GetProviderConnectionByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to load connection: "+err.Error())
		return
	}
	if conn == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Connection not found")
		return
	}
	if !isAntigravityProvider(conn.Provider) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Connection is not an Antigravity provider")
		return
	}

	data := map[string]any{}
	if strings.TrimSpace(conn.Data) != "" {
		if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Stored connection data is not valid JSON: "+err.Error())
			return
		}
	}
	psd, _ := data["providerSpecificData"].(map[string]any)
	if psd == nil {
		psd = map[string]any{}
	}
	psd[providers.ClientProfileKey] = string(profile)
	data["providerSpecificData"] = psd

	encoded, err := json.Marshal(data)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to encode connection data: "+err.Error())
		return
	}
	conn.Data = string(encoded)
	if err := h.repo.UpsertProviderConnection(conn); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to save connection: "+err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"id":      conn.ID,
		"profile": string(profile),
	})
}
