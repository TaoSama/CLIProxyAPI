package main

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// realisticCodexTurn mirrors a real Codex /v1/responses body: a multi-tool turn
// carrying prompt_cache_key, client_metadata and a hosted web_search tool.
func realisticCodexTurn() []byte {
	return []byte(`{"model":"claude-opus-4-8","instructions":"You are Codex.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"what changed in 2026"}]}],"tools":[{"type":"custom","name":"exec"},{"type":"function","name":"wait"},{"type":"web_search","external_web_access":true,"search_content_types":["text","image"]}],"tool_choice":"auto","parallel_tool_calls":false,"reasoning":{"effort":"xhigh"},"store":false,"stream":true,"prompt_cache_key":"019fdb07-cache","client_metadata":{"x-codex-window-id":"win-1"}}`)
}

// cachePrefixBeforeTools returns the byte prefix up to the "tools" key, which is
// the portion the upstream prompt cache keys on when input precedes tools.
func cachePrefixBeforeTools(body []byte) string {
	s := string(body)
	idx := strings.Index(s, `"tools"`)
	if idx < 0 {
		return s
	}
	return s[:idx]
}

// TestE2ENoSearchRunsOriginalModelUnchanged proves that a turn where the model
// does not call web_search runs exactly once, on the original model, with the
// request body cache-stable (prompt_cache_key + input prefix preserved) and no
// search backend invoked.
func TestE2ENoSearchRunsOriginalModelUnchanged(t *testing.T) {
	original := realisticCodexTurn()
	var sentBodies [][]byte
	searchCalls := 0

	runTurn := func(_ context.Context, body []byte) ([]byte, string, error) {
		sentBodies = append(sentBodies, append([]byte(nil), body...))
		return finalTextResponse("here is the answer"), "application/json", nil
	}
	search := func(_ context.Context, _ string, _ int) (string, error) {
		searchCalls++
		return "", nil
	}

	payload, _, err := orchestrateResponsesSearch(context.Background(), original, pluginConfig{}, runTurn, search)
	if err != nil {
		t.Fatal(err)
	}
	if len(sentBodies) != 1 {
		t.Fatalf("model invoked %d times, want 1 (no extra hops)", len(sentBodies))
	}
	if searchCalls != 0 {
		t.Fatalf("search invoked %d times on a non-search turn, want 0", searchCalls)
	}
	sent := sentBodies[0]
	// Original model is preserved (not switched to gpt-5.6-sol or anything else).
	if got := gjson.GetBytes(sent, "model").String(); got != "claude-opus-4-8" {
		t.Fatalf("upstream model = %q, want claude-opus-4-8 (must stay on the original model)", got)
	}
	// Cache-critical fields untouched.
	if got := gjson.GetBytes(sent, "prompt_cache_key").String(); got != "019fdb07-cache" {
		t.Fatalf("prompt_cache_key = %q, want preserved", got)
	}
	if got := gjson.GetBytes(sent, "client_metadata.x-codex-window-id").String(); got != "win-1" {
		t.Fatalf("client_metadata dropped: %s", sent)
	}
	// The cacheable prefix (everything before tools: model/instructions/input/...)
	// must be byte-identical to the original request.
	if cachePrefixBeforeTools(sent) != cachePrefixBeforeTools(original) {
		t.Fatalf("cache prefix changed:\n orig=%s\n sent=%s", cachePrefixBeforeTools(original), cachePrefixBeforeTools(sent))
	}
	// input array is byte-identical (no reordering / re-encoding of the prompt).
	if gjson.GetBytes(sent, "input").Raw != gjson.GetBytes(original, "input").Raw {
		t.Fatalf("input array mutated on a no-search turn")
	}
	if !strings.Contains(string(payload), "here is the answer") {
		t.Fatalf("final payload not forwarded: %s", payload)
	}
}

// TestE2ESearchKeepsPrefixStableAcrossTurns proves that when the model does call
// web_search, every turn stays on the original model and the cache prefix
// (model/instructions/input head + rewritten tools) is stable turn-to-turn, so
// the upstream prompt cache keeps hitting. Only input grows by appending the
// function_call + function_call_output (the intended Responses continuation).
func TestE2ESearchKeepsPrefixStableAcrossTurns(t *testing.T) {
	original := realisticCodexTurn()
	var sentBodies [][]byte

	runTurn := func(_ context.Context, body []byte) ([]byte, string, error) {
		sentBodies = append(sentBodies, append([]byte(nil), body...))
		if len(sentBodies) == 1 {
			return functionCallResponse("call_1", "2026 changes"), "application/json", nil
		}
		return finalTextResponse("grounded answer"), "application/json", nil
	}
	search := func(_ context.Context, query string, _ int) (string, error) {
		return "Result for: " + query, nil
	}

	if _, _, err := orchestrateResponsesSearch(context.Background(), original, pluginConfig{}, runTurn, search); err != nil {
		t.Fatal(err)
	}
	if len(sentBodies) != 2 {
		t.Fatalf("model turns = %d, want 2", len(sentBodies))
	}

	for i, sent := range sentBodies {
		if got := gjson.GetBytes(sent, "model").String(); got != "claude-opus-4-8" {
			t.Fatalf("turn %d model = %q, want claude-opus-4-8", i, got)
		}
		if got := gjson.GetBytes(sent, "prompt_cache_key").String(); got != "019fdb07-cache" {
			t.Fatalf("turn %d prompt_cache_key = %q, want preserved", i, got)
		}
		// tools block is the SAME rewrite on every turn (prefix-stable).
		if gjson.GetBytes(sent, "tools").Raw != gjson.GetBytes(sentBodies[0], "tools").Raw {
			t.Fatalf("turn %d tools block differs from turn 0 (breaks cache prefix)", i)
		}
	}

	// Turn 2's input must be turn 1's input plus exactly two appended items; the
	// original leading items are unchanged (cache prefix extends, never rewrites).
	in1 := gjson.GetBytes(sentBodies[0], "input").Array()
	in2 := gjson.GetBytes(sentBodies[1], "input").Array()
	if len(in2) != len(in1)+2 {
		t.Fatalf("turn 2 input len = %d, want %d (+function_call +function_call_output)", len(in2), len(in1)+2)
	}
	for i := range in1 {
		if in1[i].Raw != in2[i].Raw {
			t.Fatalf("turn 2 input[%d] diverged from turn 1 (prefix not preserved)", i)
		}
	}
	if in2[len(in2)-2].Get("type").String() != "function_call" || in2[len(in2)-1].Get("type").String() != "function_call_output" {
		t.Fatalf("appended items wrong: %s | %s", in2[len(in2)-2].Raw, in2[len(in2)-1].Raw)
	}
}
