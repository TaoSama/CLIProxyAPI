package registry

import "testing"

func TestAppendMissingModelsPreservesEmbeddedFallbacks(t *testing.T) {
	remote := []*ModelInfo{{ID: "gemini-3.7-flash-high"}}
	fallback := []*ModelInfo{
		{ID: "gemini-3.7-flash-high"},
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash"},
	}

	got := appendMissingModels(remote, fallback)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[1].ID != "gemini-3.8-flash-high" {
		t.Fatalf("fallback model ID = %q, want gemini-3.8-flash-high", got[1].ID)
	}

	fallback[1].DisplayName = "mutated"
	if got[1].DisplayName != "Gemini 3.8 Flash" {
		t.Fatalf("fallback model was not cloned, display name = %q", got[1].DisplayName)
	}
}

func TestAppendMissingModelsDoesNotDuplicateRemoteModels(t *testing.T) {
	remote := []*ModelInfo{{ID: "Gemini-3.8-Flash-High"}}
	fallback := []*ModelInfo{{ID: "gemini-3.8-flash-high"}}

	got := appendMissingModels(remote, fallback)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}

func TestDetectChangedProviders_KimiAliases(t *testing.T) {
	oldData := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2"}},
	}
	newData := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2"}, {ID: "kimi-k3"}},
	}

	changed := detectChangedProviders(oldData, newData)
	expected := map[string]bool{
		"kimi":     false,
		"kimi-ai":  false,
		"kimi.ai":  false,
		"kimi.com": false,
	}

	for _, p := range changed {
		if _, ok := expected[p]; ok {
			expected[p] = true
		}
	}

	for p, found := range expected {
		if !found {
			t.Errorf("expected changed provider %q to be reported, got %v", p, changed)
		}
	}
}
