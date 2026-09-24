package handlers

import (
	json "encoding/json/v2"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/pprof"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlers/dashboard"
	"9router/proxy/internal/handlers/media"
	"9router/proxy/internal/handlers/oauth"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
)

// Re-export TokenSaverConfig for root compatibility
type TokenSaverConfig = shared.TokenSaverConfig

// NewTokenSaverConfig re-exports shared.NewTokenSaverConfig.
func NewTokenSaverConfig(rtk, caveman, ponytail bool) *TokenSaverConfig {
	return shared.NewTokenSaverConfig(rtk, caveman, ponytail)
}

// SetupRoutes mounts all domain handlers on the provided router.
func SetupRoutes(r interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
	HandleFunc(pattern string, handlerFn http.HandlerFunc)
}, repo *db.Repo, ts *TokenSaverConfig) {
	chatH := chat.NewChatHandler(repo, ts)
	mediaH := media.NewMediaHandler(repo, ts, chatH)
	oauthH := oauth.NewOAuthHandler(repo)

	// Chat, Version & Models Domain
	r.Get("/version", chatH.HandleVersion)
	r.Get("/api/version", chatH.HandleVersion)
	r.Get("/api/version/status", chatH.HandleVersionStatus)
	r.Get("/api/version/check", chatH.HandleCheckUpdate)
	r.Post("/api/version/update", chatH.HandleTriggerUpdate)
	r.Post("/api/version/auto-update", chatH.HandleToggleAutoUpdate)
	r.Get("/models", chatH.HandleModels)
	r.Get("/models/info", chatH.HandleModelsInfo)
	r.Get("/models/{kind}", chatH.HandleModelsByKind)
	r.Get("/models/*", chatH.HandleModelLookup)
	r.Get("/v1/models", chatH.HandleModels)
	r.Get("/v1/models/*", chatH.HandleModelLookup)
	r.Get("/api/v1/models", chatH.HandleModels)
	r.Get("/api/v1/models/*", chatH.HandleModelLookup)
	r.Get("/api/models", chatH.HandleModels)
	r.Get("/api/models/*", chatH.HandleModelLookup)
	r.Get("/api/models/catalog-sync", chatH.HandleCatalogSyncStatus)
	r.Post("/api/models/catalog-sync", chatH.HandleCatalogSyncTrigger)
	r.Post("/chat/completions", chatH.HandleChatCompletions)
	r.Post("/messages", chatH.HandleMessages)
	r.Post("/messages/count_tokens", chatH.HandleCountTokens)
	r.Post("/api/chat", chatH.HandleOllamaChat)

	// Media, Audio, Video & Web Tools Domain
	r.Post("/embeddings", mediaH.HandleEmbeddings)
	r.Post("/responses", mediaH.HandleResponses)
	r.Post("/responses/compact", mediaH.HandleResponsesCompact)
	r.Post("/images/generations", mediaH.HandleImages)
	r.Post("/audio/speech", mediaH.HandleAudioSpeech)
	r.Get("/audio/voices", mediaH.HandleAudioVoices)
	r.Post("/audio/transcriptions", mediaH.HandleAudioTranscriptions)
	r.Post("/videos/generations", mediaH.HandleVideoGenerations)
	r.Post("/videos/edits", mediaH.HandleVideoEdits)
	r.Post("/videos/extensions", mediaH.HandleVideoExtensions)
	r.Get("/videos/{id}", mediaH.HandleVideoGet)
	r.Post("/search", mediaH.HandleSearch)
	r.Post("/scrape", mediaH.HandleScrape)
	r.Post("/web/fetch", mediaH.HandleWebFetch)

	// Proxy Pool Deploy Domain
	r.Post("/proxy-pools/vercel-deploy", mediaH.HandleVercelDeploy)
	r.Post("/proxy-pools/deno-deploy", mediaH.HandleDenoDeploy)
	r.Post("/proxy-pools/cloudflare-deploy", mediaH.HandleCloudflareDeploy)

	// CLI Tools Status Domain (dashboard batch status for installed CLI tools)
	r.Get("/cli-tools/all-statuses", media.NewCLIToolsHandler().HandleAllStatuses)

	// Headroom Management Domain (token-compression proxy lifecycle + dashboard proxy)
	headroomH := media.NewHeadroomHandler(repo)
	r.Post("/headroom/start", headroomH.HandleHeadroomStart)
	r.Post("/headroom/stop", headroomH.HandleHeadroomStop)
	r.Post("/headroom/restart", headroomH.HandleHeadroomRestart)
	r.Get("/headroom/status", headroomH.HandleHeadroomStatus)
	r.Get("/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Post("/headroom/extras", headroomH.HandleHeadroomExtras)
	r.Delete("/headroom/extras", headroomH.HandleHeadroomExtras)
	r.HandleFunc("/headroom/proxy", headroomH.HandleHeadroomProxy)
	r.HandleFunc("/headroom/proxy/*", headroomH.HandleHeadroomProxy)

	// OAuth & Import Tokens Domain
	r.Post("/api/oauth/{provider}/import", oauthH.HandleOAuthImport)
	r.Get("/api/oauth/kiro/social-authorize", oauthH.HandleOAuthKiroSocialAuthorize)
	r.Post("/api/oauth/kiro/social-exchange", oauthH.HandleOAuthKiroSocialExchange)
	r.Post("/api/oauth/codex/bulk-import", oauthH.HandleOAuthCodexBulkImport)
	r.Post("/api/oauth/grok-cli/bulk-import", oauthH.HandleOAuthGrokCliBulkImport)

	// Antigravity OAuth (add account from the dashboard). Authorize returns the
	// Google consent URL + PKCE; exchange turns the pasted callback into a
	// provider connection. (The public loopback callback route lives in
	// SetupServerRouter so Google's redirect needs no API key.)
	r.Get("/api/oauth/antigravity/authorize", oauthH.HandleAntigravityAuthorize)
	r.Post("/api/oauth/antigravity/exchange", oauthH.HandleAntigravityExchange)

	// CodeBuddy OAuth (device-auth handshake). Authorize starts the flow and
	// returns {state, authUrl}; exchange is polled with that state until the
	// operator approves. Stateless server-side, so it works headless.
	r.Get("/api/oauth/codebuddy/authorize", oauthH.HandleCodebuddyAuthorize)
	r.Post("/api/oauth/codebuddy/exchange", oauthH.HandleCodebuddyExchange)

	// GitHub Copilot OAuth (RFC 8628 device flow). Authorize returns
	// {deviceCode, userCode, verificationUri}; exchange is polled with the
	// deviceCode until GitHub issues the token, after which the Copilot bearer
	// token is derived and saved. Stateless server-side, so it works headless.
	r.Get("/api/oauth/github/authorize", oauthH.HandleGitHubAuthorize)
	r.Post("/api/oauth/github/exchange", oauthH.HandleGitHubExchange)

	// Cline OAuth (authorization-code flow via a loopback callback_url). Cline
	// bounces through its own WorkOS callback and then redirects to the
	// callback_url we supply, embedding the tokens as base64 JSON. The operator
	// pastes that URL back, so no callback server has to be reachable.
	r.Get("/api/oauth/cline/authorize", oauthH.HandleClineAuthorize)
	r.Post("/api/oauth/cline/exchange", oauthH.HandleClineExchange)

	// Gemini CLI OAuth (Google consumer PKCE flow, same pattern as Antigravity
	// but different client credentials and scopes). Authorize returns the Google
	// consent URL; exchange turns the pasted callback into a provider connection.
	r.Get("/api/oauth/gemini-cli/authorize", oauthH.HandleGeminiCLIAuthorize)
	r.Post("/api/oauth/gemini-cli/exchange", oauthH.HandleGeminiCLIExchange)

	// Freebuff / Codebuff CLI login (fingerprint device flow). Authorize returns
	// a browser login URL; exchange is polled with the fingerprint until the
	// operator signs in. Stateless server-side, so it works headless.
	r.Get("/api/oauth/freebuff/authorize", oauthH.HandleFreebuffAuthorize)
	r.Post("/api/oauth/freebuff/exchange", oauthH.HandleFreebuffExchange)

	// Kilo Code device flow. Its poll carries the state in the HTTP status
	// (202/403/410/200) and its initiate takes no body, neither of which the
	// generic spec shape can express, so it has a dedicated handler.
	r.Get("/api/oauth/kilocode/authorize", oauthH.HandleKilocodeAuthorize)
	r.Post("/api/oauth/kilocode/exchange", oauthH.HandleKilocodeExchange)

	// Generic OAuth flows, driven by a spec table (kimi, grok-cli, claude,
	// iflow, gitlab, xai, cursor, codex). Registered AFTER the specific routes
	// above so a provider with a dedicated handler — Antigravity's loopback
	// callback, CodeBuddy's two regions — is never shadowed by the generic
	// path. chi matches in registration order, so this ordering is load-bearing.
	r.Get("/api/oauth/{provider}/authorize", oauthH.HandleGenericAuthorize)
	r.Post("/api/oauth/{provider}/exchange", oauthH.HandleGenericExchange)
	r.Get("/api/oauth/{provider}/flow", oauthH.HandleGenericFlowInfo)

	// Live Console Logs Domain (dashboard "Monitor Console Log")
	r.Get("/translator/console-logs", HandleConsoleLogsGet)
	r.Get("/api/translator/console-logs", HandleConsoleLogsGet)
	r.Delete("/translator/console-logs", HandleConsoleLogsDelete)
	r.Delete("/api/translator/console-logs", HandleConsoleLogsDelete)
	r.Get("/translator/console-logs/stream", HandleConsoleLogsStream)
	r.Get("/api/translator/console-logs/stream", HandleConsoleLogsStream)
	// Process/host resource sample for the Console Log resource card.
	r.Get("/api/system/metrics", HandleSystemMetrics)

	// Usage Real-time SSE Stream & Stats Domain (dashboard topology animation + recent requests)
	r.Get("/usage/stream", HandleUsageStream(repo))
	r.Get("/api/usage/stream", HandleUsageStream(repo))
	r.Get("/usage/stats", HandleUsageStats(repo))
	r.Get("/api/usage/stats", HandleUsageStats(repo))

	// Debug Tracing Domain (p50/p95 latency per provider+model)
	r.Get("/debug/traces", HandleDebugTraces)

	// Provider resilience state: which breaker is open and which accounts are
	// saturated. The reset endpoint exists so an operator who has fixed an
	// upstream does not have to wait out the backoff.
	r.Get("/debug/resilience", HandleResilienceStatus)
	r.Post("/debug/resilience/reset", HandleResilienceReset)

	// Prompt Cache Domain (Exact prompt hit/miss stats & flush)
	r.Get("/api/cache/stats", chatH.HandleCacheStats)
	r.Post("/api/cache/clear", chatH.HandleCacheClear)
}

// SetupServerRouter mounts public endpoints (/health, /api/hello) and
// API-key protected routes (all engine + admin routes) on the chi router.
func SetupServerRouter(r chi.Router, repo *db.Repo, ts *TokenSaverConfig) {
	dashH := dashboard.NewHandler(repo, ts)

	// Login/session endpoints are public by necessity: the login form cannot
	// require a session. Each handler enforces its own rule (login is rate
	// limited, change-password needs a session, reset-password is local-only).
	dashH.RegisterAuthRoutes(r)

	// Everything else under the dashboard requires a session cookie, a valid API
	// key, or login disabled in settings. Mounted first so /dashboard HTML is
	// redirected to the login page rather than served.
	r.Group(func(pr chi.Router) {
		pr.Use(middleware.RequireDashboardSession(middleware.DashboardSessionConfig{
			Repo:          repo,
			VerifySession: dashH.VerifySession,
			LoginDisabled: dashH.LoginDisabled,
			LoginPath:     "/login",
		}))
		pr.HandleFunc("/dashboard", dashH.ServeUI)
		pr.HandleFunc("/dashboard/*", dashH.ServeUI)
		dashH.RegisterRoutes(pr)
	})

	// Public root and login routes load the embedded dashboard shell; the shell
	// itself calls /api/dashboard/auth/status to decide what to render.
	r.Get("/", dashH.ServeUI)
	r.Get("/login", dashH.ServeUI)

	// Provider brand logos used by the dashboard grid. Public on purpose: an
	// <img> tag carries no Authorization header, and these are static images of
	// third-party brand marks with no operator data in them. Requiring a session
	// here would leave every provider tile with a broken image on first paint.
	r.Get("/provider-logos/{file}", dashH.ServeProviderLogo)

	// Antigravity OAuth loopback callback must be reachable WITHOUT an API key:
	// the browser arrives here straight from Google's redirect and carries no
	// Authorization header. It is safe to expose — it only completes a flow
	// whose PKCE verifier is held in this process's in-memory store.
	oauthPublic := oauth.NewOAuthHandler(repo)
	r.Get("/oauth/antigravity/callback", oauthPublic.HandleAntigravityCallback)
	r.Get("/oauth/gemini-cli/callback", oauthPublic.HandleGeminiCLICallback)


	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.Write([]byte(`{"status":"ok"}`))
	})

	r.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			w.Write([]byte(`{"status":"ok","message":"hello"}`))
		}
	})

	// Profiling endpoints (pprof)
	r.HandleFunc("/debug/pprof/", pprof.Index)
	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)
	r.HandleFunc("/debug/pprof/*", pprof.Index)

	// API-key protected domain routes (includes /admin/health/reset so health
	// state cannot be reset by an unauthenticated caller — open-source hardening)
	r.Group(func(r chi.Router) {
		// Accept the dashboard session cookie as well as an API key: the OAuth
		// add-account endpoints live here and a password-logged-in browser holds
		// only the cookie. The engine routes in this group are unaffected — they
		// keep working with an API key exactly as before.
		r.Use(middleware.RequireApiKeyUnlessDisabled(repo, dashH.VerifySession,
			"/api/oauth/", "/api/dashboard/", "/api/translator/", "/translator/", "/usage/", "/api/usage/",
			// Process/host resource sample for the Console Log card. Like the
			// console-log routes above it, this is read by a password-logged-in
			// browser that holds only the httpOnly session cookie.
			"/api/system/",
			// Headroom lifecycle + dashboard proxy. The Token Saver page drives
			// start/stop/extras from the browser, which holds only the session
			// cookie — without this the status badge and every button 401.
			"/headroom/",
			// The dashboard's model pickers read the catalog to build combo and
			// provider model lists. A password-logged-in browser holds only the
			// httpOnly session cookie, so without these the picker calls the
			// catalog route, gets 401, and renders an empty list. Both spellings
			// are listed because RequestLogger strips a leading "/v1" before this
			// middleware ever sees the path. The route is read-only.
			"/v1/models", "/models"))

		// Health reset endpoint — dashboard calls this via headroom proxy
		r.Post("/admin/health/reset", func(w http.ResponseWriter, r *http.Request) {
			provider := r.URL.Query().Get("provider")
			model := r.URL.Query().Get("model")
			if err := repo.ResetProviderHealth(provider, model); err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
			json.MarshalWrite(w, map[string]string{"status": "ok"})
		})

		SetupRoutes(r, repo, ts)
	})
}
