package helps

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// buildCodexTurn builds a Codex Responses body from an ordered list of leading
// developer blocks (each with its own volatile id) followed by a shared tail.
func buildCodexTurn(t *testing.T, lead []map[string]string, tail []map[string]any) []byte {
	t.Helper()
	input := make([]map[string]any, 0, len(lead)+len(tail))
	for _, b := range lead {
		input = append(input, map[string]any{
			"type": "message",
			"role": "developer",
			"id":   b["id"],
			"content": []map[string]any{
				{"type": "input_text", "text": b["text"]},
			},
		})
	}
	input = append(input, tail...)
	body, err := json.Marshal(map[string]any{
		"model":            "claude-opus-4-8",
		"instructions":     "You are Codex.",
		"prompt_cache_key": "thread-abc",
		"input":            input,
	})
	if err != nil {
		t.Fatalf("marshal turn: %v", err)
	}
	return body
}

func leadingPrefixJSON(t *testing.T, body []byte, n int) string {
	t.Helper()
	items := gjson.GetBytes(body, "input").Array()
	if len(items) < n {
		t.Fatalf("want at least %d input items, got %d", n, len(items))
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, items[i].Raw)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func TestStabilizeCodexLeadingDeveloperPrefix_OrderAndIDJitter(t *testing.T) {
	appCtx := "<app-context>desktop</app-context>"
	maSuppress := "<multi_agent_mode>suppressed</multi_agent_mode>"
	plugins := "<recommended_plugins>list</recommended_plugins>"

	// Turn 1 order: app, ma, plugins  with one set of ids.
	turn1 := buildCodexTurn(t, []map[string]string{
		{"id": "msg_aaa", "text": appCtx},
		{"id": "msg_bbb", "text": maSuppress},
		{"id": "msg_ccc", "text": plugins},
	}, []map[string]any{
		{"type": "message", "role": "user", "id": "msg_user1",
			"content": []map[string]any{{"type": "input_text", "text": "hi"}}},
	})

	// Turn 2 order: app, plugins, ma  with fresh ids (same visible text).
	turn2 := buildCodexTurn(t, []map[string]string{
		{"id": "msg_zzz", "text": appCtx},
		{"id": "msg_yyy", "text": plugins},
		{"id": "msg_xxx", "text": maSuppress},
	}, []map[string]any{
		{"type": "message", "role": "user", "id": "msg_user2",
			"content": []map[string]any{{"type": "input_text", "text": "again"}}},
	})

	s1 := StabilizeCodexLeadingDeveloperPrefix(turn1)
	s2 := StabilizeCodexLeadingDeveloperPrefix(turn2)

	p1 := leadingPrefixJSON(t, s1, 3)
	p2 := leadingPrefixJSON(t, s2, 3)
	if p1 != p2 {
		t.Fatalf("leading developer prefix not stabilized across turns:\n turn1=%s\n turn2=%s", p1, p2)
	}

	// User item (non developer/system) must be preserved and not reordered in.
	last1 := gjson.GetBytes(s1, "input.3")
	if last1.Get("role").String() != "user" {
		t.Fatalf("expected user item preserved at tail, got role %q", last1.Get("role").String())
	}
	// Ids must be deterministic content-derived, not the original volatile ones.
	if gjson.GetBytes(s1, "input.0.id").String() == "msg_aaa" {
		t.Fatalf("expected leading id to be normalized, still volatile: %s", gjson.GetBytes(s1, "input.0.id").String())
	}
}

func TestStabilizeCodexLeadingDeveloperPrefix_PreservesUserOrder(t *testing.T) {
	// A developer block that appears AFTER a user turn must not be pulled into the
	// leading run (that would reorder real conversation history).
	body, _ := json.Marshal(map[string]any{
		"model": "claude-opus-4-8",
		"input": []map[string]any{
			{"type": "message", "role": "developer", "id": "msg_1",
				"content": []map[string]any{{"type": "input_text", "text": "B-block"}}},
			{"type": "message", "role": "developer", "id": "msg_2",
				"content": []map[string]any{{"type": "input_text", "text": "A-block"}}},
			{"type": "message", "role": "user", "id": "msg_u",
				"content": []map[string]any{{"type": "input_text", "text": "u"}}},
			{"type": "message", "role": "developer", "id": "msg_3",
				"content": []map[string]any{{"type": "input_text", "text": "post-user developer"}}},
		},
	})
	out := StabilizeCodexLeadingDeveloperPrefix(body)
	// Leading run [0,1] gets sorted (A-block before B-block); user at 2, dev at 3 stay.
	if gjson.GetBytes(out, "input.0.content.0.text").String() != "A-block" {
		t.Fatalf("leading run not sorted: %s", gjson.GetBytes(out, "input.0.content.0.text").String())
	}
	if gjson.GetBytes(out, "input.2.role").String() != "user" {
		t.Fatalf("user item moved, got role %q", gjson.GetBytes(out, "input.2.role").String())
	}
	if gjson.GetBytes(out, "input.3.content.0.text").String() != "post-user developer" {
		t.Fatalf("post-user developer block was reordered")
	}
}

func TestStabilizeCodexLeadingDeveloperPrefix_NoLeadingRun(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"model": "claude-opus-4-8",
		"input": []map[string]any{
			{"type": "message", "role": "user", "id": "msg_u",
				"content": []map[string]any{{"type": "input_text", "text": "hi"}}},
		},
	})
	out := StabilizeCodexLeadingDeveloperPrefix(body)
	if string(out) != string(body) {
		t.Fatalf("body with no leading developer run must be unchanged")
	}
}
