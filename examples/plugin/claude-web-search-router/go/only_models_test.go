package main

import "testing"

func TestModelMatchesOnly(t *testing.T) {
	cases := []struct {
		name      string
		upstream  []string
		requested string
		only      []string
		want      bool
	}{
		{"empty only matches all", []string{"model_hub/es1_orange_o48"}, "claude-opus-4-8", nil, true},
		{"upstream prefix match", []string{"model_hub/es1_orange_o48"}, "claude-opus-4-8", []string{"model_hub/*"}, true},
		{"client alias does not mask upstream", []string{"model_hub/es1_orange_o48"}, "claude-opus-4-8", []string{"claude-opus-*"}, false},
		{"any upstream candidate may match", []string{"native/claude", "model_hub/es1_orange_o48"}, "claude-opus-4-8", []string{"model_hub/*"}, true},
		{"native upstream not matched", []string{"anthropic/claude-opus-4-8"}, "claude-opus-4-8", []string{"model_hub/*"}, false},
		{"exact match", []string{"seed-code"}, "seed-code-alias", []string{"seed-code"}, true},
		{"fallback to requested model", nil, "traex/gpt-5.4", []string{"traex/*"}, true},
		{"case insensitive", []string{"MODEL_HUB/ES1_ORANGE_O48"}, "", []string{"model_hub/*"}, true},
		{"no model no match when restricted", nil, "", []string{"model_hub/*"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := modelMatchesOnly(c.upstream, c.requested, c.only); got != c.want {
				t.Fatalf("modelMatchesOnly=%v want %v", got, c.want)
			}
		})
	}
}
