package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	log "github.com/sirupsen/logrus"
)

// ClaudeRouteMetadata retains only immutable, allowlisted values, never credentials or payloads.
type ClaudeRouteMetadata struct {
	fields log.Fields
	hop    atomic.Uint64
}

func routeDigest(domain, value string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func explicitRouteSession(headers http.Header, payload []byte) (string, string) {
	body, parent, agent := session.ClaudeMetadataIdentities(payload)
	candidates := []string{body}
	for key, values := range headers {
		if strings.EqualFold(key, "X-Claude-Code-Agent-Id") {
			for _, value := range values {
				if value != "" {
					return "", "ambiguous"
				}
			}
		}
		if strings.EqualFold(key, "X-Claude-Code-Session-Id") {
			candidates = append(candidates, values...)
		}
	}
	if parent != "" || agent != "" || headers.Get("X-Claude-Code-Agent-Id") != "" {
		return "", "ambiguous"
	}
	var selected string
	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parsed, errParse := uuid.Parse(raw)
		if errParse != nil || len(raw) != 36 {
			return "", "ambiguous"
		}
		normalized := parsed.String()
		if selected != "" && normalized != selected {
			return "", "ambiguous"
		}
		selected = normalized
	}
	if selected == "" {
		return "", "unavailable"
	}
	return routeDigest("cpa:route-session:v1", selected), "explicit_uuid"
}

// NewClaudeRouteMetadata projects existing request state before any outgoing identity rewriting.
func NewClaudeRouteMetadata(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, opts cliproxyexecutor.Options, payload []byte, provider, operation string) *ClaudeRouteMetadata {
	if cfg == nil || !cfg.RouteMetadataLog {
		return nil
	}
	requestID, errID := uuid.Parse(logging.GetRequestID(ctx))
	if errID != nil {
		return nil
	}
	if operation != "messages" && operation != "count_tokens" {
		return nil
	}
	attemptID, errAttempt := uuid.NewRandom()
	if errAttempt != nil {
		log.Warn("route metadata attempt ID unavailable")
		return nil
	}
	if provider != "claude" {
		provider = "unknown"
	}
	if len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	ref, binding := explicitRouteSession(http.Header(opts.Headers), payload)
	fields := log.Fields{"request_id": requestID.String(), "route_schema": 1, "route_request_id": requestID.String(), "route_attempt_id": attemptID.String(), "route_session_binding": binding, "provider": provider, "route_operation": operation}
	if ref != "" {
		fields["route_session_ref"] = ref
	}
	if auth != nil {
		if auth.ID != "" {
			fields["route_auth_ref"] = routeDigest("cpa:route-auth:v1", auth.ID)
		}
		if index := auth.Attributes["config_index"]; index != "" {
			if n, errIndex := strconv.ParseUint(index, 10, 32); errIndex == nil {
				fields["route_config_index"] = strconv.FormatUint(n, 10)
			}
		}
	}
	// Only commit-shaped build identifiers are safe to copy from build metadata.
	if commit := buildinfo.Commit; len(commit) >= 7 && len(commit) <= 64 {
		if _, errHex := hex.DecodeString(commit); errHex == nil {
			fields["route_build"] = commit
		}
	}
	return &ClaudeRouteMetadata{fields: fields}
}

// WrapClient preserves the original transport pool, redirect policy, timeout and cancellation.
func (m *ClaudeRouteMetadata) WrapClient(client *http.Client) *http.Client {
	if m == nil || client == nil {
		return client
	}
	clone := *client
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = &claudeRouteTransport{base: base, metadata: m}
	return &clone
}

type claudeRouteTransport struct {
	base     http.RoundTripper
	metadata *ClaudeRouteMetadata
}

func safeRouteHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	return true
}

func (t *claudeRouteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Invalid targets still reach the original transport without copying their URL or errors to logs.
	if req.URL == nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || !safeRouteHost(req.URL.Hostname()) {
		return t.base.RoundTrip(req)
	}
	fields := make(log.Fields, len(t.metadata.fields)+6)
	for k, v := range t.metadata.fields {
		fields[k] = v
	}
	fields["route_hop"] = t.metadata.hop.Add(1)
	fields["upstream_scheme"] = req.URL.Scheme
	fields["upstream_host"] = strings.ToLower(req.URL.Hostname())
	fields["route_phase"] = "start"
	log.WithFields(fields).Info("route_metadata_v1")
	resp, errRoundTrip := t.base.RoundTrip(req)
	fields["route_phase"] = "end"
	outcome := "http_response"
	if errRoundTrip != nil {
		outcome = "transport_error"
		if errors.Is(errRoundTrip, context.Canceled) || errors.Is(errRoundTrip, context.DeadlineExceeded) {
			outcome = "cancelled"
		}
	}
	fields["route_outcome"] = outcome
	if resp != nil {
		fields["route_status"] = resp.StatusCode
	}
	log.WithFields(fields).Info("route_metadata_v1")
	return resp, errRoundTrip
}

func (t *claudeRouteTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
