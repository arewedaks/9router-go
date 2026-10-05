package providers

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/log"
)

const (
	ModelsDevCatalogURL = "https://models.dev/api.json"
	CatalogSyncInterval = 24 * time.Hour
)

// SyncedModelModalities holds extracted capabilities from models.dev for a single model ID.
type SyncedModelModalities struct {
	Vision      bool `json:"vision"`
	PDF         bool `json:"pdf"`
	AudioInput  bool `json:"audioInput"`
	VideoInput  bool `json:"videoInput"`
	ImageOutput bool `json:"imageOutput,omitempty"`
	Tools       bool `json:"tools,omitempty"`
	Reasoning   bool `json:"reasoning,omitempty"`
}

// SyncedModelLimits holds token limits for a provider/model pair.
type SyncedModelLimits struct {
	ContextWindow int `json:"contextWindow,omitempty"`
	MaxOutput     int `json:"maxOutput,omitempty"`
}

// SyncedCatalog represents the processed catalog file written to disk / kept in memory.
type SyncedCatalog struct {
	SyncedAt string `json:"syncedAt"`
	// Models is the cross-provider majority vote per bare model name; see
	// majorityModalities. ProviderModels is the exact per-provider listing.
	Models         map[string]SyncedModelModalities            `json:"models"`
	ProviderModels map[string]map[string]SyncedModelModalities `json:"providerModels,omitempty"`
	Providers      map[string]map[string]SyncedModelLimits     `json:"providers"`
}

type CatalogSyncState struct {
	Running    bool   `json:"running"`
	LastSync   string `json:"lastSync,omitempty"`
	LastError  string `json:"lastError,omitempty"`
	ETag       string `json:"etag,omitempty"`
	ModelCount int    `json:"modelCount"`
}

var (
	catalogMu     sync.RWMutex
	globalCatalog *SyncedCatalog
	syncState     CatalogSyncState
	syncStateMu   sync.Mutex
)

// ProviderAliases maps 9router provider IDs to models.dev provider IDs for limit resolution.
var ProviderAliases = map[string]string{
	"glm":           "zai",
	"glm-cn":        "zhipuai",
	"claude":        "anthropic",
	"gemini":        "google",
	"kimi":          "moonshotai",
	"kimi-cn":       "moonshotai-cn",
	"qwen":          "alibaba",
	"qwen-cn":       "alibaba-cn",
	"zhipu":         "zhipuai",
	"hunyuan":       "tencent",
	"doubao":        "volcengine",
	"cloudflare-ai": "cloudflare-workers-ai",
}

// GetCatalogState returns the current synchronization state.
func GetCatalogState() CatalogSyncState {
	syncStateMu.Lock()
	defer syncStateMu.Unlock()
	return syncState
}

// GetCatalogModalities looks up synced modalities for a provider/model pair.
//
// The provider's own listing wins. Otherwise the cross-provider majority is
// used, but only for names containing a digit: generic router names such as
// "auto" or "free" mean a different model on every provider, so borrowing
// another provider's entry for them would be a guess dressed up as data.
func GetCatalogModalities(provider, model string) *SyncedModelModalities {
	if model == "" {
		return nil
	}
	base := normalizeModelKey(model)

	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if globalCatalog == nil {
		return nil
	}
	prov := strings.ToLower(provider)
	for _, pid := range []string{prov, ProviderAliases[prov]} {
		if pid == "" {
			continue
		}
		if m, ok := globalCatalog.ProviderModels[pid][base]; ok {
			return &m
		}
	}
	if !strings.ContainsAny(base, "0123456789") {
		return nil
	}
	if m, ok := globalCatalog.Models[base]; ok {
		return &m
	}
	return nil
}

// normalizeModelKey strips a provider prefix and any :tag suffix so the result
// matches the keys stored in SyncedCatalog (see SyncModelCatalog).
func normalizeModelKey(model string) string {
	base := strings.ToLower(model)
	if idx := strings.Index(base, "/"); idx != -1 {
		base = base[idx+1:]
	}
	if idx := strings.Index(base, ":"); idx != -1 {
		base = base[:idx]
	}
	return base
}

// GetCatalogLimits returns the token limits synced from models.dev for a
// provider/model pair, or (0, 0) when the catalog has no entry. The provider
// argument may be a 9router provider id (aliased through ProviderAliases) or a
// models.dev provider id.
//
// The catalog only carries an exact match: a model the upstream catalogue does
// not list yields zeros and the caller falls back to pattern guessing, rather
// than silently attributing another provider's limits to this model.
func GetCatalogLimits(provider, model string) (contextWindow int, maxOutput int) {
	if model == "" {
		return 0, 0
	}
	base := normalizeModelKey(model)

	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if globalCatalog == nil || globalCatalog.Providers == nil {
		return 0, 0
	}

	// Try the provider as given, then its models.dev alias.
	provIDs := []string{strings.ToLower(provider)}
	if alias, ok := ProviderAliases[strings.ToLower(provider)]; ok {
		provIDs = append(provIDs, alias)
	}

	for _, pid := range provIDs {
		if pid == "" {
			continue
		}
		byModel, ok := globalCatalog.Providers[pid]
		if !ok {
			continue
		}
		if lim, ok := byModel[base]; ok {
			return lim.ContextWindow, lim.MaxOutput
		}
	}
	return 0, 0
}

// GetCatalogLimitsAnyProvider returns the catalogue limits for a model whose
// provider is unknown (a combo seat written as a bare model id). Providers
// differ only in what they resell, so the most generous listing is the model's
// real capability; the caller takes the largest across the combo's seats.
func GetCatalogLimitsAnyProvider(model string) (contextWindow int, maxOutput int) {
	if model == "" {
		return 0, 0
	}
	base := normalizeModelKey(model)

	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if globalCatalog == nil {
		return 0, 0
	}
	for _, byModel := range globalCatalog.Providers {
		lim, ok := byModel[base]
		if !ok {
			continue
		}
		// Upstream carries entries whose max output equals the whole context
		// window (cortecs/digitalocean/wandb/nebius deepseek-v4.1-flash),
		// which is physically impossible. Drop them, equals included, rather
		// than let one bad row win.
		if lim.ContextWindow > 0 && lim.MaxOutput >= lim.ContextWindow {
			continue
		}
		if lim.ContextWindow > contextWindow {
			contextWindow = lim.ContextWindow
		}
		if lim.MaxOutput > maxOutput {
			maxOutput = lim.MaxOutput
		}
	}
	return contextWindow, maxOutput
}

// LoadCatalogFromFile loads cached catalog from disk if it exists.
func LoadCatalogFromFile(filePath string) error {
	if filePath == "" {
		return nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	var cat SyncedCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return err
	}

	catalogMu.Lock()
	globalCatalog = &cat
	catalogMu.Unlock()

	syncStateMu.Lock()
	syncState.LastSync = cat.SyncedAt
	syncState.ModelCount = len(cat.Models)
	syncStateMu.Unlock()

	return nil
}

// SyncModelCatalog performs a download and parsing pass of models.dev API catalog.
func SyncModelCatalog(ctx context.Context, client *http.Client, filePath string) error {
	syncStateMu.Lock()
	if syncState.Running {
		syncStateMu.Unlock()
		return fmt.Errorf("sync already in progress")
	}
	syncState.Running = true
	syncStateMu.Unlock()

	defer func() {
		syncStateMu.Lock()
		syncState.Running = false
		syncStateMu.Unlock()
	}()

	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ModelsDevCatalogURL, nil)
	if err != nil {
		return fmt.Errorf("create catalog request: %w", err)
	}

	syncStateMu.Lock()
	etag := syncState.ETag
	syncStateMu.Unlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := client.Do(req)
	if err != nil {
		syncStateMu.Lock()
		syncState.LastError = err.Error()
		syncStateMu.Unlock()
		return fmt.Errorf("catalog request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		log.Info("catalog_sync", "catalog not modified (304)")
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		errText := fmt.Sprintf("catalog sync non-200 status: %d", resp.StatusCode)
		syncStateMu.Lock()
		syncState.LastError = errText
		syncStateMu.Unlock()
		return fmt.Errorf("%s", errText)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB limit
	if err != nil {
		return fmt.Errorf("read catalog body: %w", err)
	}

	synced, err := parseModelsDevCatalog(body)
	if err != nil {
		syncStateMu.Lock()
		syncState.LastError = err.Error()
		syncStateMu.Unlock()
		return fmt.Errorf("decode catalog json: %w", err)
	}
	nowStr := time.Now().UTC().Format(time.RFC3339)
	synced.SyncedAt = nowStr

	catalogMu.Lock()
	globalCatalog = synced
	catalogMu.Unlock()
	InvalidateCapabilitiesCache()

	newEtag := resp.Header.Get("ETag")
	syncStateMu.Lock()
	syncState.LastSync = nowStr
	syncState.LastError = ""
	syncState.ETag = newEtag
	syncState.ModelCount = len(synced.Models)
	syncStateMu.Unlock()

	// Write to disk if filePath configured
	if filePath != "" {
		_ = os.MkdirAll(filepath.Dir(filePath), 0755)
		if outBytes, err := json.Marshal(synced, jsontext.WithIndent("  ")); err == nil {
			_ = os.WriteFile(filePath, outBytes, 0644)
		}
	}

	log.Info("catalog_sync", "catalog synchronized successfully", "models", len(synced.Models), "providers", len(synced.Providers))
	return nil
}

// StartBackgroundCatalogSync runs initial sync and 24h timer loop.
func StartBackgroundCatalogSync(ctx context.Context, client *http.Client, filePath string) {
	_ = LoadCatalogFromFile(filePath)

	go func() {
		// Wait 30s after boot for first sync
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}

		if err := SyncModelCatalog(ctx, client, filePath); err != nil {
			log.Warn("catalog_sync", "initial sync failed", "error", err)
		}

		ticker := time.NewTicker(CatalogSyncInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := SyncModelCatalog(ctx, client, filePath); err != nil {
					log.Warn("catalog_sync", "periodic sync failed", "error", err)
				}
			}
		}
	}()
}

// parseModelsDevCatalog turns the models.dev api.json payload into a
// SyncedCatalog. The payload is providerID -> {models: modelID -> model}, where
// a model carries modalities.input/output lists plus tool_call and reasoning.
func parseModelsDevCatalog(body []byte) (*SyncedCatalog, error) {
	var raw map[string]struct {
		Models map[string]struct {
			Modalities struct {
				Input  []string `json:"input"`
				Output []string `json:"output"`
			} `json:"modalities"`
			ToolCall  bool `json:"tool_call"`
			Reasoning bool `json:"reasoning"`
			Limit     *struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	cat := &SyncedCatalog{
		ProviderModels: make(map[string]map[string]SyncedModelModalities),
		Providers:      make(map[string]map[string]SyncedModelLimits),
	}
	for provID, provData := range raw {
		for modelID, m := range provData.Models {
			base := normalizeModelKey(modelID)
			if base == "" {
				continue
			}
			// One provider can list the same base twice (route prefixes,
			// :free tags); OR them so the provider's entry is its best listing.
			if cat.ProviderModels[provID] == nil {
				cat.ProviderModels[provID] = make(map[string]SyncedModelModalities)
			}
			cur := cat.ProviderModels[provID][base]
			for _, in := range m.Modalities.Input {
				switch in {
				case "image":
					cur.Vision = true
				case "pdf":
					cur.PDF = true
				case "audio":
					cur.AudioInput = true
				case "video":
					cur.VideoInput = true
				}
			}
			for _, out := range m.Modalities.Output {
				if out == "image" {
					cur.ImageOutput = true
				}
			}
			cur.Tools = cur.Tools || m.ToolCall
			cur.Reasoning = cur.Reasoning || m.Reasoning
			cat.ProviderModels[provID][base] = cur

			if m.Limit != nil && (m.Limit.Context > 0 || m.Limit.Output > 0) {
				if cat.Providers[provID] == nil {
					cat.Providers[provID] = make(map[string]SyncedModelLimits)
				}
				cat.Providers[provID][base] = SyncedModelLimits{
					ContextWindow: m.Limit.Context,
					MaxOutput:     m.Limit.Output,
				}
			}
		}
	}
	cat.Models = majorityModalities(cat.ProviderModels)
	return cat, nil
}

// majorityModalities folds per-provider listings into one entry per base name,
// keeping a capability only when more than half of the providers listing that
// name agree. A plain OR let one reseller's typo (or a router that happens to
// reuse the name) switch a capability on for everyone.
func majorityModalities(byProv map[string]map[string]SyncedModelModalities) map[string]SyncedModelModalities {
	type tally struct{ n, vis, pdf, aud, vid, img, tools, reason int }
	counts := make(map[string]*tally)
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	for _, models := range byProv {
		for base, m := range models {
			t := counts[base]
			if t == nil {
				t = &tally{}
				counts[base] = t
			}
			t.n++
			t.vis += b2i(m.Vision)
			t.pdf += b2i(m.PDF)
			t.aud += b2i(m.AudioInput)
			t.vid += b2i(m.VideoInput)
			t.img += b2i(m.ImageOutput)
			t.tools += b2i(m.Tools)
			t.reason += b2i(m.Reasoning)
		}
	}
	out := make(map[string]SyncedModelModalities, len(counts))
	for base, t := range counts {
		maj := func(c int) bool { return c*2 > t.n }
		out[base] = SyncedModelModalities{
			Vision:      maj(t.vis),
			PDF:         maj(t.pdf),
			AudioInput:  maj(t.aud),
			VideoInput:  maj(t.vid),
			ImageOutput: maj(t.img),
			Tools:       maj(t.tools),
			Reasoning:   maj(t.reason),
		}
	}
	return out
}
