package dashboard

import (
	json "encoding/json/v2"
	"io"
	"net/http"

	"github.com/google/uuid"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
)

// HandleGetConnections handles GET /api/connections.
// Returns a JSON list of all connections.
func (h *DashboardHandler) HandleGetConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := h.Repo.GetProviderConnections("", false)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conns == nil {
		conns = []*models.ProviderConnection{}
	}
	handlerutil.WriteJSON(w, http.StatusOK, conns)
}

// HandleCreateConnection handles POST /api/connections.
// Parses id, provider, authType, name, apiKey, data and calls CreateProviderConnection.
func (h *DashboardHandler) HandleCreateConnection(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		AuthType string `json:"authType"`
		Name     string `json:"name"`
		APIKey   string `json:"apiKey"`
		Data     any    `json:"data"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}

	if req.Provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing provider")
		return
	}
	if req.ID == "" {
		req.ID = uuid.New().String()
	}
	if req.AuthType == "" {
		req.AuthType = "apikey"
	}

	apiKey := req.APIKey
	var dataStr string
	if req.Data != nil {
		switch d := req.Data.(type) {
		case string:
			dataStr = d
			if apiKey == "" {
				var m map[string]any
				if err := json.Unmarshal([]byte(d), &m); err == nil {
					if k, ok := m["apiKey"].(string); ok {
						apiKey = k
					}
				}
			}
		case map[string]any:
			if apiKey == "" {
				if k, ok := d["apiKey"].(string); ok {
					apiKey = k
				}
			}
			b, err := json.Marshal(d)
			if err == nil {
				dataStr = string(b)
			}
		default:
			b, err := json.Marshal(d)
			if err == nil {
				dataStr = string(b)
			}
		}
	}

	if err := h.Repo.CreateProviderConnection(req.ID, req.Provider, req.AuthType, req.Name, apiKey); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if dataStr != "" {
		_ = h.Repo.UpdateProviderConnection(req.ID, req.Name, 0, true, dataStr)
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"id":     req.ID,
	})
}

// HandleUpdateConnection handles PUT /api/connections/{id}.
// Parses name, priority, isActive, data and calls UpdateProviderConnection or SetConnectionStatus.
func (h *DashboardHandler) HandleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connection id")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer r.Body.Close()

	var req struct {
		Name     string `json:"name"`
		Priority *int   `json:"priority"`
		IsActive *bool  `json:"isActive"`
		Data     any    `json:"data"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	existing, err := h.Repo.GetProviderConnectionByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "connection not found")
		return
	}

	// Fast path: if only updating active status
	if req.IsActive != nil && req.Name == "" && req.Priority == nil && req.Data == nil {
		if err := h.Repo.SetConnectionStatus(id, *req.IsActive); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id, "isActive": *req.IsActive})
		return
	}

	name := req.Name
	if name == "" && existing.Name != nil {
		name = *existing.Name
	}
	priority := 0
	if req.Priority != nil {
		priority = *req.Priority
	} else if existing.Priority != nil {
		priority = *existing.Priority
	}
	isActive := existing.IsActive == 1
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	dataStr := existing.Data
	if req.Data != nil {
		switch d := req.Data.(type) {
		case string:
			dataStr = d
		default:
			b, err := json.Marshal(d)
			if err == nil {
				dataStr = string(b)
			}
		}
	}

	if err := h.Repo.UpdateProviderConnection(id, name, priority, isActive, dataStr); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}

// HandleDeleteConnection handles DELETE /api/connections/{id}.
// Calls DeleteProviderConnection.
func (h *DashboardHandler) HandleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing connection id")
		return
	}

	if err := h.Repo.DeleteProviderConnection(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
}
