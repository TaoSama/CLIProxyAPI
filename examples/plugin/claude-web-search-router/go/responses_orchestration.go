package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// orchestratedWebSearchToolName is the function tool exposed to the client
	// model in place of the hosted web_search tool. The model calls it when it
	// decides to search; the plugin executes the search and feeds results back.
	orchestratedWebSearchToolName = "web_search"
	// maxOrchestrationRounds caps how many model turns a single request may run,
	// bounding the search feedback loop.
	maxOrchestrationRounds = 4
	// defaultResponsesSearchMaxResults is the Tavily result count when the request
	// does not specify max_uses.
	defaultResponsesSearchMaxResults = 5
)

// responsesTurnRunner runs one OpenAI Responses turn for the client model and
// returns the raw response payload plus its content type. It is a seam so the
// orchestration loop can be unit-tested without the cgo host callback.
type responsesTurnRunner func(ctx context.Context, body []byte) (payload []byte, contentType string, err error)

// responsesSearchExecutor executes a single web search and returns result text.
type responsesSearchExecutor func(ctx context.Context, query string, maxResults int) (string, error)

// openAIResponsesRequestBody prefers the raw client request body.
func openAIResponsesRequestBody(exec pluginapi.ExecutorRequest) []byte {
	if len(exec.OriginalRequest) > 0 {
		return exec.OriginalRequest
	}
	return exec.Payload
}

// runOpenAIResponsesOrchestration runs the non-streaming orchestration loop.
func runOpenAIResponsesOrchestration(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID string) ([]byte, http.Header, error) {
	cfg := loadedConfig()
	model := strings.TrimSpace(exec.Model)
	runTurn := func(ctx context.Context, body []byte) ([]byte, string, error) {
		payload, status, errRun := hostModelExecuteResponses(ctx, hostCallbackID, model, body)
		if errRun != nil {
			return nil, "", errRun
		}
		if status >= 400 {
			return nil, "", fmt.Errorf("host model status %d", status)
		}
		return payload, "application/json", nil
	}
	payload, contentType, errRun := orchestrateResponsesSearch(ctx, openAIResponsesRequestBody(exec), cfg, runTurn, newResponsesSearchExecutor(cfg))
	if errRun != nil {
		return nil, nil, errRun
	}
	if contentType == "" {
		contentType = "application/json"
	}
	return payload, http.Header{"Content-Type": []string{contentType}}, nil
}

// runOpenAIResponsesOrchestrationStream streams the model turn live to the client
// while watching the first output item. If the model starts a web_search function
// call, the turn is buffered instead of forwarded, the search runs, and the loop
// continues; otherwise the answer streams straight through in a single turn (the
// common no-search path pays no extra round trip).
func runOpenAIResponsesOrchestrationStream(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID, pluginStreamID string) error {
	cfg := loadedConfig()
	model := strings.TrimSpace(exec.Model)
	search := newResponsesSearchExecutor(cfg)
	maxResults := responsesWebSearchMaxResults(openAIResponsesRequestBody(exec))
	current := rewriteResponsesWebSearchTool(openAIResponsesRequestBody(exec))

	for round := 0; round < maxOrchestrationRounds; round++ {
		// On the last allowed round, forward unconditionally so a model that keeps
		// requesting search still yields a visible turn.
		forceForward := round == maxOrchestrationRounds-1
		call, ok, errTurn := streamResponsesTurnDetectingSearch(ctx, hostCallbackID, model, current, pluginStreamID, forceForward)
		if errTurn != nil {
			return errTurn
		}
		if !ok {
			// Turn was the final answer and has already been streamed to the client.
			return nil
		}
		result := runResponsesSearch(ctx, search, call.query, maxResults)
		current = appendResponsesSearchResult(current, call, result)
	}
	return nil
}

// orchestrateResponsesSearch is the protocol-agnostic loop: rewrite the hosted
// web_search tool into a function tool, run the client model, and whenever the
// model calls web_search run the search backend and feed the result back until
// the model produces a final answer (or the round cap is reached). The final
// turn's raw payload is returned verbatim for the caller to forward.
func orchestrateResponsesSearch(ctx context.Context, body []byte, cfg pluginConfig, runTurn responsesTurnRunner, search responsesSearchExecutor) ([]byte, string, error) {
	maxResults := responsesWebSearchMaxResults(body)
	current := rewriteResponsesWebSearchTool(body)

	var lastPayload []byte
	var lastContentType string
	for round := 0; round < maxOrchestrationRounds; round++ {
		payload, contentType, errTurn := runTurn(ctx, current)
		if errTurn != nil {
			return nil, "", errTurn
		}
		lastPayload = payload
		lastContentType = contentType

		call, ok := extractResponsesWebSearchCall(payload, contentType)
		if !ok {
			// No search action requested: this turn is the final answer.
			return payload, contentType, nil
		}

		result := runResponsesSearch(ctx, search, call.query, maxResults)
		current = appendResponsesSearchResult(current, call, result)
	}
	// Round cap reached: forward the last model turn as-is.
	return lastPayload, lastContentType, nil
}

// responsesWebSearchCall captures a single web_search function call emitted by the model.
type responsesWebSearchCall struct {
	callID    string
	name      string
	arguments string
	query     string
}

// rewriteResponsesWebSearchTool replaces hosted web_search / web_search_preview
// tools with a plain function tool the client model can invoke. Other tools are
// left untouched.
func rewriteResponsesWebSearchTool(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	fnTool := []byte(`{"type":"function","name":"","description":"Search the public web and return relevant results for a query.","parameters":{"type":"object","properties":{"query":{"type":"string","description":"The search query."}},"required":["query"]}}`)
	fnTool, _ = sjson.SetBytes(fnTool, "name", orchestratedWebSearchToolName)

	rebuilt := make([]json.RawMessage, 0, len(tools.Array()))
	replaced := false
	for _, tool := range tools.Array() {
		if isOpenAIResponsesWebSearchToolType(tool.Get("type").String()) {
			if replaced {
				// Collapse duplicate web_search tools into the single function tool.
				continue
			}
			replaced = true
			rebuilt = append(rebuilt, append(json.RawMessage(nil), fnTool...))
			continue
		}
		rebuilt = append(rebuilt, json.RawMessage(tool.Raw))
	}
	if !replaced {
		return body
	}
	encoded, errMarshal := json.Marshal(rebuilt)
	if errMarshal != nil {
		return body
	}
	out, errSet := sjson.SetRawBytes(body, "tools", encoded)
	if errSet != nil {
		return body
	}
	return out
}

// extractResponsesWebSearchCall finds the model's web_search function call in a
// turn payload. It supports both the non-streaming response ("output" array) and
// the streaming SSE ("response.completed" event carries "response.output").
func extractResponsesWebSearchCall(payload []byte, contentType string) (responsesWebSearchCall, bool) {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		if root, ok := responsesCompletedFromStream(payload); ok {
			return webSearchCallFromOutput(root.Get("response.output"))
		}
		return responsesWebSearchCall{}, false
	}
	return webSearchCallFromOutput(gjson.GetBytes(payload, "output"))
}

// responsesCompletedFromStream extracts the response object from a buffered
// OpenAI Responses SSE stream's response.completed event.
func responsesCompletedFromStream(payload []byte) (gjson.Result, bool) {
	var completed gjson.Result
	found := false
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		parsed := gjson.Parse(data)
		if parsed.Get("type").String() == "response.completed" {
			completed = parsed
			found = true
		}
	}
	return completed, found
}

// webSearchCallFromOutput scans an OpenAI Responses output array for a function
// call targeting the orchestrated web_search tool.
func webSearchCallFromOutput(output gjson.Result) (responsesWebSearchCall, bool) {
	if !output.IsArray() {
		return responsesWebSearchCall{}, false
	}
	for _, item := range output.Array() {
		if item.Get("type").String() != "function_call" {
			continue
		}
		if item.Get("name").String() != orchestratedWebSearchToolName {
			continue
		}
		args := item.Get("arguments").String()
		call := responsesWebSearchCall{
			callID:    item.Get("call_id").String(),
			name:      item.Get("name").String(),
			arguments: args,
			query:     strings.TrimSpace(gjson.Get(args, "query").String()),
		}
		return call, true
	}
	return responsesWebSearchCall{}, false
}

// appendResponsesSearchResult appends the model's function_call and the search
// result as function_call_output to the request input array for the next turn.
func appendResponsesSearchResult(body []byte, call responsesWebSearchCall, result string) []byte {
	fnCall := []byte(`{"type":"function_call","call_id":"","name":"","arguments":""}`)
	fnCall, _ = sjson.SetBytes(fnCall, "call_id", call.callID)
	fnCall, _ = sjson.SetBytes(fnCall, "name", call.name)
	fnCall, _ = sjson.SetBytes(fnCall, "arguments", call.arguments)

	fnOutput := []byte(`{"type":"function_call_output","call_id":"","output":""}`)
	fnOutput, _ = sjson.SetBytes(fnOutput, "call_id", call.callID)
	fnOutput, _ = sjson.SetBytes(fnOutput, "output", result)

	out := body
	if !gjson.GetBytes(out, "input").IsArray() {
		out, _ = sjson.SetRawBytes(out, "input", []byte(`[]`))
	}
	out, _ = sjson.SetRawBytes(out, "input.-1", fnCall)
	out, _ = sjson.SetRawBytes(out, "input.-1", fnOutput)
	return out
}

// runResponsesSearch executes the search backend, returning a text result even
// when the backend fails so the model can degrade gracefully instead of erroring
// out the whole turn.
func runResponsesSearch(ctx context.Context, search responsesSearchExecutor, query string, maxResults int) string {
	if strings.TrimSpace(query) == "" {
		return "No search query was provided."
	}
	result, errSearch := search(ctx, query, maxResults)
	if errSearch != nil {
		return fmt.Sprintf("Web search failed: %s", errSearch.Error())
	}
	if strings.TrimSpace(result) == "" {
		return "No web search results were found."
	}
	return result
}

// newResponsesSearchExecutor binds the configured Tavily client into a search
// executor that formats hits as plain text for the function_call_output.
func newResponsesSearchExecutor(cfg pluginConfig) responsesSearchExecutor {
	client := newTavilyClient(cfg.TavilyAPIKeys)
	return func(ctx context.Context, query string, maxResults int) (string, error) {
		if !client.available() {
			return "", fmt.Errorf("tavily_api_keys is empty")
		}
		hits, answer, errSearch := client.search(ctx, query, maxResults)
		if errSearch != nil {
			return "", errSearch
		}
		return composeAnswerText(answer, hits), nil
	}
}

// responsesWebSearchMaxResults reads max_uses from the hosted web_search tool.
func responsesWebSearchMaxResults(body []byte) int {
	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		for _, tool := range tools.Array() {
			if !isOpenAIResponsesWebSearchToolType(tool.Get("type").String()) {
				continue
			}
			if maxUses := int(tool.Get("max_uses").Int()); maxUses > 0 {
				return maxUses
			}
		}
	}
	return defaultResponsesSearchMaxResults
}

// hostModelExecuteResponses runs one non-streaming OpenAI Responses turn for the
// client model through the host callback.
func hostModelExecuteResponses(ctx context.Context, hostCallbackID, execModel string, body []byte) ([]byte, int, error) {
	raw, errCall := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai-response",
			ExitProtocol:  "openai-response",
			Model:         execModel,
			Stream:        false,
			Body:          body,
		},
		HostCallbackID: hostCallbackID,
	})
	if errCall != nil {
		return nil, hostHTTPStatusFromError(errCall), errCall
	}
	var resp pluginapi.HostModelExecutionResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, 0, errDecode
	}
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("host model status %d", resp.StatusCode)
	}
	return resp.Body, resp.StatusCode, nil
}

// streamResponsesTurnDetectingSearch runs one streaming OpenAI Responses turn,
// buffering the whole turn to decide whether it is a search action or the final
// answer. A single turn can interleave a preamble message ("I'll search…") with
// a web_search function call, so the decision cannot be made from the first
// output item alone — the entire turn must be inspected.
//
//   - The turn contains a web_search function call: nothing is forwarded; the
//     call is extracted and (call, true) is returned so the loop can run the
//     search and continue on the same client model.
//   - The turn contains no web_search call (the final answer, or forceForward on
//     the round cap): the buffered turn is flushed live to the client and
//     ok=false is returned.
//
// Forwarding only the answer turn keeps the client stream a single coherent
// Responses stream; intermediate search turns are consumed internally.
func streamResponsesTurnDetectingSearch(ctx context.Context, hostCallbackID, execModel string, body []byte, pluginStreamID string, forceForward bool) (responsesWebSearchCall, bool, error) {
	raw, errCall := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai-response",
			ExitProtocol:  "openai-response",
			Model:         execModel,
			Stream:        true,
			Body:          body,
		},
		HostCallbackID: hostCallbackID,
	})
	if errCall != nil {
		return responsesWebSearchCall{}, false, errCall
	}
	var resp pluginapi.HostModelStreamResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return responsesWebSearchCall{}, false, errDecode
	}
	if resp.StatusCode >= 400 {
		_ = closeHostModelStream(resp.StreamID)
		return responsesWebSearchCall{}, false, fmt.Errorf("host model status %d", resp.StatusCode)
	}
	if strings.TrimSpace(resp.StreamID) == "" {
		return responsesWebSearchCall{}, false, fmt.Errorf("host model stream: empty stream_id")
	}
	defer func() { _ = closeHostModelStream(resp.StreamID) }()

	var (
		buffered [][]byte
		full     bytes.Buffer
	)
	for {
		chunkRaw, errRead := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: resp.StreamID})
		if errRead != nil {
			return responsesWebSearchCall{}, false, errRead
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if errDecode := json.Unmarshal(chunkRaw, &chunk); errDecode != nil {
			return responsesWebSearchCall{}, false, errDecode
		}
		if chunk.Error != "" {
			return responsesWebSearchCall{}, false, fmt.Errorf("%s", chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			payload := bytes.Clone(chunk.Payload)
			full.Write(payload)
			buffered = append(buffered, payload)
		}
		if chunk.Done {
			break
		}
	}

	flush := func() error {
		for _, b := range buffered {
			if errEmit := emitPluginStreamChunk(pluginStreamID, bytes.Clone(b)); errEmit != nil {
				return errEmit
			}
		}
		return nil
	}

	// On the round cap, forward whatever the model produced regardless of whether
	// it still wants to search, so the client sees a terminal turn.
	if forceForward {
		if errFlush := flush(); errFlush != nil {
			return responsesWebSearchCall{}, false, errFlush
		}
		return responsesWebSearchCall{}, false, nil
	}

	if call, ok := extractResponsesWebSearchCall(full.Bytes(), "text/event-stream"); ok {
		// Search turn: consumed internally, nothing forwarded to the client.
		return call, true, nil
	}
	// Final answer turn: forward the buffered stream live.
	if errFlush := flush(); errFlush != nil {
		return responsesWebSearchCall{}, false, errFlush
	}
	return responsesWebSearchCall{}, false, nil
}
