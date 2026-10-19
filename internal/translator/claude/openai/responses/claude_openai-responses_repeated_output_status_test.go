package responses

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_RepeatedToolOutputsPreserveErrorStatus(t *testing.T) {
	statusCases := []struct {
		name      string
		statuses  [3]string
		wantError bool
	}{
		{name: "completed", statuses: [3]string{"completed", "completed", "completed"}},
		{name: "incomplete first", statuses: [3]string{"incomplete", "completed", "completed"}, wantError: true},
		{name: "incomplete middle", statuses: [3]string{"completed", "incomplete", "completed"}, wantError: true},
		{name: "incomplete last", statuses: [3]string{"completed", "completed", "incomplete"}, wantError: true},
		{name: "normalized incomplete", statuses: [3]string{"completed", " INCOMPLETE ", "completed"}, wantError: true},
	}
	for _, callType := range []string{"function_call", "custom_tool_call"} {
		for _, tc := range statusCases {
			t.Run(callType+"/"+tc.name, func(t *testing.T) {
				raw := []byte(fmt.Sprintf(`{"input":[
					{"type":%q,"call_id":"call-repeat","name":"exec","arguments":"{}","input":"run"},
					{"type":%q,"call_id":"call-repeat","status":%q,"output":"first"},
					{"type":%q,"call_id":"call-repeat","status":%q,"output":[{"type":"input_text","text":"middle"}]},
					{"type":%q,"call_id":"call-repeat","status":%q,"output":"last"}
				]}`, callType, callType+"_output", tc.statuses[0], callType+"_output", tc.statuses[1], callType+"_output", tc.statuses[2]))
				converted, err := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
				if err != nil {
					t.Fatalf("convert request: %v", err)
				}
				root := gjson.ParseBytes(converted)
				blocks := root.Get("messages.1.content").Array()
				if len(blocks) != 1 || blocks[0].Get("type").String() != "tool_result" {
					t.Fatalf("expected one merged tool_result, got %s", root.Raw)
				}
				toolResult := blocks[0]
				if got := toolResult.Get("is_error").Bool(); got != tc.wantError {
					t.Errorf("is_error = %v, want %v; output=%s", got, tc.wantError, root.Raw)
				}
				parts := toolResult.Get("content").Array()
				if len(parts) != 3 {
					t.Fatalf("merged content count = %d, want 3; output=%s", len(parts), root.Raw)
				}
				for i, want := range []string{"first", "middle", "last"} {
					if got := parts[i].Get("text").String(); got != want {
						t.Errorf("content[%d].text = %q, want %q", i, got, want)
					}
				}
			})
		}
	}
}
