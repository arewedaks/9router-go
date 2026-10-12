package db

import "testing"

func TestJailbreakDocRoundTrip(t *testing.T) {
	repo := newSettingsRepo(t)

	// No doc saved yet: every provider is silently left alone.
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

func TestJailbreakProviderToggleOptIn(t *testing.T) {
	repo := newSettingsRepo(t)

	// Missing entry must read as off: a provider never gets injected just
	// because it exists.
	on, err := repo.JailbreakProviderEnabled("gemini")
	if err != nil {
		t.Fatalf("enabled(missing): %v", err)
	}
	if on {
		t.Fatal("default must be off")
	}

	if err := repo.SetJailbreakProvider("gemini", true); err != nil {
		t.Fatalf("set on: %v", err)
	}
	on, err = repo.JailbreakProviderEnabled("gemini")
	if err != nil || !on {
		t.Fatalf("after enable: on=%v err=%v", on, err)
	}

	// Other providers stay off.
	on, _ = repo.JailbreakProviderEnabled("wally")
	if on {
		t.Fatal("unrelated provider must stay off")
	}

	if err := repo.SetJailbreakProvider("gemini", false); err != nil {
		t.Fatalf("set off: %v", err)
	}
	on, err = repo.JailbreakProviderEnabled("gemini")
	if err != nil || on {
		t.Fatalf("after disable: on=%v err=%v", on, err)
	}

	// List only reflects providers currently opted in.
	if err := repo.SetJailbreakProvider("opencode", true); err != nil {
		t.Fatalf("set opencode: %v", err)
	}
	list, err := repo.ListJailbreakProviders()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || !list["opencode"] {
		t.Fatalf("list = %v, want only opencode", list)
	}
}
