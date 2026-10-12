package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// Jailbreak management: agent prompt presets library, plus per-model preset binding.
// Injection is opt-in, default off.

func (h *Handler) HandleGetJailbreak(w http.ResponseWriter, r *http.Request) {
	doc, err := h.repo.GetJailbreakDoc()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, doc)
}

// A jailbreak prompt is often an uploaded .md document rather than a typed
// line, so the ceiling is generous while still bounded.
const maxJailbreakBodyBytes = 512 * 1024

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
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "doc": doc})
}

func (h *Handler) HandleListJailbreakPresets(w http.ResponseWriter, r *http.Request) {
	presets, err := h.repo.ListJailbreakPresets()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, presets)
}

func (h *Handler) HandlePutJailbreakPreset(w http.ResponseWriter, r *http.Request) {
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
	if doc.ID == "" {
		base := doc.Name
		if base == "" {
			base = doc.Source
		}
		doc.ID = slugify(base)
	}
	if doc.Name == "" {
		if doc.Source != "" {
			doc.Name = doc.Source
		} else {
			doc.Name = doc.ID
		}
	}
	if err := h.repo.SaveJailbreakPreset(&doc); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "preset": doc})
}

func (h *Handler) HandleDeleteJailbreakPreset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing preset id")
		return
	}
	if err := h.repo.DeleteJailbreakPreset(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

func (h *Handler) HandleListJailbreakModels(w http.ResponseWriter, r *http.Request) {
	configs, err := h.repo.ListJailbreakModelConfigs()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, configs)
}

func (h *Handler) HandleSetJailbreakModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The id travels in the body rather than the path on purpose: model ids
	// contain "/" and ":" ("tkh/mimo-v2.6-flash:free"), and chi hands back the
	// still-encoded segment for those, so a path parameter silently stored a key
	// that never matched the real id again — the toggle looked on, then came
	// back off after a reload.
	var req struct {
		ID       string `json:"id"`
		Enabled  *bool  `json:"enabled"`
		PresetID string `json:"presetId"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ID == "" || len(req.ID) > 300 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid model id")
		return
	}

	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	} else if req.PresetID != "" {
		enabled = req.PresetID != "off" && req.PresetID != "none"
	}

	presetID := req.PresetID
	if enabled && (presetID == "" || presetID == "on") {
		presetID = "default"
	}

	if err := h.repo.SetJailbreakModelPreset(req.ID, presetID, enabled); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"id":       req.ID,
		"enabled":  enabled,
		"presetId": presetID,
	})
}

func slugify(s string) string {
	s = strings.TrimSuffix(s, ".md")
	s = strings.TrimSuffix(s, ".markdown")
	s = strings.TrimSuffix(s, ".txt")
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteRune('-')
		}
	}
	res := strings.Trim(b.String(), "-")
	if res == "" {
		return fmt.Sprintf("preset-%d", time.Now().UnixNano())
	}
	return res
}
