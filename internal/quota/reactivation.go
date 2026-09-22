// Package quota reactivates provider accounts whose quota cooldown has expired.
//
// Kimchi's free tier meters usage monthly. When the budget runs out the account
// is parked as "quota_exhausted" with a cooldown timestamp, and the budget
// refills at the start of the next month — not on a fixed timer. Nothing in the
// request path brings such an account back: it is not serving traffic, so it
// never gets a chance to succeed, and it would stay parked forever.
//
// A periodic sweep restores those accounts once their cooldown has passed.
// Ported from VansRouter's src/sse/services/kimchiQuotaReactivation.js.
package quota

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
)

// SweepInterval is how often expired quota cooldowns are checked. The cooldown
// is a month long, so the cadence only needs to be frequent enough that an
// account does not sit idle for long once the month turns.
const SweepInterval = 30 * time.Minute

// monthlyQuotaProviders lists providers whose quota resets on a calendar
// boundary rather than a rolling window, so an exhausted account must be
// reactivated by a sweep.
var monthlyQuotaProviders = []string{"kimchi"}

// reactivatedFields are the credential-blob keys cleared when an account is
// restored. They mirror the fields the exhaustion path sets, so a reactivated
// account is indistinguishable from one that never ran out.
var reactivatedFields = []string{"rateLimitedUntil", "quotaExhaustedAt", "quotaResetsAt"}

// ReactivateExpired restores every exhausted account whose cooldown has passed
// and reports how many it reactivated. Per-account failures are logged and
// skipped so one bad row cannot abort the sweep.
func ReactivateExpired(repo *db.Repo, now time.Time) int {
	reactivated := 0
	for _, provider := range monthlyQuotaProviders {
		conns, err := repo.GetProviderConnections(provider, false)
		if err != nil {
			log.Warn("quota", "reactivation: query failed", "provider", provider, "error", err)
			continue
		}
		for _, conn := range conns {
			if !shouldReactivate(conn.Data, now) {
				continue
			}
			if err := clearExhaustion(repo, conn.ID, conn.Data); err != nil {
				log.Warn("quota", "reactivation failed", "provider", provider, "conn", conn.ID, "error", err)
				continue
			}
			reactivated++
			log.Info("quota", "account reactivated after quota reset", "provider", provider, "conn", conn.ID)
		}
	}
	return reactivated
}

// shouldReactivate reports whether a stored credential blob describes an
// exhausted account whose cooldown has now passed.
//
// Both conditions are required. Without the status check an account parked for
// an unrelated reason (bad key, banned) that happens to carry a stale timestamp
// would be revived. Without a parseable timestamp there is nothing to compare
// against, so the account is left alone.
func shouldReactivate(rawData string, now time.Time) bool {
	var data map[string]any
	if err := json.Unmarshal([]byte(rawData), &data); err != nil {
		return false
	}
	if status, _ := data["testStatus"].(string); status != "quota_exhausted" {
		return false
	}
	raw, _ := data["rateLimitedUntil"].(string)
	if strings.TrimSpace(raw) == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return !now.Before(until)
}

// clearExhaustion rewrites the connection as active with the exhaustion markers
// removed.
//
// The isActive column is set alongside the blob: an exhausted account is parked
// with isActive=0, so clearing only the blob would leave it still excluded from
// the active-connection query.
func clearExhaustion(repo *db.Repo, connID, rawData string) error {
	var data map[string]any
	if err := json.Unmarshal([]byte(rawData), &data); err != nil {
		return err
	}
	for _, k := range reactivatedFields {
		data[k] = nil
	}
	data["testStatus"] = "active"
	data["lastError"] = nil
	data["errorCode"] = nil

	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return repo.ReactivateConnection(connID, string(encoded))
}

// StartBackgroundReactivation sweeps expired quota cooldowns on a timer until
// ctx is cancelled.
func StartBackgroundReactivation(ctx context.Context, repo *db.Repo) {
	go func() {
		// A short initial delay keeps startup I/O down; nothing is time-critical
		// about restoring an account that has been parked for weeks.
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}

		if n := ReactivateExpired(repo, time.Now()); n > 0 {
			log.Info("quota", "initial sweep reactivated accounts", "count", n)
		}

		ticker := time.NewTicker(SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ReactivateExpired(repo, time.Now())
			}
		}
	}()
}
