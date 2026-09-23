package db

import (
	"strings"
	"time"
)

// GetUsageStats aggregates usage for a period, mirroring the VansRouter
// dashboard contract. Short periods (today, 24h) are computed from the raw
// usageHistory rows; longer periods read the pre-aggregated usageDaily blobs.
func (r *Repo) GetUsageStats(period string, now time.Time) (*UsageStats, error) {
	days, useDaily, ok := UsagePeriodDays(period)
	if !ok {
		period = "7d"
		days, useDaily, _ = UsagePeriodDays(period)
	}

	connNames := r.ConnectionNameMap()
	nodeNames := r.ProviderNodeNameMap()
	apiKeyNames := r.APIKeyNameMap()

	stats := &UsageStats{
		ByProvider: make(map[string]UsageBucket),
		ByModel:    make(map[string]UsageBucket),
		ByAccount:  make(map[string]UsageBucket),
		ByAPIKey:   make(map[string]UsageBucket),
		ByEndpoint: make(map[string]UsageBucket),
	}

	var err error
	if useDaily {
		err = r.accumulateDaily(stats, days, connNames, nodeNames, apiKeyNames)
	} else {
		err = r.accumulateHistory(stats, period, now, connNames, nodeNames, apiKeyNames)
	}
	if err != nil {
		return nil, err
	}

	// totalRequests is derived from byProvider, exactly like upstream, rather
	// than kept as a separate counter that could drift.
	stats.TotalRequests = 0
	for _, k := range sortedKeys(stats.ByProvider) {
		stats.TotalRequests += stats.ByProvider[k].Requests
	}

	stats.RecentRequests, _ = r.recentUsageEntries(20)
	return stats, nil
}

// accumulateDaily folds the pre-aggregated daily blobs into the stats.
func (r *Repo) accumulateDaily(stats *UsageStats, days int, connNames, nodeNames, apiKeyNames map[string]string) error {
	fromKey := ""
	if days > 0 {
		now := time.Now()
		y, m, d := now.Date()
		start := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
		fromKey = DateKey(start)
	}

	dayDocs, err := r.GetUsageDailyRange(fromKey, "")
	if err != nil {
		return err
	}

	for _, dateKey := range sortedKeys(dayDocs) {
		day := dayDocs[dateKey]
		stats.TotalPromptTokens += day.PromptTokens
		stats.TotalCompletionTokens += day.CompletionTokens
		stats.TotalCachedTokens += day.CachedTokens
		stats.TotalCost += day.Cost

		for _, prov := range sortedKeys(day.ByProvider) {
			p := day.ByProvider[prov]
			b := stats.ByProvider[prov]
			b.Requests += p.Requests
			b.PromptTokens += p.PromptTokens
			b.CompletionTokens += p.CompletionTokens
			b.CachedTokens += p.CachedTokens
			b.Cost += p.Cost
			stats.ByProvider[prov] = b
		}

		for _, mk := range sortedKeys(day.ByModel) {
			m := day.ByModel[mk]
			rawModel, prov := m.RawModel, m.Provider
			if rawModel == "" {
				rawModel, prov = splitModelKey(mk)
			}
			if prov == "" {
				prov = m.Provider
			}
			key := rawModel + " (" + prov + ")"
			b := stats.ByModel[key]
			b.Requests += m.Requests
			b.PromptTokens += m.PromptTokens
			b.CompletionTokens += m.CompletionTokens
			b.CachedTokens += m.CachedTokens
			b.Cost += m.Cost
			b.RawModel = rawModel
			b.Provider = displayProvider(prov, nodeNames)
			if dateKey > b.LastUsed {
				b.LastUsed = dateKey
			}
			stats.ByModel[key] = b
		}

		for _, connID := range sortedKeys(day.ByAccount) {
			a := day.ByAccount[connID]
			accountName := connNames[connID]
			if accountName == "" {
				accountName = "Account " + shortID(connID) + "..."
			}
			// The account map is keyed by raw connection id; the model and
			// provider live on the bucket itself.
			rawModel, prov := a.RawModel, a.Provider
			key := rawModel + " (" + prov + " - " + accountName + ")"
			b := stats.ByAccount[key]
			b.Requests += a.Requests
			b.PromptTokens += a.PromptTokens
			b.CompletionTokens += a.CompletionTokens
			b.CachedTokens += a.CachedTokens
			b.Cost += a.Cost
			b.RawModel = rawModel
			b.Provider = displayProvider(prov, nodeNames)
			b.ConnectionID = connID
			b.AccountName = accountName
			if dateKey > b.LastUsed {
				b.LastUsed = dateKey
			}
			stats.ByAccount[key] = b
		}

		for _, akKey := range sortedKeys(day.ByApiKey) {
			ak := day.ByApiKey[akKey]
			// The api-key map is keyed by "<masked>|<model>|<provider>"; the
			// display name comes from resolving the masked value back to a
			// stored key, and unrecognised masks fall back to the mask itself.
			masked := ak.APIKey
			if masked == "" {
				masked = akKey
				if i := strings.Index(masked, "|"); i >= 0 {
					masked = masked[:i]
				}
			}
			keyName := apiKeyNames[masked]
			if keyName == "" {
				keyName = masked
			}
			if keyName == "" {
				keyName = "Local (No API Key)"
			}
			rawModel, prov := ak.RawModel, ak.Provider
			if rawModel == "" || prov == "" {
				if i := strings.Index(akKey, "|"); i >= 0 {
					parts := strings.Split(akKey, "|")
					if rawModel == "" && len(parts) > 1 {
						rawModel = parts[1]
					}
					if prov == "" && len(parts) > 2 {
						prov = parts[2]
					}
				}
			}
			lookupKey := masked
			if lookupKey == "" {
				lookupKey = "local-no-key"
			}
			b := stats.ByAPIKey[lookupKey]
			b.Requests += ak.Requests
			b.PromptTokens += ak.PromptTokens
			b.CompletionTokens += ak.CompletionTokens
			b.CachedTokens += ak.CachedTokens
			b.Cost += ak.Cost
			b.RawModel = rawModel
			b.Provider = displayProvider(prov, nodeNames)
			b.APIKey = masked
			b.APIKeyMasked = masked
			b.KeyName = keyName
			b.APIKeyKey = lookupKey
			if dateKey > b.LastUsed {
				b.LastUsed = dateKey
			}
			stats.ByAPIKey[lookupKey] = b
		}

		for _, epKey := range sortedKeys(day.ByEndpoint) {
			ep := day.ByEndpoint[epKey]
			endpoint := ep.Endpoint
			if endpoint == "" {
				endpoint = epKey
			}
			key := endpoint + "|" + ep.RawModel + "|" + ep.Provider
			b := stats.ByEndpoint[key]
			b.Requests += ep.Requests
			b.PromptTokens += ep.PromptTokens
			b.CompletionTokens += ep.CompletionTokens
			b.CachedTokens += ep.CachedTokens
			b.Cost += ep.Cost
			b.Endpoint = endpoint
			b.RawModel = ep.RawModel
			b.Provider = displayProvider(ep.Provider, nodeNames)
			if dateKey > b.LastUsed {
				b.LastUsed = dateKey
			}
			stats.ByEndpoint[key] = b
		}
	}

	return r.overlayLastUsed(stats, days, connNames, nodeNames)
}

// overlayLastUsed replaces the day-granular lastUsed values with precise
// timestamps taken from raw history, so "last used" is not stuck at midnight.
func (r *Repo) overlayLastUsed(stats *UsageStats, days int, connNames, nodeNames map[string]string) error {
	since := ""
	if days > 0 {
		since = time.Now().AddDate(0, 0, -days).UTC().Format(time.RFC3339)
	}
	rows, err := r.GetUsageHistorySince(since)
	if err != nil {
		return err
	}

	bump := func(m map[string]UsageBucket, key string, ts string) {
		if b, ok := m[key]; ok && ts > b.LastUsed {
			b.LastUsed = ts
			m[key] = b
		}
	}

	for _, e := range rows {
		bump(stats.ByModel, e.Model+" ("+e.Provider+")", e.Timestamp)

		if e.ConnectionID != "" {
			name := connNames[e.ConnectionID]
			if name == "" {
				name = "Account " + shortID(e.ConnectionID) + "..."
			}
			bump(stats.ByAccount, e.Model+" ("+e.Provider+" - "+name+")", e.Timestamp)
		}

		apiKeyKey := "local-no-key"
		if e.APIKey != "" {
			apiKeyKey = e.APIKey
		}
		bump(stats.ByAPIKey, apiKeyKey, e.Timestamp)

		endpoint := e.Endpoint
		if endpoint == "" {
			endpoint = "Unknown"
		}
		bump(stats.ByEndpoint, endpoint+"|"+e.Model+"|"+e.Provider, e.Timestamp)
	}
	return nil
}

// accumulateHistory folds raw history rows for short periods.
func (r *Repo) accumulateHistory(stats *UsageStats, period string, now time.Time, connNames, nodeNames, apiKeyNames map[string]string) error {
	cutoff := PeriodCutoff(period, now)
	since := ""
	if !cutoff.IsZero() {
		since = cutoff.UTC().Format(time.RFC3339)
	}
	rows, err := r.GetUsageHistorySince(since)
	if err != nil {
		return err
	}

	add := func(m map[string]UsageBucket, key string, mutate func(*UsageBucket)) {
		b := m[key]
		mutate(&b)
		m[key] = b
	}

	for _, e := range rows {
		tk := parseUsageTokens(e.Tokens)
		prompt := tk.prompt()
		completion := tk.completion()
		cached := tk.cached()
		if prompt == 0 && e.PromptTokens != 0 {
			prompt = e.PromptTokens
		}
		if completion == 0 && e.CompletionTokens != 0 {
			completion = e.CompletionTokens
		}
		cost := e.Cost
		prov := displayProvider(e.Provider, nodeNames)

		stats.TotalPromptTokens += prompt
		stats.TotalCompletionTokens += completion
		stats.TotalCachedTokens += cached
		stats.TotalCost += cost

		add(stats.ByProvider, e.Provider, func(b *UsageBucket) {
			b.Requests++
			b.PromptTokens += prompt
			b.CompletionTokens += completion
			b.CachedTokens += cached
			b.Cost += cost
		})

		add(stats.ByModel, e.Model+" ("+e.Provider+")", func(b *UsageBucket) {
			b.Requests++
			b.PromptTokens += prompt
			b.CompletionTokens += completion
			b.CachedTokens += cached
			b.Cost += cost
			b.RawModel = e.Model
			b.Provider = prov
			if e.Timestamp > b.LastUsed {
				b.LastUsed = e.Timestamp
			}
		})

		if e.ConnectionID != "" {
			name := connNames[e.ConnectionID]
			if name == "" {
				name = "Account " + shortID(e.ConnectionID) + "..."
			}
			add(stats.ByAccount, e.Model+" ("+e.Provider+" - "+name+")", func(b *UsageBucket) {
				b.Requests++
				b.PromptTokens += prompt
				b.CompletionTokens += completion
				b.CachedTokens += cached
				b.Cost += cost
				b.RawModel = e.Model
				b.Provider = prov
				b.ConnectionID = e.ConnectionID
				b.AccountName = name
				if e.Timestamp > b.LastUsed {
					b.LastUsed = e.Timestamp
				}
			})
		}

		apiKeyKey := "local-no-key"
		keyName := "Local (No API Key)"
		masked := ""
		if e.APIKey != "" {
			apiKeyKey = e.APIKey
			masked = MaskAPIKey(e.APIKey)
			keyName = apiKeyNames[e.APIKey]
			if keyName == "" {
				keyName = masked
			}
		}
		add(stats.ByAPIKey, apiKeyKey, func(b *UsageBucket) {
			b.Requests++
			b.PromptTokens += prompt
			b.CompletionTokens += completion
			b.CachedTokens += cached
			b.Cost += cost
			b.RawModel = e.Model
			b.Provider = prov
			b.APIKey = masked
			b.APIKeyMasked = masked
			b.KeyName = keyName
			b.APIKeyKey = apiKeyKey
			if e.Timestamp > b.LastUsed {
				b.LastUsed = e.Timestamp
			}
		})

		endpoint := e.Endpoint
		if endpoint == "" {
			endpoint = "Unknown"
		}
		add(stats.ByEndpoint, endpoint+"|"+e.Model+"|"+e.Provider, func(b *UsageBucket) {
			b.Requests++
			b.PromptTokens += prompt
			b.CompletionTokens += completion
			b.CachedTokens += cached
			b.Cost += cost
			b.Endpoint = endpoint
			b.RawModel = e.Model
			b.Provider = prov
			if e.Timestamp > b.LastUsed {
				b.LastUsed = e.Timestamp
			}
		})
	}
	return nil
}

// recentUsageEntries builds the deduplicated "recent requests" list.
// Consecutive identical calls within the same minute collapse into one entry,
// matching the upstream dedup key.
func (r *Repo) recentUsageEntries(limit int) ([]RecentUsageEntry, error) {
	rows, err := r.GetUsageHistoryRecent(100)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]RecentUsageEntry, 0, limit)
	for _, e := range rows {
		tk := parseUsageTokens(e.Tokens)
		prompt := tk.prompt()
		completion := tk.completion()
		if prompt == 0 && e.PromptTokens != 0 {
			prompt = e.PromptTokens
		}
		if completion == 0 && e.CompletionTokens != 0 {
			completion = e.CompletionTokens
		}
		if prompt == 0 && completion == 0 {
			continue
		}
		minute := e.Timestamp
		if len(minute) > 16 {
			minute = minute[:16]
		}
		key := e.Model + "|" + e.Provider + "|" + itoa(prompt) + "|" + itoa(completion) + "|" + minute
		if seen[key] {
			continue
		}
		seen[key] = true
		status := e.Status
		if status == "" {
			status = "ok"
		}
		out = append(out, RecentUsageEntry{
			Timestamp:        e.Timestamp,
			Model:            e.Model,
			Provider:         e.Provider,
			PromptTokens:     prompt,
			CompletionTokens: completion,
			CachedTokens:     tk.cached(),
			Status:           status,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// GetUsageChart returns the chart buckets for a period.
//
// Short periods bucket by hour (24 buckets); longer periods bucket by day.
func (r *Repo) GetUsageChart(period string, now time.Time) ([]UsagePoint, error) {
	days, _, ok := UsagePeriodDays(period)
	if !ok {
		period, days = "7d", 7
	}

	if period == "today" || period == "24h" {
		return r.hourlyChart(period, now)
	}

	bucketCount := days
	if bucketCount == 0 {
		// "all": span from the earliest day we have to today.
		fromKey, err := r.earliestUsageDateKey()
		if err != nil || fromKey == "" {
			bucketCount = 7
		} else {
			if t, err := time.Parse("2006-01-02", fromKey); err == nil {
				bucketCount = int(now.Sub(t).Hours()/24) + 1
			} else {
				bucketCount = 7
			}
		}
	}

	dayDocs, err := r.GetUsageDailyRange(DateKey(now.AddDate(0, 0, -(bucketCount-1))), "")
	if err != nil {
		return nil, err
	}

	out := make([]UsagePoint, 0, bucketCount)
	for i := 0; i < bucketCount; i++ {
		d := now.AddDate(0, 0, -(bucketCount - 1 - i))
		key := DateKey(d)
		doc := dayDocs[key]
		out = append(out, UsagePoint{
			Label:  d.Format("Jan 2"),
			Tokens: doc.PromptTokens + doc.CompletionTokens,
			Cost:   doc.Cost,
		})
	}
	return out, nil
}

// hourlyChart builds 24 hourly buckets, either for the current calendar day
// ("today") or a rolling 24 hours ("24h").
func (r *Repo) hourlyChart(period string, now time.Time) ([]UsagePoint, error) {
	const bucketCount = 24

	var start time.Time
	if period == "today" {
		y, m, d := now.Date()
		start = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	} else {
		start = now.Add(-bucketCount * time.Hour)
	}

	rows, err := r.GetUsageHistorySince(start.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}

	out := make([]UsagePoint, bucketCount)
	for i := 0; i < bucketCount; i++ {
		out[i] = UsagePoint{Label: start.Add(time.Duration(i) * time.Hour).Format("15:04")}
	}

	windowStart := start
	windowEnd := start.Add(bucketCount * time.Hour)
	for _, e := range rows {
		ts, err := time.Parse(time.RFC3339, e.Timestamp)
		if err != nil {
			// Timestamps are stored with a trailing Z or an offset; try the
			// millisecond form the request-detail writer uses.
			ts, err = time.Parse("2006-01-02T15:04:05.000Z", e.Timestamp)
			if err != nil {
				continue
			}
		}
		t := ts.In(start.Location())
		if t.Before(windowStart) || !t.Before(windowEnd) {
			continue
		}
		idx := int(t.Sub(windowStart) / time.Hour)
		if idx < 0 || idx >= bucketCount {
			continue
		}
		tk := parseUsageTokens(e.Tokens)
		tokens := tk.prompt() + tk.completion()
		if tokens == 0 {
			tokens = e.PromptTokens + e.CompletionTokens
		}
		out[idx].Tokens += tokens
		out[idx].Cost += e.Cost
	}
	return out, nil
}

// earliestUsageDateKey returns the smallest usageDaily dateKey, if any.
func (r *Repo) earliestUsageDateKey() (string, error) {
	var key string
	err := r.db.QueryRow(`SELECT COALESCE(MIN(dateKey), '') FROM usageDaily`).Scan(&key)
	if err != nil {
		return "", err
	}
	return key, nil
}

// displayProvider resolves a provider node id to its display name, falling
// back to the raw id.
func displayProvider(id string, nodeNames map[string]string) string {
	if id == "" {
		return "unknown"
	}
	if name, ok := nodeNames[id]; ok {
		return name
	}
	return id
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
