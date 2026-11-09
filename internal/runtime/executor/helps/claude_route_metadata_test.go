package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const routeTestSession = "27045d96-e0c8-48c6-8fb2-2f48595aafac"
const routeTestRequest = "01a1bca9-db05-7fa2-9cdb-49f62c3be2c0"

func routeTestHook(t *testing.T) *logtest.Hook {
	t.Helper()
	logger := log.StandardLogger()
	oldHooks := logger.ReplaceHooks(make(log.LevelHooks))
	oldOutput := logger.Out
	oldLevel := logger.GetLevel()
	logger.SetOutput(io.Discard)
	logger.SetLevel(log.InfoLevel)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() { logger.ReplaceHooks(oldHooks); logger.SetOutput(oldOutput); logger.SetLevel(oldLevel) })
	return hook
}

func routeTestMetadata() *ClaudeRouteMetadata {
	return NewClaudeRouteMetadata(logging.WithRequestID(context.Background(), routeTestRequest), &config.Config{RouteMetadataLog: true}, &cliproxyauth.Auth{ID: "secret-auth", Attributes: map[string]string{"api_key": "secret-key", "config_index": "3"}}, cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{routeTestSession}}}, nil, "claude", "messages")
}

func TestClaudeRouteMetadataBinding(t *testing.T) {
	for _, tc := range []struct{ name, header, body, binding string }{
		{"missing", "", `{}`, "unavailable"},
		{"header", routeTestSession, `{}`, "explicit_uuid"},
		{"payload", "", `{"metadata":{"user_id":"{\"session_id\":\"` + routeTestSession + `\",\"account_uuid\":\"secret-account\"}"}}`, "explicit_uuid"},
		{"conflict", routeTestRequest, `{"metadata":{"user_id":"{\"session_id\":\"` + routeTestSession + `\"}"}}`, "ambiguous"},
		{"invalid", "secret-id", `{}`, "ambiguous"},
		{"derived", "", `{"session_id":"derived:secret-id"}`, "ambiguous"},
		{"subagent", routeTestSession, `{"metadata":{"user_id":"{\"session_id\":\"` + routeTestSession + `\",\"agent_id\":\"secret-agent\"}"}}`, "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := make(http.Header)
			if tc.header != "" {
				headers.Set("X-Claude-Code-Session-Id", tc.header)
			}
			ref, binding := explicitRouteSession(headers, []byte(tc.body))
			if binding != tc.binding {
				t.Fatalf("binding %s", binding)
			}
			if binding == "explicit_uuid" && ref != routeDigest("cpa:route-session:v1", routeTestSession) {
				t.Fatal("wrong session digest")
			}
			if binding != "explicit_uuid" && ref != "" {
				t.Fatal("unbound session exported")
			}
		})
	}
	for _, host := range []string{"secret@example.com", "bad\nhost", "bad/path", "bad?query", "-bad.example", ""} {
		if safeRouteHost(host) {
			t.Fatalf("accepted %q", host)
		}
	}
	for _, host := range []string{"upstream.example", "127.0.0.1", "::1"} {
		if !safeRouteHost(host) {
			t.Fatalf("rejected %q", host)
		}
	}
}

func TestClaudeRouteMetadataDisabled(t *testing.T) {
	hook := routeTestHook(t)
	client := &http.Client{}
	for _, cfg := range []*config.Config{nil, {}, {RouteMetadataLog: false}} {
		m := NewClaudeRouteMetadata(context.Background(), cfg, nil, cliproxyexecutor.Options{}, nil, "claude", "messages")
		if m.WrapClient(client) != client {
			t.Fatal("disabled changed client")
		}
	}
	if len(hook.AllEntries()) != 0 {
		t.Fatal("disabled logging")
	}
}

func TestClaudeRouteMetadataRedirectLeakage(t *testing.T) {
	hook := routeTestHook(t)
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer final.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/secret-redirect?token=secret-query", http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	metadata := routeTestMetadata()
	wrapped := metadata.WrapClient(client)
	if client.Transport != transport || wrapped == client {
		t.Fatal("original mutated")
	}
	req, err := http.NewRequest(http.MethodGet, strings.Replace(first.URL, "http://", "http://secret-user:secret-pass@", 1)+"/secret-path?secret-query", strings.NewReader("secret-body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "secret-header")
	resp, errDo := wrapped.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	entries := hook.AllEntries()
	if len(entries) != 4 {
		t.Fatalf("entries %d", len(entries))
	}
	for i, entry := range entries {
		if entry.Data["route_request_id"] != routeTestRequest || entry.Data["route_hop"] != uint64(i/2+1) || entry.Data["upstream_host"] != "127.0.0.1" {
			t.Fatalf("wrong event %v", entry.Data)
		}
		formatted, errFormat := (&logging.LogFormatter{}).Format(entry)
		if errFormat != nil {
			t.Fatal(errFormat)
		}
		if strings.Contains(string(formatted), "secret") || strings.Contains(string(formatted), routeTestSession) {
			t.Fatalf("sensitive log %s", formatted)
		}
		for _, field := range []string{"route_request_id=", "route_session_ref=", "route_auth_ref=", "upstream_host="} {
			if !strings.Contains(string(formatted), field) {
				t.Fatalf("formatter lost %s", field)
			}
		}
	}
	if entries[3].Data["route_status"] != 403 || entries[3].Data["route_outcome"] != "http_response" {
		t.Fatal("wrong final status")
	}
	wrapped.CloseIdleConnections()
}

type routeTestTransport func(*http.Request) (*http.Response, error)

func (f routeTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClaudeRouteMetadataConcurrentErrors(t *testing.T) {
	hook := routeTestHook(t)
	client := &http.Client{Transport: routeTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		return nil, errors.New("secret-upstream-error")
	})}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sid := fmt.Sprintf("27045d96-e0c8-48c6-8fb2-%012d", i)
			rid := fmt.Sprintf("01a1bca9-db05-7fa2-9cdb-%012d", i)
			m := NewClaudeRouteMetadata(logging.WithRequestID(context.Background(), rid), &config.Config{RouteMetadataLog: true}, nil, cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{sid}}}, nil, "claude", "messages")
			req, errReq := http.NewRequest(http.MethodPost, "https://upstream.example/secret", nil)
			if errReq != nil {
				t.Error(errReq)
				return
			}
			_, errDo := m.WrapClient(client).Do(req)
			if errDo == nil {
				t.Error("missing fixture error")
			}
		}()
	}
	wg.Wait()
	entries := hook.AllEntries()
	if len(entries) != 32 {
		t.Fatalf("events %d", len(entries))
	}
	attempts := map[string]bool{}
	for _, entry := range entries {
		if entry.Data["route_phase"] == "end" {
			if entry.Data["route_outcome"] != "transport_error" {
				t.Fatal("wrong error enum")
			}
			attempts[entry.Data["route_attempt_id"].(string)] = true
			rid := entry.Data["route_request_id"].(string)
			sid := "27045d96-e0c8-48c6-8fb2-" + rid[len(rid)-12:]
			if entry.Data["route_session_ref"] != routeDigest("cpa:route-session:v1", sid) {
				t.Fatal("cross-session contamination")
			}
		}
		b, err := (&logging.LogFormatter{}).Format(entry)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "secret") {
			t.Fatal("error leaked")
		}
	}
	if len(attempts) != 16 {
		t.Fatal("attempts collided")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, "https://upstream.example", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	_, errDo := routeTestMetadata().WrapClient(client).Do(req)
	if !errors.Is(errDo, context.Canceled) {
		t.Fatal("cancel changed")
	}
	if hook.LastEntry().Data["route_outcome"] != "cancelled" {
		t.Fatal("cancel not recorded")
	}
}

func TestClaudeRouteMetadataStreaming(t *testing.T) {
	hook := routeTestHook(t)
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := io.WriteString(w, "data: first\n\n"); err != nil {
			t.Error(err)
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(released)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	resp, errDo := routeTestMetadata().WrapClient(server.Client()).Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	buf := make([]byte, len("data: first\n\n"))
	if _, errRead := io.ReadFull(resp.Body, buf); errRead != nil {
		t.Fatal(errRead)
	}
	if len(hook.AllEntries()) != 2 {
		t.Fatal("headers did not produce metadata before body EOF")
	}
	cancel()
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
}

func TestClaudeRouteMetadataRedirectHostsAndPolicy(t *testing.T) {
	hook := routeTestHook(t)
	visits := 0
	client := &http.Client{Transport: routeTestTransport(func(r *http.Request) (*http.Response, error) {
		visits++
		if r.URL.Hostname() == "first.example" {
			return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://second.example/secret-location?secret-query"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret-response")), Request: r}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://first.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := routeTestMetadata().WrapClient(client)
	resp, errDo := wrapped.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	entries := hook.AllEntries()
	if len(entries) != 4 || entries[0].Data["upstream_host"] != "first.example" || entries[2].Data["upstream_host"] != "second.example" || entries[3].Data["route_status"] != 503 {
		t.Fatal("actual redirect route lost")
	}
	if visits != 2 {
		t.Fatal("changed redirect behavior")
	}
	visits = 0
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, errDo = routeTestMetadata().WrapClient(client).Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if visits != 1 || resp.StatusCode != 307 {
		t.Fatal("CheckRedirect lost")
	}
}

func TestClaudeRouteMetadataAmbiguousHeadersAndSnapshots(t *testing.T) {
	_, binding := explicitRouteSession(http.Header{"x-claude-code-session-id": []string{routeTestSession, routeTestRequest}}, nil)
	if binding != "ambiguous" {
		t.Fatal("conflicting duplicate session headers bound")
	}
	hook := routeTestHook(t)
	auth := &cliproxyauth.Auth{ID: "secret-original", Attributes: map[string]string{"config_index": "secret-index", "base_url": "https://secret-base"}}
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{routeTestSession}}}
	m := NewClaudeRouteMetadata(logging.WithRequestID(context.Background(), routeTestRequest), &config.Config{RouteMetadataLog: true}, auth, opts, nil, "secret-provider", "messages")
	auth.ID = "changed"
	opts.Headers.Set("X-Claude-Code-Session-Id", routeTestRequest)
	client := m.WrapClient(&http.Client{Transport: routeTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	req, err := http.NewRequest(http.MethodGet, "http://upstream.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	entry := hook.LastEntry()
	if entry.Data["provider"] != "unknown" || entry.Data["route_session_ref"] != routeDigest("cpa:route-session:v1", routeTestSession) || entry.Data["route_auth_ref"] != routeDigest("cpa:route-auth:v1", "secret-original") {
		t.Fatal("snapshot changed")
	}
	if _, exists := entry.Data["route_config_index"]; exists {
		t.Fatal("invalid config index copied")
	}
}
