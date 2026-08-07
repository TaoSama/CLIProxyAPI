package main

import (
	"strings"

	"github.com/tidwall/gjson"
)

const (
	claudeWebSearchToolTypeA = "web_search_20250305"
	claudeWebSearchToolTypeB = "web_search_20260209"
	openAIWebSearchToolType  = "web_search"
	openAIWebSearchPreview   = "web_search_preview"
)

// isClaudeSourceFormat reports whether the inbound protocol is Claude / Anthropic Messages.
func isClaudeSourceFormat(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "claude", "anthropic":
		return true
	default:
		return false
	}
}

func isOpenAIResponsesSourceFormat(source string) bool {
	return strings.EqualFold(strings.TrimSpace(source), "openai-response")
}

func isClaudeTypedWebSearchToolType(toolType string) bool {
	return toolType == claudeWebSearchToolTypeA || toolType == claudeWebSearchToolTypeB
}

func hasClaudeTypedWebSearchTool(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		if isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			return true
		}
	}
	return false
}

func hasOnlyClaudeTypedWebSearchTools(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	hasWebSearch := false
	for _, tool := range tools.Array() {
		if isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			hasWebSearch = true
			continue
		}
		if tool.Get("type").String() != "" || tool.Get("name").String() != "" {
			return false
		}
	}
	return hasWebSearch
}

func isOpenAIResponsesWebSearchToolType(toolType string) bool {
	return toolType == openAIWebSearchToolType || toolType == openAIWebSearchPreview
}

func isOpenAIResponsesWebSearchRequest(body []byte, requireWebSearchOnly bool) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	hasWebSearch := false
	for _, tool := range tools.Array() {
		toolType := tool.Get("type").String()
		if isOpenAIResponsesWebSearchToolType(toolType) {
			hasWebSearch = true
			continue
		}
		if requireWebSearchOnly && toolType != "" {
			return false
		}
	}
	return hasWebSearch
}

func isSupportedWebSearchRequest(source string, body []byte, requireWebSearchOnly bool) bool {
	if isClaudeSourceFormat(source) {
		return isClaudeCodeBuiltinWebSearchRequest(body, requireWebSearchOnly)
	}
	if isOpenAIResponsesSourceFormat(source) {
		return isOpenAIResponsesWebSearchRequest(body, requireWebSearchOnly)
	}
	return false
}

func looksLikeClaudeCodeWebSearchAssistant(body []byte) bool {
	system := gjson.GetBytes(body, "system")
	if system.IsArray() {
		for _, block := range system.Array() {
			text := strings.ToLower(block.Get("text").String())
			if strings.Contains(text, "web search tool use") ||
				strings.Contains(text, "performing a web search") {
				return true
			}
		}
	}
	if system.Type == gjson.String {
		text := strings.ToLower(system.String())
		if strings.Contains(text, "web search tool use") {
			return true
		}
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return false
	}
	for _, message := range messages.Array() {
		if message.Get("role").String() != "user" {
			continue
		}
		text := strings.ToLower(extractClaudeMessageText(message.Get("content")))
		if strings.HasPrefix(text, "perform a web search for the query:") {
			return true
		}
	}
	return false
}

func isClaudeCodeBuiltinWebSearchRequest(body []byte, requireWebSearchOnly bool) bool {
	if !hasClaudeTypedWebSearchTool(body) {
		return false
	}
	if requireWebSearchOnly && !hasOnlyClaudeTypedWebSearchTools(body) {
		return false
	}
	return looksLikeClaudeCodeWebSearchAssistant(body) || hasOnlyClaudeTypedWebSearchTools(body)
}

// modelMatchesOnly reports whether any host-resolved upstream model matches one
// of the onlyModels patterns. An empty patterns list matches everything. Each
// pattern supports a trailing "*" wildcard and matching is case-insensitive.
// requestedModel is used only for compatibility with hosts that do not yet
// populate upstreamModels.
func modelMatchesOnly(upstreamModels []string, requestedModel string, onlyModels []string) bool {
	if len(onlyModels) == 0 {
		return true
	}
	models := upstreamModels
	if len(models) == 0 {
		models = []string{requestedModel}
	}
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" {
			continue
		}
		for _, pattern := range onlyModels {
			p := strings.ToLower(strings.TrimSpace(pattern))
			if p == "" {
				continue
			}
			if strings.HasSuffix(p, "*") {
				if strings.HasPrefix(model, strings.TrimSuffix(p, "*")) {
					return true
				}
				continue
			}
			if model == p {
				return true
			}
		}
	}
	return false
}

func extractClaudeWebSearchQuery(body []byte) string {
	if q := extractQueryFromPerformPrefix(body); q != "" {
		return q
	}
	return extractQueryFromUserMessages(body)
}

func extractQueryFromPerformPrefix(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	const prefix = "perform a web search for the query:"
	for _, message := range messages.Array() {
		if message.Get("role").String() != "user" {
			continue
		}
		text := strings.TrimSpace(extractClaudeMessageText(message.Get("content")))
		lower := strings.ToLower(text)
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(text[len(prefix):])
		}
	}
	return ""
}

func extractQueryFromUserMessages(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return ""
	}
	arr := messages.Array()
	for i := len(arr) - 1; i >= 0; i-- {
		message := arr[i]
		role := message.Get("role").String()
		if role != "" && role != "user" {
			continue
		}
		if query := strings.TrimSpace(extractClaudeMessageText(message.Get("content"))); query != "" {
			return query
		}
	}
	return ""
}

func extractClaudeMessageText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	for _, block := range content.Array() {
		if block.Get("type").String() != "text" {
			continue
		}
		if text := strings.TrimSpace(block.Get("text").String()); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func extractClaudeWebSearchMaxUses(body []byte, defaultMax int) int {
	if defaultMax <= 0 {
		defaultMax = 5
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return defaultMax
	}
	for _, tool := range tools.Array() {
		if !isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		if maxUses := int(tool.Get("max_uses").Int()); maxUses > 0 {
			return maxUses
		}
	}
	return defaultMax
}
