package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"9router/proxy/internal/log"
)

// GetUsageDaily returns the daily usage JSON data for a given date key.
func (r *Repo) GetUsageDaily(dateKey string) (string, error) {
	var data string
	err := r.db.QueryRow(`SELECT data FROM usageDaily WHERE dateKey = ?`, dateKey).Scan(&data)
	if err != nil {
		return "", fmt.Errorf("get daily usage %s: %w", dateKey, err)
	}
	return data, nil
}

// InsertUsageHistory logs a single request's token usage to the usageHistory table.
//
// Before inserting it looks for an existing row with the same timestamp,
// provider, model, connection, key and token counts. This mirrors upstream
// VansRouter's saveRequestUsage guard, which exists so a retried or duplicated
// completion callback does not append the same request twice. When such a row
// exists the only thing updated is a previously-empty endpoint: the token
// counts already recorded are left alone rather than summed, because a re-call
// that reports the same numbers is the same request, not an additional one.
func (r *Repo) InsertUsageHistory(provider, model, connectionID, apiKey, endpoint string, promptTokens, completionTokens int, cost float64, status string, totalTokens int, meta string, tokensJSON string) error {
	timestamp := time.Now().UTC().Format(time.RFC3339)

	// COALESCE on the text columns so a NULL written by an older version compares
	// equal to the empty string a newer one passes in; without it the guard never
	// matches legacy rows and they duplicate exactly as before.
	var existingID int64
	var existingEndpoint sql.NullString
	err := r.db.QueryRow(
		`SELECT id, endpoint FROM usageHistory
		  WHERE timestamp = ?
		    AND COALESCE(provider, '') = COALESCE(?, '')
		    AND COALESCE(model, '') = COALESCE(?, '')
		    AND COALESCE(connectionId, '') = COALESCE(?, '')
		    AND COALESCE(apiKey, '') = COALESCE(?, '')
		    AND promptTokens = ?
		    AND completionTokens = ?
		  ORDER BY id DESC LIMIT 1`,
		timestamp, provider, model, connectionID, apiKey, promptTokens, completionTokens,
	).Scan(&existingID, &existingEndpoint)
	switch {
	case err == nil:
		// JS truthiness in upstream's `if (!existing.endpoint)` treats both NULL
		// and '' as empty. In Go, sql.NullString reports Valid for '', so an
		// explicit empty check is needed or the backfill never fires for the
		// common case of a row written with an empty endpoint rather than NULL.
		if (!existingEndpoint.Valid || existingEndpoint.String == "") && endpoint != "" {
			if _, uerr := r.db.Exec(
				`UPDATE usageHistory SET endpoint = ? WHERE id = ?`, endpoint, existingID); uerr != nil {
				return fmt.Errorf("backfill usage endpoint: %w", uerr)
			}
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		// A lookup failure must not drop the row: logging usage is more
		// important than the duplicate guard, so fall through and insert.
		log.Debug("usage", "duplicate lookup failed, inserting anyway", "err", err)
	}

	_, err = r.db.Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timestamp, provider, model, connectionID, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokensJSON, meta,
	)
	if err != nil {
		return fmt.Errorf("insert usage history: %w", err)
	}
	return nil
}

// UpsertUsageDaily inserts or replaces a daily usage aggregation record.
// The data parameter should be a JSON string matching the 9Router daily aggregation format.
// NOTE: INSERT OR REPLACE is an atomic full-row replace of the pre-merged JSON
// blob. Merging happens in-process (see handlers/chat/usage.go dailyUsageMu), so
// concurrent writers from MULTIPLE processes can still clobber each other. This
// is documented as single-writer unless the aggregation moves SQL-side.
func (r *Repo) UpsertUsageDaily(dateKey string, data string) error {
	_, err := r.db.Exec(
		`INSERT OR REPLACE INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey, data,
	)
	if err != nil {
		return fmt.Errorf("upsert daily usage %s: %w", dateKey, err)
	}
	return nil
}

// InsertRequestDetail logs a request detail record for the Recent Requests dashboard tab.
func (r *Repo) InsertRequestDetail(id, provider, model, connectionID, status string, data string) error {
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	_, err := r.db.Exec(
		`INSERT OR IGNORE INTO requestDetails (id, timestamp, provider, model, connectionId, status, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, timestamp, provider, model, connectionID, status, data,
	)
	if err != nil {
		return fmt.Errorf("insert request detail %s: %w", id, err)
	}
	return nil
}

// UpdateConnectionLastUsed records that a connection served a request: it stamps
// lastUsedAt and advances the run counter that account round-robin reads.
//
// resetRun is set on a handover, where the newly chosen account begins its own
// run and its counter must go back to 1 rather than accumulate what the previous
// holder had.
//
// The timestamp keeps sub-second precision. Second-resolution values collided
// when several accounts were used inside the same second, which made "most
// recently used" ambiguous and sent round-robin back to the same account.
func (r *Repo) UpdateConnectionLastUsed(connectionID string, resetRun bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	query := `UPDATE providerConnections
		SET lastUsedAt = ?, consecutiveUseCount = COALESCE(consecutiveUseCount, 0) + 1
		WHERE id = ?`
	if resetRun {
		query = `UPDATE providerConnections
			SET lastUsedAt = ?, consecutiveUseCount = 1
			WHERE id = ?`
	}
	if _, err := r.db.Exec(query, now, connectionID); err != nil {
		return fmt.Errorf("update connection last used %s: %w", connectionID, err)
	}
	return nil
}
