package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestClaudeExecutorRouteMetadata(t *testing.T) {
	logger := log.StandardLogger()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	oldOut := logger.Out
	oldLevel := logger.GetLevel()
	logger.SetOutput(io.Discard)
	logger.SetLevel(log.InfoLevel)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() { logger.ReplaceHooks(oldHooks); logger.SetOutput(oldOut); logger.SetLevel(oldLevel) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if _, err := io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"secret-fixture-error"}}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: "secret-fixture-auth", Attributes: map[string]string{"api_key": "secret-fixture-key", "base_url": server.URL, "config_index": "1"}}
	for _, enabled := range []bool{false, true} {
		for _, mode := range []string{"json", "stream", "count_tokens"} {
			t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled]+"-"+mode, func(t *testing.T) {
				hook.Reset()
				exec := NewClaudeExecutor(&config.Config{RouteMetadataLog: enabled})
				ctx := logging.WithRequestID(context.Background(), "01a1bca9-db05-7fa2-9cdb-49f62c3be2c0")
				req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"messages":[{"role":"user","content":"secret-fixture-prompt"}],"max_tokens":8}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"X-Claude-Code-Session-Id": []string{"27045d96-e0c8-48c6-8fb2-2f48595aafac"}}}
				var err error
				if mode == "stream" {
					_, err = exec.ExecuteStream(ctx, auth, req, opts)
				} else if mode == "count_tokens" {
					_, err = exec.countTokensUpstream(ctx, auth, req, opts)
				} else {
					_, err = exec.Execute(ctx, auth, req, opts)
				}
				if err == nil {
					t.Fatal("fixture error lost")
				}
				var routeEntries int
				for _, entry := range hook.AllEntries() {
					if entry.Message != "route_metadata_v1" {
						continue
					}
					routeEntries++
					if entry.Data["route_session_binding"] != "explicit_uuid" {
						t.Fatal("original binding lost")
					}
					formatted, errFormat := (&logging.LogFormatter{}).Format(entry)
					if errFormat != nil {
						t.Fatal(errFormat)
					}
					if strings.Contains(string(formatted), "secret-fixture") {
						t.Fatal("fixture secret leaked")
					}
				}
				expected := 0
				if enabled {
					expected = 2
				}
				if routeEntries != expected {
					t.Fatalf("route events %d want %d", routeEntries, expected)
				}
			})
		}
	}
}
