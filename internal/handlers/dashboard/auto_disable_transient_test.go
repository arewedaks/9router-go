package dashboard

import (
	"strings"
	"testing"
)

// A rate-limited probe must not disable a model.
//
// Providers answer 429 when a batch asks for too much at once, which a parallel
// run over a 110-model node reliably triggers against its own upstream. The
// model is healthy; the provider is busy. Treating that as a failure deleted
// working models — observed live: three models marked "failed" by a parallel run
// all answered 200 when retried a minute later, after the limiter's cooldown.
//
// The same applies to 5xx: a gateway error says nothing about whether the model
// exists.
func TestAutoDisableSkipsRateLimitAndServerErrors(t *testing.T) {
	_, repo, _ := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	h := &Handler{repo: repo}

	// Statuses that mean "try again", not "this model is gone".
	for _, status := range []int{408, 429, 500, 502, 503, 504, 529} {
		model := "busy-" + itoa(status)
		h.maybeAutoDisable("nvidia/"+model, modelTestResult{
			ModelID: model, OK: false, Status: status,
		}, true)

		hidden, err := repo.GetAllHiddenModels()
		if err != nil {
			t.Fatalf("GetAllHiddenModels: %v", err)
		}
		if hidden["nvidia|"+model] {
			t.Errorf("HTTP %d disabled a model; that status means retry, not gone", status)
		}
	}
}

// A permanent rejection must still disable the model — otherwise the exclusion
// above would make auto-disable useless.
func TestAutoDisableStillActsOnPermanentFailures(t *testing.T) {
	_, repo, _ := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	h := &Handler{repo: repo}

	for _, status := range []int{400, 401, 403, 404, 410, 422} {
		model := "gone-" + itoa(status)
		h.maybeAutoDisable("nvidia/"+model, modelTestResult{
			ModelID: model, OK: false, Status: status,
		}, true)

		hidden, err := repo.GetAllHiddenModels()
		if err != nil {
			t.Fatalf("GetAllHiddenModels: %v", err)
		}
		if !hidden["nvidia|"+model] {
			t.Errorf("HTTP %d did not disable the model; a permanent rejection should", status)
		}
	}
}

// A status of 0 means the request never produced an HTTP response (connection
// refused, DNS failure). That is an environment problem, not evidence about the
// model, so nothing may be removed.
func TestAutoDisableSkipsUnknownStatus(t *testing.T) {
	_, repo, _ := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	h := &Handler{repo: repo}
	h.maybeAutoDisable("nvidia/no-response", modelTestResult{
		ModelID: "no-response", OK: false, Error: "dial tcp: connection refused",
	}, true)

	hidden, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	if hidden["nvidia|no-response"] {
		t.Error("a transport error disabled a model; nothing was learned about it")
	}
}

// In "full" mode, every model that does not produce a successful ping (including
// 429, timeouts, and 500) must be auto-disabled.
func TestAutoDisableFullModeDisablesAllFailingModels(t *testing.T) {
	_, repo, _ := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	h := &Handler{repo: repo}

	// In full mode, 429 and timeouts must be disabled
	h.maybeAutoDisable("nvidia/rate-limited", modelTestResult{
		ModelID: "rate-limited", OK: false, Status: 429,
	}, true, "full")

	h.maybeAutoDisable("nvidia/timed-out", modelTestResult{
		ModelID: "timed-out", OK: false, TimedOut: true,
	}, true, "full")

	hidden, err := repo.GetAllHiddenModels()
	if err != nil {
		t.Fatalf("GetAllHiddenModels: %v", err)
	}
	if !hidden["nvidia|rate-limited"] {
		t.Error("full mode should disable 429 rate-limited models")
	}
	if !hidden["nvidia|timed-out"] {
		t.Error("full mode should disable timed-out models")
	}
}

// The row must offer a way to read the full error and to copy the model id.
// A clipped tooltip made a rate limit indistinguishable from a missing model,
// which is exactly the distinction that decides whether to delete an entry.
func TestModelRowHasErrorInfoAndCopyButtons(t *testing.T) {
	ui := readEmbeddedUI(t)

	for _, need := range []string{
		`id="model-error-modal"`,
		`id="model-error-body"`,
		`function showModelError`,
		`function copyModelErrorText`,
		`function copyModelId`,
		`onclick="showModelError(`,
		`onclick="copyModelId(`,
	} {
		if !strings.Contains(ui, need) {
			t.Errorf("model row is missing %s", need)
		}
	}
}

// A retryable failure must be labelled differently from a permanent one, and
// must not carry a delete.
func TestModelRowLabelsRetryableFailures(t *testing.T) {
	ui := readEmbeddedUI(t)

	if !strings.Contains(ui, `modelTestRetryable[m.modelId] ? 'rate limited' : 'failed'`) {
		t.Error("the row does not distinguish a retryable failure from a permanent one")
	}
	if !strings.Contains(ui, "&& !modelTestRetryable[modelId]") {
		t.Error("auto-disable does not skip retryable failures on the client")
	}
}

// Parallel runs must be bounded. Promise.all over every model is what provoked
// the rate limits that were then mistaken for dead models.
func TestParallelTestIsBounded(t *testing.T) {
	ui := readEmbeddedUI(t)

	if strings.Contains(ui, "Promise.all(modelIds.map(testOne))") {
		t.Error("parallel test still fires every model at once")
	}
	if !strings.Contains(ui, "const limit = Math.max(1, Math.min(6, modelIds.length))") {
		t.Error("parallel test has no concurrency bound")
	}
}
