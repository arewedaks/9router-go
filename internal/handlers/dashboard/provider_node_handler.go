package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// defaultBaseURLs mirrors OmniRoute MODE_DEFAULTS. An empty base URL is filled
// in for OpenAI/Anthropic so "Add" works out of the box; the CC variant (and any
// custom endpoint) must be typed by the operator.
var defaultBaseURLs = map[string]string{
	providers.NodeTypeOpenAICompatible:    "https://api.openai.com/v1",
	providers.NodeTypeAnthropicCompatible: "https://api.anthropic.com/v1",
}

// compatibleNodePayload is the create body. Field names match the UI/OmniRoute so
// the same form can post to either backend.
type compatibleNodePayload struct {
	Name          string `json:"name"`
	Prefix        string `json:"prefix"`
	APIType       string `json:"apiType"`
	BaseURL       string `json:"baseUrl"`
	Type          string `json:"type"`
	ChatPath      string `json:"chatPath"`
	ModelsPath    string `json:"modelsPath"`
	IconURL       string `json:"iconUrl"`
	CompatMode    string `json:"compatMode"`
	CustomHeaders string `json:"customHeaders"`
}

// nodeDataBlob is the JSON persisted in providerNodes.data. Only the fields the
// router reads are written; unknown/empty ones are omitted so the blob stays
// close to what the resolution layer expects.
type nodeDataBlob struct {
	Prefix     string `json:"prefix"`
	APIType    string `json:"apiType,omitempty"`
	BaseURL    string `json:"baseUrl"`
	Name       string `json:"nodeName,omitempty"`
	ChatPath   string `json:"chatPath,omitempty"`
	ModelsPath string `json:"modelsPath,omitempty"`
	IconURL    string `json:"iconUrl,omitempty"`
	CompatMode string `json:"compatMode,omitempty"`
}

// HandleCreateProviderNode creates an OpenAI- or Anthropic-compatible endpoint.
// The generated id (e.g. "openai-compatible-chat-<uuid>") is returned so the UI
// can navigate to the new node's detail page.
func (h *Handler) HandleCreateProviderNode(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var p compatibleNodePayload
	if err := json.Unmarshal(body, &p); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	nodeType := strings.TrimSpace(p.Type)
	if nodeType != providers.NodeTypeOpenAICompatible && nodeType != providers.NodeTypeAnthropicCompatible {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid provider node type")
		return
	}

	name := strings.TrimSpace(p.Name)
	prefix := strings.TrimSpace(p.Prefix)
	if name == "" || prefix == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Name and prefix are required")
		return
	}

	baseURL := providers.SanitizeBaseURL(p.BaseURL, nodeType, p.CompatMode)
	if baseURL == "" {
		baseURL = defaultBaseURLs[nodeType]
	}
	if err := validateBaseURL(baseURL); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A prefix addresses exactly one node; a duplicate would make
	// "<prefix>/<model>" ambiguous. Reject instead of silently picking one.
	nodes, err := h.repo.GetAllProviderNodes()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	existing := make(map[string]string, len(nodes))
	// Label each existing node by its human name so a collision message never
	// leaks the generated node id ("openai-compatible-chat-<uuid>") to the
	// operator — the id is an internal key, only the name is ever shown.
	existingLabel := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if _, d, derr := h.repo.GetProviderNodeByID(n.ID); derr == nil && d != nil {
			existing[n.ID] = d.Prefix
			label := ""
			if n.Name != nil {
				label = strings.TrimSpace(*n.Name)
			}
			if label == "" {
				label = d.Prefix
			}
			existingLabel[n.ID] = label
		}
	}
	if other, clash := providers.NodePrefixCollision(existing, prefix, ""); clash {
		label := existingLabel[other]
		if label == "" {
			label = "another provider"
		}
		handlerutil.WriteJSONError(w, http.StatusConflict,
			fmt.Sprintf("Prefix %q is already used by %s", prefix, label))
		return
	}

	apiType := strings.TrimSpace(p.APIType)
	if nodeType == providers.NodeTypeAnthropicCompatible {
		apiType = ""
	}
	if apiType == "" && nodeType == providers.NodeTypeOpenAICompatible {
		apiType = "chat"
	}

	// compatMode records the Claude Code variant. Anthropic nodes honour an
	// explicit value; an OpenAI node never carries one, so it is cleared rather
	// than trusted.
	compatMode := strings.TrimSpace(p.CompatMode)
	if nodeType != providers.NodeTypeAnthropicCompatible {
		compatMode = ""
	} else if compatMode != providers.CompatModeCC {
		compatMode = ""
	}

	blob := nodeDataBlob{
		Prefix:     prefix,
		APIType:    apiType,
		BaseURL:    baseURL,
		Name:       name,
		ChatPath:   strings.TrimSpace(p.ChatPath),
		ModelsPath: strings.TrimSpace(p.ModelsPath),
		IconURL:    strings.TrimSpace(p.IconURL),
		CompatMode: compatMode,
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	node := &models.ProviderNode{
		ID:   providers.CompatibleNodeID(nodeType, apiType, uuid.New().String()),
		Type: &nodeType,
		Name: &name,
		Data: string(raw),
	}
	if err := h.repo.CreateProviderNode(node); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusCreated, map[string]any{"node": node})
}

// validateBaseURL rejects anything that is not an absolute http(s) URL. It also
// blocks credentials in the URL, which would otherwise be sent to a third party.
func validateBaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("Base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("Invalid base URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Base URL must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("Base URL must include a host")
	}
	if u.User != nil {
		return fmt.Errorf("Base URL must not embed credentials")
	}
	return nil
}

// validateNodePayload is the validate body.
type validateNodePayload struct {
	BaseURL    string `json:"baseUrl"`
	APIKey     string `json:"apiKey"`
	Type       string `json:"type"`
	APIType    string `json:"apiType"`
	CompatMode string `json:"compatMode"`
	ModelsPath string `json:"modelsPath"`
	ModelID    string `json:"modelId"`
}

// HandleValidateProviderNode probes an endpoint with the supplied key so the
// operator learns whether it works *before* saving the node. It mirrors
// OmniRoute's /api/provider-nodes/validate:
//
//  1. GET  {baseUrl}{modelsPath or /models} with the key
//  2. if that fails and a model id was given, POST {baseUrl}/chat/completions
//
// It never returns 5xx for an upstream failure — "the key is wrong" is a normal
// answer ({valid:false}), not a server error.
func (h *Handler) HandleValidateProviderNode(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var p validateNodePayload
	if err := json.Unmarshal(body, &p); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	nodeType := strings.TrimSpace(p.Type)
	if nodeType == "" {
		nodeType = providers.NodeTypeOpenAICompatible
	}
	baseURL := providers.SanitizeBaseURL(p.BaseURL, nodeType, p.CompatMode)
	if baseURL == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false, "error": "Base URL is required",
		})
		return
	}
	apiKey := strings.TrimSpace(p.APIKey)
	if apiKey == "" {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false, "error": "API key is required to validate",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 20 * time.Second}
	modelsPath := strings.TrimSpace(p.ModelsPath)
	if modelsPath == "" {
		modelsPath = "/models"
	}
	modelsURL := baseURL + "/" + strings.TrimLeft(modelsPath, "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false, "error": "Invalid base URL",
		})
		return
	}
	applyValidationHeaders(req, nodeType, apiKey)

	res, err := client.Do(req)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false, "error": describeValidationTransportError(err),
		})
		return
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"valid": true, "error": nil})
		return
	}
	// A wrong key cannot be rescued by trying another endpoint.
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false, "error": "API key unauthorized",
		})
		return
	}

	// Some proxies expose only the chat endpoint. When the caller supplied a
	// model id, try a minimal completion as a second opinion.
	if modelID := strings.TrimSpace(p.ModelID); modelID != "" {
		ok, status, cerr := probeChatFallback(ctx, http.DefaultClient, baseURL, nodeType, apiKey, modelID)
		if cerr != nil {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"valid": false, "error": describeValidationTransportError(cerr), "method": "chat",
			})
			return
		}
		if ok {
			handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
				"valid": true, "error": nil, "method": "chat",
			})
			return
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"valid": false, "error": chatStatusMessage(status), "method": "chat",
		})
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"valid": false, "error": modelsStatusMessage(res.StatusCode),
	})
}

// applyValidationHeaders sets the auth headers for the probe. Anthropic wants
// x-api-key + a version, but hybrid proxies accept a bearer token, so both are
// sent — matching OmniRoute.
func applyValidationHeaders(req *http.Request, nodeType, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if nodeType == providers.NodeTypeAnthropicCompatible {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
}

// probeChatFallback issues a 1-token completion. ok reports a 2xx; status is the
// upstream status so the caller can explain the failure.
func probeChatFallback(ctx context.Context, client *http.Client, baseURL, nodeType, apiKey, modelID string) (ok bool, status int, err error) {
	payload := map[string]any{
		"model":      modelID,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
	}
	raw, _ := json.Marshal(payload)

	chatURL := baseURL + "/chat/completions"
	if nodeType == providers.NodeTypeAnthropicCompatible {
		chatURL = baseURL + "/messages"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, strings.NewReader(string(raw)))
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	applyValidationHeaders(req, nodeType, apiKey)

	res, err := client.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	return res.StatusCode >= 200 && res.StatusCode < 300, res.StatusCode, nil
}

// describeValidationTransportError turns a Go network error into a message an
// operator can act on. The Docker hint is deliberate: "connection refused" on
// localhost almost always means the container cannot see the host.
func describeValidationTransportError(err error) string {
	msg := err.Error()
	low := strings.ToLower(msg)
	if strings.Contains(low, "connection refused") || strings.Contains(low, "econnrefused") {
		return "Connection refused — is the endpoint reachable? If the server runs in Docker, localhost points at the container, not your host."
	}
	if strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded") {
		return "Connection timeout — the endpoint did not answer in time."
	}
	if strings.Contains(low, "no such host") || strings.Contains(low, "dns") {
		return "Host not found — check the base URL hostname."
	}
	return "Request failed: " + msg
}

func modelsStatusMessage(status int) string {
	switch {
	case status == http.StatusNotFound:
		return "/models endpoint not found — enter a Model ID to validate via the chat endpoint instead"
	case status >= 500:
		return "Server error — try again later"
	default:
		return fmt.Sprintf("Unexpected response (%d)", status)
	}
}

func chatStatusMessage(status int) string {
	switch {
	case status == http.StatusBadRequest:
		return "Invalid model or bad request"
	case status == http.StatusNotFound:
		return "Chat endpoint not found"
	case status >= 500:
		return "Server error — try again later"
	default:
		return fmt.Sprintf("Chat request failed (%d)", status)
	}
}

// HandleUpdateProviderNode edits a compatible node's name, base URL and paths.
// The id and type are immutable — the id is the routing key connections point at
// and the type is baked into it — so they are read back from the row, not trusted
// from the body. A prefix change is rejected when another node already owns it.
func (h *Handler) HandleUpdateProviderNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, existing, err := h.repo.GetProviderNodeByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Provider node not found")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var p compatibleNodePayload
	if err := json.Unmarshal(body, &p); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	nodeType := providers.NodeTypeOpenAICompatible
	if strings.HasPrefix(id, providers.AnthropicCompatiblePrefix) {
		nodeType = providers.NodeTypeAnthropicCompatible
	}

	name := strings.TrimSpace(p.Name)
	if name == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Name is required")
		return
	}
	prefix := strings.TrimSpace(p.Prefix)
	if prefix == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Prefix is required")
		return
	}
	if strings.ContainsAny(prefix, " \t/") {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Prefix must be a single word without spaces or slashes")
		return
	}

	// Sanitise against the recorded type so a pasted endpoint suffix is stripped
	// exactly as it is on create.
	compatMode := strings.TrimSpace(p.CompatMode)
	if compatMode == "" && existing != nil {
		compatMode = existing.CompatMode
	}
	if nodeType != providers.NodeTypeAnthropicCompatible || compatMode != providers.CompatModeCC {
		compatMode = ""
	}
	baseURL := providers.SanitizeBaseURL(p.BaseURL, nodeType, compatMode)
	if baseURL == "" && existing != nil && existing.BaseURL != "" {
		baseURL = existing.BaseURL
	}
	if err := validateBaseURL(baseURL); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Prefix collision, excluding this node so keeping its own prefix is fine.
	nodes, err := h.repo.GetAllProviderNodes()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	prefixes := make(map[string]string, len(nodes))
	labels := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if _, d, derr := h.repo.GetProviderNodeByID(n.ID); derr == nil && d != nil {
			prefixes[n.ID] = d.Prefix
			label := ""
			if n.Name != nil {
				label = strings.TrimSpace(*n.Name)
			}
			if label == "" {
				label = d.Prefix
			}
			labels[n.ID] = label
		}
	}
	if other, clash := providers.NodePrefixCollision(prefixes, prefix, id); clash {
		label := labels[other]
		if label == "" {
			label = "another provider"
		}
		handlerutil.WriteJSONError(w, http.StatusConflict,
			fmt.Sprintf("Prefix %q is already used by %s", prefix, label))
		return
	}

	apiType := strings.TrimSpace(p.APIType)
	if nodeType == providers.NodeTypeAnthropicCompatible {
		apiType = ""
	}
	if apiType == "" && nodeType == providers.NodeTypeOpenAICompatible {
		apiType = "chat"
	}

	blob := nodeDataBlob{
		Prefix:     prefix,
		APIType:    apiType,
		BaseURL:    baseURL,
		Name:       name,
		ChatPath:   strings.TrimSpace(p.ChatPath),
		ModelsPath: strings.TrimSpace(p.ModelsPath),
		IconURL:    strings.TrimSpace(p.IconURL),
		CompatMode: compatMode,
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.repo.UpdateProviderNode(id, name, string(raw)); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	updated, _, err := h.repo.GetProviderNodeByID(id)
	if err != nil || updated == nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Node updated but could not be read back")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"node": updated})
}

// HandleDeleteProviderNode removes a compatible node. A node with connections is
// refused with 409 unless `?cascade=1` is passed: deleting it silently would leave
// those accounts pointed at a provider that no longer resolves.
func (h *Handler) HandleDeleteProviderNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, _, err := h.repo.GetProviderNodeByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Provider node not found")
		return
	}

	count, err := h.repo.CountProviderNodeConnections(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cascade := r.URL.Query().Get("cascade") == "1"
	if count > 0 && !cascade {
		handlerutil.WriteJSON(w, http.StatusConflict, map[string]any{
			"error":       fmt.Sprintf("This endpoint still has %d connection(s). Deleting it removes them too.", count),
			"connections": count,
		})
		return
	}

	removed := 0
	if count > 0 {
		removed, err = h.repo.DeleteConnectionsForProvider(id)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	deleted, err := h.repo.DeleteProviderNode(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !deleted {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Provider node not found")
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":            true,
		"deletedConnections": removed,
	})
}
