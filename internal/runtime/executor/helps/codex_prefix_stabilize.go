package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StabilizeCodexLeadingDeveloperPrefix makes the leading run of developer/system
// message items in a Codex Responses "input" array byte-stable across turns so
// that upstream prompt-prefix caching keeps hitting past the instructions block.
//
// Two turn-to-turn perturbations break exact-prefix caching for official Codex
// multi-agent requests even though the visible text is identical:
//
//  1. Item ordering jitter: the leading developer/system blocks (for example the
//     desktop app-context reminder, the multi-agent-mode notice, the recommended
//     plugins list, and the "You are an agent in a team" preamble) are emitted in
//     a different relative order on different turns.
//  2. Item id jitter: the same logical developer/system message is re-emitted with
//     a fresh "msg_..." id every turn.
//
// Either change alters the serialized byte prefix after "instructions", so the
// cache only matches the instructions block and every later item misses.
//
// This normalization only touches the contiguous leading run of message items
// whose role is developer or system. It:
//   - rewrites each such item id to a deterministic content-derived id, and
//   - stably sorts the run by its textual content.
//
// It never reorders or rewrites user, assistant, reasoning, tool, or
// agent_message items, and it stops at the first non developer/system message
// item, so real conversation turns and their ordering are preserved. Codex does
// not cross-reference message item ids from other items, so rewriting them is
// safe for upstream item association.
func StabilizeCodexLeadingDeveloperPrefix(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	if len(items) < 2 {
		return body
	}

	// Identify the contiguous leading run of developer/system message items.
	runEnd := 0
	for runEnd < len(items) {
		it := items[runEnd]
		if it.Get("type").String() != "message" {
			break
		}
		role := it.Get("role").String()
		if role != "developer" && role != "system" {
			break
		}
		runEnd++
	}
	if runEnd < 2 {
		// Nothing to reorder; still normalize a lone leading block's id below.
		if runEnd == 0 {
			return body
		}
	}

	type leadItem struct {
		raw     string
		content string
		id      string
	}
	lead := make([]leadItem, 0, runEnd)
	for i := 0; i < runEnd; i++ {
		it := items[i]
		lead = append(lead, leadItem{
			raw:     it.Raw,
			content: codexMessageContentText(it),
			id:      it.Get("id").String(),
		})
	}

	changed := false

	// 1. Deterministic content-derived ids remove per-turn id jitter.
	for idx := range lead {
		if lead[idx].id == "" {
			continue
		}
		stable := codexStableMessageID(lead[idx].content)
		if stable == lead[idx].id {
			continue
		}
		next, err := sjson.SetBytes([]byte(lead[idx].raw), "id", stable)
		if err != nil {
			continue
		}
		lead[idx].raw = string(next)
		lead[idx].id = stable
		changed = true
	}

	// 2. Stable sort by content removes per-turn ordering jitter. sort.SliceStable
	// keeps equal-content items in their original order.
	ordered := make([]leadItem, len(lead))
	copy(ordered, lead)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].content < ordered[j].content
	})
	for i := range ordered {
		if ordered[i].raw != lead[i].raw {
			changed = true
			break
		}
	}

	if !changed {
		return body
	}

	rebuilt := make([]string, 0, len(items))
	for i := range ordered {
		rebuilt = append(rebuilt, ordered[i].raw)
	}
	for i := runEnd; i < len(items); i++ {
		rebuilt = append(rebuilt, items[i].Raw)
	}

	updated, err := sjson.SetRawBytes(body, "input", []byte("["+strings.Join(rebuilt, ",")+"]"))
	if err != nil {
		return body
	}
	return updated
}

// codexMessageContentText concatenates the textual content of a message item so
// that ordering and identity decisions depend only on the visible payload.
func codexMessageContentText(item gjson.Result) string {
	content := item.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var sb strings.Builder
	content.ForEach(func(_, part gjson.Result) bool {
		if text := part.Get("text"); text.Exists() {
			sb.WriteString(text.String())
		}
		return true
	})
	return sb.String()
}

// codexStableMessageID derives a deterministic message id from content so the
// same logical developer/system block reuses one id across turns.
func codexStableMessageID(content string) string {
	sum := sha256.Sum256([]byte(content))
	return codexMessageItemIDPrefix + "_" + hex.EncodeToString(sum[:16])
}
