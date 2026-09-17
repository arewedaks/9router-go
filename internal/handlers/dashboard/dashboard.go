package dashboard

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	proxoauth "9router/proxy/internal/proxy/oauth"
)

//go:embed ui/*
var uiAssets embed.FS

// Handler manages dashboard API endpoints and embedded web UI.
type Handler struct {
	repo       *db.Repo
	tokenSaver *shared.TokenSaverConfig

	// auth carries the dashboard login state: password hasher, per-IP login
	// limiter, and the HMAC secret used to sign session cookies.
	auth authDeps

	// probeHostsOverride, when set, replaces the Antigravity host list used by
	// the per-account connection probe. Test-only seam: it lets a test point the
	// probe at an httptest server without mutating the global provider registry.
	probeHostsOverride []string
}

// NewHandler creates a new dashboard Handler.
func NewHandler(repo *db.Repo, ts *shared.TokenSaverConfig) *Handler {
	return NewHandlerWithDataDir(repo, ts, defaultDataDir())
}

// NewHandlerWithDataDir is NewHandler with an explicit data dir, so the session
// secret is stored next to the caller's database rather than a global default.
func NewHandlerWithDataDir(repo *db.Repo, ts *shared.TokenSaverConfig, dataDir string) *Handler {
	secret, err := LoadSessionSecret(dataDir)
	return &Handler{
		repo:       repo,
		tokenSaver: ts,
		auth: authDeps{
			hasher:   bcryptHasher{},
			limiter:  newLoginLimiter(),
			secret:   secret,
			secretEr: err,
		},
	}
}

// defaultDataDir resolves the data directory that holds the session secret,
// honouring the same DATA_DIR override the rest of the app uses.
func defaultDataDir() string {
	if d := strings.TrimSpace(os.Getenv("DATA_DIR")); d != "" {
		return d
	}
	if d := strings.TrimSpace(os.Getenv("NINE_DATA_DIR")); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".9router"
	}
	return filepath.Join(home, ".9router")
}

// VerifySession reports whether a session cookie value is valid. It exposes the
// check for the middleware gate without leaking token-format details.
func (h *Handler) VerifySession(token string) bool {
	return VerifySessionToken(h.auth.secret, token, time.Now())
}

// LoginDisabled reports whether settings have disabled the dashboard login
// requirement. A nil setting keeps the default (login required).
func (h *Handler) LoginDisabled() bool {
	settings, err := h.repo.GetSettings()
	if err != nil || settings == nil || settings.RequireLogin == nil {
		return false
	}
	return !*settings.RequireLogin
}

// RegisterAuthRoutes registers the login/session endpoints. They must be mounted
// on an UNPROTECTED router: the login form cannot itself require a session.
func (h *Handler) RegisterAuthRoutes(r chi.Router) {
	r.Route("/api/dashboard/auth", func(dr chi.Router) {
		dr.Get("/status", h.HandleAuthStatus)
		// /session is the refresh-safe check: it validates the httpOnly cookie that
		// JS cannot read, so a reload does not re-prompt for the password.
		dr.Get("/session", h.HandleAuthSession)
		dr.Post("/login", h.HandleAuthLogin)
		dr.Post("/logout", h.HandleAuthLogout)
		dr.Post("/change-password", h.HandleAuthChangePassword)
		dr.Post("/reset-password", h.HandleAuthResetPassword)
		// Legacy API-key check, kept so existing browser sessions that still hold a
		// stored key keep working after the upgrade.
		dr.Post("/verify", h.HandleAuthVerify)
	})
}

// RegisterRoutes registers all dashboard API routes and UI asset routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	// Embedded UI
	r.Get("/dashboard", h.ServeUI)
	r.Get("/dashboard/*", h.ServeUI)

	// Dashboard REST APIs
	r.Route("/api/dashboard", func(dr chi.Router) {
		// Stats & Overview
		dr.Get("/stats", h.HandleStats)
		dr.Get("/providers/catalog", h.HandleCatalog)

		// Providers
		dr.Get("/providers", h.HandleListProviders)
		dr.Get("/providers/{id}", h.HandleProviderDetail)
		dr.Post("/providers/{id}/toggle-all", h.HandleProviderToggleAll)
		dr.Post("/providers/{id}/models", h.HandleAddModel)
		dr.Get("/providers/{id}/models", h.HandleImportModels)
		dr.Delete("/providers/{id}/models", h.HandleRemoveModel)

		// Model test (ping) — mirrors upstream POST /api/models/test and
		// POST /api/providers/[id]/test-models.
		dr.Post("/models/test", h.HandleModelTest)
		dr.Post("/providers/{id}/test-models", h.HandleTestProviderModels)
		dr.Delete("/providers/{id}/models", h.HandleRemoveModel)
		dr.Post("/providers", h.HandleUpsertProvider)
		dr.Post("/providers/{id}/toggle", h.HandleToggleProvider)
		dr.Delete("/providers/{id}", h.HandleDeleteProvider)

		// Antigravity client profile (ide|cli) per connection.
		dr.Post("/connections/{id}/client-profile", h.HandleSetClientProfile)

		// Connection test (per-account) — mirrors upstream
		// POST /api/providers/[id]/test and POST /api/providers/test-batch.
		dr.Post("/connections/{id}/test", h.HandleTestConnection)
		dr.Post("/providers/{id}/test-connections", h.HandleTestProviderConnections)

		// Combos
		dr.Get("/combos", h.HandleListCombos)
		dr.Post("/combos", h.HandleUpsertCombo)
		dr.Delete("/combos/{id}", h.HandleDeleteCombo)

		// API Keys
		dr.Get("/keys", h.HandleListKeys)
		dr.Post("/keys", h.HandleCreateKey)
		dr.Post("/keys/{id}/toggle", h.HandleToggleKey)
		dr.Delete("/keys/{id}", h.HandleDeleteKey)

		// Token Saver Settings
		dr.Get("/settings", h.HandleGetSettings)
		dr.Post("/settings", h.HandleUpdateSettings)

		// Backup: export/import the whole database. Both re-check the dashboard
		// password rather than trusting the session alone — see
		// confirmBackupPassword.
		dr.Get("/database", h.HandleExportDatabase)
		dr.Post("/database", h.HandleImportDatabase)
		dr.Post("/database/inspect", h.HandleInspectBackup)
	})
}

// ServeUI serves the embedded Single Page Application.
func (h *Handler) ServeUI(w http.ResponseWriter, r *http.Request) {
	content, err := uiAssets.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "Dashboard UI not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// HandleAuthVerify checks if the provided API key is valid.
func (h *Handler) HandleAuthVerify(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Key == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Key is required")
		return
	}

	keyObj, err := h.repo.GetApiKeyByKey(req.Key)
	if err != nil || keyObj == nil || keyObj.IsActive != 1 {
		handlerutil.WriteJSONError(w, http.StatusUnauthorized, "Invalid or inactive API key")
		return
	}

	name := ""
	if keyObj.Name != nil {
		name = *keyObj.Name
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"valid":     true,
		"key":       keyObj.Key,
		"name":      name,
		"createdAt": keyObj.CreatedAt,
	})
}

// HandleStats returns dashboard overview metrics.
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	providersList, err := h.repo.GetAllProviderConnections()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to load providers")
		return
	}

	combos, err := h.repo.GetCombos()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to load combos")
		return
	}

	keys, err := h.repo.GetAllApiKeys()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to load keys")
		return
	}

	activeProviders := 0
	providerTypeCounts := make(map[string]int)

	// Category buckets mirror the upstream Next.js sections:
	// OAuth, Free Tier (free + freeTier), API Key, Web Cookie, Compatible Endpoints.
	categoryCounts := map[string]map[string]int{
		"oauth":     {"total": 0, "active": 0},
		"freeTier":  {"total": 0, "active": 0},
		"free":      {"total": 0, "active": 0},
		"apikey":    {"total": 0, "active": 0},
		"webCookie": {"total": 0, "active": 0},
		"custom":    {"total": 0, "active": 0},
	}

	for _, p := range providersList {
		cat, _, _ := providers.ClassifyProvider(p.Provider)
		catKey := string(providers.SectionCategory(cat))
		if _, ok := categoryCounts[catKey]; !ok {
			categoryCounts[catKey] = map[string]int{"total": 0, "active": 0}
		}
		categoryCounts[catKey]["total"]++

		if p.IsActive == 1 {
			activeProviders++
			categoryCounts[catKey]["active"]++
		}
		providerTypeCounts[p.Provider]++
	}

	stats := map[string]any{
		"totalProviders":     len(providersList),
		"activeProviders":    activeProviders,
		"totalCombos":        len(combos),
		"totalKeys":          len(keys),
		"providerTypeCounts": providerTypeCounts,
		"categories":         categoryCounts,
		"tokenSaver": map[string]any{
			"rtk":            h.tokenSaver.RTKEnabled(),
			"caveman":        h.tokenSaver.CavemanEnabled(),
			"cavemanLevel":   h.tokenSaver.CavemanLevel(),
			"ponytail":       h.tokenSaver.PonytailEnabled(),
			"ponytailLevel":  h.tokenSaver.PonytailLevel(),
			"injectionGuard": h.tokenSaver.InjectionGuardEnabled(),
		},
	}

	handlerutil.WriteJSON(w, http.StatusOK, stats)
}

// ProviderSummary is a client-friendly representation of ProviderConnection.
type ProviderSummary struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	DisplayName   string  `json:"displayName"`
	Category      string  `json:"category"`
	CategoryLabel string  `json:"categoryLabel"`
	AuthType      string  `json:"authType"`
	Name          *string `json:"name,omitempty"`
	Email         *string `json:"email,omitempty"`
	Priority      *int    `json:"priority,omitempty"`
	IsActive      int     `json:"isActive"`
	Data          string  `json:"data"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`

	// Account detail fields extracted from the Data JSON blob so the UI can
	// list which accounts are connected under each provider.
	AccountLabel string `json:"accountLabel,omitempty"`
	TestStatus   string `json:"testStatus,omitempty"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	LastUsedAt   string `json:"lastUsedAt,omitempty"`
	Expired      bool   `json:"expired,omitempty"`

	// ClientProfile is the selected Antigravity client identity ("ide" or
	// "cli") for Antigravity connections; empty for other providers.
	ClientProfile string `json:"clientProfile,omitempty"`

	// Upstream display metadata so cards match the Next.js UI exactly.
	Icon            string   `json:"icon,omitempty"`
	Color           string   `json:"color,omitempty"`
	RegistryName    string   `json:"registryName,omitempty"`
	Website         string   `json:"website,omitempty"`
	PriorityRank    int      `json:"priorityRank,omitempty"`
	ServiceKinds    []string `json:"serviceKinds,omitempty"`
	Deprecated      bool     `json:"deprecated,omitempty"`
	DeprecationNote string   `json:"deprecationNotice,omitempty"`
}

// accountInfo holds the subset of the credential blob surfaced to the UI.
type accountInfo struct {
	AccountLabel string
	TestStatus   string
	ExpiresAt    string
	LastUsedAt   string
	Expired      bool
}

// extractAccountInfo parses the stored credential JSON blob and pulls out
// non-sensitive account metadata (never tokens or secrets).
func extractAccountInfo(raw string) accountInfo {
	var info accountInfo
	if raw == "" {
		return info
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return info
	}

	str := func(key string) string {
		if v, ok := m[key].(string); ok {
			return v
		}
		return ""
	}

	// Prefer an explicit account identity: email first, then username/handle.
	// Note: projectId is deliberately excluded — it is not an account label.
	for _, key := range []string{"email", "githubEmail", "username", "firstName", "lastName", "accountId", "userId", "sub"} {
		if v := str(key); v != "" {
			info.AccountLabel = v
			break
		}
	}

	info.TestStatus = str("testStatus")
	info.ExpiresAt = str("expiresAt")
	info.LastUsedAt = str("lastUsedAt")

	if info.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, info.ExpiresAt); err == nil {
			info.Expired = time.Now().After(t)
		}
	}

	return info
}

// applyProviderMeta copies upstream display fields onto a summary.
func applyProviderMeta(s *ProviderSummary, m providers.ProviderMeta, ok bool) {
	if !ok {
		return
	}
	s.Icon = m.Icon
	s.Color = m.Color
	s.RegistryName = m.Name
	s.Website = m.Website
	s.PriorityRank = m.Priority
	s.ServiceKinds = m.ServiceKinds
	s.Deprecated = m.Deprecated
	s.DeprecationNote = m.DeprecationNote
}

// HandleCatalog returns known providers structured by category.
func (h *Handler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	catalog := providers.GetCatalogByCategory()
	handlerutil.WriteJSON(w, http.StatusOK, catalog)
}

// ProviderDetail is the payload for the provider sub-page (upstream route
// /dashboard/providers/[id]). It bundles registry capabilities, every
// connection, and the cached model catalogue.
type ProviderDetail struct {
	Provider      string            `json:"provider"`
	DisplayName   string            `json:"displayName"`
	Category      string            `json:"category"`
	CategoryLabel string            `json:"categoryLabel"`
	AuthType      string            `json:"authType"`
	Description   string            `json:"description,omitempty"`
	Icon          string            `json:"icon,omitempty"`
	Color         string            `json:"color,omitempty"`
	Website       string            `json:"website,omitempty"`
	SignupURL     string            `json:"signupUrl,omitempty"`
	PriorityRank  int               `json:"priorityRank,omitempty"`
	ServiceKinds  []string          `json:"serviceKinds,omitempty"`
	Deprecated    bool              `json:"deprecated,omitempty"`
	RiskNotice    string            `json:"deprecationNotice,omitempty"`
	Aliases       []string          `json:"aliases,omitempty"`
	Connections   []ProviderSummary `json:"connections"`
	Models        []db.CachedModel  `json:"models"`
	ActiveCount   int               `json:"activeCount"`
	TotalCount    int               `json:"totalCount"`
}

// HandleProviderDetail returns capabilities, connections and cached models for
// a single provider, resolving short aliases (e.g. "ag" -> "antigravity").
func (h *Handler) HandleProviderDetail(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	canonical := providers.ResolveAlias(raw)

	meta, hasMeta := providers.GetProviderMeta(canonical)
	if !hasMeta {
		meta, hasMeta = providers.GetProviderMeta(raw)
	}

	detail := ProviderDetail{
		Provider:    canonical,
		Connections: []ProviderSummary{},
		Models:      []db.CachedModel{},
	}

	if hasMeta {
		detail.DisplayName = meta.Name
		detail.Category = string(meta.Category)
		detail.CategoryLabel = providers.GetCategoryLabel(meta.Category)
		detail.AuthType = meta.AuthType
		detail.Description = meta.Description
		detail.Icon = meta.Icon
		detail.Color = meta.Color
		detail.Website = meta.Website
		detail.PriorityRank = meta.Priority
		detail.ServiceKinds = meta.ServiceKinds
		detail.Deprecated = meta.Deprecated
		detail.RiskNotice = meta.DeprecationNote
	} else {
		detail.DisplayName = raw
	}

	// Connections: collect across canonical ID and the raw key supplied.
	all, err := h.repo.GetAllProviderConnections()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nodeMap := map[string]*models.ProviderNode{}
	if nodes, err := h.repo.GetAllProviderNodes(); err == nil {
		for _, n := range nodes {
			nodeMap[n.ID] = n
		}
	}

	for _, c := range all {
		if c.Provider != canonical && c.Provider != raw {
			continue
		}
		cat, catLabel, dispName := providers.ClassifyProvider(c.Provider)
		if node, ok := nodeMap[c.Provider]; ok && node.Name != nil && *node.Name != "" {
			dispName = *node.Name
		}
		acct := extractAccountInfo(c.Data)
		label := acct.AccountLabel
		if label == "" && c.Email != nil {
			label = *c.Email
		}
		if label == "" && c.Name != nil {
			label = *c.Name
		}
		m := meta
		sum := ProviderSummary{
			ID:            c.ID,
			Provider:      c.Provider,
			DisplayName:   dispName,
			Category:      string(cat),
			CategoryLabel: catLabel,
			AuthType:      c.AuthType,
			Name:          c.Name,
			Email:         c.Email,
			Priority:      c.Priority,
			IsActive:      c.IsActive,
			Data:          c.Data,
			CreatedAt:     c.CreatedAt,
			UpdatedAt:     c.UpdatedAt,
			AccountLabel:  label,
			TestStatus:    acct.TestStatus,
			ExpiresAt:     acct.ExpiresAt,
			LastUsedAt:    acct.LastUsedAt,
			Expired:       acct.Expired,
		}
		if isAntigravityProvider(c.Provider) {
			sum.ClientProfile = string(antigravityClientProfileFromData(c.Data))
		}
		applyProviderMeta(&sum, m, hasMeta)
		detail.Connections = append(detail.Connections, sum)
		detail.TotalCount++
		if c.IsActive == 1 {
			detail.ActiveCount++
		}
	}

	// Models: the cache is keyed by whatever identifier the provider used
	// (canonical ID or short alias), so query the canonical ID, the raw
	// request key, and every registered alias for this provider.
	keys := []string{canonical, raw}
	keys = append(keys, providers.AliasesFor(canonical)...)
	if raw != canonical {
		keys = append(keys, providers.AliasesFor(raw)...)
	}
	models, err := h.repo.ListCachedModels(keys...)
	if err == nil && len(models) > 0 {
		// Free-ness is a property of the (provider, model) pair, so it is
		// computed here rather than in the Repo: see providers.IsModelFreeBadge.
		for i := range models {
			models[i].IsFree = providers.IsModelFreeBadge(canonical, providers.FreeModelCandidate{
				ID:          models[i].ModelID,
				DisplayName: models[i].DisplayName,
			})
		}
		detail.Models = models
	}

	// Aliases power the UI when composing full model IDs for tests
	// (e.g. "ag/claude-sonnet-4-6"). The first alias is the display alias.
	aliases := providers.AliasesFor(canonical)
	if len(aliases) == 0 && raw != canonical {
		aliases = providers.AliasesFor(raw)
	}
	detail.Aliases = aliases

	handlerutil.WriteJSON(w, http.StatusOK, detail)
}

// HandleProviderToggleAll enables or disables every connection of a provider,
// mirroring the upstream "Enable All" / "Disable All" buttons.
func (h *Handler) HandleProviderToggleAll(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	canonical := providers.ResolveAlias(raw)

	var body struct {
		Active *int `json:"active"`
	}
	if data, err := io.ReadAll(r.Body); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &body)
	}

	// Default to "disable all" when no explicit state is supplied.
	target := 0
	if body.Active != nil {
		target = *body.Active
	}

	n, err := h.repo.SetProviderActive(canonical, target)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if raw != canonical {
		if n2, err := h.repo.SetProviderActive(raw, target); err == nil {
			n += n2
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"updated": n, "active": target})
}

// HandleImportModels fetches the live model catalogue from the provider's own
// /models endpoint and (optionally) persists every model not yet cached. This
// mirrors the upstream "Import from /models" button on
// /dashboard/providers/[id]. Pass ?save=1 to persist, otherwise the list is
// returned for preview only.
func (h *Handler) HandleImportModels(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	canonical := providers.ResolveAlias(raw)

	// Use any active connection for this provider to obtain credentials.
	conns, err := h.repo.ListConnectionsByProvider(raw)
	if err != nil || len(conns) == 0 {
		if canonical != raw {
			conns, err = h.repo.ListConnectionsByProvider(canonical)
		}
	}
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var creds string
	var credsConnID string
	for _, c := range conns {
		if c.IsActive == 1 {
			creds = c.Data
			credsConnID = c.ID
			break
		}
	}
	if creds == "" && len(conns) > 0 {
		creds = conns[0].Data
		credsConnID = conns[0].ID
	}
	if creds == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			"Add a connection to enable importing models.")
		return
	}

	// Some providers (notably GitHub Copilot, whose bearer token lives ~24h)
	// store a short-lived derived token. Refresh it on demand so Import does not
	// silently fall back to a static list just because the cached token aged out.
	// Failure is not fatal: the fetch below still degrades gracefully.
	if credsConnID != "" {
		if refreshed, _, _, rerr := proxoauth.RefreshStoredConnection(r.Context(), h.repo, nil, credsConnID, false); rerr == nil && refreshed != "" {
			if conn, cerr := h.repo.GetProviderConnectionByID(credsConnID); cerr == nil && conn != nil {
				creds = conn.Data
			}
		}
	}

	result, err := h.fetchUpstreamModels(canonical, creds, 20*time.Second)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Annotate each candidate with the dashboard’s Free flag so the Import
	// modal can show the same badge the detail page renders.
	for i := range result.Models {
		result.Models[i].IsFree = providers.IsModelFreeBadge(canonical, providers.FreeModelCandidate{
			ID:          result.Models[i].ID,
			DisplayName: result.Models[i].Name,
		})
	}
	if result.Error != "" && !result.Supported {
		// Provider simply has no listing endpoint — report cleanly.
		handlerutil.WriteJSON(w, http.StatusOK, result)
		return
	}
	if result.Error != "" {
		handlerutil.WriteJSON(w, http.StatusBadGateway, result)
		return
	}

	save := r.URL.Query().Get("save") == "1"
	if save {
		aliases := append(providers.AliasesFor(canonical), providers.AliasesFor(raw)...)
		key := h.repo.ResolveModelCacheKey(canonical, aliases)

		existing, _ := h.repo.ListCachedModels(key)
		have := make(map[string]bool, len(existing))
		for _, m := range existing {
			have[m.ModelID] = true
		}

		added := 0
		for _, m := range result.Models {
			// Always re-upsert so an already-cached model picks up a display name
			// it did not have before (the DB upsert never blanks a stored name).
			if err := h.repo.AddCachedModelWithName(key, m.ID, m.Kind, "imported", m.Name); err != nil {
				continue
			}
			if !have[m.ID] {
				added++
			}
		}
		result.Warning = fmt.Sprintf("Imported %d new model(s) under cache key %q.", added, key)
	}

	handlerutil.WriteJSON(w, http.StatusOK, result)
}

// HandleAddModel adds a custom model to a provider's model cache, mirroring
// the upstream "Add Custom Model" action on /dashboard/providers/[id].
func (h *Handler) HandleAddModel(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	canonical := providers.ResolveAlias(raw)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var payload struct {
		ModelID string `json:"modelId"`
		Kind    string `json:"kind"`
		OwnedBy string `json:"ownedBy"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	payload.ModelID = strings.TrimSpace(payload.ModelID)
	if payload.ModelID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Model ID is required")
		return
	}

	// Store under the same key the existing cache uses (usually a short
	// alias like "ag") so the model is actually picked up by the router.
	aliases := append(providers.AliasesFor(canonical), providers.AliasesFor(raw)...)
	key := h.repo.ResolveModelCacheKey(canonical, aliases)

	if err := h.repo.AddCachedModel(key, payload.ModelID, payload.Kind, payload.OwnedBy); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"provider": canonical,
		"cacheKey": key,
		"modelId":  payload.ModelID,
		"kind":     firstNonEmpty(payload.Kind, "llm"),
	})
}

// HandleRemoveModel deletes a model from a provider's model cache.
func (h *Handler) HandleRemoveModel(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "id")
	canonical := providers.ResolveAlias(raw)
	modelID := r.URL.Query().Get("modelId")
	if modelID == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Model ID is required")
		return
	}

	aliases := append(providers.AliasesFor(canonical), providers.AliasesFor(raw)...)
	keys := append([]string{canonical, raw}, aliases...)

	removed := 0
	for _, k := range keys {
		if err := h.repo.RemoveCachedModel(k, modelID); err == nil {
			removed++
		}
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

// firstNonEmpty returns the first non-blank string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// HandleListProviders returns all provider connections.
func (h *Handler) HandleListProviders(w http.ResponseWriter, r *http.Request) {
	conns, err := h.repo.GetAllProviderConnections()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	nodes, _ := h.repo.GetAllProviderNodes()
	nodeMap := make(map[string]*models.ProviderNode)
	for _, n := range nodes {
		nodeMap[n.ID] = n
	}

	res := make([]ProviderSummary, 0, len(conns))
	for _, c := range conns {
		cat, catLabel, dispName := providers.ClassifyProvider(c.Provider)
		if node, ok := nodeMap[c.Provider]; ok && node.Name != nil && *node.Name != "" {
			dispName = *node.Name
		}

		acct := extractAccountInfo(c.Data)

		// Pull upstream display metadata (icon, colour, priority, notices) so
		// the embedded UI mirrors the Next.js provider cards 1:1.
		meta, hasMeta := providers.GetProviderMeta(c.Provider)

		// Fall back to the connection's own email/name columns when the
		// credential blob does not carry an account identity.
		accountLabel := acct.AccountLabel
		if accountLabel == "" && c.Email != nil {
			accountLabel = *c.Email
		}
		if accountLabel == "" && c.Name != nil {
			accountLabel = *c.Name
		}

		expired := acct.Expired
		summary := ProviderSummary{
			ID:            c.ID,
			Provider:      c.Provider,
			DisplayName:   dispName,
			Category:      string(cat),
			CategoryLabel: catLabel,
			AuthType:      c.AuthType,
			Name:          c.Name,
			Email:         c.Email,
			Priority:      c.Priority,
			IsActive:      c.IsActive,
			Data:          c.Data,
			CreatedAt:     c.CreatedAt,
			UpdatedAt:     c.UpdatedAt,
			AccountLabel:  accountLabel,
			TestStatus:    acct.TestStatus,
			ExpiresAt:     acct.ExpiresAt,
			LastUsedAt:    acct.LastUsedAt,
			Expired:       expired,
		}
		applyProviderMeta(&summary, meta, hasMeta)
		res = append(res, summary)
	}

	handlerutil.WriteJSON(w, http.StatusOK, res)
}

// UpsertProviderPayload defines the input body for creating/updating a provider.
type UpsertProviderPayload struct {
	ID       string  `json:"id"`
	Provider string  `json:"provider"`
	AuthType string  `json:"authType"`
	Name     *string `json:"name,omitempty"`
	Email    *string `json:"email,omitempty"`
	Priority *int    `json:"priority,omitempty"`
	IsActive *int    `json:"isActive,omitempty"`
	ApiKey   string  `json:"apiKey,omitempty"`
	BaseURL  string  `json:"baseUrl,omitempty"`
	Data     string  `json:"data,omitempty"`
}

// HandleUpsertProvider creates or updates a provider connection.
func (h *Handler) HandleUpsertProvider(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var payload UpsertProviderPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if payload.Provider == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Provider is required")
		return
	}
	if payload.AuthType == "" {
		payload.AuthType = "apikey"
	}

	id := payload.ID
	if id == "" {
		id = uuid.New().String()
	}

	isActive := 1
	if payload.IsActive != nil {
		isActive = *payload.IsActive
	}

	dataStr := payload.Data
	if dataStr == "" {
		dataObj := make(map[string]any)
		if payload.ApiKey != "" {
			dataObj["apiKey"] = payload.ApiKey
		}
		if payload.BaseURL != "" {
			dataObj["baseUrl"] = payload.BaseURL
		}
		b, _ := json.Marshal(dataObj)
		dataStr = string(b)
	}

	conn := &models.ProviderConnection{
		ID:       id,
		Provider: payload.Provider,
		AuthType: payload.AuthType,
		Name:     payload.Name,
		Email:    payload.Email,
		Priority: payload.Priority,
		IsActive: isActive,
		Data:     dataStr,
	}

	if err := h.repo.UpsertProviderConnection(conn); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, conn)
}

// HandleToggleProvider toggles active state.
func (h *Handler) HandleToggleProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing connection ID")
		return
	}

	newState, err := h.repo.ToggleProviderConnection(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":       id,
		"isActive": newState,
	})
}

// HandleDeleteProvider deletes a provider connection.
func (h *Handler) HandleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing connection ID")
		return
	}

	if err := h.repo.DeleteProviderConnection(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// ComboPayload represents combo create/update request.
type ComboPayload struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     *string  `json:"kind,omitempty"`
	Models   []string `json:"models"`
	Strategy string   `json:"strategy"`
}

// HandleListCombos returns all combos.
func (h *Handler) HandleListCombos(w http.ResponseWriter, r *http.Request) {
	combos, err := h.repo.GetCombos()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type ComboResponse struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		Kind      *string  `json:"kind,omitempty"`
		Models    []string `json:"models"`
		CreatedAt string   `json:"createdAt"`
		UpdatedAt string   `json:"updatedAt"`
	}

	resp := make([]ComboResponse, 0, len(combos))
	for _, c := range combos {
		var modelList []string
		_ = json.Unmarshal([]byte(c.Models), &modelList)
		resp = append(resp, ComboResponse{
			ID:        c.ID,
			Name:      c.Name,
			Kind:      c.Kind,
			Models:    modelList,
			CreatedAt: c.CreatedAt,
			UpdatedAt: c.UpdatedAt,
		})
	}

	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleUpsertCombo creates or updates a combo.
func (h *Handler) HandleUpsertCombo(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var payload ComboPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	if strings.TrimSpace(payload.Name) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Combo name is required")
		return
	}

	id := payload.ID
	if id == "" {
		id = uuid.New().String()
	}

	modelsJSON, err := json.Marshal(payload.Models)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid models array")
		return
	}

	combo := &models.Combo{
		ID:       id,
		Name:     payload.Name,
		Kind:     payload.Kind,
		Models:   string(modelsJSON),
		Strategy: payload.Strategy,
	}

	if err := h.repo.UpsertCombo(combo); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, combo)
}

// HandleDeleteCombo deletes a combo.
func (h *Handler) HandleDeleteCombo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing combo ID")
		return
	}

	if err := h.repo.DeleteCombo(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// HandleListKeys lists all client API keys.
func (h *Handler) HandleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.repo.GetAllApiKeys()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, keys)
}

// HandleCreateKey creates a new API key.
func (h *Handler) HandleCreateKey(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(body, &req)

	if req.Name == "" {
		req.Name = "default-key"
	}

	// Generate standard sk-... key
	rawBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, rawBytes); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Entropy error")
		return
	}
	keyStr := "sk-" + hex.EncodeToString(rawBytes)
	id := uuid.New().String()

	apiKey, err := h.repo.CreateApiKeyWithDetails(id, keyStr, req.Name)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, apiKey)
}

// HandleToggleKey toggles active status of a key.
func (h *Handler) HandleToggleKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing key ID")
		return
	}

	newState, err := h.repo.ToggleApiKey(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":       id,
		"isActive": newState,
	})
}

// HandleDeleteKey removes an API key.
func (h *Handler) HandleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing key ID")
		return
	}

	if err := h.repo.DeleteApiKey(id); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// HandleGetSettings retrieves token saver and system configuration.
func (h *Handler) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.repo.GetSettings()
	if err != nil {
		s = db.DefaultSettings()
	}

	resp := map[string]any{
		"rtkEnabled":            h.tokenSaver.RTKEnabled(),
		"cavemanEnabled":        h.tokenSaver.CavemanEnabled(),
		"cavemanLevel":          h.tokenSaver.CavemanLevel(),
		"ponytailEnabled":       h.tokenSaver.PonytailEnabled(),
		"ponytailLevel":         h.tokenSaver.PonytailLevel(),
		"injectionGuardEnabled": h.tokenSaver.InjectionGuardEnabled(),
		"autoUpdate":            s.AutoUpdate,
	}

	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleUpdateSettings updates token saver settings in memory and SQLite.
func (h *Handler) HandleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}
	var req struct {
		RTKEnabled            *bool   `json:"rtkEnabled"`
		CavemanEnabled        *bool   `json:"cavemanEnabled"`
		CavemanLevel          *string `json:"cavemanLevel"`
		PonytailEnabled       *bool   `json:"ponytailEnabled"`
		PonytailLevel         *string `json:"ponytailLevel"`
		InjectionGuardEnabled *bool   `json:"injectionGuardEnabled"`
		AutoUpdate            *bool   `json:"autoUpdate"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}

	s, err := h.repo.GetSettings()
	if err != nil {
		s = db.DefaultSettings()
	}

	if req.RTKEnabled != nil {
		h.tokenSaver.SetRTK(*req.RTKEnabled)
		s.RTKEnabled = *req.RTKEnabled
	}
	if req.CavemanEnabled != nil {
		lvl := h.tokenSaver.CavemanLevel()
		if req.CavemanLevel != nil && *req.CavemanLevel != "" {
			lvl = *req.CavemanLevel
		}
		h.tokenSaver.SetCaveman(*req.CavemanEnabled, lvl)
		s.CavemanEnabled = *req.CavemanEnabled
		s.CavemanLevel = lvl
	}
	if req.PonytailEnabled != nil {
		lvl := h.tokenSaver.PonytailLevel()
		if req.PonytailLevel != nil && *req.PonytailLevel != "" {
			lvl = *req.PonytailLevel
		}
		h.tokenSaver.SetPonytail(*req.PonytailEnabled, lvl)
		s.PonytailEnabled = *req.PonytailEnabled
		s.PonytailLevel = lvl
	}
	if req.InjectionGuardEnabled != nil {
		h.tokenSaver.SetInjectionGuard(*req.InjectionGuardEnabled)
	}
	if req.AutoUpdate != nil {
		s.AutoUpdate = *req.AutoUpdate
	}

	if err := h.repo.SaveSettingsData(s); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "Failed to save settings: "+err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":               true,
		"rtkEnabled":            h.tokenSaver.RTKEnabled(),
		"cavemanEnabled":        h.tokenSaver.CavemanEnabled(),
		"cavemanLevel":          h.tokenSaver.CavemanLevel(),
		"ponytailEnabled":       h.tokenSaver.PonytailEnabled(),
		"ponytailLevel":         h.tokenSaver.PonytailLevel(),
		"injectionGuardEnabled": h.tokenSaver.InjectionGuardEnabled(),
		"autoUpdate":            s.AutoUpdate,
	})
}
