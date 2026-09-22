# Changelog


## [Unreleased]

### 🚀 Features & Upstream Parity

**Generic OAuth Flows for 8 More Providers:**
- `internal/handlers/oauth/generic_oauth.go` — a spec table plus three handlers, replacing the "add a handler per provider" approach. VansRouter keeps every flow in one PROVIDERS map behind a catch-all route; this mirrors that shape. Flow kinds: `device_code` (poll until approved), `authorization_code` (consent URL + code exchange), `import_token` (paste a CLI credential).
- Providers covered: **kimi**, **grok-cli** (device code); **claude**, **iflow**, **gitlab**, **xai** (authorization code); **cursor**, **codex** (import). Each spec is transcribed from VansRouter's `open-sse/providers/registry/*.js` `oauth:` block.
- `internal/handlers/oauth/generic_flow.go` — PKCE verifier store shared by the code flows, one-shot per state.
- `internal/providers/oauth.go` — added the missing client ids (kimi, grok-cli, claude, gitlab).
- `internal/handlers/dashboard/ui/index.html` — the OAuth panel dispatch is now a table rather than a growing ternary chain, plus one generic panel that discovers its shape from `GET /api/oauth/{provider}/flow`. That endpoint exists so the panel can learn the flow kind **without** starting one — probing `/authorize` would mint a device code, which is a one-shot resource.
- GitLab needs an operator-registered OAuth application, so `clientId`/`clientSecret` are accepted on both authorize and exchange, and the panel shows those fields when `flow` reports `needsApp`.

Deliberately **not** covered, because their flows do not fit the generic shapes — a spec they cannot execute would be worse than none:
- **qoder** — no device-code endpoint; the PKCE verifier, nonce and machine id are generated locally and become the poll identity
- **kilocode** — POST to initiate then GET `{pollUrl}/{deviceCode}`, with the state in the HTTP status (202/403/410/200)
- **zed** — not OAuth at all (RSA keypair callback, `<user_id> <token>` auth header)

These keep the generic paste-credential form until they get a dedicated handler.

Two providers were also **mislabeled as `oauth`** and are corrected to match VansRouter: `xiaomi-mimo` is an API-key provider, `mimo-free` needs no credential at all.

Verified against live upstreams: kimi and grok-cli return real device codes and user codes, and an unapproved poll returns `{pending:true,error:"authorization_pending"}` for both. Claude, iflow, xai and GitLab build consent URLs with the right host, scopes, PKCE and extra params; GitLab also honours an operator-supplied base URL and client id. In the dashboard, kimi shows the device panel with a live user code, cursor shows the token/machine-id form, and GitLab shows the app-registration fields.

**Four Providers Added, Claude-Format Upstreams Supported:**
- `internal/providers/category.go` + `registry_models.go` + `aliases.go` — registered **AgentRouter** (freeTier, `$200` signup credits), **ZCode** (Z.ai GLM coding plan), **DeepInfra**, and **ZenMux**, with their model lists and aliases (`ar`, `zc`).
- `internal/providers/providers.go` — new `FormatClaude` marker for providers whose upstream speaks the Anthropic Messages API without being Anthropic. AgentRouter and ZCode use it: their request body is converted to Messages format and the response translated back, but they do **not** inherit Anthropic OAuth or Claude-Code cloaking, since they are not Anthropic's host.
- `internal/handlers/chat/fallback.go` — `claudeNative` now recognises a Claude-format provider, and the new `upstreamClaudeWanted` helper decides response translation. Previously only a provider literally named `claude`/`anthropic` with Anthropic's own baseURL could take this path.

**Kimchi Monthly Quota Auto-Reactivation:**
- `internal/quota/reactivation.go` — new periodic sweep that restores accounts parked as `quota_exhausted` once their cooldown has passed. Kimchi's free budget refills monthly, not on a timer, so nothing in the request path brings such an account back: it is not serving traffic, so it never gets a chance to succeed. Ported from VansRouter's `src/sse/services/kimchiQuotaReactivation.js`.
- `internal/db/accounts.go` — `ReactivateConnection` sets `isActive=1` alongside the blob; clearing only the blob would leave the account out of the active-connection query.
- `cmd/9router-go/main.go` — starts the sweep (30s after boot, then every 30 minutes).

Verified against a live server: an expired account went `isActive=1` / `testStatus=active` with the cooldown and error cleared and its credential intact, while an account still cooling stayed parked.

Test fix: `models_orphan_test.go` used `"agentrouter"` as its example of "neither a KnownProvider nor an alias". Registering that provider invalidated the premise, so the fixture now uses a deliberately fictional name.

**Provider Circuit Breaker and Account Semaphore:**
- `internal/resilience/circuitbreaker.go` — new in-memory breaker per provider with CLOSED → DEGRADED → OPEN → HALF_OPEN states, exponential backoff on repeated failed probes (capped), and VansRouter's failure model: only 5xx/408/timeout count. **429 is deliberately excluded** — it is per-account rate limiting, and counting it would open the breaker for every healthy account once a few are throttled. Ported from `open-sse/utils/circuitBreaker.js`.
- `internal/resilience/semaphore.go` — new per-account FIFO concurrency limiter keyed `provider:account:proxy`, with a bounded queue, wait timeout, context cancellation, and `Block` for holding an account after a 429. Ported from `open-sse/services/accountSemaphore.js`. A connection sets `providerSpecificData.maxConcurrency` to override; `0` disables it.
- `internal/handlers/chat/resilience_wiring.go` — connects both to the request path: acquire a slot per attempt, feed each outcome back to the breaker, block the account on 429.
- `internal/handlers/chat/fallback.go` — the breaker is checked **before** the pinned-connection path, which returns early and would otherwise let a pinned request bypass an open circuit. A client-cancelled request is not counted as a provider failure.
- `GET /debug/resilience` reports breaker and semaphore state; `POST /debug/resilience/reset` closes every breaker so an operator who has fixed an upstream need not wait out the backoff.

Verified against a live server: seven failures against a dead upstream opened the breaker (`OPEN`, `retryAfterMs≈30s`), the next request was refused in under 1 ms with `provider breaker open, skipping` and no upstream dial, reset returned it to `CLOSED`, and three concurrent requests to a `maxConcurrency=1` account reported `running=1 queued=2`.

Test isolation: breakers and semaphores are process-global, so `setupChatTestDB` now resets them per test — otherwise a test that drives a provider into failure left an OPEN breaker for the next test using the same provider.

**Per-API-Key ACL Enforced (providers / combos / kinds):**
- `internal/models/types.go` — `APIKey` gained `AllowedProviders`, `AllowedCombos`, `AllowedKinds` plus `IsProviderAllowed` / `IsComboAllowed` / `IsKindAllowed`. Semantics match VansRouter: `nil` = all allowed, `[]` = none, `[x]` = only x.
- `internal/db/repos.go` — `GetApiKeyByKey` now reads the three ACL columns and decodes them via `decodeACLList`, which keeps NULL as `nil` (unrestricted) and a stored `[]` as an empty non-nil slice (nothing allowed). A malformed column falls back to `nil` so a broken ACL cannot lock an operator out.
- `internal/db/dashboard.go` — `GetAllApiKeys` surfaces the ACL columns; new `UpdateApiKeyACL` writes them.
- `internal/handlers/chat/acl.go` — enforcement helpers. A combo is checked against `allowedCombos` **and** against every provider it can route to, so a provider restriction cannot be bypassed by fanning out through a combo.
- `internal/handlers/chat/chat.go` — `HandleChatCompletions` and `HandleMessages` authorise before any upstream work.
- `internal/handlers/media/media.go` — `forwardMediaRequest` (all 11 media routes) plus `HandleEmbeddings` authorise, with the kind derived from the endpoint.
- `internal/handlers/dashboard/dashboard.go` — `PUT /api/dashboard/keys/{id}/acl`. A field absent from the body is left unchanged; an explicit `null` clears the restriction.
- `internal/middleware/auth.go` — `WithApiKeyForTest` so handler packages can exercise per-key authorisation directly.

Keys created before this change have NULL ACL columns and remain unrestricted — verified end-to-end.

**Freebuff & Devin CLI Added to Provider Catalog:**
- `internal/providers/category.go` — Registered `devin-cli` (no-auth, Devin CLI via ACP/stdio) and `freebuff` (OAuth, free ad-supported Codebuff agent) in the `free` category. Both had configs and executors wired but were absent from the catalog, so they never rendered in the providers grid.
- `internal/providers/registry_models.go` — Added Freebuff's model list under both `fb` and `freebuff` keys (10 entries, mirroring the CLI's `FREEBUFF_ROOT_AGENT_ID_BY_MODEL` plus the executor's extra agent mappings).
- `internal/handlers/oauth/freebuff_oauth.go` — New fingerprint device-flow login (ported from VansRouter's `src/lib/oauth/providers/freebuff.js`): `POST freebuff.com/api/auth/cli/code` → browser sign-in → `GET /api/auth/cli/status` polled until `user.authToken` lands. Stateless server-side, so it works headless.
- `internal/handlers/router.go` — Routes `GET /api/oauth/freebuff/authorize` and `POST /api/oauth/freebuff/exchange`.
- `internal/handlers/dashboard/ui/index.html` — Freebuff sign-in panel with automatic polling; freebuff panel wired into the provider-detail OAuth dispatch.

## [v1.8.17] — 2026-09-18

### 🚀 Features & Upstream Parity

**Claude OAuth Subscription (`sk-ant-oat`) Support End-to-End (PR #13):**
- Contributed by **@rezhajulio** ([#13](https://github.com/luqman-v1/9router-go/pull/13)) — Special thanks for bringing full Claude Pro/Max subscription parity from the dashboard to the native Go proxy!
- `internal/handlers/chat/fallback.go` — Automatic header switching to `Authorization: Bearer` and appending `?beta=true` for Claude OAuth credentials (`sk-ant-oat` or `accessToken`), supporting direct Anthropic API as well as Edge Relay proxy pools.
- `internal/handlers/chat/claude_cloaking.go` — Injected official `x-anthropic-billing-header` into `system[0]`, deterministic account `metadata.user_id`, client tool name obfuscation with `_ide` suffix, and decoy tools (`CCDecoyTools`) preventing false HTTP 429 anti-abuse rate limits.
- `internal/proxy/executor/claude_decloak.go` — Streaming and non-streaming response decloaker restoring original tool names and translating decoy tool invocations into clean text blocks.
- `internal/proxy/executor/providers.go` — Added `sanitizeToolUseID` to rewrite foreign/Gemini tool IDs deterministically to Anthropic-compliant `toolu_<sha256>`.
- `internal/tokensaver/prompts.go` — Added `InjectSystemPromptClaude` for format-aware system prompt injection at top-level `system`.

**Antigravity Zen Free-Tier Tool Quartet Renaming (PR #12):**
- Contributed by **@yxxrn** ([#12](https://github.com/luqman-v1/9router-go/pull/12)) — Special thanks for identifying the exact upstream fingerprinting gate and eliminating Claude Code 403/500 errors!
- `internal/translator/fingerprint.go` — Implemented `ConcealFingerprintTools` to rename uppercase tool quartet variants from Claude Code CLI (`Bash`, `Glob`, `Grep`, `Read`) to canonical lowercase (`bash`, `glob`, `grep`, `read`), eliminate duplicates (preventing upstream HTTP 500), retarget `tool_choice`, and restore original tool names in response payloads via `RestoreToolNamesInPayload` / `RestoreToolNamesInSSE`.
- `internal/proxy/executor/toolname_writer.go` — Embedded `toolNameRestoringWriter` on responses ensuring client tools are seamlessly restored across both streaming and non-streaming responses.

### 🐛 Bug Fixes & Improvements

**CommandCode CLI User-Agent & Schema Wrapping (PR #14, fixes #9):**
- Reported by **@jhonoryza** ([#9](https://github.com/luqman-v1/9router-go/issues/9)) — Thank you for reporting the Cloudflare challenge error!
- `internal/providers/providers.go` & `internal/proxy/executor/providers.go` — Added official `User-Agent: commandcode/0.25.7 (cli)` and `x-command-code-version: 0.25.7`, eliminating Cloudflare WAF bot-challenge intercepts (HTTP 403 `Attention Required!`).
- `internal/proxy/executor/providers.go` — Implemented `buildCommandcodeBody` wrapping OpenAI payloads into `{threadId, memory, config, params}` schema required by CommandCode's `/alpha/generate` endpoint.
- `internal/handlers/chat/fallback.go` & `internal/proxy/proxy.go` — Enhanced error parsing in `extractErrorText` and `UpstreamError.Error()` to summarize Cloudflare challenge pages cleanly without dumping raw HTML.

**Responses API (`POST /v1/responses`) Public Provider Fallback & String Input (PR #15, fixes #10):**
- Reported by **@pankaj-raikar** ([#10](https://github.com/luqman-v1/9router-go/issues/10)) — Thank you for the detailed reproduction report!
- `internal/handlers/media/media.go` — Added automatic fallback for public/free-tier providers (`DefaultAPIKey: "public"`) in `forwardMediaRequest`, eliminating `"no active connections for provider: opencode"` when no SQLite connection is seeded.
- `internal/handlers/media/media.go` & `internal/handlers/chat/resolution.go` — Routed `opencode`, `opencode-go`, and `antigravity/muse-spark-*` models on `/responses` directly to `ForwardOpencode` so session tracking, request headers, and tool-name concealing work out of the box.
- `internal/proxy/executor/transform.go` — Updated `buildResponsesBody` to support both string inputs (`"input": "Say hello"`) and array inputs (`Input []any`), normalizing string prompts into valid Responses message items.

## [v1.8.16] — 2026-09-17

### 🐛 Upstream Parity & Bug Fixes

**Active Provider Connections & Capabilities on `/v1/models` and `/api/models` (PR #8, fixes #7):**
- `internal/providers/registry_models.go` — Added comprehensive upstream provider models registry mapped from 128 provider definitions (`decolua/9router` parity) with `GetProviderModels()`.
- `internal/providers/capabilities.go` — Implemented `CapabilitiesDetail` and `GetCapabilitiesDetailForModel()`, outputting full multimodal flags (`vision`, `pdf`, `audioInput`, `videoInput`, `imageOutput`, `audioOutput`), `thinkingCanDisable`, and dual token limits (`contextWindows` and `contextWindow`).
- `internal/handlers/chat/chat.go` — Replaced empty `/v1/models` responses by dynamically aggregating models for active connections (`isActive = 1`), honoring custom prefixes and connection-enabled models. Custom models from deactivated connections (`disabledProviders`) are automatically excluded, and clean static catalogs are returned when no database connections are configured.
- `internal/handlers/router.go` — Mounted `GET /api/models` and `GET /api/models/*` serving both `"data"` and `"models"` top-level keys for universal compatibility across OpenAI-compliant IDE clients and Next.js web dashboards.
- `internal/handlers/chat/models_provider_test.go` — Added regression tests verifying model discovery for active Codex (`cx`) connections, fallback registry, and single-model lookups.

## [v1.8.15] — 2026-09-17

### 🚀 Features & Upstream Parity

**Claude Messages to OpenAI Response Translation for Zen / Union-Alpha (PR #4099, #4111):**
- `internal/translator/claude_response.go` — Added on-the-fly streaming (`TranslateClaudeChunkToOpenAI`) and non-streaming (`TranslateClaudeResponseToOpenAI`) response translation engines. Transforms Claude Messages SSE events (`content_block_delta`, `thinking_delta`, `tool_use`, `input_json_delta`, `message_delta`, `message_stop`) into standard OpenAI chunks (`choices[0].delta.content`, `reasoning_content`, `tool_calls`) so that client harnesses (e.g. omp, Cursor, Cline) receive native responses.
- `internal/proxy/executor/claude_messages.go` — Added dedicated streaming handler (`handleClaudeMessagesStream`) and non-streaming handler (`handleClaudeMessagesNonStream`) wired into `ForwardOpencode` and `ForwardOpencodeGo` for `union-alpha` routes.
- `internal/proxy/opencode.go` — Updated Antigravity Zen headers to comply with upstream PR #4111 (`User-Agent: antigravity/1.18.31 ai-sdk/provider-utils/4.0.46 runtime/bun/1.3.14`, `x-antigravity-client: cli`, dynamic 40-character hex project IDs `GenerateOpenCodeProjectID()`, and `x-api-key: public`).

**Anthropic Tools & Messages Schema Normalization:**
- `internal/proxy/executor/providers.go` — Added `convertOpenAIToolsToClaude`, `ensureMessagesMaxTokens`, `extractClaudeSystemPrompt`, and `convertOpenAIMessagesToClaude` to convert incoming OpenAI tool definitions (`type: "function"`) into Claude tools (`{name, description, input_schema}`), normalize `tool_choice`, and merge adjacent same-role messages for compliant Anthropic payload delivery.
- `internal/proxy/sse.go` — Expanded terminal detection buffer to 64 bytes and added recognition for Anthropic terminal signals (`"stop_reason":` non-null and `"message_stop"`) to eliminate premature `finish_reason: "network_error"` synthesis at clean stream EOF.

### 🐛 Bug Fixes & Resilience

**Google RPC `quotaResetDelay` Automatic Duration Locking with Deadlock Prevention:**
- `internal/handlers/chat/fallback.go` — Implemented `extractResetDuration` to parse Google RPC ErrorInfo metadata `quotaResetDelay` (e.g. `"1h12m28.109534319s"`) and text patterns (`"Resets in XhYmZs."`). Enforces safety bounds (min 5s, hard cap at 2 hours) to avoid perpetual lockouts or deadlocks.
- `internal/handlers/chat/combo.go` — Updated `comboLockRetryable` to use the parsed reset duration for connection and model locks instead of falling back to 8s exponential backoff.
- `internal/handlers/chat/antigravity_quota.go` & `internal/handlers/chat/gemini_handler.go` — Added `BlockAntigravityModelUntil` to cache exhausted model quotas and canonical synonyms (`gemini-3.8-flash-tiered`) in RAM until the verified reset timestamp, preventing continuous 429 spam to Google upstream while automatically unblocking the moment reset time is reached.

## [v1.8.14] — 2026-09-17

### 🐛 Upstream Parity & Bug Fixes

**Muse Spark 1.3 FreeTier Authorization & Session Normalization (PR #4105, #4061, #4062):**
- `internal/proxy/antigravity.go` — Updated Antigravity OpenCode user-agent default to `antigravity/1.18.31` and implemented descending canonical 30-character session (`ses_` + 12 hex + 14 Base62) and message (`msg_` + 12 hex + 14 Base62) ID generators. This resolves HTTP 403 `FreeTierError` when routing requests to `oc/muse-spark-1.3-contributor-free`.
- `internal/proxy/executor/providers.go` — Added `normalizeMuseSparkResponsesBody` to strip prior multi-turn reasoning content and encrypted content blocks that trigger HTTP 400 parameter errors, while explicitly normalizing `tool_choice` to `"auto"` for Muse Spark 1.3.

**Missing `tool_call_id` FIFO Repair (PR #4090):**
- `internal/handlers/chat/tool_repair.go` & `internal/translator/request.go` — Added automatic repairing for client requests where `role: "tool"` or `function_call_output` messages omit `tool_call_id`. Uses FIFO pairing with un-paired assistant tool calls or mints deterministic call IDs to prevent strict upstreams (OpenAI, DeepSeek, Antigravity) from failing with HTTP 400.
- `internal/handlers/chat/chat.go`, `internal/handlers/chat/combo.go`, `internal/handlers/chat/fallback.go`, & `internal/proxy/executor/transform.go` — Integrated tool call repair across OpenAI chat completions, combo routing, and account fallback handlers.

**Stream Interruption Terminal Synthesis (PR #4079):**
- `internal/proxy/sse.go` — Implemented terminal frame synthesis (`finish_reason: "network_error"` followed by `data: [DONE]\n\n`) when upstream SSE connections terminate abruptly at EOF before emitting a terminal frame, preventing IDE client hangs and errors in Cline/Pi.

**Claude Tool Result Image Hoisting (PR #4083):**
- `internal/translator/request.go` — Converted base64 image blocks embedded inside Claude `tool_result` into follow-up user messages with `[Image from tool result <id>]` and OpenAI `image_url` blocks, allowing vision models to inspect tool screenshot outputs without violating text-only tool-role schema constraints.

**Grok CLI Tool Result Neutral Placeholder (PR #4109):**
- `internal/proxy/grokcli.go` & `internal/mitm/handlers/kiro.go` — Replaced placeholder `"continue"` with neutral `"Tool results provided."` for tool-result user turns in Kiro request payloads to prevent assistant hallucination loops.

**Thinking Variant Route Stripping & Union Alpha Messages Route (PR #4084, #4099):**
- `internal/proxy/executor/providers.go` — Stripped model suffix before checking `isOpencodeResponsesModel`, enabling models like `gpt-5.6-luna(high)` to correctly route to `/responses`.
- `internal/proxy/executor/providers.go` — Routed Antigravity Zen `union-alpha` directly to `/zen/v1/messages` with `anthropic-version: 2023-06-01`.
- `internal/providers/capabilities.go` — Registered model capabilities for `union-alpha`, `deepseek-v4.1-flash`, and `deepseek-flash`.

## [v1.8.13] — 2026-09-16

### 🐛 Bug Fixes & Parity

**Grok CLI Responses Endpoint Fix (PR #4, fixes #2):**
- `internal/providers/providers.go` — Updated `grok-cli` `BaseURL` from bare root `https://cli-chat-proxy.grok.com` to `https://cli-chat-proxy.grok.com/v1/responses`, resolving HTTP 404 HTML edge errors when calling `gcli/*` models (`grok-4.5`, `grok-4.6`).
- `internal/proxy/grokcli.go` — Added auto-normalization in `ForwardGrokCLI` so that if `cfg.BaseURL` is empty or lacks the `/v1/responses` path, it automatically normalizes to `/v1/responses`.
- `internal/handlers/chat/grokcli_handler_test.go` — Added regression tests for grok-cli BaseURL and endpoint routing.

**Custom Provider Nodes Prefix Priority & Model Mapping (PR #5, fixes #3):**
- `internal/handlers/chat/resolution.go` — Prioritized custom `providerNode` prefix resolution (`h.resolvePrefixProvider(prefix, model)`) before checking built-in provider aliases (`resolveProviderAlias(prefix)`). This prevents short prefixes like `oa` or `cc` from being shadowed by `openai` or `claude`, eliminating false 502 "no active connections for provider: openai" failures when the custom node has active connections.
- `internal/handlers/chat/resolution.go` — Added fallback so that when the alias-resolved provider has no active connections, unresolved prefix queries report the matching custom `providerNode.id` instead of falsely blaming the shadowed built-in provider.
- `internal/handlers/chat/resolution.go` — Added defensive nil checks for `h.Repo` across all model and combo resolution helpers.
- `internal/db/repos.go` — Added `GetProviderNodePrefixMap()` to map internal row IDs (`openai-compatible-chat-0489...`) to user-configured prefixes (e.g. `nara`, `orca`, `oa`).
- `internal/db/repos.go` — Updated `GetCustomModels()` with fallback parsing from keys (`<providerAlias>|<modelId>|<kind>`) and removed restrictive `type == "llm"` filtering, exposing all custom chat and completion models.
- `internal/handlers/chat/chat.go` — In `HandleModels` (`GET /v1/models`) and `HandleModelLookup` (`GET /v1/models/*`), mapped `cm.ProviderAlias` through the prefix map so models are published under their clean user-configured prefix (e.g. `nara/glm-5.3`) with `owned_by` set to the prefix rather than leaking internal database row IDs.
- `internal/handlers/chat/resolution_test.go`, `internal/handlers/chat/chat_v065_test.go`, & `internal/db/repos_test.go` — Added comprehensive unit and regression tests for custom prefix priority, fallback error reporting, prefix map caching, key-fallback parsing, and `/v1/models` prefix output.

## [v1.8.12] — 2026-09-16

### 🚀 Features & Provider Additions

**Freebuff Provider Integration (`fb`):**
- `internal/proxy/executor/freebuff.go` — Added native Freebuff executor supporting `https://www.codebuff.com/api/v1/chat/completions` with 1-hour session token lifecycle caching, agent run tracking (`/api/v1/agent-runs`), Buffy system prompt marker injection, and `end_turn` tool injection for sub-agent orchestration.
- `internal/providers/providers.go` & `internal/providers/aliases.go` — Registered provider `freebuff` and alias `fb`.

**Provider Connection Routing Strategies:**
- `internal/db/settings.go` & `internal/handlers/chat/connections.go` — Added configurable multi-connection routing strategies per provider: `sticky` (with configurable `stickyLimit`), `round-robin`, `random`, and `none`.

**Model Capabilities & Limits:**
- `internal/providers/capabilities.go` — Added capabilities for Upstage Solar Pro (`*solar-pro*`) and LongCat (`*longcat*`) with reasoning, tools, 200,000 token context window, and 32,000 max output tokens.

### 🐛 Bug Fixes & Resiliency

**Cline & Clinepass OAuth Refresh Overhaul:**
- `internal/proxy/oauth/cline.go` — Registered `clinepass` alongside `cline` in the OAuth registry, resolving issues where ClinePass connections fell back to incompatible standard form-urlencoded OAuth refresh.
- Migrated token refresh endpoint from deprecated `/v1/auth/refresh` (which returned 401 "Please make sure you're using the latest version of Cline") to active upstream `/api/v1/auth/refresh`.
- Emulated full Cline CLI identity headers on refresh (`User-Agent: Cline/3.0.61`, `X-CLIENT-TYPE: cline-cli`, `X-CLIENT-VERSION: 3.0.61`, `X-CORE-VERSION: 3.0.61`, `X-PLATFORM: cli`).
- Added token rotation support: propagated rotated `refreshToken` to SQLite database across `BuildConnectionUpdate` and `forceRefreshOAuthToken`.
- `internal/handlers/chat/fallback.go` — Ensured `refreshedKey` is normalized with `NormalizeProviderToken` on reactive 401 retries so WorkOS prefix (`workos:`) is preserved.

**Zero-Sleep Failover & Canonical Model Locking:**
- `internal/handlers/chat/combo.go` — Removed synchronous blocking sleeps (`time.Sleep`) during combo failover loops on transient errors (502, 503, 504), enabling immediate non-blocking failover to backup models/connections without stalling client turns.
- `internal/handlers/chat/connections.go` & `internal/handlers/chat/combo.go` — Added `canonicalLockModel(provider, model)` to atomically lock shared tier pools across connections (e.g., Antigravity `gemini-3.8-flash-low`/`high` map to canonical lock key `gemini-3.8-flash-tiered`). Prevents split-lock failure loops across shared tier accounts.
- `internal/providers/errorclassify.go` — Expanded error classification to trigger backoff for `resource_exhausted`, `model_capacity_exhausted`, and HTTP 502/503/504 status codes.

**Stream Telemetry & Connection Safety:**
- `internal/proxy/sse.go` — Added rolling 16-byte tail buffer in `SSECopy` to safely detect `[DONE]` across chunk boundaries and cleanly terminate SSE streams without hanging on keep-alive connections.
- `internal/handlers/chat/fallback.go` — Guaranteed `usageHistory` database logging on completed streams even when the client disconnects at stream end.
- Advertised `context_window` in `/v1/models` and `/v1/models/info` dynamically from capabilities.

## [v1.8.11] — 2026-09-12

### 🐛 Bug Fixes & Parity — Upstream PR Porting

**Gemini Multiple System Messages Preservation (PR #3973):**
- `internal/translator/gemini.go` — Preserved all `role: "system"` messages in `req.SystemInstruction.Parts` rather than overwriting earlier instructions with the last turn, ensuring all system prompts and developer directives reach Gemini models.

**Antigravity Thinking Budget & Output Tokens Guard (PR #3981):**
- `internal/translator/gemini.go` & `internal/translator/antigravity.go` — Guarded `maxOutputTokens > thinkingBudget` across `TranslateOpenAIToGemini` and `hardenAntigravityRequest`, preventing HTTP 400 `INVALID_ARGUMENT: max_tokens must be greater than thinking.budget_tokens` and erroneous connection locks on reasoning models.
- Added support for `max_completion_tokens`, `thinking.budget_tokens`, and `thinking_budget`.

**Claude Document Block Support for Antigravity & OpenAI (PR #3968):**
- `internal/translator/request.go` & `internal/translator/types.go` — Added `document` block handling in `TranslateClaudeToOpenAI` and `OpenAIFile` struct in `OpenAIContentBlock`, converting base64 PDF documents into OpenAI file format that flows into Gemini/Antigravity `inlineData`.

**Client Cancellation Tracing & Stream Telemetry:**
- `internal/handlers/chat/fallback.go` — Differentiated client cancellations (`errors.Is(fwdErr, context.Canceled)` or `ctx.Err() != nil`) from true upstream failures, logging `INF [fallback] client canceled request` and recording status `499` in traces instead of raising false `WRN upstream failed` alarms.
- `internal/handlers/chat/gemini_handler.go` — Added accurate `totalBytesWritten` accumulation for OpenAI format streaming branches and terminal `[DONE]` frame.
- `internal/handlers/chat/fallback.go` & `internal/handlers/chat/combo.go` — Refactored hardcoded HTTP status codes to standard `net/http` constants (`http.StatusOK`, `StatusClientClosedRequest`).

**Memory Leak Protections & High-Traffic Concurrency:**
- `internal/translator/usage.go` & `internal/translator/response.go` — Added `pendingFragment` struct with `createdAt` timestamps and 10-minute TTL pruning in `pruneStaleStatesLocked()`. Prevents abandoned fragmented SSE streams from accumulating in the global `pendingJSON` map.
- `internal/handlers/chat/connections.go` — Pooled and cached `*http.Client` and `*http.Transport` instances by proxy URL using `sync.RWMutex`. Eliminates per-request transport allocations, enables TCP keep-alive reuse across proxy pool traffic, and prevents socket/goroutine exhaustion.

**Live Profiling & Diagnostics:**
- `internal/handlers/router.go` — Mounted Go standard `net/http/pprof` endpoints (`/debug/pprof/`, `/debug/pprof/heap`, `/debug/pprof/goroutine`, `/debug/pprof/profile`) for real-time heap and concurrency inspection.

**Documentation & Client Guides:**
- `README.md` — Added comprehensive pre-built binary download links (macOS, Linux, Windows), one-liner install script, Docker setup, and configuration examples for Claude Code, `omp`, and Cursor/Cline.

## [v1.8.10] — 2026-09-11

### ✨ Features & Parity — Next.js v0.5.75 Sync (27 Commits)

**Gemini & Antigravity Content Normalization:**
- `internal/translator/gemini.go` & `internal/translator/antigravity.go` — Added `NormalizeGeminiContents` merging adjacent same-role messages and filtering out empty parts (parity with `#e7b5f09`).
- `internal/handlers/chat/antigravity_quota.go` — Added Antigravity weekly quota tracking (`gemini_weekly`, `claude_gpt_weekly`) and free-tier handling via `retrieveUserQuotaSummary`, caching summaries and reconciling against exhausted model families (#3892).

**Kiro Routing & Wire Payload Cleanup:**
- `internal/providers/providers.go` & `internal/proxy/grokcli.go` — Routed Kiro through Amazon Q first (`https://q.us-east-1.amazonaws.com/generateAssistantResponse`), deprecated legacy runtime path to avoid 400 `REQUEST_BODY_INVALID` (#3776).
- Injected `x-amz-sso-bearer`, `x-amzn-kiro-agent-mode: spec`, and `x-amzn-codewhisperer-machine-id: kiro-desktop` headers.
- `internal/proxy/grokcli.go` & `internal/mitm/handlers/kiro.go` — Stripped top-level `systemPrompt`, `agentMode`, and `conversationState` continuation fields (`agentContinuationId`, `agentTaskType`) that modern Kiro gateways reject with 400 (#1892ed7).

**Opencode-Go Catalog Refresh & Responses API:**
- `internal/proxy/executor/providers.go` — Routed `grok-4.6` and `gpt-5.6-luna` on `opencode-go` to the `/zen/go/v1/responses` endpoint alongside `muse-spark`.
- `internal/providers/capabilities.go` & `internal/proxy/executor/providers.go` — Registered newly published Go models (`deepseek-flash`, `glm-5.3`, `kimi-k3`, `longcat-2.0`, `qwen3.8-max`, `qwen3.8-flash`, `hy4-preview`, `hy3`), with `deepseek-flash` (DeepSeek V4.1 Flash) priority and Qwen 3.8 models in `opencodeGoMessagesModels`.

**Codex CLI Bump & Unicode Schema Sanitization:**
- `internal/providers/providers.go` — Updated Codex CLI User-Agent to `codex_cli_rs/0.154.0` (parity with `#a7047a0`).
- `internal/proxy/executor/transform.go` — Added `StripCodexUnsupportedPatterns` to sanitize `\p{...}` / `\P{...}` Unicode property escapes in tool parameters that Codex's `/responses` validator rejects with HTTP 400 (#3922).
- `internal/providers/capabilities.go` — Added Codex image models (`gpt-image-2.5`, `gpt-image-2.5-flare`, `gpt-image-2.5-sunburst`, `gpt-image-2`, `gpt-image-1.5`) with `ImageOutput` capability and `*gpt-image*` pattern match.

**Claude Cache Budget & Single-Object Turns:**
- `internal/translator/request.go` — Enforced Anthropic 4-marker `cache_control` budget in `AnchorClaudeCache`: pins head anchors (last system block, last non-deferred tool) and keeps at most 2 tail message markers, trimming earlier ones (#8a81085).
- `internal/translator/request.go` — Supported single-object content turns (`content: {type: "text", ...}`) across `convertClaudeMessage`, `SanitizeClaudePassthrough`, and `AnchorClaudeCache`.
- `internal/handlers/chat/chat.go` — Scoped Claude tool type defaulting to gateways declaring `requireClaudeToolType` (MiniMax / MiniMax-CN), avoiding 400 `unknown variant custom` on DeepSeek Anthropic endpoint (#3905, #45ec1d3).

**Cline Envelope Unwrapping & Token Refresh:**
- `internal/translator/response.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go` — Added `UnwrapClineEnvelope` unwrapping `{"success":true,"data":{...}}` for non-streaming completions on `cline` and `clinepass` (#122f23ee).
- `internal/providers/oauth.go` — Registered `clinepass` in OAuth token refresh config.
- `internal/providers/capabilities.go` — Replaced `deepseek-v4-flash` with `deepseek-v4.1-flash` for `codebuddy-cn` (#807553e).

**Connection Health & Security:**
- `internal/db/accounts.go` — Added `ResetConnectionHealthState` clearing `modelLock_*`, `errorCode`, `rateLimitedUntil`, and resetting `backoffLevel = 0` upon connection activation (#3830).
- `internal/handlers/media/media.go` — Rejected path-escaping characters (`..`, `/`, `\`) in `HandleVideoGet` (#da6aa901).

**Responses API Stream & Tool Calling Fixes:**
- `internal/proxy/executor/stream.go` — Fixed tool call argument duplication (`InputValidationError` caused by duplicate concatenated JSON bodies such as `{"q":"*.yaml"}{"q":"*.yaml"}`) on Responses API streams by tracking `ArgsEmitted` during `response.function_call_arguments.delta` and suppressing redundant full argument re-emission on `response.output_item.done`.
- `internal/handlers/chat/gemini_handler.go` — Ensured terminal `data: [DONE]\n\n` SSE frame is emitted upon stream completion for OpenAI-format streaming clients and cleaned up redundant newlines.
- `internal/handlers/chat/live_e2e_test.go` — Added live non-mock E2E tests for Antigravity (`gemini-2.5-flash`, `gemini-3.8-flash-high`), DeepSeek, and OpenCode covering parallel multi-tool calls, multi-turn tool execution, streaming, and weekly quota retrieval.

## [v1.8.9] — 2026-09-06

### ✨ Features & Parity — Next.js v0.5.69 Sync (19 Commits)

**Gemini & Antigravity ThoughtSignatureStore:**
- `internal/translator/thought_signature_store.go` — Added thread-safe in-memory LRU store (capacity: 2,000 entries, 1-hour memory TTL) scoped by `sessionId` + `toolCallId`. Caches and replays thought signatures across turns to prevent corrupted thought signature errors during multi-turn reasoning conversations.
- `internal/translator/gemini.go` & `antigravity.go` — On parallel function calls, only the first call receives the signature/fallback, leaving sibling calls unsigned per Google Gemini 3+ specification.

**New Models & Capabilities Sync:**
- `internal/providers/capabilities.go` — Registered **`gpt-6-astra`** (Vision, Reasoning, Search, Tools; 272K window / 128K max output) and added `*gpt-6*` pattern match.
- Registered GPT-5.6 image aliases: `gpt-5.6-sol-image`, `gpt-5.6-terra-image`, and `gpt-5.6-luna-image` with `ImageOutput` capability.
- Qoder capabilities catalog refresh (`ultimate`, `performance`, `gmodel`, `gfmodel`, `qmodel_38max`, etc.).
- CodeBuddy-CN capabilities updated (`glm-5.2` vision enabled).

**Anti-Abuse Google Token Refresh (Antigravity):**
- `internal/handlers/chat/antigravity_project.go` — Configurable `ONBOARD_MAX_ATTEMPTS` (default 2, down from 5) and `ONBOARD_RETRY_DELAY_MS` (default 12s) to prevent Google account rate-limit blocks during multi-account refresh (#3813).

**Anthropic-Beta Header Forwarding & Effort Normalization:**
- `internal/handlers/chat/fallback.go` — Automatically injects `Anthropic-Beta: prompt-caching-scope-2026-01-05, context-management-2025-06-27` for `anthropic-compatible-*` nodes serving Claude models (#3797).
- `internal/translator/request.go` & `types.go` — Normalizes Claude adaptive auto effort (`output_config.effort="auto"` and `"xhigh"` $\to$ `"high"`) (#3792).

**OpenCode-Go Executor & Responses Parallel Tool Calls Fixes:**
- `internal/proxy/executor/providers.go` — Added `deriveOpencodeSession` generating stable `x-opencode-session: ses_<32hex>` headers for all `opencode-go` requests, with fallback and client tool isolation (#3800).
- Routed `muse-spark-1.2-contributor` and `muse-spark-1.3-contributor` on `opencode-go` to the `/responses` endpoint (#3819, #3820).
- `internal/proxy/executor/stream.go` — Fixed parallel tool calls argument collision on Responses API SSE stream by indexing events via `item_id`. Emits arguments from `response.output_item.done` when upstreams send arguments on item completion without deltas.
- `internal/proxy/executor/stream.go` — Fixed `handleCodexStream` SSE stream truncation and disconnects on `muse-spark-1.3` (and 1.2) by switching to `proxy.ScanStream` (up to 10MB buffered scanner), preventing line fragmentation when handling large (>3KB) encrypted reasoning payloads across TCP packet boundaries.
- `internal/proxy/executor/stream.go` — Added support for `response.reasoning_summary_text.delta`, `response.reasoning_text.delta`, and `response.thought.delta` emitting `reasoning_content` delta chunks for thinking models.
- `internal/proxy/sse.go` — Added `HeartbeatWriter` emitting periodic `: keep-alive\n\n` comments every 15 seconds during prolonged upstream reasoning phases (fixes #3796 stream stall timeouts on strict clients like Oh My Pi during deep thinking on `ag/gemini-3.8-flash*`).
- `internal/proxy/stall.go` — Added `NewStallReaderWithContext` binding client `ctx.Done()` directly to body closer, immediately freeing upstream sockets on client abort and eliminating Windows socket leaks (`CLOSE_WAIT`/`FIN_WAIT_1`).
**Database Path Configuration:**
- `internal/config/config.go` — Enhanced `DB_PATH` resolution to automatically detect `db/data.sqlite`, `data.sqlite`, or `9router.db` when pointed directly to a directory (e.g. `E:\project\database\9router`).
## [v1.8.8] — 2026-09-03

### ✨ E2E & Parity — Next.js v0.5.65 (31 commits)

**E2E Gemini 3.8 Flash High + tool calling (deterministic mocks):**
- `internal/handlers/chat/gemini38_e2e_test.go` — `gemini-3.8-flash-high` via Antigravity `2.11.0` non-stream + multi-turn + stream SSE `get_weather_ide` uncloaking, `prefixItems` cleaning, `thoughtSignature` backfill, `tool_calls` dedup. Ports `decolua/9router` `gemini-3.8-flash-medium/high/low` + `capabilities.go:*gemini-3.8*` + `antigravity.go:gemini-3.8-flash-tiered` + `proxy/gemini.go:2.11.0`.

**E2E Opencode muse-spark (deterministic mocks, no real network):**
- `internal/handlers/chat/opencode_mock_e2e_test.go` — `oc/muse-spark-1.2` & `1.3` via `Responses API /v1/responses` SSE `output_item.added` + `function_call_arguments.delta/done` aggregation, `reasoning max→xhigh`, `Vision:true` (`capabilities.go:124` pattern `*muse-spark*`), `image_url` preservation. Fixes routing `muse-spark-1.3` `500` → `200` (`providers.go:293` `Contains(muse-spark)` + `capabilities.go:124` `1.3`).

**Unit tests — now locking logic (previously untested):**
- `providers_v065_test.go` — `claude-cli/2.1.258` + full `Anthropic-Beta`, `ollama FetchURL https://ollama.com/api/web_fetch`, `gemini-3.8` caps, `muse-spark 1.2/1.3`, `codebuddy-cn hy3/hy3-x/hy4-preview/x/glm-5.3/kimi-k3-1` + EOL `glm-5.0/4.7` removed, `GetModelTokenLimits` 3.8.
- `translator/claude_cache_test.go` — `LastCacheableToolIndex` + `AnchorClaudeCache` for `defer_loading:true` tail, all-deferred, stripping client `cache_control` (#3567).
- `handlerutil/ssrf_test.go` — `trailing dot` (`localhost.`), `CGNAT 100.64/10`, `169.254.169.254`, IPv6 `::ffff:7f00:1` hex, `64:ff9b::`, `fe80/fc`, `normalizeHost`, `parseIPv6ToGroups`.
- `mitm/handlers/mitm_handlers_test.go` — `HandleKiro` removes `systemPrompt` + `userInputMessage.images → image_url data:`, `HandleAntigravity` preserves `fetchAvailableModels(2.11.0)` vs overrides `generateContent→1.23.2`.
- `usagetracker/quota_parsers_test.go` `TestParseGroqQuotasFromHeaders` — `x-ratelimit-*` Go duration `2m59.56s` → `requests/tokens` `used/total/resetAt`.
- `handlers/chat/chat_v065_test.go` — `HandleModelLookup` kind `image` + `cc/claude-sonnet-4-6` + encoded slash + 404 `model_not_found`, `HandleModels` custom `cc/my-custom-vision` caps, `StrikeReassert` 3×429 optimistic 90% → `CACHE_BLOCK 15m` + re-assert after `Refresh`.
- `proxy/executor/opencode_test.go` `MuseSpark13_ResponsesRouting` + `OCPrefix` — routing `1.3` + `oc/` to `/responses`.

**Fixes:**
- **Opencode 1.3 `500` → `200`** — `ForwardOpencode` routing `Contains(muse-spark)` + `capabilities` `1.3` Vision (fixes report `14:11:47` `muse-spark-1.3 500`).
- **jcode tool_smoke 3→1** — `stream.go:118` dedup `ToolCallIdx` + `codebuddy.go:168` `sseToOpenAIJSON` dedup `arguments` for `bash` `intent` split (fixes `echo JCODE_TOOL_OK` 3 tool_calls).
- **Flaky real upstream 429** — `muse_spark_e2e_test.go` real `opencode.ai` `429 FreeUsageLimitError` now `Skip` instead of `Fail`.
- **DB flaky `429` in `go test ./...`** — `go vet` clean, `ps` `9router-go 20130` health `{"status":"ok"}` (not stopped, log stopped due to `user stepped away` recap 98k prompt).

## [v1.8.7] — 2026-09-02

### 🐛 Bug Fixes

- **Gemini `system_instruction` Empty Part 400 Fix** — `StripCompetitivePrompts` now drops empty `system_instruction` parts after `rewriteCompetingBranding` (e.g. `"You are a Claude agent..."` -> `""`) and filters empty text parts in `contents`; if all parts are empty the `system_instruction` is removed (`nil`) instead of emitting `{"parts":[{}]}` which Gemini rejects as `system_instruction.parts[0].data: required oneof field 'data' must have one initialized field`. Also `TranslateOpenAIToGemini` now `TrimSpace` checks system content. Fixes `ForwardGemini (antigravity/gemini-3.7-flash-high): ...system_instruction.parts[0].data: required oneof`. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Gemini Tool Schema `required` Inside `properties` 400 Fix** — `cleanGeminiSchema` now detects misplaced `required` array inside `properties` (e.g. `{"properties":{"query":{...},"required":["query"]}}`) and promotes it to top-level `required`, fixing `Invalid value at 'request.tools[0].function_declarations[0].parameters.properties[0].value' (Map), Cannot have repeated items ('required') within a map. Unknown name ""`. This was the root cause of the `12:14:50` `query_db_ide` 400 after the `where.items` fix. Also sanitizes all OpenAI-compatible providers (including `opencode`) via `fallback.go` `SanitizeOpenAITools`. (`internal/translator/schema.go`, `internal/handlers/chat/fallback.go`)
- **Opencode `muse-spark` Tool Name Triplication Fix** — `sseToOpenAIJSON` `internal/proxy/executor/codebuddy.go:168` now only sets `name` if empty and avoids duplicating `arguments` already sent via `delta`/`done`; `ProcessCodexEvent` `stream.go:148` for `response.function_call_arguments.delta/done` now deduplicates `name` and tracks `ToolCallArgs` to prevent `get_weather` -> `get_weatherget_weatherget_weather` and `{"location":"Jakarta"}{"location":"Jakarta"}` on `combo-wombo` (`oc/muse-spark-1.2`) non-stream and stream. (`internal/proxy/executor/codebuddy.go`, `internal/proxy/executor/stream.go`)

## [v1.8.6] — 2026-09-02

### 🐛 Bug Fixes

- **Gemini Tool Schema `where.items.items: missing field` 400 Fix** — `cleanGeminiSchema` now ensures every `type: array` has a valid `items` schema (default `{"type":"string"}`), flattens `prefixItems` (2020-12 tuple) and `items: [...]` tuple to single `items`, and auto-fills inner `items` without `type`/`properties`/`enum`. Fixes `ForwardGemini (antigravity/gemini-3.7-flash-high): upstream returned 400: ...where.items.items: missing field.` when Claude Code sends DB-like tools with nested `array<array>` params. Added `internal/proxy/gemini.go` 400 payload dump to `/tmp/9router-gemini-400.json` for post-mortem. (`internal/translator/schema.go`, `internal/proxy/gemini.go`)
- **Bare Alias `ag` -> `antigravity` Resolution** — `resolveModel("ag")` now checks `ProviderAliasMap` before common-provider fallback, so `POST /search` with `{"model":"ag"}` correctly routes to `antigravity` instead of `deepseek/ag` -> `404 Via cloudfront`. Parity with Next.js `ag` search. (`internal/handlers/chat/resolution.go`)
- **Antigravity Search Default Model Parity** — `handleAntigravitySearch` default changed `gemini-3-flash-agent` -> `gemini-2.5-flash` (Next.js `ag` search returns `answer.model: gemini-2.5-flash`), fixing `500 UNKNOWN` from `daily-cloudcode-pa.googleapis.com` for bare `ag` search. (`internal/handlers/media/antigravity_search.go`)
- **Claude `call_` Tool Name Fallback Fix (Gateway IP Check)** — `TranslateOpenAIToClaude` no longer falls back to `tc.ID` (`call_...`) when `Function.Name` is empty; skips invalid tool calls instead of emitting `tool_use` with `name: call_...` which caused `No such tool available: call_...` and `Invalid tool parameters` churn on `toloing cek config gateway` via `opencode/muse-spark`. (`internal/translator/response.go:165`, `cf42120`, port `decolua/9router#2077`/`#3685`)
- **Claude Streaming Tool Delta Deduplication** — `TranslateOpenAIToClaudeStreamSession` now checks `state.ToolCalls[idx]` before creating a new `content_block_start`; second delta for same `idx`/`id` (Codex `output_item.added` + `function_call_arguments.delta` same `call_...`) no longer creates duplicate `tool_use` and empty `partial_json: "{}"`; correctly buffers `arguments` and emits `{"command":"ip route ..."}`. Fixes `InputValidationError: Bash missing command` on `Gue cek gateway IP...` streaming. (`internal/translator/response.go:468`, `ea10c16`)
- **Bash Extra Fields Strip** — `sanitizeBashArgs` now keeps only `command`, deletes hallucinated `description` etc. inside `input` (`{"command":"ls ...","description":"List home..."}` -> `{"command":"ls ..."}`), fixing `Bash(input JSON failed to parse — 433 bytes)` on `Cek gateway config — lagi intip file-file di home`. (`internal/translator/sanitize.go:239`, `6b94535`)
- **Server Tool Use Foreign ID Drop (Combo Poison)** — `SanitizeClaudePassthrough` drops `server_tool_use` with id not matching `^srvtoolu_` (e.g. `call_` from `z.ai/glm` `analyze_image`) and paired `tool_result`, strips empty text and empty messages. Port `decolua/9router#3686`. (`internal/translator/request.go:342`, `cf42120`)
- **1M Context Marker Strip** — `stripModelContextMarker` strips trailing `[1m]`/`[1M]` from `claude-opus-5[1m]` before `resolveModel`, so combo `combo-wombo[1m]` routes correctly instead of `Invalid model format`. Port `decolua/9router#3691`. (`internal/handlers/chat/resolution.go:135`)
- **Streaming Model Echo** — `SeedStreamState` pre-seeds `StreamState.Model` with client-requested model via `WithRequestedModel` context so `message_start` echoes `combo-wombo` not `claude-3-5-sonnet` provider model. Port `decolua/9router#3693`. (`internal/translator/usage.go:41`, `forward.go:91`)
- **GPT-5 / o-series `max_completion_tokens`** — `requiresMaxCompletionTokens` (`/gpt-5|o[134]-/i`) emits `max_completion_tokens` instead of `max_tokens` for `gpt-5`/`o1-`/`o3-`/`o4-` in `TranslateClaudeToOpenAI`. Port `decolua/9router#3657`. (`internal/translator/request.go:12`, `types.go:202`)
- **Antigravity Optimistic Quota Strike-Breaker** — after 3 consecutive `429` for same `connection|model` within 60s while quota `remaining>0`, `HandleAntigravityQuotaError` returns `CACHE_BLOCK 15m` instead of looping 300s `modelLock`. Port `decolua/9router#3684`. (`internal/handlers/chat/antigravity_quota.go:44`)
- **Forced-SSE JSON for Claude Clients** — `handleJSONResponse` detects `isSSEBody` when `translate=true` and `stream:false` retry hits forced-stream provider (Responses-API), aggregates via `sseToClaudeJSON` then `TranslateOpenAIToClaude` to return `Anthropic Message` not `chat.completion`. Port `decolua/9router#3683`. (`internal/handlers/chat/forward.go:144`)
- **OpenRouter Pattern Compat + Combo Tools Detection** — `NormalizeToolSchemasForProvider("openrouter")` strips invalid `pattern` regex (keep valid, handle `properties` named `properties`), and `DetectRequiredCapabilities` now requires `tools` capability for `type:function`/`functionDeclarations`. Port `decolua/9router#3665`. (`internal/translator/tool_schema.go`, `combo.go:129`, `forward.go:35`)

## [v1.8.5] — 2026-08-31

### ✨ Features & Parity (Next.js v0.5.59 Sync)

- **New Search Providers & Credential Fallback** — added `xquik` (X search provider with raw API key), `ollama-search`, and `zai-search` (GLM Coding web search). Added automatic credential fallback where search providers borrow API keys from parent chat connections (`ollama` / `glm`) when dedicated search connections are absent. (`internal/providers/providers.go`, `internal/providers/aliases.go`, `internal/handlers/chat/connections.go`)
- **Antigravity Web Search Provider** — added Antigravity as a web search provider via Google Search grounding, with full Next.js parity for the search response structure. (`internal/handlers/media/antigravity_search.go`)
- **New Models & Capabilities Sync** — registered new flagship models: `GLM-5.3-Flash` (1M context window + native vision multimodal), `GLM-5.3`, `DeepSeek V4 Vision`, `Grok 4.5/4.6` (500k context window), and `muse-spark-1.2-contributor-free`. (`internal/providers/capabilities.go`, `internal/providers/aliases.go`)
- **Claude Tool Type Defaulting (`type: "custom"`)** — added `DefaultClaudeToolType` ensuring tools in Claude-format requests always carry a valid `type` (defaulting to `"custom"` when omitted), preventing HTTP 400 rejection on strict Anthropic-compatible gateways such as MiniMax. (`internal/translator/request.go`, `internal/handlers/chat/chat.go`)
- **Claude Code Session ID Header Support** — prioritized `x-claude-code-session-id` in `ExtractSessionID` to ensure stable prompt caching and avoid conversation fragmentation across client tool calls. (`internal/handlerutil/response.go`)
- **CommandCode In-Stream Error Peeking** — peeks the initial NDJSON event in CommandCode stream for `type: "error"` before committing HTTP 200 OK headers, transforming internal stream errors into real HTTP error statuses (429, 503, 401, etc.) so combo and account fallback trigger seamlessly. (`internal/proxy/executor/stream.go`)
- **OpenCode Responses API Parity (v0.5.59)** — completed Responses API translation for OpenCode Muse Spark: proper tool names emitted on `response.output_item.added`, accurate usage and prompt-cache token extraction from `response.completed`, Claude SSE streaming translation and non-streaming support in `handleCodexStream`, and 64-char clamping for `call_id`. (`internal/proxy/executor/`)

### 🐛 Bug Fixes

- **Gemini Cached Token Extraction** — added support for both `cachedContentTokenCount` and `cachedContentToken` keys in Gemini stream and non-stream responses. (`internal/translator/gemini.go`)
- **Non-Interactive Test Execution** — bypassed interactive `sudo security` CA keychain install when executing unit tests, ensuring fast, deterministic test suite completion. (`internal/mitm/cert.go`)
- **Self-Update SHA256 Verification** — `PerformSelfUpdate` now downloads to memory, verifies the expected SHA-256 checksum, and refuses to install mismatched binaries, eliminating the risk of installing corrupted or tampered updates. (`internal/updater/updater.go`)
- **Graceful Self-Restart** — replaced abrupt `os.Exit` after self-update with `syscall.Kill(SIGTERM)` plus a graceful fallback, giving in-flight requests and DB connections a chance to drain cleanly. (`internal/updater/updater.go`)
- **Cross-Platform Restart** — extracted the self-signal into a platform-specific `signalSelfShutdown` helper (`signal_unix.go` sends SIGTERM; `signal_windows.go` is a no-op that falls back to `os.Exit(0)`), fixing the Windows cross-compile of the release binaries. (`internal/updater/signal_unix.go`, `internal/updater/signal_windows.go`)
- **Smart Archive Executable Selection** — `extractExecutableBytes` now scores archive entries (penalizing README/LICENSE/`*.md`/`*.sha256`) and validates ELF/Mach-O/PE magic bytes, reliably picking the real binary from multi-file release archives. (`internal/updater/updater.go`)
- **SSE Copy Race Condition** — replaced the shared pooled buffer in `SSECopy` with a per-call local buffer, eliminating concurrent read/write races on the pool buffer. (`internal/proxy/sse.go`)
- **Nil Guard in Token-Saving Compression** — guarded against a nil `rawMap` when the upstream body cannot be decoded, preventing a panic on malformed responses. (`internal/tokensaver/compress.go`)
- **Quota Percentage Clamping** — clamped `RemainingPercentage` to a sane `[0, 100]` range so upstream values >100 or negative cannot skew quota-block and dashboard logic. (`internal/usagetracker/quota_parsers.go`)
- **Exponential Backoff for Antigravity Onboarding** — replaced the fixed 2s sleep between `onboardUser` retries with exponential backoff (2s, 4s, ...) that also honors context cancellation, so a 429 burst no longer gets hammered by fixed-interval retries. (`internal/handlers/chat/antigravity_project.go`)
- **Decloak Deduplication** — extracted a shared `decloakContentBlockStart` helper used by both `DecloakStreamChunk` and `DecloakClaudeStreamEvent`, removing duplicate content-block-start logic. (`internal/translator/antigravity.go`)
- **Tool Property Sanitization** — preserved tool parameters named after reserved keywords and sanitized `required` fields against the declared `properties`, preventing schema validation failures. (`internal/translator/sanitize.go`)

## [v1.8.4] — 2026-08-14

### 🐛 Bug Fixes & Resilience

- **Combo Cycle Graceful Recovery & Fault Tolerance** — `flattenComboModels` now gracefully skips recursive / self-referencing combo branches with a warning log instead of failing hard with HTTP 400 (`combo cycle detected`), ensuring chatbot requests continue executing remaining valid models seamlessly. (`internal/handlers/chat/resolution.go`)
- **Safe Model Resolution on Leaf Models** — eliminates potential slice index-out-of-range edge cases when resolving combo leaf models that do not contain a provider prefix. (`internal/handlers/chat/resolution.go`)
- **Multi-Level Nested Combo Support** — verified recursive cascading combo expansion (e.g. `super-combo` → `mid-combo` → `base-combo` → leaf models) so all reachable models participate in round-robin, sticky, and fallback strategies. (`internal/handlers/chat/resolution.go`, `internal/handlers/chat/resolution_test.go`)

## [v1.8.3] — 2026-08-14

### ✨ Features

- **Antigravity Gemini 3.7 Flash Model Mapping** — canonical model IDs and aliases for `gemini-3.7-flash`, `gemini-3.7-flash-high`, `gemini-3.7-flash-agent`, `gemini-3.7-flash-medium`, `gemini-3.7-flash-low`, `gemini-3.7-flash-extra-low`, and `gemini-3.7-flash-thinking` correctly mapped to Google Antigravity backend model IDs (`gemini-3-flash-agent` / `gemini-3.5-flash-low`), fixing upstream 404 errors. (`internal/translator/antigravity.go`)
- **Enriched Prompt-Injection Guard** — enhanced prompt-injection detector with heuristic patterns for raw model delimiters (`<|im_start|>system`, `<<SYS>>`, `[SYSTEM PROMPT]`, `[INST]`), verbatim system prompt extraction attempts, and developer/admin mode override simulations. (`internal/tokensaver/injection.go`)
- **Accurate Gemini Cached Token Tracking** — correctly unmarshals and propagates `cachedContentTokenCount` from Gemini stream and non-stream responses into `OpenAIUsage.CachedTokens`, providing accurate cache hit reporting and cost calculation. (`internal/translator/gemini.go`)
- **Gemini Vision FileData & Audio Modalities** — added support for remote HTTP/HTTPS image URLs (`fileData: { fileUri, mimeType: "image/*" }`), base64 input audio (`input_audio`, `audio_url`), and uploaded documents in Gemini native translator, matching Next.js full multimodal capabilities. (`internal/translator/gemini.go`)
- **Realtime SSE Usage Stream & Topology Animation** — added in-memory in-flight request tracker (`internal/usagetracker`), real-time SSE broadcasting (`GET /api/usage/stream` and `GET /usage/stream`), and recent requests ring buffer matching the Next.js dashboard shape, enabling instant glowing pulse node & marching-ants edge animations on the Usage Topology graph when requests are handled by `9router-go`. (`internal/usagetracker/tracker.go`, `internal/handlers/usage_stream.go`, `internal/handlers/chat/fallback.go`, `internal/handlers/chat/usage.go`)
- **Antigravity Anti-Competitive Prompt Stripping & 429 Prevention** — automatically strips competitor identity phrases (e.g. `"You are a Claude agent, built on Anthropic's Claude Agent SDK."` from Zed IDE and Claude agents) from `system_instruction` and message contents, preventing Antigravity from returning synthetic `429 Quota Exhausted` errors. (`internal/translator/antigravity.go`)
- **Edge Relay URL Rewriting & Header Forwarding** — automatically rewrites `BaseURL` to the relay deployment and injects `x-relay-target` and `x-relay-path` headers when a connection uses a Vercel, Cloudflare Worker, or Deno Edge Relay Proxy Pool. (`internal/handlers/chat/connections.go`)
- **No-Auth Provider Proxy Pool Strategy** — automatically respects `settings.providerStrategies` for no-auth providers (e.g. `mimo-free`, `opencode`), attaching configured proxy pools or rotation strategies to virtual connections. (`internal/handlers/chat/connections.go`, `internal/db/settings.go`)
- **Snake_case Model Limits on `/v1/models` & `/v1/models/info`** — exposes `context_length`, `max_completion_tokens`, `max_input_tokens`, and `max_output_tokens` so clients like Cline, Roo Code, and LibreChat resolve proper context ceilings. (`internal/handlers/chat/chat.go`, `internal/providers/capabilities.go`)
- **CodeBuddy OAuth Configuration** — registered `codebuddy-cn` and `codebuddy-intl` OAuth token refresh configurations. (`internal/providers/oauth.go`)
- **OpenCode Official Client Fingerprint Headers** — injects official headers (`User-Agent: opencode`, `x-opencode-client: desktop`, `x-opencode-session: ses_...`, `x-opencode-request: msg_...`, `x-opencode-project: global`) on free-tier OpenCode requests to prevent rate limiting from unidentified client traffic. (`internal/proxy/opencode.go`, `internal/proxy/executor/providers.go`)
- **Kimchi Dual Authentication** — supports direct API keys (`Authorization: Bearer <key>`) in addition to OAuth tokens with seamless credential resolution. (`internal/handlers/chat/connections.go`, `internal/handlers/chat/kimchi_handler_test.go`)
- **Startup Banner & Version Display** — dynamically displays current version in CLI startup banner (`🚀 9Router Go Proxy (v1.8.3) on :20130`) and server ready logs. (`cmd/9router-go/main.go`)
- **New Provider Registries & Aliases** — added Alibaba Token Plan Singapore (`alitp-intl` / `ali-tp` / `alitp`) and Fish Audio Text-to-Speech (`fish-audio` / `fish`). (`internal/providers/providers.go`, `internal/providers/aliases.go`)

### 🐛 Bug Fixes

- **Invalid Tool Parameters & Decoy Schemas** — provided valid non-empty `properties.reason` schema for all 21 Antigravity decoy tools and mapped `tool_call_id` to exact function names in OpenAI-to-Gemini conversation history, eliminating protobuf validation errors when using Claude Code or other tool-calling clients. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Antigravity Upstream Model Resolution** — prevented invalid model aliases like `gemini-3.7-flash-high` from reaching Google Cloud Code without being translated to their backend model IDs. (`internal/translator/antigravity.go`)

### 📚 Documentation

- Comprehensive refresh of `README.md`, `COMPARISON.md`, `DATABASE.md`, `ARCHITECTURE.md`, `TECHNICAL_DEBT.md` (0 open items), and newly added `ROADMAP.md`.

## [v1.8.2] — 2026-08-14

### ✨ Features

- **Antigravity Tool Cloaking & Anti-Ban Decoy System** — automatically cloaks client tool declarations with `_ide` suffixes (e.g. `Bash_ide`), injects 21 official Antigravity IDE decoy tools (`run_command`, `replace_file_content`, `grep_search`, `list_dir`, etc.), synchronizes conversation history functionCall/functionResponse names, and seamlessly uncloaks tool names on response SSE stream and non-stream outputs. (`internal/translator/antigravity.go`, `internal/translator/gemini.go`)
- **Antigravity Native Image Generation** — added image model detection (`imagen`, `*image*`), aspect ratio suffix parsing (`16x9`, `4:3`, `1:1`, custom resolutions via GCD reduction), `requestType: "image_gen"` envelope wrapping with forced non-streaming `/v1internal:generateContent`, and OpenAI-compatible base64 image response formatting. (`internal/translator/antigravity.go`, `internal/proxy/gemini.go`)
- **Edge Relay & Transport Engine** — added transport support for Vercel, Cloudflare Worker, and Deno edge relays using `x-relay-target` and `x-relay-path` headers, wildcard `noProxy` domain filtering, and legacy connection-level proxy configuration fallback (`connectionProxyUrl`). (`internal/proxy/transport.go`, `internal/handlers/chat/connections.go`)

### 🐛 Bug Fixes

- **Proxy Pool DB Parsing Bug** — fixed `GetProxyPool` (`internal/db/proxyPools.go`) failing to parse single string `proxyUrl` created by Next.js UI / `InsertProxyPool`, which previously caused proxy pools to be silently ignored and requests to fall back to direct connections. Added parsing for `type`, `noProxy`, and `strictProxy` metadata.

## [v1.8.1] — 2026-08-12

### ✨ Features

- **Combo strategy sync with Next.js reference** — per-combo rotation state, correct auto-switch ordering, and a capabilities provider (`internal/providers/capabilities.go`) replacing the hardcoded vision/pdf maps with tiered capability detection.
- **Flatten nested combos** — `combo-wombo → free-tier` now expands to its four leaf models, so round-robin actually rotates across them instead of always landing on the first leaf (this was hammering one account and producing the `429 all connections for this provider are rate-limited` error).
- **Turn-aware rotation** — `applyComboStrategy` advances the rotation index only on a new turn; mid-turn tool-use requests reuse the model serving the turn, so the provider never switches mid-turn (which broke Gemini thinking models that require a `thought_signature` on current-turn function calls).
- **Bounded retry-once on total combo 429** — when every combo model fails with a Retry-After ≤ 8s, the pass waits once and retries before surfacing a hard 429 (`comboRetryAfter`).
- **Backfill default `thought_signature`** — every `functionCall` part now carries a `thoughtSignature` (the real one via `__ts__` transport when present, else the Next.js `DEFAULT_THINKING_AG_SIGNATURE`), closing the last 400-`thought_signature` gaps on mixed combos.
- **Gemini tool-schema keyword parity** — strip the remaining unsupported JSON-Schema keywords (`multipleOf`, `uniqueItems`, `contains`, `unevaluated*`, `contentSchema`) and fill bare `{}` schemas with the object placeholder, matching Next.js `cleanJSONSchemaForAntigravity` (fixes `Invalid tool parameters` 400 from antigravity).
- **Sanitize tools on the OpenAI-compat Gemini path** — the `gemini` provider is now marked `gemini-openai` and its `/v1beta/openai` bodies are run through `SanitizeOpenAITools`, so the strict schema validation applies on both Gemini routes.

### 🐛 Bug Fixes

- **Emit camelCase `thoughtSignature`** — the Gemini-native `generateContent` endpoint only recognizes the camelCase part field; the snake_case regression caused the `400 Function call is missing a thought_signature` error. Both read and write directions now handle camelCase.
- **Use the daily antigravity endpoint** — migrate `cloudcode-pa.googleapis.com` → `daily-cloudcode-pa.googleapis.com` for the `antigravity` provider (`providers.go`, `antigravity_project.go`, MITM domain list) to avoid strict rate limits.
- **Combo connection retry-loop parity** — connection retry loop now matches the single-model path.

### 🧹 Chores / Docs

- Remove the stray `patch_combo.go` throwaway script.
- Add design specs and implementation plans for the combo sync / thought_signature / backfill / schema-parity work.

## [v1.8.0] — 2026-08-11

### 🐛 Bug Fixes

- **Combo/router fallback bypasses model-lock backoff on retryable errors → antigravity rate-limit loop** — `handleComboFallback` / `handleMessagesComboFallback` (`internal/handlers/chat/combo.go`) called `tryForwardWithConnection` directly, so `LockConnectionModel` was never invoked on retryable errors (429/500s) — unlike the single-model path `handleAccountFallback`. The exponential 429 backoff was dead in the router path: every request re-tried all combo models back-to-back on the same connection/account, got 429, returned 429, and the client's ~35s retry repeated the loop forever. Fix:
  - New `comboLockRetryable` helper runs on every `RetryableStatusCodes` error in both combo loops — classifies via `ClassifyError`, calls `LockConnectionModel(connID, model, cooldownSec, newBackoffLevel)` so the exponential backoff persists across requests, and appends the conn to a request-local `excludeIDs` passed into `getBestConnection` so remaining combo models don't re-select the same connection (same account = same quota bucket).
  - A locked-connection skip covers pinned connections whose direct-fetch branch bypasses `getBestConnection`'s lock check.
  - `context.Background()` → `ctx` in `handleComboFallback` so client cancels propagate; the 502/503/504 transient-wait sleep is preserved.
  - Test: `TestHandleMessagesComboFallback_429LocksAndExcludesConnection` asserts a 429 locks the connection AND keeps the second combo model from re-hitting it (exactly 1 upstream hit).

## [v1.7.2] — 2026-08-08

### 🐛 Bug Fixes

- **Antigravity 429/404 failure-loop fix** — An unprovisioned Antigravity account
  (`onboardUser` returns `200` with an empty `cloudaicompanionProject`) left the
  connection without a `projectID`. The router then force-refreshed the OAuth
  token on every request (never an auth problem, so it never helped), fell through
  to a guaranteed-404 OpenAI-compatible lane on `cloudcode-pa.googleapis.com`,
  and repeated client retries rammed Google's rate limit (`429`). (`internal/handlers/chat/gemini_handler.go`, `internal/handlers/chat/antigravity_project.go`)
  - `fetchAntigravityProjectID` now reports the outcome (`projectID`, `authFailed`,
    `noProject`). Token refresh runs **only** on a genuine `401/403` — never on a
    missing/empty project.
  - When antigravity has no project ID, it no longer burns a request on the dead
    OpenAI lane; it returns an error and the fallback chain moves straight to the
    next provider.
  - **Negative cache (10 min, per connection):** once Google confirms "no project",
    later requests skip the `loadCodeAssist`/`onboardUser` RPCs entirely — this is
    what stops the repeated `429` hammering.
  - Onboarding guidance is logged once per connection per window
    ("onboard the account via Antigravity IDE/CLI, then re-login"); repeated
    failures log at `Debug` instead of spamming `Warn`. (`internal/handlers/chat/fallback.go`)

### 🧪 Tests

- `antigravity_project_test.go` — pins the probe classification (project found /
  token rejected `401`+`403` / project definitively missing / transient `429`+`503`)
  and the negative-cache expiry semantics. (`internal/handlers/chat/antigravity_project_test.go`)

## [v1.7.1] — 2026-08-08

### 🐛 Bug Fixes

- **Cached-token parity across every provider** — Prompt-cache accounting no longer works only for antigravity. Gemini `usageMetadata.cachedContentToken` now flows through both non-stream and stream translation into OpenAI `usage.cached_tokens`; the `!translate` response path uses a dual-format parser (`ParseResponseUsage`) that reads Claude `cache_read_input_tokens`/`cache_creation_input_tokens` and OpenAI `prompt_tokens_details.cached_tokens`, so cached tokens survive any provider → OpenAI → Claude double translation. (`internal/translator/gemini.go`, `internal/translator/response.go`)
- **Gemini tool-schema `const` re-injection** — `stripUnsupported` now re-runs after `anyOf`/`oneOf` flattening so `const` and vendor `x-*` keys can't leak back into the merged branch. (`internal/translator/schema.go`)
- **Provider 403 is now retryable** — Gemini/antigravity daily-quota errors can arrive as HTTP 403; these now trigger the connection fallback instead of a hard failure. (`internal/providers/providers.go`)
- **CodeBuddy CN stream cleanup** — The stall reader is now closed after the stream, stopping its shutdown watcher + stall timer (no per-request goroutine leak). (`internal/proxy/executor/codebuddy.go`)

### ⚙️ Graceful Shutdown Hardening

- New `internal/shutdown` package: a process-wide signal the first Ctrl+C / SIGTERM fires.
- `StallReader` now closes in-flight SSE upstream bodies on shutdown, so `server.Shutdown` drains streams in milliseconds instead of waiting out the 15s deadline — and the deferred DB/log-file close always runs.
- Translate-path SSE handlers emit a final `data: [DONE]` on abort so clients get a clean end instead of a truncated stream.
- A second Ctrl+C / SIGTERM force-quits immediately (stuck-drain escape hatch).
- Shutdown timeout logs a warning instead of `log.Fatalf`, so `conn.Close()` and the log file are still closed gracefully. (`internal/proxy/stall.go`, `cmd/9router-go/main.go`, `internal/handlers/chat/forward.go`, `internal/proxy/executor/openai.go`)

### 🔧 Internal

- Stream handlers now carry the request `ctx` and pull accumulated usage (incl. cached tokens) out of the translation session, so logged usage reflects real token counts instead of the character-estimate fallback.

## [v1.7.0] — 2026-08-06

### 🚀 New Executors

- **Trae SOLO remote agent** (`internal/proxy/executor/trae.go`) — Port of `open-sse/executors/trae.js`: `POST {base}/chat_sessions` creates a session, `GET {base}/chat_sessions/{id}/events` streams `plan_item` / `token_usage` / `done` as SSE. Cumulative `plan_item.thought` rendering (longest-wins per id, delta-only emission), `Cloud-IDE-JWT` auth, and `work`/`auto`/manual model modes. Non-stream requests aggregate into a single `chat.completion`. Round-trip test: `TestForwardTrae_StreamsAccumulatedThought`.
- **Windsurf gRPC-web** (`internal/proxy/executor/windsurf.go`) — Port of `open-sse/executors/windsurf.js`: hand-rolled protobuf `GetChatMessageRequest` encoder (Metadata.api_key + cascade_id + model_or_alias + repeated messages), gRPC-web framing (0x00 flag + big-endian length), and a `CompletionChunk` decoder (content / done+UsageStats / error) streaming OpenAI SSE. Catalog→wire model alias map ported verbatim; `crypto/rand` session/cascade ids. Non-stream requests aggregate frames into `chat.completion`. Round-trip test: `TestForwardWindsurf_StreamsGRPCWeb`.

### 🎙️ Xiaomi MiMo TTS

- `/v1/audio/speech` for the `xiaomi-mimo` provider now uses the chat-completions contract (port of `open-sse/handlers/ttsProviders/xiaomi-mimo.js`): target text in `role:assistant`, style/language instructions in `role:user`, voice via top-level `audio.voice`, base64 audio from `choices[0].message.audio.data`. (`internal/handlers/media/media.go`)

### ➕ Providers

- **tokenrouter** — Registered as an OpenAI-compatible upstream (`https://api.tokenrouter.com/v1/chat/completions`).

### 🗑️ Removed

- **qwen provider** — Removed from providers, OAuth config, and the alias map (deprecated upstream).

### 📋 Docs

- `TECHNICAL_DEBT.md` — windsurf + trae moved to resolved; zed + devin-cli documented with the safe-stopgap note (devin-cli corrected: ACP over **stdio** subprocess, not HTTP).

## [v1.6.1] — 2026-08-05

### 🐛 Bug Fixes

- **CodeBuddy CN 502** (`internal/proxy/executor/codebuddy.go`) — `codebuddy-cn` / `codebuddy-intl` now use a dedicated executor that forces `stream=true` upstream (CodeBuddy rejects non-stream with HTTP 400 code 11101), injects the CLI/IDE static headers, and re-aggregates OpenAI-chat SSE into a single `chat.completion` for non-stream clients (`sseToOpenAIJSON`, mirroring JS `parseSSEToOpenAIResponse`).
- Provider parity with the reference implementation.

## [v1.6.0] — 2026-08-04

### 🚀 Next.js Engine Feature Ports

- **TTS Voice Listing** (`/audio/voices`) — Full voice-listing with provider support (`edge-tts` default, `elevenlabs`, `gemini`, `local-device`), `?lang` filter, 24h in-process cache, and `byLang`/`languages` grouping matching the dashboard's media-providers page.
- **Proxy-Pools Deploy** (`/proxy-pools/{vercel,deno,cloudflare}-deploy`) — Deploy edge relay functions to Vercel/Deno/Cloudflare with status polling, plus a new `InsertProxyPool` DB method writing byte-compatible `data` JSON.
- **Headroom Management** (`/headroom/*`) — Full headroom-ai lifecycle in Go: binary/Python detection, spawn/stop/restart, compression extras install/uninstall, `/headroom/proxy` reverse proxy with SSRF guard, and dashboard HTML rewrite.
- **CLI-Tools Status** (`/cli-tools/all-statuses`) — Batch detection of 14 CLI tools (Claude, Codex, OpenCode, etc.) installed state + version.
- **Live Console Logs** (`/translator/console-logs`, `/stream`) — In-process ring buffer + SSE streaming of engine log output so the dashboard's "Monitor Console Log" shows Go logs live (25s keepalive, init/line/clear events).

### 🔍 Observability

- **Lightweight Request Tracing** (`/debug/traces`) — In-memory span recording + p50/p95/p99 latency per provider+model with `?n=` cap. Stdlib-only, no OpenTelemetry SDK dependency.

### 🛡️ Security

- **Prompt-Injection Guard** — Heuristic detection (`messages[]`, `input[]`, Claude content blocks) tagging classic injection attempts in logs. Toggle via `--no-injection-guard` / `INJECTION_GUARD_DISABLED` (on by default).
- **`/admin/health/reset` Moved Behind API-Key Auth** — Previously public; now requires a valid API key to prevent unauthenticated health-state resets (open-source hardening).
- **MITM Binds Loopback Only** — TLS proxy binds `127.0.0.1:443` instead of all interfaces, preventing LAN clients from using it as an open proxy.

### 🐛 Bug Fixes & Stability

- **SSE Fragment Rejoin** — Fixed `unexpected end of JSON input` on opencode free-tier by buffering/rejoining truncated SSE JSON payloads per session (1 MiB cap).
- **Codex/CommandCode Tool-Call Streams** — Stable per-call tool IDs/indices (using upstream `call_id`), correct `[DONE]` framing, and checked `w.Write` errors.
- **MITM Goroutine Leaks** — `Stop()` drains in-flight connections (WaitGroup + active conn close); request bodies bounded at 10 MiB.
- **Executor/OAuth Registry Mutexes** — Package-level registry maps now guarded by `sync.RWMutex` (race-free on re-registration).
- **Token Saver JSON Number Preservation** — `CompressMessages`/`InjectSystemPrompt` use `json.Number` so numeric fields (temperature, large ints) round-trip unchanged.
- **`interface{}` → `any`** — Lint cleanup across stream/log packages.

## [v1.5.0] — 2026-07-24

### 🚀 Architecture & Observability Enhancements

- **Modular `main.go` Refactoring** — Extracted CLI subcommands (`mitmEnable`, `mitmDisable`, `mitmStatus`, `resolveDataDir`) to `cmd/9router-go/commands.go` and encapsulated server routing setup into `handlers.SetupServerRouter()`.
- **Structured Request Logging Middleware** — Moved `statusWriter` and `RequestLogger` to `internal/middleware/logging.go`. Requests are logged with Correlation ID (`id=req_...`) using structured logger (`slog.Info`, `slog.Warn`, `slog.Error`).
- **Dynamic HTTP Status Log Levels** — Requests with status 5xx are logged at `ERROR` level, 4xx at `WARN` level, and 2xx/3xx at `INFO` level for clean log filtering in production.
- **Upstream Memory Exhaustion Protection** — Added `io.LimitReader` caps (1MB for upstream error bodies, 10MB for non-streaming completion bodies) to protect proxy memory from rogue upstreams.
- **Double WriteHeader Prevention** — Added `written bool` guard to `statusWriter` and `cw.IsCommitted()` checks across combo fallback handlers to eliminate `superfluous response.WriteHeader` warnings.
- **Typed Request ID Context Key** — Shared `log.RequestIDKey` across middleware and logging packages to ensure context lookups match reliably.

## [v1.4.0] — 2026-07-23

### 🛠️ Technical Debt Remediations (All 9 Items Resolved)

- **Context-based Per-Request Usage Capture** — Replaced global `translator.lastUsage` with context-captured isolation (`WithUsageCapture`, `SetUsage`, `GetAndClearUsage`) to eliminate cross-request data races under concurrent traffic. (`internal/translator/usage.go`)
- **Thread-safe Daily Usage Updates** — Protected `upsertDailyUsage()` with `dailyUsageMu` mutex to prevent concurrent SQLite read-modify-write races. (`internal/handlers/chat/usage.go`)
- **Committed Response Writer** — Wrapped `http.ResponseWriter` with `committedResponseWriter` to prevent safe-retry attempts after response headers have already been sent to the client. (`internal/handlers/chat/response_writer.go`)
- **Strict Context Propagation** — Replaced all `http.NewRequest` with `http.NewRequestWithContext` across handlers, proxy execution drivers, and OAuth helpers to prevent orphaned upstream connections.
- **Graceful Shutdown** — Implemented `http.Server` graceful shutdown with signal drain (15-second timeout) on SIGINT/SIGTERM. (`cmd/9router-go/main.go`)
- **SQLite Connection Pool Optimization** — Reduced SQLite `SetMaxOpenConns(4)` for optimal WAL mode performance and zero connection contention. (`internal/db/client.go`)
- **Thread-Safe ProxyPool Cache** — Added `sync.Map` `proxyPoolCache` in `internal/db/proxyPools.go` to preserve round-robin rotation indices across requests. (`internal/db/proxyPools.go`)
- **Unbounded Request Body Guard** — Added `middleware.MaxBody` (10MB limit) to protect all endpoints from OOM attacks. (`internal/middleware/max_body.go`, `cmd/9router-go/main.go`)

### ⚡ Metrics, Latency & Token Accounting Fixes

- **TTFT & Latency Tracking** — Added `StartTime` and `TTFT` tracking across all streaming and non-streaming proxy execution drivers (`openai`, `opencode`, `deepseek`, `claude`, `grok-cli`, `qoder`, etc.). (`internal/proxy/executor/`)
- **Input Token Calculation Fix** — Added `[]byte` type support to `CountValueChars` so fallback prompt token calculation accurately estimates token size instead of defaulting to 1 token. (`internal/handlers/chat/chat.go`)
- **Output Token Calculation Fix** — Connected `ResponseBuf` in `executor.Request` to record stream output tokens when upstream omits token usage objects. (`internal/proxy/executor/openai.go`)
- **Prompt Caching Tokens Support** — Updated `OpenAIUsage` to extract `cached_tokens` (`prompt_tokens_details.cached_tokens`) and `cache_creation_input_tokens`. (`internal/translator/types.go`, `internal/handlers/chat/usage.go`)

### 🧪 End-to-End Integration Test Suite

- **E2E Test Suite** — Added `internal/handlers/chat/e2e_integration_test.go` to test real HTTP streaming SSE, non-streaming JSON responses, TTFT latency, token accounting, and SQLite DB usage logging end-to-end.

### 🌐 Endpoints

- **`/api/hello`** — Registered `/api/hello` route returning `200 OK` for ping probes from Claude Code CLI. (`cmd/9router-go/main.go`)

## [v1.3.0] — 2026-07-22

### 🏥 Next.js-Compatible Health System

- **Connection-based health** — Replaced old `kv`-based `IsProviderHealthy`/`RecordProviderHealth` with `modelLock_*` fields in `providerConnections.data` JSON blob, matching Next.js `markAccountUnavailable` / `clearAccountError` flow. (`internal/db/health.go`, `internal/db/accounts.go`)
- **Per-connection model locks** — `LockConnectionModel` / `UnlockConnectionModel` / `IsConnectionModelLocked` use SQLite `json_set()` on shared `providerConnections.data`. Dashboard can read/write same fields. (`internal/db/accounts.go`)
- **`IsProviderAvailable`** — New `Repo` method checks if ANY connection for a provider has no active `modelLock_<model>`, replacing the old kv-based pre-check. (`internal/db/accounts.go`)
- **`POST /admin/health/reset`** — Resets `modelLock_*` on connections via query params `?provider=X&model=X`. Dashboard can call via headroom proxy. (`cmd/9router-go/main.go`)
- **Eliminated duplication** — Package-level `IsProviderHealthy` / `ResetProviderHealth` now delegate to `NewRepo(database)` instead of duplicating lock JSON parsing logic. (`internal/db/health.go`)

### 🧪 Test Fixes

- **False-pass assertions** — 3 handler tests were checking old kv-based `repo.IsModelLocked()` which always returned `false` vacuously. Changed to `repo.IsConnectionModelLocked(connID, model)` to actually verify connection-level locks. (`internal/handlers/chat_test.go`)

## [v1.2.0] — 2026-07-22

### 🎯 Gemini Tool Calling Fixes

- **thought_signature round-trip** — Gemini response encodes `thought_signature` into tool call `id` via `__ts__` separator; request decoder restores it for valid verification. Works for both streaming and non-streaming. (`internal/translator/gemini.go`)
- **Antigravity (AGY) support** — Custom `GeminiPart.UnmarshalJSON` handles `thoughtSignature` (camelCase) AND `thought_signature` (snake_case) since the internal `v1internal` endpoint returns camelCase. (`internal/translator/gemini.go`)
- **Tool response name fix** — `tool_call_id` with `__ts__` suffix no longer corrupts `functionResponse.name` extraction, preventing Gemini validation errors on turn 2. (`internal/translator/gemini.go`)

### 🎨 Logging

- **ANSI color-coded logs** — `INF` = green, `WRN` = yellow, `ERR` = red, `DBG` = cyan. Auto-detects TTY (disabled when piped). Disable via `NO_COLOR=1`. (`internal/log/log.go`)

### 🔧 Streaming Fixes

- **SSE multi-line** — Gemini stream chunks with multiple SSE lines (`data: ...\ndata: ...`) are now split and translated individually. Error on one line continues to next instead of aborting. (`internal/handlers/gemini_handler.go`)

### 🧹 Cleanup

- `fallback.go`: Removed misleading `WRN tokensaver failed` logs — replaced with idiomatic `if next, did := ...; did` pattern.
- `test_opencode.go`: Removed (stale temporary test file).
- `internal/translator/gemini_test.go`: Added (unit tests for `thought_signature` round-trip).

## [v1.1.0] — 2026-07-21

### 🚀 New Features

- **SSRF protection** — `/v1/web/fetch` now blocks requests to private/internal IPs (RFC 1918, loopback, link-local, cloud metadata). Matches Next.js `assertPublicUrl()`. (`internal/handlerutil/ssrf.go`)
- **Bypass handler** — Detects Claude Code naming, warmup, and count requests. Returns fake responses without calling upstream, preventing wasted combo rotation slots. (`internal/handlers/bypass.go`)
- **Structured logging** — New `internal/log` package with Info/Warn/Error/Debug levels, runtime config via `LOG_LEVEL` env var. All ~100 `log.Printf` calls replaced across 24 files.
- **Per-connection model locks** — Model locks now stored as `modelLock_<model>` in `providerConnections.data` JSON blob. DB-compatible with Next.js dashboard. Connection A and B can have independent lock states.
- **SSE stall detection** — `StallReader` wrapper closes upstream connection after 6 minutes of no data, preventing hung streams. Integrated into all 4 SSE stream paths.
- **Error classification** — Text-based error rules (8 patterns) + status-based rules (5 codes) + exponential backoff (2s–5min). Fully matching Next.js `checkFallbackError()`.
- **Retry-after tracking** — Tracks earliest `retryAfter` across combo models, includes `Retry-After` header in error responses.
- **Request ID tracing** — Every response includes `X-Request-ID` header, access log includes `id=xxx` prefix.
- **Combo strategies aligned with Next.js** — Sticky round-robin, auto-capability-switch (vision/pdf detection).
- **Health/lock check in combo loops** — Skip unhealthy or locked models during fallback iteration.

### 🔧 Refactoring

- **Error response consistency** — `WriteJSONError` now status-code-aware (e.g., 401 → `authentication_error`, 429 → `rate_limit_error`). `auth.go` inline JSON replaced.
- **SSE consolidation** — `proxy.WriteSSEHeaders` shared by all 4 SSE stream functions. `proxy.SSECopy` with optional `onChunk` callback.
- **Shared test fixture** — `internal/dbtest` package provides canonical `CreateTables()` eliminating duplicated schema in 5+ test files.
- **`stringBuilder` → `bytes.Buffer`** — Removed duplicate custom type in favor of standard library.

### 📚 Documentation

- `ARCHITECTURE.md` — 10 Mermaid flow diagrams (request lifecycle, combo, fusion, error classification, etc.)
- `DATABASE.md` — All 11 tables, JSON blob structure, Go vs Next.js differences

### 🐛 Fixes

- `RetryAfter` ceiling calculation corrected from floor to proper ceiling (`time.Second - 1`)
- Stream translation now handles `[DONE]` marker before JSON parsing
- `TranslateResp` field now passed in `tryForwardWithConnection`

## [v1.0.2] — Previous

- Initial release with OpenAI/Claude SSE proxy, combo fallback, token savers, benchmark results.
