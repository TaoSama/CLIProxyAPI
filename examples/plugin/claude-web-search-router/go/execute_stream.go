package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type streamOrchestrationRunner func(context.Context, pluginapi.ExecutorRequest, string, string) error

type pluginStreamCloser func(string, string)

func executeStream(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return startExecutorStream(req, runWebSearchStreamDispatch, closePluginStream)
}

// runWebSearchStreamDispatch selects the streaming orchestration by inbound
// protocol: OpenAI Responses turns run the client's own model with function-tool
// web_search orchestration, while Claude turns keep the existing backend fallback.
func runWebSearchStreamDispatch(ctx context.Context, exec pluginapi.ExecutorRequest, hostCallbackID, pluginStreamID string) error {
	if isOpenAIResponsesSourceFormat(exec.SourceFormat) {
		return runOpenAIResponsesOrchestrationStream(ctx, exec, hostCallbackID, pluginStreamID)
	}
	return runWebSearchStreamOrchestration(ctx, exec, hostCallbackID, pluginStreamID)
}

func startExecutorStream(req rpcExecutorRequest, runner streamOrchestrationRunner, closeStream pluginStreamCloser) ([]byte, error) {
	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream"), nil
	}
	if runner == nil {
		return errorEnvelope("executor_error", "stream orchestration runner is unavailable"), nil
	}
	if closeStream == nil {
		closeStream = func(string, string) {}
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				closeStream(streamID, fmt.Sprintf("stream orchestration panic: %v", recovered))
			}
		}()
		errRun := runner(context.Background(), req.ExecutorRequest, req.HostCallbackID, streamID)
		if errRun != nil {
			closeStream(streamID, errRun.Error())
			return
		}
		closeStream(streamID, "")
	}()
	return okEnvelope(map[string]any{
		"headers": http.Header{"Content-Type": []string{"text/event-stream"}},
	})
}
