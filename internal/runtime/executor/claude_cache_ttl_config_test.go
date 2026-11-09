package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestClaudeExecutorCredentialCacheTTLAppliesToBothExecutionModes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, test := range []struct {
			name string
			key  string
			want string
		}{
			{name: "matching credential", key: "key-payload-rule", want: "1h"},
			{name: "unrelated credential", key: "unrelated-key", want: ""},
		} {
			mode := "nonstream/"
			if stream {
				mode = "stream/"
			}
			t.Run(mode+test.name, func(t *testing.T) {
				cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
					APIKey: test.key,
					Cloak:  &config.CloakConfig{CacheTTL: "1h"},
				}}}
				payload := []byte(`{"model":"claude-opus-5","max_tokens":100,"system":"Helpful assistant","tools":[{"name":"lookup","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}`)
				body := executeClaudeContextManagementRequest(t, cfg, payload, stream)
				markers := 0
				forEachClaudeCacheControlPathDoc(ojson.NewDoc(body), func(path string, cacheControl ojson.Result) bool {
					markers++
					if got := cacheControl.Get("ttl").String(); got != test.want {
						t.Errorf("%s ttl = %q, want %q", path, got, test.want)
					}
					return true
				})
				if markers == 0 {
					t.Fatalf("no cache breakpoints found: %s", body)
				}
			})
		}
	}
}
