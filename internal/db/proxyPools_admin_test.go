package db

import (
	"errors"
	"path/filepath"
	"testing"
)

func newProxyPoolRepo(t *testing.T) *Repo {
	t.Helper()
	database, err := OpenDatabase(filepath.Join(t.TempDir(), "pools.sqlite"))
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := EnsureSchema(database); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return NewRepo(database)
}

func seedPool(t *testing.T, r *Repo, name string, urls []string) string {
	t.Helper()
	pool, err := r.InsertProxyPool(ProxyPoolData{Name: name, URLs: urls, ProxyURL: urls[0]})
	if err != nil {
		t.Fatalf("InsertProxyPool: %v", err)
	}
	id, _ := pool["id"].(string)
	if id == "" {
		t.Fatal("InsertProxyPool returned no id")
	}
	return id
}

// GetProxyPoolDetail must return the URL list as stored, because the edit form
// round-trips through it. GetProxyPool (the request-path reader) collapses to
// the legacy single-URL shape, so using it here would silently shrink a pool on
// every save.
func TestGetProxyPoolDetailPreservesURLList(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "multi", []string{"http://a:1@1.1.1.1:8080", "http://b:2@2.2.2.2:8080"})

	got, err := r.GetProxyPoolDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	urls, ok := got["urls"].([]any)
	if !ok || len(urls) != 2 {
		t.Fatalf("urls = %#v, want the 2 stored entries", got["urls"])
	}
	if got["urlCount"] != 2 {
		t.Errorf("urlCount = %v, want 2", got["urlCount"])
	}
	if got["name"] != "multi" {
		t.Errorf("name = %v, want multi", got["name"])
	}
}

// A single-URL pool is the shape the create endpoint writes, so the detail
// reader has to report one URL for it too.
func TestGetProxyPoolDetailSingleURL(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "single", []string{"http://only@9.9.9.9:3128"})

	got, err := r.GetProxyPoolDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	if got["urlCount"] != 1 {
		t.Errorf("urlCount = %v, want 1", got["urlCount"])
	}
}

// Toggling active must not disturb the proxy data: the inline switch sends only
// isActive, and a partial update that wiped the URL would break routing.
func TestUpdateProxyPoolPartialKeepsURLs(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "keep", []string{"http://u:p@3.3.3.3:8080"})

	off := false
	if err := r.UpdateProxyPool(id, nil, nil, nil, nil, nil, &off, nil); err != nil {
		t.Fatal(err)
	}

	got, err := r.GetProxyPoolDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	if got["isActive"] != false {
		t.Errorf("isActive = %v, want false", got["isActive"])
	}
	if got["proxyUrl"] != "http://u:p@3.3.3.3:8080" {
		t.Errorf("proxyUrl = %v, want it untouched by an isActive-only update", got["proxyUrl"])
	}
}

// Writing a new list must clear the stale single URL, otherwise "which proxy
// does this pool use" would have two answers.
func TestUpdateProxyPoolReplacesURLListAndClearsSingle(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "swap", []string{"http://old@1.1.1.1:8080"})

	if err := r.UpdateProxyPool(id, nil, nil, nil, nil, []string{"http://new@4.4.4.4:9090"}, nil, nil); err != nil {
		t.Fatal(err)
	}

	got, err := r.GetProxyPoolDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	if got["urlCount"] != 1 {
		t.Errorf("urlCount = %v, want 1", got["urlCount"])
	}
	if got["proxyUrl"] != "http://new@4.4.4.4:9090" {
		t.Errorf("proxyUrl = %v, want the new value", got["proxyUrl"])
	}
	urls := got["urls"].([]any)
	if len(urls) != 1 || urls[0] != "http://new@4.4.4.4:9090" {
		t.Errorf("urls = %#v, want only the new entry", urls)
	}
}

// Test results are persisted so the list can show a known-bad pool without
// re-testing on every load.
func TestRecordProxyPoolTest(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "probe", []string{"http://x@5.5.5.5:8080"})

	if err := r.RecordProxyPoolTest(id, false, "proxy refused the tunnel: HTTP/1.1 407"); err != nil {
		t.Fatal(err)
	}
	got, _ := r.GetProxyPoolDetail(id)
	if got["testStatus"] != "error" {
		t.Errorf("testStatus = %v, want error", got["testStatus"])
	}
	if got["lastError"] != "proxy refused the tunnel: HTTP/1.1 407" {
		t.Errorf("lastError = %v, want the reason stored", got["lastError"])
	}
	if got["lastTestedAt"] == nil || got["lastTestedAt"] == "" {
		t.Error("lastTestedAt must be set so the UI can show staleness")
	}

	if err := r.RecordProxyPoolTest(id, true, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = r.GetProxyPoolDetail(id)
	if got["testStatus"] != "active" {
		t.Errorf("testStatus = %v, want active after a pass", got["testStatus"])
	}
	// A later pass must clear the old failure, or the UI would keep showing a
	// stale error next to a working pool.
	if _, present := got["lastError"]; present {
		t.Errorf("lastError = %v, want it cleared on success", got["lastError"])
	}
}

// Deleting a pool a provider still routes through would turn that provider's
// egress into a silent direct connection, so the delete is refused.
func TestDeleteProxyPoolRefusesWhileReferenced(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "in-use", []string{"http://a@6.6.6.6:8080"})

	if err := r.SetProviderStrategy("someprovider", ProviderStrategy{ProxyPoolID: id, RotateStrategy: "none"}); err != nil {
		t.Fatal(err)
	}

	bound, err := r.DeleteProxyPool(id)
	if err != nil {
		t.Fatal(err)
	}
	if bound != 1 {
		t.Fatalf("bound = %d, want 1 reference reported", bound)
	}
	if _, err := r.GetProxyPoolDetail(id); err != nil {
		t.Fatal("pool must survive a refused delete")
	}
}

// Unreferenced pools delete cleanly, and the reference count drops to zero.
func TestDeleteProxyPoolSucceedsWhenUnreferenced(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "free", []string{"http://b@7.7.7.7:8080"})

	bound, err := r.DeleteProxyPool(id)
	if err != nil {
		t.Fatal(err)
	}
	if bound != 0 {
		t.Fatalf("bound = %d, want 0", bound)
	}
	if _, err := r.GetProxyPoolDetail(id); err == nil {
		t.Fatal("pool should be gone")
	}
}

// A pool listed in proxyPoolTargets (the multi-pool rotation subset) also binds
// it, even when proxyPoolId points elsewhere.
func TestDeleteProxyPoolCountsTargetReferences(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "targeted", []string{"http://c@8.8.8.8:8080"})

	if err := r.SetProviderStrategy("targeter", ProviderStrategy{TargetProxyPoolIds: []string{id}}); err != nil {
		t.Fatal(err)
	}
	bound, err := r.DeleteProxyPool(id)
	if err != nil {
		t.Fatal(err)
	}
	if bound != 1 {
		t.Fatalf("bound = %d, want the target reference counted", bound)
	}
}

// The detail reader is what the API returns, and it must not leak the proxy
// credentials of a pool the caller did not ask about.
func TestGetProxyPoolDetailMissIsAnError(t *testing.T) {
	r := newProxyPoolRepo(t)
	if _, err := r.GetProxyPoolDetail("does-not-exist"); err == nil {
		t.Fatal("a missing pool must be an error, not an empty success")
	}
}

// The edit modal sends the proxy type, so it has to survive a round trip
// through the JSON blob rather than being silently dropped on save.
func TestUpdateProxyPoolPersistsType(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "typed", []string{"http://t@1.2.3.4:8080"})

	socks := "socks5"
	if err := r.UpdateProxyPool(id, nil, nil, nil, &socks, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	got, err := r.GetProxyPoolDetail(id)
	if err != nil {
		t.Fatal(err)
	}
	if got["type"] != "socks5" {
		t.Errorf("type = %v, want socks5", got["type"])
	}
}

// The list must carry the failure reason, not just the status. Without it the
// UI can say "failing" but not why, and the operator has to open each pool to
// find which one is misconfigured.
func TestListProxyPoolsCarriesFailureReason(t *testing.T) {
	r := newProxyPoolRepo(t)
	id := seedPool(t, r, "sad", []string{"http://a@1.2.3.4:8080"})

	if err := r.RecordProxyPoolTest(id, false, "proxy refused the tunnel: HTTP/1.1 407"); err != nil {
		t.Fatal(err)
	}
	pools, err := r.ListProxyPools(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 {
		t.Fatalf("got %d pools, want 1", len(pools))
	}
	if pools[0].LastError != "proxy refused the tunnel: HTTP/1.1 407" {
		t.Errorf("LastError = %q, want the probe reason on the summary", pools[0].LastError)
	}
	if pools[0].LastTestedAt == "" {
		t.Error("LastTestedAt must be set so the UI can show staleness")
	}
}

// Deleting a pool that does not exist must be distinguishable from deleting one
// that did, so the HTTP layer can answer 404 instead of a success.
func TestDeleteProxyPoolMissingIsNotFound(t *testing.T) {
	r := newProxyPoolRepo(t)
	_, err := r.DeleteProxyPool("no-such-pool")
	if !errors.Is(err, ErrProxyPoolNotFound) {
		t.Fatalf("err = %v, want ErrProxyPoolNotFound", err)
	}
}
