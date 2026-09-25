// Package keyless seeds the model cache for providers that authenticate with no
// credential at all, so a brand-new install can use them immediately.
//
// A keyless provider (OpenCode Free) owns no connection row, and every model
// list in the gateway — /v1/models, the dashboard's "Available Models" tab — is
// built from the imported model cache. On a fresh database that cache is empty,
// so the provider listed zero models and looked broken until someone worked out
// that they had to open the provider and press "Import from /models". Nothing
// about the provider needs configuring, so that step was pure friction.
package keyless

import (
	"context"
	"fmt"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// SeedIfEmpty writes the shipped keyless catalogue into the model cache for
// every keyless provider that currently has no models of its own.
//
// Two guards keep this from fighting the operator:
//   - it only seeds a provider whose cache is completely empty, so a provider
//     they curated by hand is never rewritten or topped up;
//   - it skips any model they explicitly removed (HideModel), so a deletion is
//     not undone on the next restart.
//
// Seeding is best-effort: a failure is logged, never fatal. A gateway that
// refuses to boot because a convenience seed failed would be strictly worse
// than one that boots with a provider needing a manual import.
func SeedIfEmpty(ctx context.Context, repo *db.Repo) error {
	if repo == nil {
		return nil
	}
	var firstErr error
	for _, meta := range providers.NoAuthProviders() {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids := providers.KeylessModelIDs(meta.ID)
		if len(ids) == 0 {
			continue
		}

		keys := append([]string{meta.ID, providers.ResolveAlias(meta.ID)}, providers.AliasesFor(meta.ID)...)
		existing, err := repo.ListCachedModels(keys...)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("seed %s: %w", meta.ID, err)
			}
			continue
		}
		if len(existing) > 0 {
			continue // the operator already has this provider populated
		}

		hidden, _ := repo.GetAllHiddenModels()
		seeded := 0
		for _, modelID := range ids {
			if isHidden(hidden, keys, modelID) {
				continue // deliberately removed; do not resurrect it
			}
			if err := repo.AddCachedModelWithName(meta.ID, modelID, "llm", "keyless", ""); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("seed %s/%s: %w", meta.ID, modelID, err)
				}
				continue
			}
			seeded++
		}
		if seeded > 0 {
			log.Info("keyless", "seeded free models", "provider", meta.ID, "count", seeded)
		}
	}
	return firstErr
}

func isHidden(hidden map[string]bool, providerKeys []string, modelID string) bool {
	for _, k := range providerKeys {
		if k != "" && hidden[k+"|"+modelID] {
			return true
		}
	}
	return false
}
