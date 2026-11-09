package executor

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexCacheHelperStabilizesPrefixBeforeFinalPayloadRules(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","input":[{"type":"message","role":"developer","id":"msg_volatile_b","content":"B"},{"type":"message","role":"developer","id":"msg_volatile_a","content":"A"},{"type":"message","role":"user","content":"hello"}]}`)
	// Explicit user rules must be allowed to replace the input without a later
	// normalization undoing either its order or IDs.
	configuredInput := `[{"type":"message","role":"developer","id":"user_chosen_b","content":"B"},{"type":"message","role":"developer","id":"user_chosen_a","content":"A"}]`
	calls := 0
	ctx := helps.WithPayloadFinalizer(context.Background(), func(payload []byte) []byte {
		calls++
		doc := ojson.NewDoc(payload)
		if got := doc.Get("input.0.content").String(); got != "A" {
			t.Fatalf("finalizer saw unstabilized prefix: %s", payload)
		}
		if got := doc.Get("input.0.id").String(); got == "msg_volatile_a" || got == "" {
			t.Fatalf("finalizer saw volatile or missing ID: %q", got)
		}
		if err := doc.SetRaw("input", configuredInput); err != nil {
			t.Fatalf("set final user input: %v", err)
		}
		return doc.Bytes()
	})
	executor := &CodexExecutor{}
	httpReq, finalBody, err := executor.cacheHelper(ctx, sdktranslator.FormatOpenAIResponse,
		"https://example.com/responses", cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: body}, body, nil)
	if err != nil {
		t.Fatalf("cacheHelper: %v", err)
	}
	defer func() {
		if err := httpReq.Body.Close(); err != nil {
			t.Errorf("close request body: %v", err)
		}
	}()
	if calls != 1 {
		t.Fatalf("payload rules applied %d times, want once", calls)
	}
	if got := ojson.GetBytes(finalBody, "input").Raw; got != configuredInput {
		t.Fatalf("final user input changed: %s", got)
	}
	wireBody, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read wire body: %v", err)
	}
	if !bytes.Equal(wireBody, finalBody) {
		t.Fatal("wire body differs from finalized payload")
	}
}
