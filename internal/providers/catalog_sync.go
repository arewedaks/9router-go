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
	Vision     bool `json:"vision"`
	PDF        bool `json:"pdf"`
	AudioInput bool `json:"audioInput"`
	VideoInput bool `json:"videoInput"`
}

// SyncedModelLimits holds token limits for a provider/model pair.
type SyncedModelLimits struct {
	ContextWindow int `json:"contextWindow,omitempty"`
	MaxOutput     int `json:"maxOutput,omitempty"`
}

// SyncedCatalog represents the processed catalog file written to disk / kept in memory.
type SyncedCatalog struct {
	SyncedAt  string                                  `json:"syncedAt"`
	Models    map[string]SyncedModelModalities        `json:"models"`
	Providers map[string]map[string]SyncedModelLimits `json:"providers"`
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

// GetCatalogModalities looks up dynamically synced modalities for a model ID.
func GetCatalogModalities(model string) *SyncedModelModalities {
	if model == "" {
		return nil
	}
	base := normalizeModelKey(model)

	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if globalCatalog == nil || globalCatalog.Models == nil {
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

	// models.dev format: map of providerID -> providerData { models: map[modelID]modelData }
	var rawData map[string]struct {
		Models map[string]struct {
			Modality map[string]bool `json:"modality"`
			Limit    *struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	}

	if err := json.Unmarshal(body, &rawData); err != nil {
		syncStateMu.Lock()
		syncState.LastError = err.Error()
		syncStateMu.Unlock()
		return fmt.Errorf("decode catalog json: %w", err)
	}

	modelsMap := make(map[string]SyncedModelModalities)
	providersMap := make(map[string]map[string]SyncedModelLimits)

	for provID, provData := range rawData {
		for modelID, mData := range provData.Models {
			base := strings.ToLower(modelID)
			if idx := strings.Index(base, "/"); idx != -1 {
				base = base[idx+1:]
			}
			if idx := strings.Index(base, ":"); idx != -1 {
				base = base[:idx]
			}

			// Aggregate modalities
			cur := modelsMap[base]
			if mData.Modality["image"] || mData.Modality["vision"] {
				cur.Vision = true
			}
			if mData.Modality["pdf"] {
				cur.PDF = true
			}
			if mData.Modality["audio"] {
				cur.AudioInput = true
			}
			if mData.Modality["video"] {
				cur.VideoInput = true
			}
			modelsMap[base] = cur

			// Limits
			if mData.Limit != nil && (mData.Limit.Context > 0 || mData.Limit.Output > 0) {
				if providersMap[provID] == nil {
					providersMap[provID] = make(map[string]SyncedModelLimits)
				}
				providersMap[provID][base] = SyncedModelLimits{
					ContextWindow: mData.Limit.Context,
					MaxOutput:     mData.Limit.Output,
				}
			}
		}
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	synced := &SyncedCatalog{
		SyncedAt:  nowStr,
		Models:    modelsMap,
		Providers: providersMap,
	}

	catalogMu.Lock()
	globalCatalog = synced
	catalogMu.Unlock()
	InvalidateCapabilitiesCache()

	newEtag := resp.Header.Get("ETag")
	syncStateMu.Lock()
	syncState.LastSync = nowStr
	syncState.LastError = ""
	syncState.ETag = newEtag
	syncState.ModelCount = len(modelsMap)
	syncStateMu.Unlock()

	// Write to disk if filePath configured
	if filePath != "" {
		_ = os.MkdirAll(filepath.Dir(filePath), 0755)
		if outBytes, err := json.Marshal(synced, jsontext.WithIndent("  ")); err == nil {
			_ = os.WriteFile(filePath, outBytes, 0644)
		}
	}

	log.Info("catalog_sync", "catalog synchronized successfully", "models", len(modelsMap), "providers", len(providersMap))
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
