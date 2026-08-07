package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func responsesRequestBody() []byte {
	return []byte(`{
		"model":"claude-opus-4-8",
		"input":[{"role":"user","content":[{"type":"input_text","text":"what is new in 2026"}]}],
		"tools":[
			{"type":"function","name":"exec","parameters":{"type":"object"}},
			{"type":"web_search","external_web_access":true}
		]
	}`)
}

func functionCallResponse(callID, query string) []byte {
	body := []byte(`{"output":[{"type":"function_call","status":"completed","name":"web_search","call_id":"","arguments":""}]}`)
	body, _ = sjson.SetBytes(body, "output.0.call_id", callID)
	body, _ = sjson.SetBytes(body, "output.0.arguments", `{"query":"`+query+`"}`)
	return body
}

func finalTextResponse(text string) []byte {
	body := []byte(`{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}]}`)
	body, _ = sjson.SetBytes(body, "output.0.content.0.text", text)
	return body
}

func TestRewriteResponsesWebSearchToolReplacesHostedTool(t *testing.T) {
	out := rewriteResponsesWebSearchTool(responsesRequestBody())
	tools := gjson.GetBytes(out, "tools")
	if tools.Get("#").Int() != 2 {
		t.Fatalf("tools count = %d, want 2: %s", tools.Get("#").Int(), out)
	}
	// exec tool preserved.
	if tools.Get("0.name").String() != "exec" {
		t.Fatalf("first tool not preserved: %s", out)
	}
	// web_search converted to a function tool.
	fn := tools.Get("1")
	if fn.Get("type").String() != "function" || fn.Get("name").String() != orchestratedWebSearchToolName {
		t.Fatalf("web_search not rewritten to function tool: %s", out)
	}
	if !fn.Get("parameters.properties.query").Exists() {
		t.Fatalf("function tool missing query parameter: %s", out)
	}
}

func TestRewriteResponsesWebSearchToolNoWebSearchIsNoOp(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"exec"}]}`)
	out := rewriteResponsesWebSearchTool(body)
	if string(out) != string(body) {
		t.Fatalf("expected no-op, got %s", out)
	}
}

func TestExtractResponsesWebSearchCallNonStream(t *testing.T) {
	call, ok := extractResponsesWebSearchCall(functionCallResponse("call_1", "golang 1.26"), "application/json")
	if !ok {
		t.Fatal("expected a web_search call")
	}
	if call.callID != "call_1" || call.query != "golang 1.26" || call.name != orchestratedWebSearchToolName {
		t.Fatalf("call = %#v", call)
	}
}

func TestExtractResponsesWebSearchCallStream(t *testing.T) {
	sse := "event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"name\":\"web_search\",\"call_id\":\"c9\",\"arguments\":\"{\\\"query\\\":\\\"news\\\"}\"}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"function_call\",\"name\":\"web_search\",\"call_id\":\"c9\",\"arguments\":\"{\\\"query\\\":\\\"news\\\"}\"}]}}\n\n"
	call, ok := extractResponsesWebSearchCall([]byte(sse), "text/event-stream")
	if !ok {
		t.Fatalf("expected a web_search call from stream")
	}
	if call.callID != "c9" || call.query != "news" {
		t.Fatalf("call = %#v", call)
	}
}

func TestExtractResponsesWebSearchCallNoneWhenPlainText(t *testing.T) {
	if _, ok := extractResponsesWebSearchCall(finalTextResponse("hello"), "application/json"); ok {
		t.Fatal("plain text turn should not report a web_search call")
	}
}

func TestOrchestrateResponsesSearchFeedsResultAndContinues(t *testing.T) {
	var turns [][]byte
	searchCalls := 0
	runTurn := func(_ context.Context, body []byte) ([]byte, string, error) {
		turns = append(turns, append([]byte(nil), body...))
		switch len(turns) {
		case 1:
			return functionCallResponse("call_1", "openai responses api"), "application/json", nil
		default:
			return finalTextResponse("final answer"), "application/json", nil
		}
	}
	search := func(_ context.Context, query string, _ int) (string, error) {
		searchCalls++
		if query != "openai responses api" {
			t.Fatalf("search query = %q", query)
		}
		return "Tavily result text", nil
	}

	payload, _, err := orchestrateResponsesSearch(context.Background(), responsesRequestBody(), pluginConfig{}, runTurn, search)
	if err != nil {
		t.Fatal(err)
	}
	if searchCalls != 1 {
		t.Fatalf("search called %d times, want 1", searchCalls)
	}
	if len(turns) != 2 {
		t.Fatalf("model turns = %d, want 2", len(turns))
	}
	// First turn: hosted web_search rewritten to a function tool.
	firstTools := gjson.GetBytes(turns[0], "tools")
	if firstTools.Get("1.type").String() != "function" {
		t.Fatalf("first turn tool not rewritten: %s", turns[0])
	}
	// Second turn: input has function_call + function_call_output appended with the same call_id.
	input := gjson.GetBytes(turns[1], "input")
	n := int(input.Get("#").Int())
	if n < 3 {
		t.Fatalf("second turn input too short (%d): %s", n, turns[1])
	}
	fc := input.Get(fmt.Sprintf("%d", n-2))
	fco := input.Get(fmt.Sprintf("%d", n-1))
	if fc.Get("type").String() != "function_call" || fc.Get("call_id").String() != "call_1" {
		t.Fatalf("appended function_call wrong: %s", fc.Raw)
	}
	if fco.Get("type").String() != "function_call_output" || fco.Get("call_id").String() != "call_1" {
		t.Fatalf("appended function_call_output wrong: %s", fco.Raw)
	}
	if fco.Get("output").String() != "Tavily result text" {
		t.Fatalf("function_call_output content = %q", fco.Get("output").String())
	}
	if !strings.Contains(string(payload), "final answer") {
		t.Fatalf("final payload = %s", payload)
	}
}

func TestOrchestrateResponsesSearchNoCallForwardsFirstTurn(t *testing.T) {
	searchCalls := 0
	runTurn := func(_ context.Context, _ []byte) ([]byte, string, error) {
		return finalTextResponse("direct answer"), "application/json", nil
	}
	search := func(_ context.Context, _ string, _ int) (string, error) {
		searchCalls++
		return "", nil
	}
	payload, _, err := orchestrateResponsesSearch(context.Background(), responsesRequestBody(), pluginConfig{}, runTurn, search)
	if err != nil {
		t.Fatal(err)
	}
	if searchCalls != 0 {
		t.Fatalf("search should not run, called %d", searchCalls)
	}
	if !strings.Contains(string(payload), "direct answer") {
		t.Fatalf("payload = %s", payload)
	}
}

func TestOrchestrateResponsesSearchRoundCapForwardsLastTurn(t *testing.T) {
	runTurn := func(_ context.Context, _ []byte) ([]byte, string, error) {
		// Always ask for another search: the round cap must stop the loop.
		return functionCallResponse("loop", "again"), "application/json", nil
	}
	turns := 0
	search := func(_ context.Context, _ string, _ int) (string, error) {
		turns++
		return "r", nil
	}
	payload, _, err := orchestrateResponsesSearch(context.Background(), responsesRequestBody(), pluginConfig{}, runTurn, search)
	if err != nil {
		t.Fatal(err)
	}
	if turns != maxOrchestrationRounds {
		t.Fatalf("search ran %d times, want %d (round cap)", turns, maxOrchestrationRounds)
	}
	if !gjson.GetBytes(payload, "output.0.type").Exists() {
		t.Fatalf("expected last turn forwarded: %s", payload)
	}
}

func TestRunResponsesSearchDegradesOnFailure(t *testing.T) {
	failing := func(_ context.Context, _ string, _ int) (string, error) {
		return "", context.DeadlineExceeded
	}
	out := runResponsesSearch(context.Background(), failing, "q", 5)
	if !strings.Contains(out, "Web search failed") {
		t.Fatalf("expected graceful failure text, got %q", out)
	}
	if empty := runResponsesSearch(context.Background(), failing, "  ", 5); !strings.Contains(empty, "No search query") {
		t.Fatalf("expected empty-query text, got %q", empty)
	}
}
