package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// HandleSetAntigravityClientProfile sets the Antigravity client profile
// ("ide" or "cli") for the whole provider.
//
// The profile selects which official client identity 9router presents to
// Google's Code Assist backend. It describes the client being emulated, not the
// account sending the request, so it is stored once in the settings row rather
// than on every connection. It previously lived per connection, which let six
// accounts drift apart and made "which profile is actually in use?" unanswerable
// without inspecting each one.
//
// POST /api/dashboard/settings/antigravity-profile
//
//	{"profile":"ide"|"cli"}
func (h *Handler) HandleSetAntigravityClientProfile(w http.ResponseWriter, r *http.Request) {
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

	if err := h.repo.SetAntigravityClientProfile(string(profile)); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to save settings: "+err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"profile": string(profile),
	})
}
