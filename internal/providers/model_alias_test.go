package providers

import "testing"

// TestOutputAliasPrefersPrefix pins the upstream rule that a custom endpoint is
// advertised under its configured prefix, never its generated id:
//
//	const outputAlias = (providerSpecificData.prefix || alias || providerId).trim()
//
// The BAI/Atria nodes are stored as "openai-compatible-chat-<uuid>"; clients
// must see "bai" and "atri".
func TestOutputAliasPrefersPrefix(t *testing.T) {
	const nodeID = "openai-compatible-chat-8e96ef73-6f8a-4043-bcf4-42285b6de114"

	if got := OutputAlias(nodeID, "bai"); got != "bai" {
		t.Fatalf("OutputAlias(node, \"bai\") = %q, want bai", got)
	}
	// Whitespace in a configured prefix must not leak into model ids.
	if got := OutputAlias(nodeID, "  atri  "); got != "atri" {
		t.Fatalf("OutputAlias trims the prefix, got %q", got)
	}
	// No prefix: a known provider still resolves to its static alias.
	if got := OutputAlias("openrouter", ""); got != "openrouter" {
		t.Fatalf("OutputAlias(openrouter) = %q, want openrouter", got)
	}
	if got := OutputAlias("cf", ""); got != "cloudflare-ai" {
		t.Fatalf("OutputAlias(cf) = %q, want cloudflare-ai", got)
	}
	// Nothing known: fall back to the raw id rather than dropping the model.
	if got := OutputAlias(nodeID, ""); got != nodeID {
		t.Fatalf("OutputAlias(node, \"\") = %q, want the raw id", got)
	}
}

// TestNormalizeModelAliasRewritesGeneratedNodeID covers the restore case: a
// VansRouter backup may legitimately store the generated node id in
// customModels.providerAlias. Exporting it verbatim is what produced 1500
// uuid-prefixed model names; it must resolve to the node's prefix instead.
func TestNormalizeModelAliasRewritesGeneratedNodeID(t *testing.T) {
	const nodeID = "anthropic-compatible-0b2ee40e-a1f9-4186-9dc5-3e0884f091e9"
	nodes := map[string]string{
		nodeID: "cbai",
		"openai-compatible-chat-8e96ef73-6f8a-4043-bcf4-42285b6de114": "bai",
	}

	if got := NormalizeModelAlias(nodeID, nodes); got != "cbai" {
		t.Fatalf("NormalizeModelAlias(node id) = %q, want cbai", got)
	}
	// A plain alias that is not a node id is left untouched.
	if got := NormalizeModelAlias("openrouter", nodes); got != "openrouter" {
		t.Fatalf("NormalizeModelAlias(openrouter) = %q, want openrouter", got)
	}
	// An orphan alias (node deleted upstream) has no prefix to resolve to; the
	// alias is returned unchanged so the model id is stable rather than lossy.
	orphan := "openai-compatible-chat-16dc7b96-9c2f-4cf5-9903-2543176744fb"
	if got := NormalizeModelAlias(orphan, nodes); got != orphan {
		t.Fatalf("NormalizeModelAlias(orphan) = %q, want it unchanged", got)
	}
	// Empty input stays empty so callers do not build "/model".
	if got := NormalizeModelAlias("   ", nodes); got != "" {
		t.Fatalf("NormalizeModelAlias(blank) = %q, want empty", got)
	}
}

// TestIsGeneratedNodeID pins the detection of auto-generated node keys. The
// trailing uuid contains dashes, so a naive split on the last "-" never
// matches — which is exactly the bug that let "openai-compatible-chat-<uuid>"
// keep leaking into display fields.
func TestIsGeneratedNodeID(t *testing.T) {
	generated := []string{
		"openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2",
		"openai-compatible-chat-8e96ef73-6f8a-4043-bcf4-42285b6de114",
		"anthropic-compatible-0b2ee40e-a1f9-4186-9dc5-3e0884f091e9",
		"custom-embedding-12345678-1234-1234-1234-123456789012",
	}
	for _, k := range generated {
		if !IsGeneratedNodeID(k) {
			t.Errorf("IsGeneratedNodeID(%q) = false, want true", k)
		}
	}

	notGenerated := []string{
		"openrouter",
		"bai",
		"antigravity",
		"openai-compatible-custom",     // no uuid suffix
		"openai-compatible-chat-nonid", // not hex
		"openai-compatible-chat-12345678-1234-1234-1234-12345678901", // 35 chars
		"openai", // shares a prefix but is a real provider
	}
	for _, k := range notGenerated {
		if IsGeneratedNodeID(k) {
			t.Errorf("IsGeneratedNodeID(%q) = true, want false", k)
		}
	}
}

// TestOutputAliasForNodePrefixDocumentsAtria is the concrete regression from the
// bug report: the Atria node must advertise "atri/Atria-Dawn-Preview", never
// "openai-compatible-chat-8db0a9e6-.../Atria-Dawn-Preview".
func TestOutputAliasForNodePrefixDocumentsAtria(t *testing.T) {
	const atria = "openai-compatible-chat-8db0a9e6-970e-4903-9a9f-27365f3763e2"
	if got := OutputAlias(atria, "atri"); got != "atri" {
		t.Fatalf("OutputAlias(atria) = %q, want atri", got)
	}
	got := OutputAlias(atria, "atri") + "/Atria-Dawn-Preview"
	if got != "atri/Atria-Dawn-Preview" {
		t.Fatalf("model id = %q, want atri/Atria-Dawn-Preview", got)
	}
}
