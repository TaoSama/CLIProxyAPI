package helps

import (
	"net/http"
	"strings"
	"unicode"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

// Route evidence must preserve raw invalid/present identities that routing normalization may omit.
// This guard examines identity envelopes only, never message/tool content, and does not alter routing.
type routeSessionGuard struct {
	candidates []string
	ambiguous  bool
}

func routeIdentityKey(key string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
}

func routeHierarchyKey(key string) bool {
	key = routeIdentityKey(key)
	return strings.HasPrefix(key, "parent") || strings.HasPrefix(key, "agent") || strings.HasPrefix(key, "subagent") || strings.HasPrefix(key, "child") || strings.HasPrefix(key, "fork") || strings.HasPrefix(key, "previoussession") || key == "isfork" || key == "issubagent" || key == "iscompaction" || key == "nodekind"
}

func (g *routeSessionGuard) candidate(raw string) {
	// Empty, null, non-string and malformed explicit identities cannot be treated as absent.
	parsed, errParse := uuid.Parse(raw)
	if errParse != nil || len(raw) != 36 || raw != strings.TrimSpace(raw) {
		g.ambiguous = true
		return
	}
	g.candidates = append(g.candidates, parsed.String())
}

func (g *routeSessionGuard) userIdentity(value ojson.Result, depth int) {
	if depth > 4 {
		g.ambiguous = true
		return
	}
	if value.Type != ojson.String || strings.TrimSpace(value.Str) == "" {
		g.ambiguous = true
		return
	}
	raw := value.Str
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		if !ojson.Valid(raw) {
			g.ambiguous = true
			return
		}
		g.envelope(ojson.Parse(raw), depth+1)
		// A structured identity without an explicit UUID cannot support a stronger header claim.
		if !ojson.Parse(raw).Get("session_id").Exists() {
			g.ambiguous = true
		}
		return
	}
	// Reuse the legacy extractor, but reject controls before its normalization can erase them.
	for _, c := range raw {
		if unicode.IsControl(c) {
			g.ambiguous = true
			return
		}
	}
	doc := ojson.NewDoc([]byte(`{}`))
	if errSet := doc.Set("metadata.user_id", raw); errSet != nil {
		g.ambiguous = true
		return
	}
	sid, parent, agent := session.ClaudeMetadataIdentities(doc.Bytes())
	if parent != "" || agent != "" {
		g.ambiguous = true
		return
	}
	g.candidate(sid)
}

func (g *routeSessionGuard) envelope(object ojson.Result, depth int) {
	if depth > 4 || !object.IsObject() {
		g.ambiguous = true
		return
	}
	seen := make(map[string]bool)
	object.ForEach(func(key, value ojson.Result) bool {
		normalized := routeIdentityKey(key.Str)
		relevant := routeHierarchyKey(normalized) || normalized == "sessionid" || normalized == "metadata" || normalized == "extrabody" || normalized == "request" || normalized == "userid"
		if !relevant {
			return true
		}
		if seen[normalized] {
			g.ambiguous = true
			return false
		}
		seen[normalized] = true
		if routeHierarchyKey(normalized) {
			g.ambiguous = true
			return false
		}
		switch normalized {
		case "sessionid":
			if value.Type != ojson.String {
				g.ambiguous = true
			} else {
				g.candidate(value.Str)
			}
		case "userid":
			g.userIdentity(value, depth+1)
		case "metadata", "extrabody", "request":
			g.envelope(value, depth+1)
		}
		return !g.ambiguous
	})
}

func explicitRouteSession(headers http.Header, payload []byte) (string, string) {
	guard := routeSessionGuard{}
	canonicalHeaders := make(http.Header)
	for key, values := range headers {
		normalized := routeIdentityKey(key)
		if strings.HasPrefix(normalized, "xclaudecode") && routeHierarchyKey(strings.TrimPrefix(normalized, "xclaudecode")) || normalized == "xagentid" {
			return "", "ambiguous"
		}
		if strings.EqualFold(key, "X-Claude-Code-Session-Id") {
			if len(values) != 1 || len(canonicalHeaders.Values("X-Claude-Code-Session-Id")) != 0 {
				return "", "ambiguous"
			}
			guard.candidate(values[0])
			canonicalHeaders["X-Claude-Code-Session-Id"] = values
		}
	}
	if len(payload) > 0 {
		if !ojson.Valid(string(payload)) {
			return "", "ambiguous"
		}
		guard.envelope(ojson.ParseBytes(payload), 0)
	}
	if guard.ambiguous {
		return "", "ambiguous"
	}
	if len(guard.candidates) == 0 {
		return "", "unavailable"
	}
	selected := guard.candidates[0]
	for _, candidate := range guard.candidates[1:] {
		if candidate != selected {
			return "", "ambiguous"
		}
	}
	// The original extractor remains the source of routing semantics. A raw UUID alone is insufficient.
	info, ok := session.ExtractSessionInfo(canonicalHeaders, payload, nil)
	if !ok || info.ClientType != "claude" || info.ParentSessionID != "" || info.AgentName != "main" || info.IsSubagent || info.IsFork || info.IsCompaction || session.NormalizeToCanonicalUUID(info.SessionID) != selected {
		return "", "ambiguous"
	}
	return routeDigest("cpa:route-session:v1", selected), "explicit_uuid"
}
