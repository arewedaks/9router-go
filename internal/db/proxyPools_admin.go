package db

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"9router/proxy/internal/handlerutil"
)

// GetProxyPoolDetail returns one pool as the dashboard edit form needs it,
// including the URL list. GetProxyPool is the request-path reader and resolves
// the single-URL legacy shape; this one deliberately returns data as stored so
// an edit round-trips without silently collapsing a multi-URL pool.
func (r *Repo) GetProxyPoolDetail(poolID string) (map[string]any, error) {
	var data string
	var isActive int
	var testStatus *string
	var createdAt, updatedAt string
	err := r.db.QueryRow(
		`SELECT data, isActive, testStatus, createdAt, updatedAt FROM proxyPools WHERE id = ?`, poolID,
	).Scan(&data, &isActive, &testStatus, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("proxy pool %s: %w", poolID, err)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("parse pool data: %w", err)
	}

	out := map[string]any{
		"id":          poolID,
		"isActive":    isActive == 1,
		"createdAt":   createdAt,
		"updatedAt":   updatedAt,
		"name":        handlerutil.GetString(raw, "name"),
		"proxyUrl":    handlerutil.GetString(raw, "proxyUrl"),
		"noProxy":     handlerutil.GetString(raw, "noProxy"),
		"type":        handlerutil.GetString(raw, "type"),
		"strictProxy": raw["strictProxy"] == true,
	}
	for _, key := range []string{"strategy", "urls", "lastTestedAt", "lastError"} {
		if v, ok := raw[key]; ok {
			out[key] = v
		}
	}
	if testStatus != nil {
		out["testStatus"] = *testStatus
	} else {
		out["testStatus"] = "unknown"
	}
	if urls, ok := out["urls"].([]any); ok && len(urls) > 0 {
		out["urlCount"] = len(urls)
	} else if out["proxyUrl"] != "" {
		out["urlCount"] = 1
	} else {
		out["urlCount"] = 0
	}
	return out, nil
}

// UpdateProxyPool rewrites a pool's data blob and/or active flag. Fields left
// nil are left as stored, so the UI can send just {"isActive": false} from the
// inline toggle without round-tripping the whole record.
func (r *Repo) UpdateProxyPool(poolID string, name, proxyURL, noProxy, poolType *string, urls []string, isActive *bool, strictProxy *bool) error {
	var data string
	err := r.db.QueryRow(`SELECT data FROM proxyPools WHERE id = ?`, poolID).Scan(&data)
	if err != nil {
		return fmt.Errorf("proxy pool %s: %w", poolID, err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		raw = map[string]any{}
	}
	if raw == nil {
		raw = map[string]any{}
	}

	if name != nil {
		raw["name"] = *name
	}
	if noProxy != nil {
		raw["noProxy"] = *noProxy
	}
	if poolType != nil && *poolType != "" {
		raw["type"] = *poolType
	}
	if strictProxy != nil {
		raw["strictProxy"] = *strictProxy
	}
	// urls and proxyUrl are one logical value: a pool carries either a list or a
	// single URL. Accepting both and keeping the other stale would leave two
	// answers to "what does this pool proxy through", so writing one clears the
	// other.
	if urls != nil {
		raw["urls"] = urls
		if len(urls) > 0 {
			raw["proxyUrl"] = urls[0]
		} else {
			delete(raw, "proxyUrl")
		}
	} else if proxyURL != nil {
		if *proxyURL == "" {
			delete(raw, "proxyUrl")
		} else {
			raw["proxyUrl"] = *proxyURL
		}
		delete(raw, "urls")
	}

	dataBytes, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal pool data: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	if isActive != nil {
		active := 0
		if *isActive {
			active = 1
		}
		_, err = r.db.Exec(`UPDATE proxyPools SET data = ?, isActive = ?, updatedAt = ? WHERE id = ?`,
			string(dataBytes), active, now, poolID)
	} else {
		_, err = r.db.Exec(`UPDATE proxyPools SET data = ?, updatedAt = ? WHERE id = ?`,
			string(dataBytes), now, poolID)
	}
	if err != nil {
		return fmt.Errorf("update proxy pool: %w", err)
	}
	InvalidateProxyPoolCache(poolID)
	return nil
}

// DeleteProxyPool removes a pool. It refuses when any provider connection still
// selects the pool, because deleting a pool another row points at turns that
// provider's egress into a silent direct connection.
//
// Returns the number of blocking references so the API can report it, matching
// the reference dashboard's 409 + boundConnectionCount behaviour.
func (r *Repo) DeleteProxyPool(poolID string) (int, error) {
	// A pool that is already gone is reported as such: answering "deleted" for a
	// no-op makes a double-click look like it removed something.
	var exists int
	if err := r.db.QueryRow(`SELECT COUNT(1) FROM proxyPools WHERE id = ?`, poolID).Scan(&exists); err != nil {
		return 0, fmt.Errorf("look up proxy pool: %w", err)
	}
	if exists == 0 {
		return 0, ErrProxyPoolNotFound
	}

	bound, err := r.countProxyPoolReferences(poolID)
	if err != nil {
		return 0, err
	}
	if bound > 0 {
		return bound, nil
	}
	if _, err := r.db.Exec(`DELETE FROM proxyPools WHERE id = ?`, poolID); err != nil {
		return 0, fmt.Errorf("delete proxy pool: %w", err)
	}
	InvalidateProxyPoolCache(poolID)
	return 0, nil
}

// ErrProxyPoolNotFound reports that a pool id does not exist, so callers can
// answer 404 rather than a generic failure.
var ErrProxyPoolNotFound = errors.New("proxy pool not found")

// countProxyPoolReferences counts provider strategies whose proxyPoolId or
// targetProxyPoolIds mentions this pool.
//
// The strategies live inside the single settings row as JSON, not in a table of
// their own, so they are read through GetSettings rather than queried directly.
func (r *Repo) countProxyPoolReferences(poolID string) (int, error) {
	s, err := r.GetSettings()
	if err != nil || s == nil {
		// No readable settings means no strategy can reference a pool.
		return 0, nil //nolint:nilerr // absent settings is not a reference
	}

	count := 0
	for _, strat := range s.ProviderStrategies {
		if strat.ProxyPoolID == poolID {
			count++
			continue
		}
		for _, t := range strat.TargetProxyPoolIds {
			if t == poolID {
				count++
				break
			}
		}
	}
	return count, nil
}

// RecordProxyPoolTest stores the outcome of a connectivity test so the list can
// show which pools are known-bad without re-testing on every page load.
func (r *Repo) RecordProxyPoolTest(poolID string, ok bool, testErr string) error {
	var data string
	err := r.db.QueryRow(`SELECT data FROM proxyPools WHERE id = ?`, poolID).Scan(&data)
	if err != nil {
		return fmt.Errorf("proxy pool %s: %w", poolID, err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		raw = map[string]any{}
	}
	if raw == nil {
		raw = map[string]any{}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	raw["lastTestedAt"] = now
	if testErr == "" {
		delete(raw, "lastError")
	} else {
		raw["lastError"] = testErr
	}

	dataBytes, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal pool data: %w", err)
	}
	status := "error"
	if ok {
		status = "active"
	}
	if _, err := r.db.Exec(`UPDATE proxyPools SET data = ?, testStatus = ?, updatedAt = ? WHERE id = ?`,
		string(dataBytes), status, now, poolID); err != nil {
		return fmt.Errorf("record proxy pool test: %w", err)
	}
	InvalidateProxyPoolCache(poolID)
	return nil
}

// InvalidateProxyPoolCache drops a cached pool so the next request re-reads it.
// Exported because the request path and the dashboard both mutate pools.
func InvalidateProxyPoolCache(poolID string) {
	proxyPoolCache.Delete(poolID)
}
