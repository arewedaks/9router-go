package dashboard

import (
	"testing"
)

// A model the operator removed must not come back on the provider page.
//
// /v1/models filters through hiddenModels, but the provider detail payload is
// built from the import cache alone and never consulted that set. Removing a
// model therefore looked like it did nothing: the row vanished from the API and
// stayed on the page, which is how auto-disable was reported as broken when the
// server had already done its part.
func TestProviderDetailOmitsHiddenModels(t *testing.T) {
	_, repo, r := setupTestDashboard(t)

	if _, err := repo.DB().Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt)
		 VALUES ('nv-1','nvidia','apikey','NVIDIA',1,1,'{"apiKey":"k"}','2026-07-18T00:00:00Z','2026-07-18T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}
	for _, m := range []string{"keep-me", "remove-me"} {
		if _, err := repo.DB().Exec(
			`INSERT INTO kv (scope, key, value) VALUES ('customModels', ?, ?)`,
			"nvidia|"+m+"|llm", `{"providerAlias":"nvidia","id":"`+m+`","type":"llm"}`,
		); err != nil {
			t.Fatalf("seed custom model %s: %v", m, err)
		}
	}

	// Both listed to begin with.
	var before providerDetailBody
	if code := getJSON(t, r, "/api/dashboard/providers/nvidia", &before); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if len(before.Models) != 2 {
		t.Fatalf("baseline models = %d, want 2", len(before.Models))
	}

	if err := repo.HideModel([]string{"nvidia"}, "remove-me"); err != nil {
		t.Fatalf("HideModel: %v", err)
	}

	var after providerDetailBody
	if code := getJSON(t, r, "/api/dashboard/providers/nvidia", &after); code != 200 {
		t.Fatalf("status = %d", code)
	}
	for _, m := range after.Models {
		if m.ModelID == "remove-me" {
			t.Errorf("removed model is still on the provider page: %+v", after.Models)
		}
	}
	if len(after.Models) != 1 || after.Models[0].ModelID != "keep-me" {
		t.Errorf("models = %+v, want just keep-me", after.Models)
	}
}
