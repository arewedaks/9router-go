# Fork Notes — `arewedaks/9router-go`

This repository is a fork of [`luqman-v1/9router-go`](https://github.com/luqman-v1/9router-go).

The `feat/go-dashboard` branch adds a **self-contained Go web dashboard** so the
gateway can run without the Node.js dashboard process. That matters on
low-resource hosts — on an Android/Termux phone the legacy dashboard costs about
128 MB RSS, while the embedded one costs roughly 25–45 MB.

## Branch layout

| Branch | Purpose |
|---|---|
| `main` | Mirrors upstream. Kept pristine for easy rebases. |
| `feat/go-dashboard` | All dashboard work (default branch of this fork). |

Remotes:

```sh
origin    https://github.com/arewedaks/9router-go.git   # this fork
upstream  https://github.com/luqman-v1/9router-go.git   # original project
```

Sync with upstream:

```sh
git fetch upstream
git checkout main && git merge --ff-only upstream/main
git checkout feat/go-dashboard && git rebase main
```

## What `feat/go-dashboard` adds

**Embedded dashboard** — dark-mode SPA served from `//go:embed ui/*`, no build
step, no framework. Routes are registered in `internal/handlers/router.go`.

**Provider sub-page**, mirroring upstream `/dashboard/providers/[id]`:

- Connections tab — per-account toggle, delete, "Add Connection" form
- Capabilities tab — the provider's declared service kinds
- Available Models tab — list, add, remove, import
- Enable All / Disable All

**Model management**

- `Add Custom Model`
- `Remove model`
- `Import from /models` — fetches the provider's live catalogue, previews it in
  a modal with per-model checkboxes, then persists the selection
- **Test model** — per-model ping button plus a "Test All Models" toolbar with a
  parallel toggle, an **auto-disable failed** checkbox, and a Stop button. Tests
  route through the proxy's own `/v1/chat/completions` (LLM), `/v1/embeddings`,
  and `/v1/images/generations` endpoints, so OAuth refresh, combos and fallback
  behave exactly as they do for real traffic.
- **Antigravity model discovery** — Antigravity exposes no OpenAI-style
  `/models`; the dashboard instead POSTs to `v1internal:fetchAvailableModels`
  (and `v1internal:models`) across the `daily-*` and stable Cloud Code hosts,
  drops non-chat/retired/internal entries, and labels the survivors. When every
  host fails it degrades to a local catalogue with a warning instead of erroring.
- **CodeBuddy model import** — CodeBuddy (both `codebuddy-cn` and
  `codebuddy-intl`) has **no** model-listing route: `/v2/models`, `/v1/models`
  and every plausible alias answer `404` against a valid account. The fork used
  to answer "Provider codebuddy does not support models listing", so `Import
  from /models` could never work for it. It now returns a built-in catalogue
  (GLM / MiniMax / DeepSeek / Hunyuan / Kimi, derived from the model locks seen
  on real accounts) with a warning explaining why the list is static. Retired ids
  (e.g. `deepseek-v4-flash`, which upstream rejects with
  `service info not found`, code 11102) and non-chat surfaces are filtered out.
  OmniRoute does the same — it ships a CodeBuddy catalogue and declares no fetch
  endpoint.
- **Antigravity client profile (IDE / CLI)** — each Antigravity connection can
  present either the IDE or the standalone Antigravity CLI (`agy`) client
  identity. The profile is stored per connection under
  `providerSpecificData.clientProfile` and selects the outbound `User-Agent`
  (chat, web search and model discovery all honour it). Default is `ide`, so
  existing connections are unchanged.
- **Test connection (per-account)** — a `Test` button on each connection row and
  a `Test All Accounts` button per provider. Unlike Model Test, this does **not**
  loop back to the proxy: the chat layer resolves a connection by priority only
  and no header can pin it, so a loopback "test this account" would probe
  whichever account happened to be highest-priority and mislabel the result. The
  probe therefore calls the provider directly with the connection's own
  credentials.

  For Antigravity the probe targets the **real Cloud Code model surface**
  (`v1internal:generateContent`) with a properly wrapped `AntigravityRequest`
  body. An earlier generation probed only Google's OAuth `userinfo` endpoint,
  which is not geo-restricted, so accounts reported healthy while every model
  call failed. Verdicts: `2xx` = valid; `400` (non-geo) = inconclusive, reported
  as valid-with-warning; `400` carrying a Google region refusal = `geoBlocked`,
  surfaced as amber (never green — a blocked server is not a healthy account);
  `401`/`403` = rejected credential.

  The probe is deliberately **read-only**. Upstream persists
  `testStatus`/`lastError`/`rateLimitedUntil`/`backoffLevel`, but that is safe
  there only because the route is authenticated and a credential-health
  scheduler re-probes and clears the state. Here the dashboard routes are
  unauthenticated and there is no recovery loop, so writing would be a remote
  DoS primitive (loop the endpoint to flip working accounts out of rotation)
  with sticky-bad state. It also **never refreshes** OAuth tokens: this fork's
  refresher has no per-connection mutex or in-flight dedup, so a test-triggered
  refresh could race on the same refresh token and invalidate a healthy account.
  A `401` is reported as "the next chat request will refresh it".

  Endpoints: `POST /api/dashboard/connections/{id}/test` and
  `POST /api/dashboard/providers/{id}/test-connections`

- **Antigravity project discovery sends the Antigravity User-Agent** — the
  onboarding RPCs (`loadCodeAssist`, `onboardUser`) used to identify as
  `google-api-nodejs-client/9.15.1`. Google answers that with a **200 that omits
  `cloudaicompanionProject`**, even for fully provisioned accounts, so the fork
  concluded "no project" and every chat returned `502` for a healthy login.
  Verified live on one token: the generic UA yields no project, while the
  Antigravity UA (`antigravity/ide/…` or `antigravity/cli/…`) returns the real
  project id. Both RPCs now send the connection's own UA (the same IDE/CLI
  profile used for chat), falling back to the legacy value only when the caller
  has no profile. This is why accounts that looked "failed" in Test Connection
  recovered with no change to their credentials.

  Note the two used to be different failures: Test Connection short-circuited an
  expired access token without a network call, so an "expired" verdict was not a
  dead account. That short-circuit is now gone — see the token-refresh section
  below; Test Connection refreshes an expired token before probing.
  (`{"parallel":false,"connectionIds":[]}`; sequential by default as a pacing
  choice, not a safety one — refreshes are serialised per connection).
  Timeout via `CONNECTION_TEST_TIMEOUT_MS` (default 30s).

**Per-connection single-flight OAuth refresh (Test Connection now refreshes)** —
this fork's OAuth refresher had no per-connection lock, so two concurrent
refreshes of the *same* connection could both send the same single-use refresh
token; the loser got `refresh_token_reused` / `invalid_grant` and a healthy
account was bricked until re-login. Because of that, the original Test
Connection deliberately refused to refresh and reported `Token expired`
instead.

That is fixed. `oauth.RefreshStoredConnection` is now the single entry point
for refreshing a stored connection. It takes a per-connection lock for the whole
`read → refresh → persist` sequence and **re-checks expiry inside the lock**, so
N concurrent callers produce exactly one exchange and the rest adopt the token
the winner wrote. Both the chat path (`refreshOAuthTokenIfExpired`,
`forceRefreshOAuthToken`) and the dashboard probe go through it, so a
"Test Connection" click can no longer race a live chat request on the same
refresh token.

Test Connection therefore refreshes an expired token *before* probing and
reports `refreshed: true` when it did. A failed refresh is not fatal: the probe
still runs and reports the real upstream verdict (401/403). The batch endpoint
stays sequential by default, but that is now pacing rather than a correctness
requirement.

**Provider taxonomy** — the exact category scheme from the Next.js build
(`free`, `freeTier`, `oauth`, `apikey`, `webCookie`, `custom`), plus display
metadata for 119 registry entries (76 live providers): icon, brand colour,
website, priority, service kinds, and deprecation risk notices.

## API endpoints added

```
GET    /dashboard
GET    /api/dashboard/stats
GET    /api/dashboard/providers
POST   /api/dashboard/providers
GET    /api/dashboard/providers/{id}
POST   /api/dashboard/providers/{id}/toggle-all
GET    /api/dashboard/providers/{id}/models          # live fetch (?save=1 to persist)
POST   /api/dashboard/providers/{id}/models          # add custom model
DELETE /api/dashboard/providers/{id}/models?modelId= # remove model
POST   /api/dashboard/models/test                    # ping one model
POST   /api/dashboard/providers/{id}/test-models     # batch test + auto-disable
POST   /api/dashboard/connections/{id}/client-profile # set Antigravity ide|cli
```

All accept the same `Authorization: Bearer <api-key>` used by the proxy.

## Building

Go 1.26 is required. Official Go 1.27 toolchains are **not** available for
`android/arm64`, so local builds must pin the toolchain:

```sh
GOTOOLCHAIN=local GOEXPERIMENT=jsonv2 go build -ldflags="-s -w" -o 9router-go ./cmd/9router-go
```

`GOEXPERIMENT=jsonv2` is needed because the code imports `encoding/json/v2`.
The `Makefile` exports both variables, so `make build` works as-is.

Cross-compile for a VPS:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o 9router-go-linux-amd64 ./cmd/9router-go
```

## Running

```sh
./9router-go --port 20129 --db-path "$HOME/.9router/db/data.sqlite"
```

Then open `http://localhost:20129/` and paste your API key in the UI.

Flags: `--port`, `--db-path`, `--data-dir` (with tilde expansion).

## Tests

```sh
GOTOOLCHAIN=local GOEXPERIMENT=jsonv2 go test -short ./internal/handlers/dashboard/... ./internal/db/... ./internal/providers/...
```

The OAuth refresh and connection-test packages carry concurrency tests that are
only meaningful under the race detector:

```sh
GOTOOLCHAIN=local GOEXPERIMENT=jsonv2 go test -race ./internal/proxy/oauth/ ./internal/handlers/dashboard/
```

`TestRefreshStoredConnection_ConcurrentCallersRefreshOnce` asserts that N
concurrent callers produce exactly one token exchange, and
`TestRefreshExpiredTokenForTest_AdoptsConcurrentRefresh` asserts that a caller
which lost the race still probes with the winner's fresh token rather than its
stale copy.

The `TestLiveE2E_*` tests in `internal/handlers/chat` hit real upstreams with the
connections in your local `~/.9router/db/data.sqlite`; they fail (rather than
skip) when a stored credential is missing or out of quota, so a red
`TestLiveE2E_*` is not necessarily a regression. Run them with
`-skip TestLiveE2E` for a hermetic unit run.

## Notes and gotchas

- The provider detail panel is fully re-rendered on every state change — each
  model test, add, import, and remove calls `renderProviderDetail` again. The
  open sub-tab therefore lives in the `activeDetailTab` variable, not in the DOM:
  `renderProviderDetail` reads it to mark the active button and show the right
  pane. It used to hard-code Connections as active, so testing a model snapped
  the operator back to the Connections tab on every result. `openProviderDetail`
  takes a `keepTab` flag; actions taken *inside* the panel pass `true`, while
  opening a provider from the grid resets to Connections.
- The model cache (`cachedProviderModels`) is keyed by **short alias** (`ag`),
  not the canonical provider ID (`antigravity`). `ResolveModelCacheKey` writes
  new models under whichever key already holds data, otherwise the router never
  sees them.
- Each model row shows the **full id the router expects** (`<alias>/<modelId>`,
  e.g. `cbai/deepseek-v4.1-flash`) on top, and the upstream's own label beneath
  it when it differs. The label is stored in the otherwise-unused
  `cachedProviderModels.capabilities` column as `{"name":"…"}` — no schema
  migration — and a re-import never blanks an already-stored name.
- The green **Free** badge is ported from OmniRoute's
  `src/shared/utils/freeModels.ts` + `open-sse/config/freeModelCatalog.data.ts`,
  in `internal/providers/freemodels.go`. A model is free when its id is in the
  shipped catalogue for that provider, or — only for a provider with a
  documented free tier — it carries a payload signal (`isFree: true`, a `:free`
  suffix, or zero prompt *and* completion prices). The `free` field is only a
  signal when it is literally `true`/`"true"`/`"free"`, so a truthy-but-not-`true`
  value such as the string `"false"` never badges. CodeBuddy has **no**
  documented free tier, so `cbai/glm-5.2` and friends are never free no matter
  the payload; the catalogue is deliberately small (only providers this fork
  routes) because a missing entry fails safe to "not free".
- Model IDs contain `/`, so the delete endpoint takes `?modelId=` as a query
  parameter rather than a path segment.
- `/models` responses are capped at 2 MB. OpenRouter returns 443 models; an
  uncapped read could exhaust memory on a phone.
- Go ignores files with a leading dot, so scratch Go scripts must not be named
  `.something.go`, and `/tmp` is not writable under Termux.
- Only non-offline asset is the Material Symbols icon font from Google
  Fonts. Embedding a subset would make the UI fully offline; not done yet.
- Antigravity is one backend behind two official client fingerprints (the IDE
  app and the standalone CLI, `agy`). They share an OAuth client id and return
  the same catalogue from `fetchAvailableModels`; only the `User-Agent` differs.
  9router-go has no separate `agy` provider — the profile is a per-connection
  setting on `antigravity` instead, stored under
  `providerSpecificData.clientProfile` (`ide` | `cli`, legacy `harness`/`sdk`
  read as `cli`). The `User-Agent` is injected as
  `ProviderConfig.StaticHeaders["User-Agent"]` in `getProviderConfig`, so chat,
  search and discovery all agree without threading connection data everywhere.
- Dashboard endpoints trust the caller (they are registered outside the
  `RequireApiKey` group). This is pre-existing fork behaviour; the browser UI
  authenticates via `POST /api/dashboard/auth/verify` and stores the key.
- Antigravity accounts can be added by signing in with Google instead of
  pasting a token:
  - `GET  /api/oauth/antigravity/authorize?profile=ide|cli` → consent URL +
    `state` + PKCE `codeVerifier` (10-minute TTL, single-use).
  - `POST /api/oauth/antigravity/exchange` → finishes the flow from a pasted
    callback URL or a raw `code`. Key-protected (dashboard only).
  - `GET  /oauth/antigravity/callback` → public loopback callback. Mounted in
    `SetupServerRouter`, **outside** `RequireApiKey`, because Google's redirect
    arrives with no `Authorization` header. Safe: it only completes a flow whose
    PKCE verifier is held in this process's in-memory store.
  The consent URL deliberately omits the `openid` scope to avoid Google's
  hanging native-app consent, and requests `access_type=offline` for a refresh
  token. Enrichment (account email, Cloud Code project id) is best-effort: a
  failure there still stores a usable connection whose project is discovered
  lazily. On a remote/headless server the loopback callback never arrives, so
  the dashboard keeps the paste-the-callback-URL field as the fallback.
- Antigravity OAuth client credentials are already embedded in
  `internal/providers/oauth.go` (`KnownOAuthConfigs["antigravity"]`) and match
  the public installed-app client OmniRoute ships; `envOr` overrides still win.
- CodeBuddy accounts can also be added by signing in, using Tencent's custom
  *device-auth* handshake (no client_id/secret — the client ships none). The two
  regions are **genuinely different endpoints**, not one backend with two names,
  so the region config (host, `platform`, headers, User-Agent) is threaded
  through every call (mirrors VansRouter's
  `open-sse/providers/registry/codebuddy-{cn,intl}.js`):
  - `codebuddy-cn` → `copilot.tencent.com`, `platform=CLI`, `CLI/2.63.2` UA.
  - `codebuddy-intl` → `www.codebuddy.ai`, `platform=ide`, `IDE/2.63.2` UA,
    `X-Domain: www.codebuddy.ai`.
  - `GET  /api/oauth/codebuddy/authorize?provider=codebuddy-cn|codebuddy-intl`
    → `{ state, authUrl }`. It POSTs `{stateUrl}?platform=<platform>` with a
    `{}` body; the platform **must** be a query param — body-only returns
    `400 "platform is empty"`.
  - `POST /api/oauth/codebuddy/exchange` `{provider, state, name?}` → polled by
    the dashboard. Answers `202` while the operator has not approved yet
    (upstream `code 11217 RetryFetchToken`) and `200` once the account is saved.
  The poll is a **GET with `state` in the query string**, not POST/body — that
  detail is what makes it match the real client. The flow is stateless
  server-side (the browser holds the state), so it works on a headless server
  with no loopback callback. Aliases `cbcn`/`cbai` are accepted.
- CodeBuddy token **refresh** is likewise non-standard: it POSTs a `{}` body and
  carries the token in the **`X-Refresh-Token` header** (plus
  `X-Auth-Refresh-Source: plugin`, `X-Domain` = region host), per VansRouter's
  `tokenRefresh/providers.js#refreshCodebuddyToken`. Handled by
  `internal/proxy/oauth/codebuddy.go` (`RefreshCodebuddy`), not the shared
  StandardRefresher, and each region refreshes against its own host.

## Contributing back

`main` mirrors upstream, so upstream-friendly changes can be cherry-picked from
`feat/go-dashboard`:

```sh
git checkout main
git cherry-pick <sha>
git push origin main
```

then open a PR against `luqman-v1:main`.
