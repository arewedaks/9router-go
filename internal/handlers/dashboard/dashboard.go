package dashboard

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	"9router/proxy/internal/tokensaver"
)

// ui/* already descends into subdirectories, so it would embed ui/providers
// too. The all: prefix is used deliberately for a different reason: it also
// picks up files whose names begin with "_" or ".", so dropping a logo in as
// `_scratch.webp` while iterating cannot silently vanish from the build.
//
//go:embed all:ui
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
		// Compatible endpoints (OpenAI/Anthropic) are created as provider nodes,
		// not connections, so they get their own create + pre-save probe routes.
		dr.Post("/provider-nodes", h.HandleCreateProviderNode)
		dr.Post("/provider-nodes/validate", h.HandleValidateProviderNode)
		dr.Patch("/provider-nodes/{id}", h.HandleUpdateProviderNode)
		dr.Delete("/provider-nodes/{id}", h.HandleDeleteProviderNode)

		// Providers
		dr.Get("/providers", h.HandleListProviders)
		dr.Get("/providers/{id}", h.HandleProviderDetail)
		dr.Post("/providers/{id}/toggle-all", h.HandleProviderToggleAll)
		dr.Post("/providers/{id}/models", h.HandleAddModel)
		dr.Get("/providers/{id}/models", h.HandleImportModels)
		dr.Delete("/providers/{id}/models", h.HandleRemoveModel)
		// Live credit/quota balances per account (upstream getUsageForProvider).
		dr.Get("/providers/{id}/quota", h.HandleProviderQuota)

		// Model test (ping) — mirrors upstream POST /api/models/test and
		// POST /api/providers/[id]/test-models.
		dr.Post("/models/test", h.HandleModelTest)
		dr.Post("/providers/{id}/test-models", h.HandleTestProviderModels)
		dr.Delete("/providers/{id}/models", h.HandleRemoveModel)
		dr.Post("/providers", h.HandleUpsertProvider)
		dr.Post("/providers/{id}/toggle", h.HandleToggleProvider)
		dr.Delete("/providers/{id}", h.HandleDeleteProvider)

		// Antigravity presents one client identity ("ide" or "cli") for the whole
		// provider. It is a setting, not a per-account property: the identity says
		// which official client we imitate, which is the same for every account.
		dr.Post("/settings/antigravity-profile", h.HandleSetAntigravityClientProfile)

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
		dr.Put("/keys/{id}/acl", h.HandleUpdateKeyACL)
		dr.Delete("/keys/{id}", h.HandleDeleteKey)

		// Token Saver Settings
		dr.Get("/settings", h.HandleGetSettings)
		dr.Post("/settings", h.HandleUpdateSettings)

		// Self-update. status/check read the release source; apply replaces the
		// running binary and restarts, so it re-checks the dashboard password.
		dr.Get("/update/status", h.HandleUpdateStatus)
		dr.Get("/update/check", h.HandleUpdateCheck)
		dr.Post("/update/apply", h.HandleUpdateApply)

		// Proxy pools. Read-only here: the provider-strategies picker needs to
		// list pools by id + name, and backup preview already counts them. Pool
		// creation stays in the deploy/import paths, so there is no POST.
		dr.Get("/proxy-pools", h.HandleListProxyPools)

		// Historical usage. These power the Usage page and are deliberately
		// separate from the live /api/usage/* endpoints, which stream in-flight
		// requests and are mounted outside the session group.
		dr.Get("/usage/stats", h.HandleUsageHistoryStats)
		dr.Get("/usage/chart", h.HandleUsageHistoryChart)
		dr.Get("/usage/history", h.HandleUsageHistory)
		dr.Get("/usage/filters", h.HandleUsageFilterOptions)
		dr.Get("/usage/request-details", h.HandleRequestDetails)
		dr.Get("/usage/request-details/{id}", h.HandleRequestDetailByID)

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

// ServeProviderLogo serves an embedded provider brand image from
// ui/providers/{id}.{webp,png}. It is a plain static asset: the same bytes go to
// every caller, so there is nothing to authorise beyond the request itself.
//
// 404s are expected and cheap — the UI keeps a glyph fallback for providers with
// no brand asset — so a miss is reported without an error body.
func (h *Handler) ServeProviderLogo(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "file")
	// Only a bare "<id>.webp"/"<id>.png" is addressable. Rejecting anything with a
	// separator or a leading dot keeps ".." and nested reads out of the embed FS.
	if name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	// Only the two raster formats we actually ship. SVG is deliberately absent:
	// it can carry script, and serving it from this origin would turn a future
	// logo drop into stored XSS.
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	var contentType string
	switch ext {
	case "webp":
		contentType = "image/webp"
	case "png":
		contentType = "image/png"
	default:
		http.NotFound(w, r)
		return
	}

	data, err := uiAssets.ReadFile("ui/providers/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// The assets are immutable for a given build, so let the browser keep them.
	// ETag lets a hard-refresh still revalidate instead of re-downloading.
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("ETag", logoETag(data))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// logoETag derives a stable validator from the asset bytes. FNV-1a is plenty:
// this only has to change when the file does, not resist an attacker.
func logoETag(data []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return `"` + strconv.FormatUint(h.Sum64(), 36) + `"`
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

	// The chips are labelled by a connection's provider key, which for custom
	// OpenAI/Anthropic-compatible endpoints is a generated identifier like
	// "openai-compatible-chat-<uuid>" (upstream builds it the same way:
	// `${OPENAI_COMPATIBLE_PREFIX}${apiType}-${randomUUID()}`). That identifier
	// is an internal key, never a name: upstream shows `node.name` and keeps the
	// id out of sight. Resolve the node name here so the Overview chips read
	// "BAI: 33", not a UUID.
	nodeMap := make(map[string]*models.ProviderNode)
	if nodes, err := h.repo.GetAllProviderNodes(); err == nil {
		for _, n := range nodes {
			nodeMap[n.ID] = n
		}
	}
	// displayKeyFor maps a provider key to the label to show for it.
	displayKeyFor := func(providerKey string) string {
		if node, ok := nodeMap[providerKey]; ok && node.Name != nil && *node.Name != "" {
			return *node.Name
		}
		return providerKey
	}

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

		// "Active" means the account can actually serve a request, not merely
		// that its toggle is on: a credit-exhausted connection stays isActive=1
		// while testStatus reports unavailable. Counting the toggle alone made
		// the dashboard claim 33 broken accounts were connected.
		acct := extractAccountInfo(p.Data)
		if providers.IsEffectivelyActiveWithError(p.IsActive, acct.TestStatus, acct.HasActiveCooldown, acct.LastError) {
			activeProviders++
			categoryCounts[catKey]["active"]++
		}
		providerTypeCounts[displayKeyFor(p.Provider)]++
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

	// EffectiveStatus is the status the dashboard should show for this
	// connection. It is derived (see providers.EffectiveStatus) rather than
	// read verbatim from testStatus, because a stale "unavailable" marker with
	// no live cooldown means the account is usable again. IsEffectivelyActive
	// is the matching boolean used by the counters.
	EffectiveStatus     string `json:"effectiveStatus,omitempty"`
	IsEffectivelyActive bool   `json:"isEffectivelyActive"`
	HasActiveCooldown   bool   `json:"hasActiveCooldown,omitempty"`

	// ClientProfile is no longer reported per connection: the Antigravity client
	// identity is a provider-wide setting (see
	// HandleSetAntigravityClientProfile), so a per-account value would be a second
	// source of truth the router does not consult.

	// NoConnection marks a card synthesised for a registry provider that needs no
	// credential (see providers.NoAuthProviders) and therefore has no connection
	// row. The UI uses it to say "no account needed" instead of rendering a
	// 0/0 account counter, which would read as "broken".
	NoConnection bool `json:"noConnection,omitempty"`

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

	// HasActiveCooldown is true when the credential blob still carries a live
	// "modelLock_*" entry. It is what distinguishes a genuinely broken account
	// from one carrying a stale "unavailable" marker left behind by an expired
	// cooldown.
	HasActiveCooldown bool

	// LastError is the stored upstream error. It matters beyond diagnostics:
	// a spent credit/quota budget does not refill on a timer, so the status
	// stays "quota_exhausted" instead of silently recovering to "active".
	LastError string
}

// hasLiveModelLock reports whether the credential blob still holds any
// modelLock_* entry whose deadline is in the future.
func hasLiveModelLock(m map[string]any, now time.Time) bool {
	for key, val := range m {
		if !strings.HasPrefix(key, "modelLock_") {
			continue
		}
		s, ok := val.(string)
		if !ok || s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil && now.Before(t) {
			return true
		}
	}
	return false
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
	info.LastError = str("lastError")
	info.HasActiveCooldown = hasLiveModelLock(m, time.Now().UTC())

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

// nodePrefixOf extracts the configured prefix from a provider node's data blob.
func nodePrefixOf(node *models.ProviderNode) string {
	if node == nil || node.Data == "" {
		return ""
	}
	var raw struct {
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal([]byte(node.Data), &raw); err != nil {
		return ""
	}
	return strings.TrimSpace(raw.Prefix)
}

// buildProviderNodeInfo assembles the read-only endpoint view the compatible-node
// card renders. It reuses the node accessor so it sees the same fields the
// executor does; a missing/partial blob degrades to empty strings rather than an
// error, because an older node may predate any given field.
func buildProviderNodeInfo(h *Handler, node *models.ProviderNode) *ProviderNodeInfo {
	if node == nil {
		return nil
	}
	info := &ProviderNodeInfo{ID: node.ID}
	if node.Name != nil {
		info.Name = strings.TrimSpace(*node.Name)
	}

	var blob struct {
		Prefix     string `json:"prefix"`
		APIType    string `json:"apiType"`
		BaseURL    string `json:"baseUrl"`
		NodeName   string `json:"nodeName"`
		ChatPath   string `json:"chatPath"`
		ModelsPath string `json:"modelsPath"`
		IconURL    string `json:"iconUrl"`
		CompatMode string `json:"compatMode"`
	}
	if node.Data != "" {
		_ = json.Unmarshal([]byte(node.Data), &blob)
	}
	info.Prefix = strings.TrimSpace(blob.Prefix)
	info.APIType = strings.TrimSpace(blob.APIType)
	info.BaseURL = strings.TrimSpace(blob.BaseURL)
	info.ChatPath = strings.TrimSpace(blob.ChatPath)
	info.ModelsPath = strings.TrimSpace(blob.ModelsPath)
	info.IconURL = strings.TrimSpace(blob.IconURL)
	info.CompatMode = strings.TrimSpace(blob.CompatMode)
	if info.CompatMode == "" && providers.IsClaudeCodeNodeID(node.ID) {
		info.CompatMode = providers.CompatModeCC
	}
	if info.Name == "" {
		info.Name = strings.TrimSpace(blob.NodeName)
	}

	// The protocol in words: an Anthropic node speaks the Messages API whichever
	// api type is recorded, and the Claude Code variant is still Anthropic.
	nodeType := providers.NodeTypeOpenAICompatible
	if strings.HasPrefix(node.ID, providers.AnthropicCompatiblePrefix) {
		nodeType = providers.NodeTypeAnthropicCompatible
	}
	info.APILabel = providers.APITypeLabel(nodeType, info.APIType)
	info.APIPath = providers.APIPath(nodeType, info.CompatMode, info.APIType, info.ChatPath)
	return info
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
	Prefix        string            `json:"prefix,omitempty"`
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

	// NoConnection marks a keyless provider that owns no connection row by
	// design (see providers.NoAuthProviders). Without it the hero renders
	// "0/0 active", which reads as a broken provider rather than one that needs
	// no credential at all.
	NoConnection bool `json:"noConnection,omitempty"`

	// Node carries the endpoint details of a compatible provider (the upstream
	// "CompatibleNodeCard"). It is nil for a built-in provider. The UI shows
	// these so an operator can see — and verify — the URL a node actually
	// points at, which is otherwise invisible after creation.
	Node *ProviderNodeInfo `json:"node,omitempty"`
}

// ProviderNodeInfo is the read-only view of a compatible node, mirroring the
// fields upstream renders on its CompatibleNodeCard.
type ProviderNodeInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
	APIType    string `json:"apiType,omitempty"`
	ChatPath   string `json:"chatPath,omitempty"`
	ModelsPath string `json:"modelsPath,omitempty"`
	IconURL    string `json:"iconUrl,omitempty"`
	CompatMode string `json:"compatMode,omitempty"`
	// APILabel is the protocol in words ("Messages API", "Chat Completions",
	// ...) and APIPath the endpoint path with its leading slash removed, so the
	// UI can render "<baseUrl>/<apiPath>" verbatim.
	APILabel string `json:"apiLabel,omitempty"`
	APIPath  string `json:"apiPath,omitempty"`
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
		// A no-auth provider needs no account, so "0/0 active" would misdescribe it.
		detail.NoConnection = meta.AuthType == "none"
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

	// A compatible endpoint is addressed by its generated key, which the UI
	// sends back for writes (add/remove model). Keep Provider as that stable
	// internal handle and show the node's name/prefix for display only, so the
	// page reads "Atria AI" / "atri" and never a uuid.
	//
	// The page may also be opened by prefix ("atri") — that is what the model
	// ids use and what a user is most likely to type or link to — so resolve the
	// prefix back to its node id before matching connections.
	targetID := canonical
	if providers.IsGeneratedNodeID(canonical) {
		detail.Provider = canonical
		detail.DisplayName = canonical
		if node, ok := nodeMap[canonical]; ok {
			if node.Name != nil && *node.Name != "" {
				detail.DisplayName = *node.Name
			}
			detail.Prefix = nodePrefixOf(node)
		}
	} else if raw != canonical && providers.IsGeneratedNodeID(raw) {
		targetID = raw
	} else {
		// Look for a node whose prefix equals the requested key.
		for id, node := range nodeMap {
			if nodePrefixOf(node) == canonical && canonical != "" {
				targetID = id
				detail.Provider = id
				detail.Prefix = canonical
				if node.Name != nil && *node.Name != "" {
					detail.DisplayName = *node.Name
				} else {
					detail.DisplayName = canonical
				}
				break
			}
		}
	}

	// Compatible endpoints get the upstream "CompatibleNodeCard": the endpoint the
	// node actually calls, plus its own Add/Edit/Delete actions. A built-in
	// provider has no node, so this stays nil and the UI renders as before.
	if node, ok := nodeMap[targetID]; ok {
		detail.Node = buildProviderNodeInfo(h, node)
	}

	for _, c := range all {
		if c.Provider != targetID && c.Provider != canonical && c.Provider != raw {
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

			EffectiveStatus:     providers.EffectiveStatusWithError(c.IsActive, acct.TestStatus, acct.HasActiveCooldown, acct.LastError),
			IsEffectivelyActive: providers.IsEffectivelyActiveWithError(c.IsActive, acct.TestStatus, acct.HasActiveCooldown, acct.LastError),
			HasActiveCooldown:   acct.HasActiveCooldown,
		}
		applyProviderMeta(&sum, m, hasMeta)
		if providers.IsGeneratedNodeID(c.Provider) {
			if node, ok := nodeMap[c.Provider]; ok && node.Name != nil && *node.Name != "" {
				sum.RegistryName = *node.Name
			}
		}
		detail.Connections = append(detail.Connections, sum)
		detail.TotalCount++
		if sum.IsEffectivelyActive {
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
	models, err := h.repo.ListCachedModelsForDashboard(keys...)
	if err == nil && len(models) > 0 {
		// A model the operator removed must not reappear here. /v1/models
		// filters through hiddenModels; this payload did not, so removing a
		// model took it out of the API and left it on the page — which reads
		// as "the removal did nothing".
		hiddenKeys := append([]string{}, keys...)
		if p := h.nodePrefixKey(raw); p != "" {
			hiddenKeys = append(hiddenKeys, p)
		}
		if p := h.nodePrefixKey(canonical); p != "" {
			hiddenKeys = append(hiddenKeys, p)
		}
		hidden, _ := h.repo.GetAllHiddenModels()
		kept := make([]db.CachedModel, 0, len(models))
		for _, m := range models {
			if isHiddenModel(hidden, hiddenKeys, m.ModelID) {
				continue
			}
			kept = append(kept, m)
		}
		models = kept
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
	if creds == "" && !providers.IsNoAuthProvider(canonical) && !providers.IsNoAuthProvider(raw) {
		// Only a credential-requiring provider needs a connection. Keyless
		// providers authenticate with their registry default key below, so
		// refusing here would make a provider the operator can chat with look
		// like it cannot list models at all.
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

	// Re-adding a previously removed model must clear the hidden marker, or the
	// operator could never bring a model back.
	_ = h.repo.UnhideModel(providerKeyCandidates(canonical, raw), payload.ModelID)

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
	// A node advertises its models under its configured prefix, so the marker
	// must be readable under that spelling as well as the generated node id.
	if p := h.nodePrefixKey(raw); p != "" {
		keys = append(keys, p)
	}
	if p := h.nodePrefixKey(canonical); p != "" {
		keys = append(keys, p)
	}
	keys = dedupeNonEmpty(keys)

	removed := 0
	for _, k := range keys {
		if err := h.repo.RemoveCachedModel(k, modelID); err == nil {
			removed++
		}
	}

	// A cache-only delete is not enough: the engine rebuilds /v1/models from
	// the static registry, customModels and enabledModels, so the removed model
	// would reappear. Record an explicit hidden marker so the model list honours
	// the removal regardless of where the id came from.
	if err := h.repo.HideModel(keys, modelID); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

// providerKeyCandidates returns the canonical id, the raw request key, and all
// registered aliases for a provider, deduplicated. Every candidate is stored so
// a later lookup by any spelling of the provider matches.
func providerKeyCandidates(canonical, raw string) []string {
	keys := []string{canonical, raw}
	keys = append(keys, providers.AliasesFor(canonical)...)
	if raw != canonical {
		keys = append(keys, providers.AliasesFor(raw)...)
	}
	return dedupeNonEmpty(keys)
}

// nodePrefixKey returns the prefix a compatible-endpoint node advertises its
// models under ("xkiro" for node "openai-compatible-chat-<uuid>"), or "" when
// the id is not a node or has no prefix configured.
// /v1/models renders a node's models as "<prefix>/<model>" and checks hidden
// markers against that same prefix, so anything recording a removal has to use
// it. Writing the marker under the node's generated id instead produced 13
// markers that the list could never match, and the "failed" models stayed
// advertised after auto-disable reported success.
func (h *Handler) nodePrefixKey(provID string) string {
	if h.repo == nil || provID == "" {
		return ""
	}
	prefixes, err := h.repo.GetProviderNodePrefixMap()
	if err != nil {
		return ""
	}
	return prefixes[provID]
}

// dedupeNonEmpty drops empty strings and duplicates, preserving order.
func dedupeNonEmpty(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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
		effective := providers.EffectiveStatusWithError(c.IsActive, acct.TestStatus, acct.HasActiveCooldown, acct.LastError)
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

			EffectiveStatus:     effective,
			IsEffectivelyActive: providers.IsEffectivelyActiveWithError(c.IsActive, acct.TestStatus, acct.HasActiveCooldown, acct.LastError),
			HasActiveCooldown:   acct.HasActiveCooldown,
		}
		applyProviderMeta(&summary, meta, hasMeta)
		// A generated node id is not a registry name. When the registry has no
		// entry (compatible endpoints never do), show the node's own name so the
		// card reads "Atria AI" rather than a uuid.
		if providers.IsGeneratedNodeID(c.Provider) {
			if node, ok := nodeMap[c.Provider]; ok && node.Name != nil && *node.Name != "" {
				summary.RegistryName = *node.Name
			}
		}
		res = append(res, summary)
	}

	// A no-auth provider (OpenCode Free, the local TTS/search servers) has nothing
	// to connect, so it owns no connection row — yet the grid above is built from
	// connections and would never render it. Append a zero-connection card for any
	// registry provider that authenticates with no credential and is not already
	// present, mirroring upstream's always-visible "No Auth" section (#3290).
	seen := make(map[string]bool, len(res))
	for _, c := range res {
		seen[providers.ResolveAlias(c.Provider)] = true
	}
	for _, meta := range providers.NoAuthProviders() {
		if seen[meta.ID] {
			continue
		}
		summary := ProviderSummary{
			// Stable synthetic id: the detail page resolves either a uuid or a
			// provider prefix, so the registry id is a valid handle here and is
			// what the UI already passes to openProviderDetail().
			ID:                  meta.ID,
			Provider:            meta.ID,
			DisplayName:         meta.Name,
			Category:            string(meta.Category),
			CategoryLabel:       providers.GetCategoryLabel(meta.Category),
			AuthType:            meta.AuthType,
			IsActive:            1,
			EffectiveStatus:     "active",
			IsEffectivelyActive: true,
			NoConnection:        true,
		}
		if m, ok := providers.GetProviderMeta(meta.ID); ok {
			applyProviderMeta(&summary, m, true)
		}
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

// comboNameRe mirrors VansRouter's VALID_NAME_REGEX: combo names double as
// model IDs, so keep them path-safe.
var comboNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.\-]+$`)

// comboStrategies is the set ApplyComboStrategy implements; anything else
// silently behaves like "fallback", which is worse than rejecting it.
var comboStrategies = map[string]bool{
	"fallback":    true,
	"round-robin": true,
	"capacity":    true,
	"fusion":      true,
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
		Strategy  string   `json:"strategy"`
		CreatedAt string   `json:"createdAt"`
		UpdatedAt string   `json:"updatedAt"`
	}

	resp := make([]ComboResponse, 0, len(combos))
	for _, c := range combos {
		var modelList []string
		_ = json.Unmarshal([]byte(c.Models), &modelList)
		strategy := c.Strategy
		if strategy == "" {
			strategy = "fallback"
		}
		resp = append(resp, ComboResponse{
			ID:        c.ID,
			Name:      c.Name,
			Kind:      c.Kind,
			Models:    modelList,
			Strategy:  strategy,
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
	// VansRouter parity: names become client-facing model IDs, so they must
	// stay URL/path safe. Anything else breaks routing for callers that
	// percent-encode or glob the model name.
	if !comboNameRe.MatchString(payload.Name) {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Name can only contain letters, numbers, -, _ and .")
		return
	}
	if payload.Strategy == "" {
		payload.Strategy = "fallback"
	}
	if !comboStrategies[payload.Strategy] {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Unknown strategy: "+payload.Strategy)
		return
	}

	id := payload.ID
	if id == "" {
		if existing, err := h.repo.GetComboByName(payload.Name); err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		} else if existing != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "Combo name already exists")
			return
		}
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

// HandleUpdateKeyACL sets the access lists on a key.
//
// PUT /api/dashboard/keys/{id}/acl
// Body: {"allowedProviders":null|[], "allowedCombos":null|[], "allowedKinds":null|[]}
//
// A field absent from the body is left unchanged; an explicit null clears the
// restriction. This is the operator-facing half of the ACL: enforcement lives
// in the chat/media handlers.
func (h *Handler) HandleUpdateKeyACL(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing key ID")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Failed to read body")
		return
	}

	// Pointer-to-slice distinguishes "absent" (leave alone) from "null"
	// (clear the restriction) from "[]" (allow nothing).
	var req struct {
		AllowedProviders *[]string `json:"allowedProviders"`
		AllowedCombos    *[]string `json:"allowedCombos"`
		AllowedKinds     *[]string `json:"allowedKinds"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	keys, err := h.repo.GetAllApiKeys()
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var existing *models.APIKey
	for _, k := range keys {
		if k.ID == id {
			existing = k
			break
		}
	}
	if existing == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "Key not found")
		return
	}

	providers, combos, kinds := existing.AllowedProviders, existing.AllowedCombos, existing.AllowedKinds
	if req.AllowedProviders != nil {
		providers = *req.AllowedProviders
	}
	if req.AllowedCombos != nil {
		combos = *req.AllowedCombos
	}
	if req.AllowedKinds != nil {
		kinds = *req.AllowedKinds
	}

	if err := h.repo.UpdateApiKeyACL(id, providers, combos, kinds); err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"id":               id,
		"allowedProviders": providers,
		"allowedCombos":    combos,
		"allowedKinds":     kinds,
	})
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
		// Headroom is an optional external proxy; the UI needs its switch, URL,
		// timeout and extras so the card can render without a second request.
		"headroomEnabled":              h.tokenSaver.HeadroomEnabled(),
		"headroomUrl":                  h.tokenSaver.HeadroomURL(),
		"headroomTimeoutMs":            h.tokenSaver.HeadroomTimeoutMs(),
		"headroomCodeAware":            h.tokenSaver.HeadroomCodeAware(),
		"headroomKompress":             h.tokenSaver.HeadroomKompress(),
		"headroomCompressUserMessages": h.tokenSaver.HeadroomCompressUserMessages(),
		// The level vocabularies the server accepts, so the dropdowns cannot
		// drift from what GetCavemanPrompt/GetPonytailPrompt understand.
		"cavemanLevels":  tokensaver.CavemanLevels,
		"ponytailLevels": tokensaver.PonytailLevels,
		"autoUpdate":     s.AutoUpdate,
		// The provider-wide Antigravity client identity. Normalized to a concrete
		// value so the UI dropdown always has a valid selection to render.
		"antigravityClientProfile": string(providers.NormalizeAntigravityClientProfile(s.AntigravityClientProfile)),
		// providerStrategies drives proxy routing for no-auth/free providers.
		// Normalized to an object so the UI never has to null-check.
		"providerStrategies": providerStrategiesOrEmpty(s.ProviderStrategies),
		// Reverse-proxy awareness. These are editable from the UI and take effect
		// immediately. The bind address is included for diagnostics only: it is
		// fixed at process start and cannot be changed from here.
		"trustProxy":       h.effectiveTrustProxy(s),
		"authCookieSecure": h.effectiveCookieSecure(s),
		// requireApiKey mirrors VansRouter. Absent means the default (required),
		// resolved to a concrete bool so the toggle always has a state to render.
		"requireApiKey": s.RequireAPIKey == nil || *s.RequireAPIKey,
		// allowRemoteNoApiKey only matters while requireApiKey is off; it is the
		// second, explicit step that widens access past this machine.
		"allowRemoteNoApiKey":  s.AllowRemoteNoApiKey != nil && *s.AllowRemoteNoApiKey,
		"proxyEnvTrustProxy":   strings.EqualFold(os.Getenv("TRUST_PROXY"), "true"),
		"proxyEnvCookieSecure": strings.EqualFold(os.Getenv("AUTH_COOKIE_SECURE"), "true"),
		"listenHost":           db.ListenHost(),
		"listenPort":           db.ListenPort(),
		"behindProxySuggested": db.ListenHost() == "127.0.0.1" || db.ListenHost() == "localhost",
	}

	handlerutil.WriteJSON(w, http.StatusOK, resp)
}

// HandleListProxyPools returns the proxy pools a provider strategy may target.
// Only metadata is exposed; proxy URLs stay server-side so credentials embedded
// in them never reach the browser.
func (h *Handler) HandleListProxyPools(w http.ResponseWriter, r *http.Request) {
	pools, err := h.repo.ListProxyPools(false)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pools == nil {
		pools = []db.ProxyPoolSummary{}
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"proxyPools": pools})
}

// providerStrategiesOrEmpty returns a non-nil map so the JSON payload always
// carries "providerStrategies": {} rather than null.
// effectiveTrustProxy resolves the proxy flag the way the auth handler does:
// the stored setting wins, otherwise the environment variable decides.
func (h *Handler) effectiveTrustProxy(s *db.SettingsData) bool {
	if s != nil && s.TrustProxy != nil {
		return *s.TrustProxy
	}
	return strings.EqualFold(os.Getenv("TRUST_PROXY"), "true")
}

// effectiveCookieSecure resolves the Secure-cookie flag, falling back to the
// environment variable when the UI has never set it.
func (h *Handler) effectiveCookieSecure(s *db.SettingsData) bool {
	if s != nil && s.AuthCookieSecure != nil {
		return *s.AuthCookieSecure
	}
	return strings.EqualFold(os.Getenv("AUTH_COOKIE_SECURE"), "true")
}

func providerStrategiesOrEmpty(m map[string]db.ProviderStrategy) map[string]db.ProviderStrategy {
	if m == nil {
		return map[string]db.ProviderStrategy{}
	}
	return m
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
		// Headroom is the optional external compression proxy. The URL and
		// timeout are accepted here so the Token Saver page saves in one request.
		HeadroomEnabled        *bool   `json:"headroomEnabled"`
		HeadroomUrl            *string `json:"headroomUrl"`
		HeadroomTimeoutMs      *int    `json:"headroomTimeoutMs"`
		HeadroomCodeAware      *bool   `json:"headroomCodeAware"`
		HeadroomKompress       *bool   `json:"headroomKompress"`
		HeadroomCompressUserMs *bool   `json:"headroomCompressUserMessages"`
		// AntigravityClientProfile is the provider-wide client identity. Accepted
		// here as well so the settings form can save everything in one request.
		AntigravityClientProfile *string `json:"antigravityClientProfile"`
		// Reverse-proxy awareness, editable from the UI settings page.
		TrustProxy       *bool `json:"trustProxy"`
		AuthCookieSecure *bool `json:"authCookieSecure"`
		// requireApiKey mirrors VansRouter's flag of the same name. Absent leaves
		// the current value alone; explicit false lets /v1/* answer without a key.
		RequireAPIKey *bool `json:"requireApiKey"`
		// allowRemoteNoApiKey widens the above from loopback-only to anyone who
		// can reach the port. Same name and semantics as VansRouter's.
		AllowRemoteNoApiKey *bool `json:"allowRemoteNoApiKey"`
		// providerStrategies is a full-replace map keyed by provider id, matching
		// VansRouter's PATCH /api/settings behavior. An entry with no fields left
		// set is dropped so the settings blob stays clean.
		ProviderStrategies *map[string]db.ProviderStrategy `json:"providerStrategies"`
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
	// The switch and the level are independent fields: the UI sends the level
	// alone when the operator picks an intensity, and requiring the enable flag
	// alongside it would silently drop that choice.
	if req.CavemanLevel != nil && *req.CavemanLevel != "" {
		if !tokensaver.ValidLevel(*req.CavemanLevel, tokensaver.CavemanLevels) {
			handlerutil.WriteJSONError(w, http.StatusBadRequest,
				`cavemanLevel must be one of: lite, full, ultra`)
			return
		}
		lvl := tokensaver.NormalizeLevel(*req.CavemanLevel, tokensaver.CavemanLevels)
		h.tokenSaver.SetCaveman(h.tokenSaver.CavemanEnabled(), lvl)
		s.CavemanLevel = lvl
	}
	if req.CavemanEnabled != nil {
		h.tokenSaver.SetCaveman(*req.CavemanEnabled, h.tokenSaver.CavemanLevel())
		s.CavemanEnabled = *req.CavemanEnabled
		s.CavemanLevel = h.tokenSaver.CavemanLevel()
	}
	if req.PonytailLevel != nil && *req.PonytailLevel != "" {
		if !tokensaver.ValidLevel(*req.PonytailLevel, tokensaver.PonytailLevels) {
			handlerutil.WriteJSONError(w, http.StatusBadRequest,
				`ponytailLevel must be one of: lite, full, ultra`)
			return
		}
		lvl := tokensaver.NormalizeLevel(*req.PonytailLevel, tokensaver.PonytailLevels)
		h.tokenSaver.SetPonytail(h.tokenSaver.PonytailEnabled(), lvl)
		s.PonytailLevel = lvl
	}
	if req.PonytailEnabled != nil {
		h.tokenSaver.SetPonytail(*req.PonytailEnabled, h.tokenSaver.PonytailLevel())
		s.PonytailEnabled = *req.PonytailEnabled
		s.PonytailLevel = h.tokenSaver.PonytailLevel()
	}
	if req.HeadroomEnabled != nil || req.HeadroomUrl != nil || req.HeadroomTimeoutMs != nil {
		enabled := h.tokenSaver.HeadroomEnabled()
		if req.HeadroomEnabled != nil {
			enabled = *req.HeadroomEnabled
		}
		url := h.tokenSaver.HeadroomURL()
		if req.HeadroomUrl != nil && strings.TrimSpace(*req.HeadroomUrl) != "" {
			url = strings.TrimSpace(*req.HeadroomUrl)
		}
		timeout := h.tokenSaver.HeadroomTimeoutMs()
		if req.HeadroomTimeoutMs != nil && *req.HeadroomTimeoutMs > 0 {
			timeout = *req.HeadroomTimeoutMs
		}
		h.tokenSaver.SetHeadroom(enabled, url, timeout)
		s.HeadroomEnabled = enabled
		s.HeadroomUrl = url
		s.HeadroomTimeoutMs = timeout
	}
	if req.HeadroomCodeAware != nil || req.HeadroomKompress != nil || req.HeadroomCompressUserMs != nil {
		code := h.tokenSaver.HeadroomCodeAware()
		if req.HeadroomCodeAware != nil {
			code = *req.HeadroomCodeAware
		}
		kompress := h.tokenSaver.HeadroomKompress()
		if req.HeadroomKompress != nil {
			kompress = *req.HeadroomKompress
		}
		compressUser := h.tokenSaver.HeadroomCompressUserMessages()
		if req.HeadroomCompressUserMs != nil {
			compressUser = *req.HeadroomCompressUserMs
		}
		h.tokenSaver.SetHeadroomFlags(code, kompress, compressUser)
		s.HeadroomCodeAware = code
		s.HeadroomKompress = kompress
		s.HeadroomCompressUM = compressUser
	}
	if req.InjectionGuardEnabled != nil {
		h.tokenSaver.SetInjectionGuard(*req.InjectionGuardEnabled)
	}
	if req.AutoUpdate != nil {
		s.AutoUpdate = *req.AutoUpdate
	}
	if req.AntigravityClientProfile != nil {
		if !providers.IsAntigravityClientProfile(*req.AntigravityClientProfile) {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, `antigravityClientProfile must be "ide" or "cli"`)
			return
		}
		s.AntigravityClientProfile = string(providers.NormalizeAntigravityClientProfile(*req.AntigravityClientProfile))
	}
	if req.TrustProxy != nil {
		s.TrustProxy = req.TrustProxy
	}
	if req.AuthCookieSecure != nil {
		s.AuthCookieSecure = req.AuthCookieSecure
	}
	if req.RequireAPIKey != nil {
		s.RequireAPIKey = req.RequireAPIKey
	}
	if req.AllowRemoteNoApiKey != nil {
		s.AllowRemoteNoApiKey = req.AllowRemoteNoApiKey
	}
	if req.ProviderStrategies != nil {
		// providerStrategies arrives as the operator's complete view of the map,
		// but other callers (scripts, the Next.js dashboard) may patch a single
		// provider. Replacing the whole map would silently wipe every other
		// provider's routing, so merge per provider key instead: a key present in
		// the payload wins (including an all-empty entry, which is how a provider
		// is explicitly cleared), and absent keys are left untouched.
		merged := make(map[string]db.ProviderStrategy, len(s.ProviderStrategies)+len(*req.ProviderStrategies))
		for k, v := range s.ProviderStrategies {
			merged[k] = v
		}
		for k, v := range *req.ProviderStrategies {
			if k == "" {
				continue
			}
			if v.ProxyPoolID == "__none__" {
				v.ProxyPoolID = ""
			}
			if v.IsEmpty() {
				delete(merged, k)
				continue
			}
			merged[k] = v
		}
		s.ProviderStrategies = sanitizeProviderStrategies(merged)
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
		"providerStrategies":    providerStrategiesOrEmpty(s.ProviderStrategies),
		// Echoed back so the endpoint toggle can settle on the stored value
		// instead of guessing. Absent means the default, which is "required".
		"requireApiKey":       s.RequireAPIKey == nil || *s.RequireAPIKey,
		"allowRemoteNoApiKey": s.AllowRemoteNoApiKey != nil && *s.AllowRemoteNoApiKey,
	})
}

// sanitizeProviderStrategies drops entries that carry no configuration, so a
// provider whose fields were all cleared does not linger as an empty object.
// Mirrors VansRouter, which deletes the key when the override becomes empty.
func sanitizeProviderStrategies(in map[string]db.ProviderStrategy) map[string]db.ProviderStrategy {
	out := make(map[string]db.ProviderStrategy, len(in))
	for id, strat := range in {
		if id == "" {
			continue
		}
		if strat.ProxyPoolID == "__none__" {
			strat.ProxyPoolID = ""
		}
		if strat.IsEmpty() {
			continue
		}
		out[id] = strat
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isHiddenModel reports whether modelID was explicitly removed for any of the
// given provider spellings. The hidden set is keyed "<provider>|<model>", so
// every spelling the provider can be addressed by must be tried — an alias and
// a node prefix produce different keys for the same model.
func isHiddenModel(hidden map[string]bool, providerKeys []string, modelID string) bool {
	if len(hidden) == 0 {
		return false
	}
	for _, k := range providerKeys {
		if k == "" {
			continue
		}
		if hidden[k+"|"+modelID] {
			return true
		}
	}
	return false
}
