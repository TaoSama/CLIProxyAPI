package main

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// defaultWebSearchFallbackChain is the ordered backend try list when route=fallback.
// Prefer Tavily for predictable, low-cost agent search, then fall back to Codex
// server-side web_search if Tavily is unavailable or returns a retryable error.
func defaultWebSearchFallbackChain() []routeBackend {
	return []routeBackend{
		backendTavily,
		backendCodexWebSearch,
	}
}

func isFallbackRoute(route string) bool {
	r := strings.ToLower(strings.TrimSpace(route))
	return r == "" || r == string(backendFallback)
}

// tryRouteBackend returns a handled ModelRouteResponse and true when this backend can serve the request.
func tryRouteBackend(backend routeBackend, cfg pluginConfig, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	switch backend {
	case backendTavily:
		client := newTavilyClient(cfg.TavilyAPIKeys)
		if !client.available() {
			return pluginapi.ModelRouteResponse{Handled: false, Reason: "tavily_unavailable"}, false
		}
		return pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
			Reason:     "claude_code_web_search_tavily",
		}, true
	case backendAntigravityGoogle:
		if !hasProvider(req.AvailableProviders, "antigravity") {
			return pluginapi.ModelRouteResponse{Handled: false, Reason: "antigravity_unavailable"}, false
		}
		targetModel := resolveAntigravityWebSearchTargetModel(cfg.AntigravityModel, req.RequestedModel)
		if targetModel == "" {
			return pluginapi.ModelRouteResponse{Handled: false, Reason: "antigravity_web_search_model_unresolved"}, false
		}
		return pluginapi.ModelRouteResponse{
			Handled:     true,
			TargetKind:  pluginapi.ModelRouteTargetProvider,
			Target:      "antigravity",
			TargetModel: targetModel,
			Reason:      "claude_code_web_search_antigravity_google",
		}, true
	case backendCodexWebSearch:
		if !hasProvider(req.AvailableProviders, "codex") {
			return pluginapi.ModelRouteResponse{Handled: false, Reason: "codex_unavailable"}, false
		}
		targetModel := resolveCodexWebSearchTargetModel(cfg.CodexModel)
		return pluginapi.ModelRouteResponse{
			Handled:     true,
			TargetKind:  pluginapi.ModelRouteTargetProvider,
			Target:      "codex",
			TargetModel: targetModel,
			Reason:      "claude_code_web_search_codex",
		}, true
	case backendXAIWebSearch:
		if !hasProvider(req.AvailableProviders, "xai") {
			return pluginapi.ModelRouteResponse{Handled: false, Reason: "xai_unavailable"}, false
		}
		targetModel := resolveXAIWebSearchTargetModel(cfg.XAIModel)
		return pluginapi.ModelRouteResponse{
			Handled:     true,
			TargetKind:  pluginapi.ModelRouteTargetProvider,
			Target:      "xai",
			TargetModel: targetModel,
			Reason:      "claude_code_web_search_xai",
		}, true
	case backendDefaultProvider:
		provider := cfg.DefaultProvider
		if provider == "" || !hasProvider(req.AvailableProviders, provider) {
			return pluginapi.ModelRouteResponse{Handled: false, Reason: "default_provider_unavailable"}, false
		}
		return pluginapi.ModelRouteResponse{
			Handled:     true,
			TargetKind:  pluginapi.ModelRouteTargetProvider,
			Target:      provider,
			TargetModel: cfg.DefaultProviderModel,
			Reason:      "claude_code_web_search_default_provider",
		}, true
	default:
		return pluginapi.ModelRouteResponse{Handled: false}, false
	}
}

func routeWithFallback(cfg pluginConfig, req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	return routeWithExecutionOrchestration(cfg, req, string(backendFallback))
}

func routeOpenAIResponsesWebSearch(cfg pluginConfig, req pluginapi.ModelRouteRequest) pluginapi.ModelRouteResponse {
	route := strings.TrimSpace(cfg.Route)
	if isFallbackRoute(route) {
		resp, ok := tryRouteBackend(backendCodexWebSearch, cfg, req)
		if ok {
			return resp
		}
		return pluginapi.ModelRouteResponse{Handled: false, Reason: "responses_web_search_codex_unavailable"}
	}

	backend := routeBackend(route)
	if backend == backendTavily {
		return pluginapi.ModelRouteResponse{Handled: false, Reason: "responses_web_search_tavily_unsupported"}
	}
	resp, ok := tryRouteBackend(backend, cfg, req)
	if ok {
		return resp
	}
	if strings.TrimSpace(resp.Reason) != "" {
		return resp
	}
	return pluginapi.ModelRouteResponse{Handled: false, Reason: "responses_web_search_backend_unavailable"}
}

func routeWithExecutionOrchestration(cfg pluginConfig, req pluginapi.ModelRouteRequest, route string) pluginapi.ModelRouteResponse {
	plans := executionPlansForRoute(cfg, req, route)
	if len(plans) == 0 {
		return pluginapi.ModelRouteResponse{Handled: false, Reason: "web_search_fallback_exhausted"}
	}
	return pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "claude_code_web_search_orchestrated",
	}
}
