package helps

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestClaudeRouteMetadataRawIdentityGuards(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		headers    http.Header
	}{
		{"agent-without-userid", `{"metadata":{"agent_id":"child-123"}}`, nil},
		{"parent-header", `{}`, http.Header{"X-Claude-Code-Parent-Agent-Id": []string{"parent-123"}}},
		{"empty-parent-header", `{}`, http.Header{"x-claude-code-parent-agent-id": []string{""}}},
		{"metadata-parent", `{"metadata":{"parent_session_id":"` + routeTestRequest + `"}}`, nil},
		{"invalid-body-session", `{"metadata":{"user_id":"{\"session_id\":\"invalid\\nsession\"}"}}`, nil},
		{"empty-body-session", `{"metadata":{"user_id":"{\"session_id\":\"\"}"}}`, nil},
		{"numeric-body-session", `{"metadata":{"user_id":"{\"session_id\":1}"}}`, nil},
		{"null-body-session", `{"metadata":{"user_id":"{\"session_id\":null}"}}`, nil},
		{"parent-camel", `{"metadata":{"parentAgentId":"parent"}}`, nil},
		{"empty-parent", `{"metadata":{"parent_session_id":""}}`, nil},
		{"invalid-agent", `{"metadata":{"agent_id":"bad\nidentity"}}`, nil},
		{"nested-agent", `{"request":{"metadata":{"subagent_id":"child"}}}`, nil},
		{"nested-extra-parent", `{"request":{"extra_body":{"parentThreadID":"parent"}}}`, nil},
		{"fork", `{"extra_body":{"forkSource":{"sessionId":"parent"}}}`, nil},
		{"duplicate-metadata", `{"metadata":{},"metadata":{"agent_id":"child"}}`, nil},
		{"case-duplicate-metadata", `{"metadata":{},"Metadata":{"agent_id":"child"}}`, nil},
		{"duplicate-user-id", `{"metadata":{"user_id":"{}","user_id":"{}"}}`, nil},
		{"case-duplicate-session", `{"metadata":{"user_id":"{\"session_id\":\"` + routeTestSession + `\",\"SessionId\":\"` + routeTestSession + `\"}"}}`, nil},
		{"case-duplicate-header", `{}`, http.Header{"x-claude-code-session-id": []string{routeTestSession}}},
		{"empty-session-header", `{}`, http.Header{"x-claude-code-session-id": []string{""}}},
		{"invalid-payload", `{`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{"X-Claude-Code-Session-Id": []string{routeTestSession}}
			for key, values := range tc.headers {
				headers[key] = values
			}
			ref, binding := explicitRouteSession(headers, []byte(tc.body))
			if ref != "" || binding != "ambiguous" {
				t.Fatalf("unsafe strong binding: binding=%s ref_present=%v", binding, ref != "")
			}
		})
	}
	// Identity-looking strings inside business messages are never identity signals.
	ref, binding := explicitRouteSession(http.Header{"X-Claude-Code-Session-Id": []string{routeTestSession}}, []byte(`{"messages":[{"role":"user","content":"parent_session_id=secret"}]}`))
	if ref == "" || binding != "explicit_uuid" {
		t.Fatal("message content influenced binding")
	}
}

func TestClaudeRouteMetadataIdentityEnvelopeDepth(t *testing.T) {
	raw := `{"session_id":"` + routeTestSession + `"}`
	for i := 0; i < 8; i++ {
		doc := ojson.NewDoc([]byte(`{}`))
		if errSet := doc.Set("user_id", raw); errSet != nil {
			t.Fatal(errSet)
		}
		raw = doc.String()
	}
	doc := ojson.NewDoc([]byte(`{}`))
	if errSet := doc.Set("metadata.user_id", raw); errSet != nil {
		t.Fatal(errSet)
	}
	ref, binding := explicitRouteSession(http.Header{"X-Claude-Code-Session-Id": []string{routeTestSession}}, doc.Bytes())
	if ref != "" || binding != "ambiguous" {
		t.Fatal("nested identity not bounded")
	}
}

func TestClaudeRouteMetadataExecutionHierarchy(t *testing.T) {
	for _, metadata := range []map[string]any{
		{cliproxyexecutor.ParentSessionIDMetadataKey: ""},
		{cliproxyexecutor.ParentSessionIDMetadataKey: nil},
		{cliproxyexecutor.IsCompactionMetadataKey: true},
		{cliproxyexecutor.IsForkMetadataKey: true},
		{cliproxyexecutor.IsForkMetadataKey: "true"},
		{cliproxyexecutor.IsForkMetadataKey: []string{"invalid"}},
		{cliproxyexecutor.IsCompactionMetadataKey: []string{"invalid"}},
		{cliproxyexecutor.NodeKindMetadataKey: map[string]string{"invalid": "type"}},
		{cliproxyexecutor.NodeKindMetadataKey: "fork"},
	} {
		m := NewClaudeRouteMetadata(logging.WithRequestID(context.Background(), routeTestRequest), &config.Config{RouteMetadataLog: true}, nil, cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{routeTestSession}}, Metadata: metadata}, nil, "claude", "messages")
		if m.fields["route_session_binding"] != "ambiguous" {
			t.Fatal("execution hierarchy bound")
		}
		if _, exists := m.fields["route_session_ref"]; exists {
			t.Fatal("execution hierarchy emitted session reference")
		}
	}
}

func TestClaudeRouteMetadataFalseHierarchyFlags(t *testing.T) {
	m := NewClaudeRouteMetadata(logging.WithRequestID(context.Background(), routeTestRequest), &config.Config{RouteMetadataLog: true}, nil, cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{routeTestSession}}, Metadata: map[string]any{cliproxyexecutor.IsForkMetadataKey: false, cliproxyexecutor.IsCompactionMetadataKey: false, cliproxyexecutor.NodeKindMetadataKey: "trunk"}}, nil, "claude", "messages")
	if m.fields["route_session_binding"] != "explicit_uuid" {
		t.Fatal("false hierarchy flags changed main binding")
	}
}
