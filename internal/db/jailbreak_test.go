package db

import "testing"

func TestJailbreakDocRoundTrip(t *testing.T) {
	repo := newSettingsRepo(t)

	// No doc saved yet: every model is silently left alone.
	doc, err := repo.GetJailbreakDoc()
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if doc.Prompt != "" || doc.GeminiSafety {
		t.Fatalf("expected zero doc, got %+v", doc)
	}

	in := &JailbreakDoc{Prompt: "agents.md text", GeminiSafety: true}
	if err := repo.SaveJailbreakDoc(in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := repo.GetJailbreakDoc()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Prompt != "agents.md text" || !out.GeminiSafety {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestJailbreakModelToggleOptIn(t *testing.T) {
	repo := newSettingsRepo(t)

	// The key is the id /v1/models lists — prefixes and suffixes are part of
	// the id, exactly as the client sends them.
	const model = "tkh/mimo-v2.6-flash:free"

	// Missing entry must read as off: a model never gets injected just because
	// it exists.
	on, err := repo.JailbreakModelEnabled(model)
	if err != nil {
		t.Fatalf("enabled(missing): %v", err)
	}
	if on {
		t.Fatal("default must be off")
	}

	if err := repo.SetJailbreakModel(model, true); err != nil {
		t.Fatalf("set on: %v", err)
	}
	on, err = repo.JailbreakModelEnabled(model)
	if err != nil || !on {
		t.Fatalf("after enable: on=%v err=%v", on, err)
	}

	// A different model id stays off — no prefix/wildcard bleed.
	on, _ = repo.JailbreakModelEnabled("tkh/mimo-v2.6-flash")
	if on {
		t.Fatal("nearby model id must stay off")
	}

	if err := repo.SetJailbreakModel(model, false); err != nil {
		t.Fatalf("set off: %v", err)
	}
	on, err = repo.JailbreakModelEnabled(model)
	if err != nil || on {
		t.Fatalf("after disable: on=%v err=%v", on, err)
	}

	// List only reflects models currently opted in.
	if err := repo.SetJailbreakModel("gemini-flash", true); err != nil {
		t.Fatalf("set gemini-flash: %v", err)
	}
	list, err := repo.ListJailbreakModels()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || !list["gemini-flash"] {
		t.Fatalf("list = %v, want only gemini-flash", list)
	}
}
