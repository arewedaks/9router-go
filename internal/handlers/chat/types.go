package chat

import (
	"net/http"
	"sync"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/promptcache"
)

type comboStickyState struct {
	Index               int
	ConsecutiveUseCount int
	// ServingIndex is the model index currently owning the turn. Mid-turn
	// requests reuse it so a tool-use sequence stays on the same provider,
	// even after Index has advanced for the next turn.
	ServingIndex int
}

// ChatHandler handles /v1/chat/completions (OpenAI) and /v1/messages (Claude) endpoints.
type ChatHandler struct {
	Repo        *db.Repo
	Client      *http.Client
	TokenSaver  *shared.TokenSaverConfig
	PromptCache *promptcache.Cache
	stickyMu    sync.Mutex
	stickyState map[string]*comboStickyState
	// leases backs cross-process coordination for providers that share one
	// upstream session across processes (free-pi). Nil disables it.
	leases executor.LeaseStore
}

// Type aliases for shared types
type ModelInfo = shared.ModelInfo
type ConnectionData = shared.ConnectionData
type UsageLogInfo = shared.UsageLogInfo
type streamMetrics = shared.StreamMetrics
type upstreamError = proxy.UpstreamError
