package dashboard

// Devin CLI model catalogue.
//
// devin-cli has no HTTP surface at all — its registry BaseURL is the pseudo
// scheme devin://acp/stdio and the credential lives in the `devin` CLI's own
// login, not in a connection row. So there is no /models route to call, and the
// generic lookup answered "Provider devin-cli does not support models listing".
//
// The CLI does know the account's entitlement: `devin models list --format json`
// prints exactly the models that account may use. Import shells out to it, which
// keeps the list honest per account instead of freezing one snapshot. When the
// binary is missing or not logged in, the registry catalogue is served so Import
// still completes and the warning says why.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

// devinCLIPath resolves the CLI binary: DEVIN_BIN wins, else PATH.
func devinCLIPath() string {
	if p := strings.TrimSpace(os.Getenv("DEVIN_BIN")); p != "" {
		return p
	}
	return "devin"
}

// fetchDevinModels asks the CLI for the account's own model list.
func (h *Handler) fetchDevinModels(providerID string, timeout time.Duration) (*ModelFetchResult, error) {
	canonical := providers.ResolveAlias(providerID)
	if canonical == "" {
		canonical = "devin-cli"
	}
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 20 * time.Second
	}

	bin := devinCLIPath()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "models", "list", "--format", "json")
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := cmd.Output()
	if err == nil {
		if models := parseDevinModels(out); len(models) > 0 {
			return &ModelFetchResult{
				Provider:  canonical,
				Models:    models,
				Supported: true,
				Warning:   "Live catalogue from `devin models list --format json`.",
			}, nil
		}
	}

	reason := "`devin models list` returned nothing usable"
	if err != nil {
		reason = "`devin models list` failed: " + strings.TrimSpace(err.Error())
	}
	return &ModelFetchResult{
		Provider:  canonical,
		Models:    devinStaticModels(),
		Supported: true,
		Warning:   reason + " — serving the registry catalogue instead.",
	}, nil
}

// devinStaticModels is the registry fallback, sourced from the same entry the
// /v1/models listing uses so the two cannot drift.
func devinStaticModels() []UpstreamModel {
	ids := providers.GetProviderModels("devin-cli")
	out := make([]UpstreamModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, UpstreamModel{ID: id, Name: id, Kind: "llm"})
	}
	return out
}

// parseDevinModels walks the CLI's JSON and collects every object that names a
// model. The exact envelope (families[] / models[] / flat) is not contractual,
// so the walk keys off the id-bearing fields instead of a struct: a decode that
// fails on a schema change is worse than no live path at all.
func parseDevinModels(raw []byte) []UpstreamModel {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	var out []UpstreamModel
	seen := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			// Family/group objects carry only a "name"; requiring an id-bearing
			// field keeps those out of the model list.
			id := ""
			for _, k := range []string{"id", "model", "model_id", "modelId", "slug"} {
				if s, ok := v[k].(string); ok && strings.TrimSpace(s) != "" {
					id = strings.TrimSpace(s)
					break
				}
			}
			if id != "" && !seen[id] {
				seen[id] = true
				name := id
				for _, k := range []string{"display_name", "displayName", "label", "title", "name"} {
					if s, ok := v[k].(string); ok && strings.TrimSpace(s) != "" {
						name = strings.TrimSpace(s)
						break
					}
				}
				out = append(out, UpstreamModel{ID: id, Name: name, Kind: "llm"})
			}
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(doc)
	return out
}
