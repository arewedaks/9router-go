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

**Dashboard login** — password sign-in with a signed session cookie, ported from
VansRouter. The password is a bcrypt hash in settings (default `123456`), the
cookie is `httpOnly` and lasts 24h, and repeated failures lock the client IP
progressively. The UI and `/api/dashboard/*` are gated by Go middleware, so
protection no longer depends on client-side JavaScript. API keys still
authenticate the proxy's `/v1` endpoints. See *Dashboard authentication* under
Notes for the details.

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
- **Cline model import** — Cline publishes two live catalogues on
  `api.cline.bot`:

  ```
  GET /api/v1/ai/cline/models               # full catalogue (~444 rows)
  GET /api/v1/ai/cline/recommended-models   # {recommended, free, clinePass, clineCloud}
  ```

  Neither is an OpenAI-style `/models` route, so the generic registry branch
  answered "Provider cline does not support models listing" and Import could
  never work. Both ids (`cline` and `clinepass`) are now routed to a dedicated
  catalogue handler, mirroring OmniRoute's `clinepassModels.ts`. Two rules keep
  the model namespaces **disjoint**, which is what stops Import from offering a
  subscription-only id on a free connection (the live API answers
  `403 ENTITLEMENT_ERROR` for `cline-pass/*` without an active subscription):

  - `cline` → the full catalogue, **minus** `cline-pass/*` and **minus** rows
    whose declared output modality has no `text` (embedding/rerank entries).
    Rows that omit architecture metadata are kept, so a schema change degrades
    to a longer list instead of an empty one.
  - `clinepass` → **only** the `cline-pass/*` rows, read from the recommended
    endpoint's `clinePass` bucket.

  Cline's WorkOS auth contract (`Bearer workos:<token>` plus the Cline client
  identity headers) is also required for the catalogue call, not just for chat.
  The prefix rule already lived in `chat.NormalizeProviderToken`; `providers.
  ClineAuthHeader` / `ClineClientIdentityHeaders` expose the same rule to
  callers outside that package so there is one definition, not two. A curated
  fallback catalogue is used only when live discovery is unreachable.
- **Cline sign-in** — Cline accounts are added with **Add via Cline sign-in**,
  not by pasting a key. The flow is a plain authorization-code handshake that is
  easy to get wrong, so it is worth spelling out:

  1. Open
     `https://api.cline.bot/api/v1/auth/authorize?client_type=extension&callback_url=<loopback>`.
  2. `api.cline.bot` answers `302` to **WorkOS**, with
     `redirect_uri=https://api.cline.bot/api/v1/auth/callback` — Cline's *own*
     callback. Our loopback URL is never given to WorkOS, which is why no local
     server has to be reachable.
  3. After sign-in, Cline redirects the browser to the `callback_url` we
     supplied, carrying the credential as a **base64-encoded JSON blob**
     (`accessToken` / `refreshToken` / `expiresAt` / `email`), not as an opaque
     code. Cline already did the token exchange, so the common path is a local
     decode with **no network round-trip**.
  4. If that decode fails, the raw code is POSTed to `/api/v1/auth/token` as a
     fallback.

  Because the credential is in the URL, the operator copies the address bar
  back into the form — the same paste-back pattern Antigravity uses, so the
  flow works on a headless server. The access token is stored **without** the
  `workos:` prefix: `providers.ClineAccessToken` adds it on every request, and
  storing the bare token keeps the refresh contract (which sends the bare
  token) correct. Tests cover the padded/unpadded base64 and trailing-byte
  quirks the reference decoder tolerates.

  **Cline can answer `500 {"error":"empty response content","success":false}`
  intermittently** — roughly two in five identical requests to a free model during
  verification, while direct probes with the same token and headers alternated
  `200`/`500` on the same input with no pattern. It is an upstream fault, and the
  proxy correctly forwards it. Do not read a lone failure as a regression in the
  Cline sign-in or duplicate-detection code: re-run the request before drawing a
  conclusion, and compare against a baseline build if unsure.
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
GET    /api/oauth/github/authorize                    # GitHub device-code start
POST   /api/oauth/github/exchange                     # poll + store GitHub Copilot
GET    /api/oauth/codebuddy/authorize                 # CodeBuddy device-code start
POST   /api/oauth/codebuddy/exchange                  # poll + store CodeBuddy
GET    /api/oauth/cline/authorize                     # Cline callback-url start
POST   /api/oauth/cline/exchange                       # decode callback + store Cline
```

All accept the same `Authorization: Bearer <api-key>` used by the proxy.

### Dashboard login (session cookie)

```
GET    /login                              # same SPA shell; renders the sign-in form
GET    /api/dashboard/auth/status          # {requireLogin, hasPassword} — public
POST   /api/dashboard/auth/login           # {password} -> sets auth_token cookie
POST   /api/dashboard/auth/logout          # clears the cookie
POST   /api/dashboard/auth/change-password # needs a session
POST   /api/dashboard/auth/reset-password  # LOCAL ONLY -> back to the default
POST   /api/dashboard/auth/verify          # legacy API-key check (kept)
```

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

Then open `http://localhost:20129/`. You are asked for the **dashboard
password** (default `123456` when unset). Change it from
**Dashboard Security** in the API Keys tab, or reset it to the default with
`POST /api/dashboard/auth/reset-password` from the same machine. LLM clients
authenticate to `/v1` with `Authorization: Bearer sk-…` as before.

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

Some tests in `internal/handlers/chat` also fail **on a clean checkout** — e.g.
`TestHandleAccountFallback_503CapacityLocksCanonicalModel` and the
`TestE2E_Gemini38_*` tool-call tests — so confirm a failure reproduces at
`HEAD` before blaming your change.

## Notes and gotchas

### Dashboard authentication

The dashboard signs in with a **password**, not a pasted API key. The model is
ported from VansRouter (`src/lib/auth/*`, `src/app/api/auth/*`):

- The password is stored as a **bcrypt hash** in `settings.data.password`. An
  empty hash means "unset", and the default is the literal `123456` (override
  with `INITIAL_PASSWORD`). The hash is never returned by any endpoint.
- A successful login mints a signed session token and sets it as the `auth_token`
  cookie (`httpOnly`, `SameSite=Lax`, `Secure` when the request is HTTPS). The
  token is `base64url(payload).base64url(HMAC-SHA256)` — the same shape as a JWT
  but built with `crypto/hmac`, so **no JWT dependency** is added. It carries
  `{authenticated, iat, exp}` and lives **24 hours**.
- The signing secret comes from `JWT_SECRET`, else a 32-byte hex secret is
  generated once under `<data dir>/jwt-secret` (mode `0600`). Because the secret
  persists, sessions survive a restart.
- Failed logins are rate limited **per IP**: 5 failures lock the IP for 30s, then
  2m, 10m, 30m. A correct password while locked is still refused, otherwise the
  lock is pointless. `TRUST_PROXY=true` makes the limiter read
  `X-Forwarded-For`; otherwise the header is ignored so a client cannot rotate it
  to escape the lock.
- The gate itself is Go middleware (`RequireDashboardSession`), so protection no
  longer depends on JavaScript. Three ways in: a valid session cookie, a valid
  **API key** (`Authorization: Bearer sk-…` / `X-API-Key`), or login disabled in
  settings. Browsers hitting `/dashboard` without a session get a `302` to
  `/login`; API calls get a `401` JSON body.
- **API keys were not removed.** They remain the credential for `/v1` and can
  still open the dashboard, so existing scripts and CI keep working. Only the
  browser flow changed.
- **The session cookie also authenticates the browser-facing OAuth routes.**
  The add-account endpoints (`/api/oauth/*`) and the other engine routes share
  one API-key middleware, so a browser that signed in with the password had no
  key to send and every "Sign in" button answered `401` (surfaced in the UI as
  `[object Object]`, because the error body is an object). `RequireApiKey` gained
  an optional session verifier (`RequireApiKeyWithSession`), and the router
  passes `dashH.VerifySession` with an **allow-list** of `/api/oauth/` and
  `/api/dashboard/`. The list matters: an unscoped cookie would also unlock
  `/v1` and `/chat/completions`, quietly turning a browser session into a proxy
  credential. Engine routes stay key-only.
- **Error bodies are rendered, not stringified.** Responses come in two shapes
  (`{error:"…"}` and `{error:{message,type,code}}`); the UI now has one
  `errorMessage()` helper that unwraps both, so an object never prints as
  `[object Object]`.
- **Signing in an account that is already connected no longer creates a second
  row.** It did: each `save*Connection` handler inserted unconditionally, so
  re-running an OAuth flow produced two connections for one account. That is not
  cosmetic — the account fallback logic walks connections in order and locks the
  failing one per model, so a duplicate makes the router "fall back" to the same
  account's quota and lock it twice (the production DB had exactly this for
  `codebuddy-intl`: two rows, six days apart, sharing one JWT `sub`).
  - Identity is provider-specific and read from the credential, not the label:
    CodeBuddy/Antigravity-style JWTs key off the `sub` claim (`jwtSubject`),
    GitHub off the immutable numeric user id (`githubUserId`, falling back to
    login/email), Antigravity off the Google account email. Providers without a
    stable identity (kiro, cloudflare-ai, `openai-compatible-*`) are deliberately
    **not** deduplicated — many connections there are legitimate.
  - The default is to **refuse**: the endpoint answers `409` with a `duplicate`
    object (existing id, name, identity) and no row is written. The UI turns that
    into an "Update that account instead" button instead of dead-ending.
  - `replace:true` on the exchange request refreshes the existing connection
    **in place**, keeping its id and priority, and clears its model locks and
    backoff via `ClearConnectionModelLocks` — the old failure state described the
    old token, so keeping it would leave a healthy account cooling down. The
    clear is scoped to that one connection.
  - `FindDuplicateConnection` matches on the identity only; a renamed account is
    still recognised.

- `EnsureSchema` (`internal/db/schema.go`) now runs at startup. Before this, the
  Go proxy assumed the Next.js dashboard had already migrated the SQLite file, so
  a fresh install errored with `no such table: settings` on the first write. The
  statements are `CREATE TABLE IF NOT EXISTS`, so an existing database is
  untouched.

- The provider detail panel is fully re-rendered on every state change — each
  model test, add, import, and remove calls `renderProviderDetail` again. The
  open sub-tab therefore lives in the `activeDetailTab` variable, not in the DOM:
  `renderProviderDetail` reads it to mark the active button and show the right
  pane. It used to hard-code Connections as active, so testing a model snapped
  the operator back to the Connections tab on every result. `openProviderDetail`
  takes a `keepTab` flag; actions taken *inside* the panel pass `true`, while
  opening a provider from the grid resets to Connections.

- **The current view is remembered across a refresh, via the URL hash.** Before
  this, reloading always landed on Overview: the visible pane was only ever set
  by a click, so there was nothing to restore. The hash is the natural fix — it
  survives a reload and gives back/forward something to move through.
  - Shape: `#overview`, `#providers`, `#combos`, `#keys`, `#health`, `#settings`,
    and `#provider/<id>/<subtab>` for a detail page (`conn`, `caps`, `models`).
    A recognised tab wins; anything else falls back to Overview, so a stale or
    hand-typed hash is harmless.
  - `navigateTab` is the user-facing entry point (select + record). `switchTab`
    only selects, so internal callers that do not represent navigation — such as
    restoring state — do not rewrite the URL and fight the hashchange handler.
  - `writeHash` sets a `writingHash` flag that `hashchange` checks, so a click
    does not trigger a second fetch of the same data; a genuine back/forward
    still does.
  - All three sign-in paths (no-login, session cookie, legacy API key) call
    `restoreFromHash()` instead of `loadDashboardStats()`; otherwise a refresh
    would restore the hash only to overwrite it a moment later.
  - `signOut` clears the hash with `history.replaceState` — restoring a provider
    page after signing out would open it against a session that is gone.
  - This also removed a latent bug: `switchTab` read the global `event` to
    highlight the nav button, which only exists for real click events, so every
    programmatic call left the nav bar unlit. Buttons now carry `id="nav-…"`
    and are resolved by that.
  - `ui_navigation_test.go` asserts against the **served** bytes (not the file on
    disk), covering the helpers, the three restore call sites, the absence of
    the `event` global, and that every nav button records the hash.
  - Restoring after `checkAuth()` resolves was not enough on its own: the pane
    was still hard-coded `active` in the markup, so the browser painted Overview
    first and then snapped to the real view — a visible flicker on every refresh
    that got worse as the auth round-trip got slower. The `active` class is now
    gone from the static markup, and a **synchronous inline bootstrap** placed
    just after `</main>` (after the panes are parsed, before the main bundle)
    pre-selects the pane named in the hash. It does no fetching — the bundle
    still loads the data — so the first paint is already correct. Covered by
    `TestUINoFlickerOnRefresh`, which fails if a pane is hard-coded active or if
    the bootstrap slips behind the bundle.

- **Navigation lives in a left sidebar, not the top bar.** The header was out
  of horizontal room and the list only grows; the shell is now a flex row of
  `aside.sidebar` + `.shell` (top bar over `main`).
  - The `.nav-btn` class is deliberately kept even though the pill styling is
    gone, so the navigation tests and the `id="nav-…"` highlight lookup still
    line up.
  - Below 860px the rail becomes a drawer toggled by `#sidebar-toggle`, with a
    scrim behind it. `switchTab` closes it on every navigation, since a drawer
    that stays open after a tap is just a thing in the way.
  - `#page-title` in the top bar is set by both the pre-paint bootstrap and
    `switchTab`, so the title is never wrong for a frame.

- **The old "Token Savers" tab is now the Settings tab**, with a Security
  section above it. Token Savers was already editing exactly the fields
  `GET/POST /api/dashboard/settings` manages, so keeping two pages that write
  the same settings only invited confusion about which one had saved.
  - Settings → Security exposes change-password and reset-password against the
    endpoints the backend already served. The four token toggles and their
    level selectors are unchanged, just relocated.
  - **`reset-password` requires no authentication** — only `isLocalRequest`.
    Anyone able to reach the dashboard from the machine can put the password
    back to the default and sign in. That is upstream behaviour, so the UI does
    not hide it, but it does confirm first, warns on the page, and signs the
    operator out afterwards.
  - The API Keys tab used to carry its own **"Dashboard Security"** block that
    changed the same password — the same job in two places, so an operator could
    edit one form and find the other still showing the old fields. That block
    (and its orphaned `changeDashboardPassword` handler) is removed; Settings →
    Security is the single place for credentials. The API Keys tab keeps only
    client key management. `authHasPassword` is still tracked, since the login
    modal needs to know whether a password exists.
  - This move surfaced a real latent bug: the Settings password fields were
    first given the ids `pw-current`/`pw-new`/`pw-confirm`, which the login
    modal's own first-run password form already used. Duplicate ids make
    `getElementById` return whichever element comes first, so one form would
    silently read or clear the other's fields. Settings now uses `set-cur-pw` /
    `set-new-pw` / `set-confirm-pw`, and `TestUIHasNoDuplicateIDs` fails on any
    repeated id in the served markup.
- **Settings has a Backup & Restore section**, ported from VansRouter's Profile
  page (`src/app/api/settings/database/route.js` + `exportDb`/`importDb` in
  `src/lib/db/index.js`). Two things about the reference are load-bearing and
  kept: a restore **replaces** rather than merges, and both directions re-check
  the dashboard password instead of trusting the session alone.
  - `GET /api/dashboard/database` returns the whole database as one JSON file
    (`Content-Disposition: attachment`, `Cache-Control: no-store`). The password
    travels in an `x-9r-password` header; the UI saves the reply through a Blob
    so the password never reaches the URL bar, history, or an access log.
  - `POST /api/dashboard/database` restores from that file. `POST
    /api/dashboard/database/inspect` describes a file without applying it.
    `internal/dbbackup` does the work: rows are read and written as **maps**, not
    typed structs, because each table keeps its variable parts in a JSON `data`
    column whose keys grow as providers are added — a struct would silently drop
    every field it did not know about, which is the one thing a backup must
    never do.
  - Import is **one transaction**. A failure part-way leaves the previous
    configuration untouched. `TestImportUsesASingleTransaction` holds a
    competing write lock and fails if an import reports success while it is
    held, which is what a table-by-table commit would do.
  - The wipe is **scoped**: settings, connections, nodes, proxy pools, keys,
    combos and the two kv scopes are replaced, but `usageHistory`,
    `requestDetails` and `cachedProviderModels` are preserved. They are derived
    or historical data that no backup carries, and clearing them would destroy
    real records for nothing.
  - Deleting is skipped for tables that are absent, and the optional columns
    (`apiKeys.allowedProviders`/`allowedCombos`/`allowedKinds`,
    `combos.context_length`) are read and bound only when the file has them. A
    clean install does **not** create `proxyPools`/`proxyPoolFitness`, so an
    export that assumed they exist would fail with a 500 there.
  - `settings.password` is carried deliberately: a restore is meant to bring the
    credentials of the machine that produced the file. The hash is checked with
    `bcrypt.Cost` **before** anything is written, because restoring an unusable
    hash succeeds and then locks the operator out with no way back in short of
    editing the database by hand. A hash is *not* accepted as the dashboard
    password itself, so a backup file alone cannot open the dashboard
    (`TestBackupHashIsNotAUsablePassword`).
  - The session secret lives in `dataDir`, not the database
    (`LoadSessionSecret`), so a restore does not invalidate existing sessions and
    a backup can never be used to forge one.
  - **A real VansRouter backup restores as-is.** The two dashboards share these
    tables but split them differently, and the first attempt at a real reference
    file failed on three separate layouts before it would load. Each was a
    difference in *packaging*, not content, so each is now normalised rather
    than rejected:
    - `modelAliases`/`customModels` are objects keyed by alias in the reference
      and lists of `{key, value}` here. `KVScopeRows` decodes either, and sorts
      object keys so a round trip is deterministic. The reference also lists
      `customModels` as bare model objects rather than `{key, value}` rows; the
      key is derivable (`providerAlias|id|type`) and is built during decoding.
    - `providerConnections`, `providerNodes` and `proxyPools` keep the
      provider-specific remainder (`apiKey`, oauth tokens, `baseUrl`, `prefix`,
      `testStatus`, one `modelLock_<model>` flag per locked model) as **flat
      columns**, while this build keeps them inside a JSON `data` column and
      writes no such columns. `foldFlatRow` moves everything that is not a
      known column into `data`. Without it a reference file dies on its first
      row with `NOT NULL constraint failed: providerConnections.data`.
    - Unknown top-level fields (`mitmAlias`, `pricing`, `password`) are ignored
      rather than fatal, so a file with extras still restores.
    A row that already carries a `data` object is passed through untouched, so
    our own exports still round-trip exactly.
  - A **NOT NULL column the file omits** gets a default (`createdAt`/`updatedAt`
    to now, `data` to `{}`) instead of aborting the restore. The reference sets
    those columns, but nothing promises it, and failing an entire import over a
    timestamp the operator cannot act on helps nobody. The atomicity test had to
    move to a trigger the importer genuinely cannot repair (`combos.models`,
    NOT NULL with no default) — its old trigger was a nil `data` column, which is
    now recovered instead.
  - Restoring surfaced a genuine mismatch: `EnsureSchema` had drifted from the
    schema production actually runs — it omitted `apiKeys`' three scoped columns,
    `combos.context_length`, and both proxy tables, and defined
    `cachedProviderModels` with `payload`/`cachedAt` instead of the
    `kind`/`ownedBy`/`capabilities`/`updatedAt` the code queries. A fresh install
    therefore could not store a scoped key, a combo context length, or a proxy
    pool, while a database migrated by the Next.js dashboard had them all.
    `EnsureSchema` now matches production column for column, `dbtest` delegates
    to it rather than keeping a second copy, and
    `TestSchemaStatementsCoverEveryUsedTable` fails if the schema stops covering
    a table or column the code names.
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
- **GitHub Copilot** is a public **device-code** OAuth client (RFC 8628), so
  there is no redirect and no loopback: the operator types a short code at
  `github.com/login/device`. Unlike CodeBuddy, GitHub *does* ship a public
  `client_id` (`KnownOAuthConfigs["github"]`) and **no** client_secret.
  - `GET  /api/oauth/github/authorize` → `{ userCode, verificationUri,
    deviceCode, interval, expiresIn }` from `POST github.com/login/device/code`.
    The dashboard shows `userCode` + `verificationUri`; the raw `deviceCode` is
    only echoed back so the stateless exchange can poll with it.
  - `POST /api/oauth/github/exchange` `{ deviceCode, name?, interval? }` polls
    `POST github.com/login/oauth/access_token` with
    `grant_type=urn:ietf:params:oauth:grant-type:device_code`. `202` while
    `authorization_pending`/`slow_down`, `200` once stored, `410`/`400` when the
    code expired or was denied — the standard RFC 8628 error table, so the poll
    loop is generic.
  - Storing a connection does **two** exchanges: GitHub token → **Copilot
    token** (`GET api.github.com/copilot_internal/v2/token`) → `GET
    api.github.com/user` for the login/email. The Copilot token is the credential
    chat actually sends; the GitHub token is retained as the refresh input.
- Copilot **chat identity is the CLI profile, not `vscode-chat`**. OmniRoute's
  comment is the whole reason: `copilot-developer-cli` "is the catalog-unlock
  lever: it exposes the full entitled model set ... where `vscode-chat` returns a
  narrower list". `internal/providers/github_copilot_profile.go` centralises the
  constants (`copilot-integration-id: copilot-developer-cli`, UA
  `GitHubCopilotChat/1.0.81-6`, `editor-version: vscode/1.110.0`,
  `openai-intent: conversation-agent`, `x-github-api-version: 2026-08-01`) and
  `providers.go` uses the same headers for the `github` provider.
- Copilot **refresh is not OAuth2**. There is no `grant_type=refresh_token` and
  no refresh token in the usual sense: the Copilot bearer is *derived* from the
  long-lived GitHub token. `internal/proxy/oauth/github.go` (`RefreshGitHub`)
  re-derives it with two easy-to-miss details: the scheme is `token <pat>` (**not
  `Bearer`** — the internal endpoint rejects Bearer) and the UA is the legacy
  `GithubCopilot/1.0` (the refresh host rejects the newer CLI UA).

  Consequential detail, learned the hard way against a live account: the
  refresher returns the **GitHub token unchanged in `AccessToken`** and puts the
  derived Copilot bearer in `providerSpecificData.copilotToken` (via
  `TokenResult.ProviderSpecificData`, deep-merged into the stored blob). Writing
  the Copilot token into `AccessToken` looks tidier but breaks the *next*
  refresh, because the refresher then feeds a Copilot token back to GitHub and
  gets `401 Bad credentials`. Any reader of the derived token (the model
  catalogue prefers `copilotToken` over `accessToken`) must see the refreshed
  value, which is exactly what the nested field guarantees.

  Because `OAuthConnectionData.IsExpired()` keys off `expiresAt`, the stored
  expiry describes the **Copilot** token's lifetime — that is the credential
  requests carry, and what should trigger a refresh.
- Copilot **model import** hits `GET api.githubcopilot.com/models` with the
  Copilot token and returns only what the account is *entitled* to. The filter is
  capability-driven (`internal/handlers/dashboard/github_catalog.go`), keyed on
  `capabilities.type == "chat"` plus a chat-shaped `supported_endpoints`
  fallback — not an id allowlist, so a newly entitled model appears with no code
  change. `policy.state` is authoritative: anything with a state other than
  `enabled` is dropped.

  `model_picker_enabled` is deliberately **not** consulted. It means "show this
  in the editor's model picker", not "you may call this". Filtering on it was
  tested against a live account and the catalogue collapsed from 30 models to 0 —
  the account was entitled to 9 models that all reported
  `model_picker_enabled: false`. The second live finding was subtler: a fresh
  `/models` call returned `401` because the *cached* Copilot token had aged out
  while `accessToken` still looked usable, so `HandleImportModels` now refreshes
  the chosen connection on demand before fetching. A static fallback catalogue is
  still served (with a `warning`) when the live call fails, so Import always
  completes.

### Connection health: a toggle is not a connection

**Symptom reported:** the OpenAI-compatible category showed accounts but "none of
them connect".

**What was actually true.** The two `openai-compatible-chat-*` nodes (BAI, Atria
AI) held 53 keys, all `isActive=1`. Every BAI key carried
`lastError: [400] {"error":{"message":"credit insufficient balance: balance=0 …"}}`
and `testStatus: "unavailable"` — the accounts were genuinely out of balance.

**Bug 1 — the dashboard lied.** `HandleStats`, the provider cards and the account
rows all treated `isActive == 1` as "active", so the category read **33/33
active** while nothing could serve a request. Fixed by deriving the status from
health, not the switch position:

- `providers.EffectiveStatus` / `IsEffectivelyActive` (`internal/providers/connection_status.go`)
  are the single source of truth, mirroring upstream
  `getStatusVariant(isActive, effectiveStatus)` (three buckets: success / error /
  default).
- `ProviderSummary` now carries `effectiveStatus`, `isEffectivelyActive` and
  `hasActiveCooldown`; the SPA has matching `effectiveStatusOf()`, `acctDotClass()`,
  `statusPillFor()` and `connStatusClass()` helpers so no widget re-derives it.
- The provider card counts `accounts.filter(isEffectivelyActive).length`, and the
detail page's `activeCount` uses the same predicate.

**Bug 2 — a spent balance healed itself every 30 seconds.** Upstream clears
`testStatus: "unavailable"` back to `"active"` once no cooldown remains
(`providers/page.js`: `testStatus === "unavailable" && !isCooldown ? "active" : …`),
and a lapsed lock *does* mean a transient throttle has passed. But a zero balance
is not a timer: `errorclassify.go` had no quota rule at all, so
`credit insufficient balance` fell through to the 30-second transient default,
surfacing the account as healthy again half a minute after every failure.

- Added the upstream `classify429` vocabulary: `quotaExhaustedPatterns` +
  `LooksLikeQuotaExhausted`, and quota rules at the head of `ErrorRules` with
  `QuotaExhaustedCooldownMs = 1h` (`Quota` flag on `ErrorRule`). A rate limit
  still backs off exponentially; a spent budget is parked for an hour.
- `EffectiveStatusWithError` refuses the timer recovery when the stored
  `lastError` reads as quota/billing exhaustion, so the connection reports
  `quota_exhausted` until real evidence of recovery exists. This is the upstream
  `terminalStatus.ts` idea (`credits_exhausted` is terminal) applied to the
  dashboard's read path only — the request path is unchanged.

**Verified** against the real database: the BAI card went from `33/33` to `0/33`,
`custom.active` from 33 to 0, and the detail page renders
`quota_exhausted` chips (`conn-status err`) with amber dots — no page errors. A
transient `unavailable` with a lapsed lock still recovers to `active`, and a
disabled connection never counts, both pinned by tests.

### A generated key is not a name

**Symptom reported:** why is the provider called
`openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2` when the JSON
already carries the prefix?

The identifier is **correct and intentional** — it is upstream's format, not a
bug. `src/app/api/provider-nodes/route.js` creates the node as:

```js
id: `${OPENAI_COMPATIBLE_PREFIX}${apiType}-${randomUUID()}`,
```

so `openai-compatible-chat-<uuid>` is the *internal key*, while the model-routing
`prefix` (`bai`, `atri`) and the display `name` (`BAI`, `Atria AI`) are separate
fields on the same node. Upstream renders `node.name` everywhere
(`providers/page.js`: `name: node.name || "OpenAI Compatible"`) and never shows
the id.

Our cards and detail titles already resolved `displayName || provider`. The one
place the raw key leaked was the Overview "Grouped Chips", which keyed its
buckets on `p.Provider`:

- `HandleStats` now builds a node map (as `HandleListProviders` already did) and
  buckets `providerTypeCounts` by `node.name`, falling back to the raw key only
  when no node exists.
- The SPA routes the label through a new `niceProviderLabel()` helper, so an
  unresolved key degrades to "OpenAI Compatible" instead of printing a UUID.
- Pinned by `TestStatsChipsUseNodeNameNotGeneratedKey` (backend) and
  `TestUINeverLabelsChipsWithGeneratedProviderKey` (served bytes).

Verified in the browser: the chip reads `BAI: 33`, and a full-page scan finds no
remaining `openai-compatible-chat-<uuid>` text.

### Model ids, not just labels: the prefix must win

**Symptom reported:** model names are still long because they use the uuid
instead of the prefix.

This is a second, deeper layer than the chip label. `GET /v1/models` advertised
1504 of 2068 models as `openai-compatible-chat-<uuid>/model`. Upstream never does
that — `buildConnectedProviderIds` computes:

```js
const outputAlias = (conn?.providerSpecificData?.prefix || alias || providerId).trim();
// ...
entries.push({ id: `${outputAlias}/${modelId}`, owned_by: outputAlias });
```

The prefix always wins. So `customModels.providerAlias` may legitimately hold the
generated node id (that is what a VansRouter-era backup stores), but the model
must be **exported** under the node's prefix.

Two fixes in `HandleModels` (`internal/handlers/chat/chat.go`), backed by new
helpers in `internal/providers/model_alias.go`:

- `OutputAlias(providerID, prefix)` encodes the upstream precedence
  (prefix → static alias → raw id).
- `NormalizeModelAlias(alias, nodePrefixByID)` rewrites a custom model's stored
  alias to the node's prefix when the alias is a known node id, and leaves
  everything else alone. A blank alias still yields a blank result so callers
  never build `/model`.

Verified against the real database: the node-keyed models now export as
`bai/...`, while `prefix-based` requests already routed correctly before this
change (`bai/glm-5.3-flash` reaches upstream; `<uuid>/gpt-5.5` answers
`no active connections for provider: <uuid>`).

**Residual data issue, deliberately not "fixed" in code:** of 2057 custom models,
only 4 resolve to a live node. The other 1504 point at 19 node ids that exist in
*neither* `providerNodes` *nor* `providerConnections` — orphan rows the upstream
VansRouter instance was already carrying (its own backup shipped 66 connections,
2 nodes and 2057 custom models). There is no prefix to recover: no node means no
prefix, and upstream would not list them either, because it only emits models for
aliases backed by an active connection. They are inert (a request for one fails
with `no active connections for provider`), but they pollute `/v1/models`.

Hiding orphans silently was rejected: it is client-visible behaviour and would
paper over a data-integrity problem. The dashboard/cleanup options are tracked as
an open decision rather than guessed at.

**Decision: orphan custom models are no longer advertised (option 2).**

`HandleModels` now builds an `aliveAliases` set — `KnownProviders`,
`ProviderAliasMap`, configured node ids and their prefixes, and every provider
with a connection — and skips any custom model whose alias is not in it. This
mirrors upstream, which only calls `buildConnectedProviderIds` for providers in
`activeConnectionByProvider`; a request for an orphan fails anyway with
`no active connections for provider: <uuid>`.

Including `ProviderAliasMap` is load-bearing: `gh`, `oc`, `cl` and `cbai` are
aliases (`github`, `opencode`, `cline`, `codebuddy-intl`), not connections, and
must keep being listed. Dropping them would have hidden working routes.

Measured on the real database:

| | before | after |
|---|---|---|
| `/v1/models` total | 2069 | **562** |
| uuid-prefixed | 1504 | **0** |

`openrouter` (433), `nvidia` (72), `gh` (31), `oc` (7), `bai` (3), `cl` (2),
`cbai` (1) and `atri` (1) are all preserved; only the 19 orphan node ids, the
unrecognised `agentrouter` alias and their models disappear.

### Every surface, not just /v1/models

The prefix fix above only covered `GET /v1/models`. The same generated key kept
leaking from two more places:

- `GET /v1/models/info` reported `owned_by: "openai-compatible-chat-<uuid>"`.
- `GET /api/dashboard/providers` reported it as `provider` and `registryName`,
  and the detail page showed it as the title and the `sub-id` chip.

The two chat handlers now share `nodePrefixByID()`, so `/v1/models` and
`/v1/models/info` cannot disagree about a model's owner id. The dashboard sets
`displayName`/`registryName` to the node's `name` and exposes a new `prefix`
field for the chip (`atri`); `provider` deliberately **stays** the generated id,
because the UI sends it back when adding or removing models — it is the stable
internal handle, not a label. The detail page also accepts either the id or the
prefix in its URL, since the prefix is what model ids and links use.

A subtle trap: `providers.IsGeneratedNodeID` originally split on the last `-`,
but a uuid contains dashes, so `len(key)-dash-1` was never 36 and the helper
returned `false` for every real node id — the display fields quietly kept the
uuid. It now inspects the trailing 36 characters, which the tests pin.

Final state on the real database for the Atria node:

| surface | before | after |
|---|---|---|
| `/v1/models` | `<uuid>/Atria-Dawn-Preview` | `atri/Atria-Dawn-Preview` |
| `/v1/models/info` `owned_by` | `<uuid>` | `atri` |
| providers `registryName` | `<uuid>` | `Atria AI` |
| detail `displayName` | `<uuid>` | `Atria AI` |
| detail `prefix` | — | `atri` |

The model list *inside* a provider page had the same bug for a different reason:
it composes the label as `"<alias>/<modelId>"` from `currentModelAlias`, which
used to be `d.aliases[0] || d.provider`. A compatible node has no `aliases`
entry — it has a `prefix` — so the fallback rendered the uuid as the prefix
(`openai-compatible-chat-8e96ef73-.../claude-fable-5`). The precedence is now
`prefix → alias → provider`, matching `OutputAlias` on the backend:

| provider page model row | before | after |
|---|---|---|
| BAI node | `<uuid>/claude-fable-5` | `bai/claude-fable-5` |
| Atria node | `<uuid>/Atria-Dawn-Preview` | `atri/Atria-Dawn-Preview` |
| alias-only (`gh`) | `gh/...` (unchanged) | `gh/...` |

Note that `provider` is still the raw key in every JSON payload — it is the
write handle (`addModel`, `removeModel`, `testModel` all post it back). Only the
labels resolve to the prefix/name.

## Contributing back

`main` mirrors upstream, so upstream-friendly changes can be cherry-picked from
`feat/go-dashboard`:

```sh
git checkout main
git cherry-pick <sha>
git push origin main
```

then open a PR against `luqman-v1:main`.
