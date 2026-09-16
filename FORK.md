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

**Provider taxonomy** — the exact category scheme from the Next.js build
(`free`, `freeTier`, `oauth`, `apikey`, `webCookie`, `custom`), plus display
metadata for all 77 providers: icon, brand colour, website, priority, service
kinds, and deprecation risk notices.

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

## Notes and gotchas

- The model cache (`cachedProviderModels`) is keyed by **short alias** (`ag`),
  not the canonical provider ID (`antigravity`). `ResolveModelCacheKey` writes
  new models under whichever key already holds data, otherwise the router never
  sees them.
- Model IDs contain `/`, so the delete endpoint takes `?modelId=` as a query
  parameter rather than a path segment.
- `/models` responses are capped at 2 MB. OpenRouter returns 443 models; an
  uncapped read could exhaust memory on a phone.
- Go ignores files with a leading dot, so scratch Go scripts must not be named
  `.something.go`, and `/tmp` is not writable under Termux.
- The only non-offline asset is the Material Symbols icon font from Google
  Fonts. Embedding a subset would make the UI fully offline; not done yet.

## Contributing back

`main` mirrors upstream, so upstream-friendly changes can be cherry-picked from
`feat/go-dashboard`:

```sh
git checkout main
git cherry-pick <sha>
git push origin main
```

then open a PR against `luqman-v1:main`.
