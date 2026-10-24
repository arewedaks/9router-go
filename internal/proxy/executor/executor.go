package executor

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"9router/proxy/internal/providers"
)

// Request holds all inputs for an executor.
type Request struct {
	Ctx            context.Context
	Client         *http.Client
	Config         *providers.ProviderConfig
	APIKey         string
	Body           []byte
	IsStream       bool
	TranslateResp  bool
	ConnectionID   string            // for OAuth refresh by fallback
	SessionID      string            // client session / conversation id
	ProjectID      string            // for gemini-native (antigravity)
	Provider       string            // canonical provider id (for per-provider request rules)
	ModelName      string            // extracted model name
	Endpoint       string            // custom URL override (azure)
	ToolNameMap    map[string]string // claude OAuth tool-cloak map (suffixed -> original)
	UpstreamClaude bool              // upstream responds in Claude format while client sent OpenAI format
	IsCompact      bool              // Responses-API compaction: appends /compact to the upstream URL
	ResponseBuf    io.Writer         // writer to capture response text for token estimation & logging
	StartTime      time.Time         // request start time for TTFT tracking
	TTFT           *int64            // pointer to TTFT metric (ms to first chunk)
	// Perch account identity, captured at sign-in and sent as the attribution
	// block on every model call. Empty for every other provider.
	PerchUserID      string
	PerchWorkspaceID string
	// Qoder account identity. Qoder resolves the caller from the uid inside the
	// COSY payload, not from the token: a per-request UUID is rejected with 403
	// "Login expired". These come from the sign-in stored on the connection.
	QoderIdentity QoderIdentity
}

// Executor forwards a request upstream and writes the response.
type Executor func(w http.ResponseWriter, req *Request) error

// GetForConfig returns the executor that speaks the config's transport, falling
// back to the provider's own. A Responses-API upstream reuses the codex
// executor: it converts the chat body to Responses format and translates the
// event stream back to chat chunks. Without this a gateway that serves one key
// on several endpoints got chat completions for every model and rejected the
// ones that require /responses.
func GetForConfig(provider string, cfg *providers.ProviderConfig) Executor {
	if cfg != nil && cfg.Format == providers.FormatOpenAIResponses {
		return Get("codex")
	}
	return Get(provider)
}

type executorFactory func() Executor

var (
	registryMu sync.RWMutex
	registry   = map[string]executorFactory{}
)

// Register adds an executor factory for the given provider name.
func Register(provider string, fn executorFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider] = fn
}

// Get returns the executor for the given provider, or nil if not found.
func Get(provider string) Executor {
	registryMu.RLock()
	defer registryMu.RUnlock()
	fn, ok := registry[provider]
	if !ok {
		return nil
	}
	return fn()
}

// Default returns the default OpenAI-compat executor.
func Default() Executor { return ForwardOpenAI }

// IsGeminiNative checks if a config uses gemini-native format.
func IsGeminiNative(cfg *providers.ProviderConfig) bool { return cfg.Format == "gemini-native" }
