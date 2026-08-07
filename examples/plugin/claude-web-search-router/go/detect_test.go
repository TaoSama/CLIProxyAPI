package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectClaudeCodeWebSearchFromFixture(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "temp", "1.json")
	raw, errRead := os.ReadFile(root)
	if errRead != nil {
		t.Skipf("fixture not found: %v", errRead)
	}
	// Fixture is HTTP capture; extract JSON request body between first blank line after headers.
	body := extractHTTPJSONBody(raw)
	if len(body) == 0 {
		t.Fatal("empty JSON body in fixture")
	}
	if !hasClaudeTypedWebSearchTool(body) {
		t.Fatal("fixture should declare web_search_20250305")
	}
	if !looksLikeClaudeCodeWebSearchAssistant(body) {
		t.Fatal("fixture should match Claude Code web search assistant heuristics")
	}
	if !isClaudeCodeBuiltinWebSearchRequest(body, true) {
		t.Fatal("expected match with require_web_search_only=true")
	}
	query := extractClaudeWebSearchQuery(body)
	if query == "" {
		t.Fatal("expected non-empty search query")
	}
	if want := "北京天气 2026年6月16日"; query != want {
		t.Fatalf("query = %q, want %q", query, want)
	}
}

// TestOpenAIResponsesWebSearchDetection locks the exclusive-gate relaxation the
// router applies for the openai-response path (Codex attaches web_search to
// multi-tool reasoning turns).
func TestOpenAIResponsesWebSearchDetection(t *testing.T) {
	multiTool := []byte(`{"tools":[{"type":"function","name":"exec"},{"type":"web_search"}]}`)
	if isOpenAIResponsesWebSearchRequest(multiTool, true) {
		t.Fatal("multi-tool turn should not match with require_web_search_only=true")
	}
	if !isOpenAIResponsesWebSearchRequest(multiTool, false) {
		t.Fatal("multi-tool turn should match when the exclusive gate is relaxed")
	}
	if !isSupportedWebSearchRequest("openai-response", multiTool, false) {
		t.Fatal("isSupportedWebSearchRequest should accept relaxed openai-response web_search")
	}
	noSearch := []byte(`{"tools":[{"type":"function","name":"exec"}]}`)
	if isOpenAIResponsesWebSearchRequest(noSearch, false) {
		t.Fatal("turn without web_search must not match")
	}
}

func extractHTTPJSONBody(raw []byte) []byte {
	text := string(raw)
	idx := 0
	for {
		next := findDoubleNewline(text, idx)
		if next < 0 {
			return nil
		}
		rest := trimLeft(text[next:])
		if len(rest) > 0 && rest[0] == '{' {
			return []byte(rest)
		}
		idx = next + 1
	}
}

func findDoubleNewline(s string, from int) int {
	for i := from; i+1 < len(s); i++ {
		if s[i] == '\n' && s[i+1] == '\n' {
			return i + 2
		}
		if s[i] == '\r' && i+3 < len(s) && s[i+1] == '\n' && s[i+2] == '\r' && s[i+3] == '\n' {
			return i + 4
		}
	}
	return -1
}

func trimLeft(s string) string {
	for len(s) > 0 && (s[0] == '\r' || s[0] == '\n' || s[0] == ' ') {
		s = s[1:]
	}
	return s
}
