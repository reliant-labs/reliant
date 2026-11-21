// Copyright (c) 2025 Reliant Labs
package historyrepair

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/models/message"
)

// uniqueID gives a repeated tool-call id the first _dup<n> suffix no call in
// the history holds yet. It is a function of the history alone, so every
// request built from that history renames the call the same way.
func uniqueID(id string, taken map[string]bool) string {
	head, tail, signed := strings.Cut(id, liteLLMThoughtSignatureSeparator)
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s_dup%d", head, n)
		if signed {
			candidate += liteLLMThoughtSignatureSeparator + tail
		}
		if !taken[candidate] {
			return candidate
		}
	}
}

// idAccepted reports whether id meets rules.
func idAccepted(id string, rules IDRules) bool {
	if id == "" {
		return false
	}
	if rules.MaxLen > 0 && len(id) > rules.MaxLen {
		return false
	}
	if rules.Charset {
		for _, c := range id {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}

// rewriteID derives an accepted id from one the provider would refuse:
// call_ followed by 32 hex digits of the id's SHA-256, the shape
// llm.NewToolCallID mints (at most 40 characters, only [a-zA-Z0-9_]). It
// depends on the id alone, so a call and its results get the same new id, and
// so does every later request: the provider's prompt cache keeps matching.
func rewriteID(id string, rules IDRules) string {
	sum := sha256.Sum256([]byte(id))
	h := hex.EncodeToString(sum[:])
	out := "call_" + h[:32]
	if rules.MaxLen > 0 && len(out) > rules.MaxLen {
		out = h[:rules.MaxLen]
	}
	return out
}

// rewriteIDs replaces every tool-call id that breaks rules, on assistant tool
// calls and tool results alike. The zero IDRules accepts any non-empty id;
// an empty id is left alone (there is nothing to pair it by).
func rewriteIDs(ms []message.Message, rules IDRules, r *Report) {
	if rules == (IDRules{}) {
		return
	}
	fix := func(id string) (string, bool) {
		if id == "" || idAccepted(id, rules) {
			return id, false
		}
		return rewriteID(id, rules), true
	}
	for i := range ms {
		for j, part := range ms[i].Parts {
			switch p := part.(type) {
			case message.ToolCall:
				if id, changed := fix(p.ID); changed {
					p.ID = id
					ms[i].Parts[j] = p
					r.RewrittenIDs++
				}
			case message.ToolResult:
				if id, changed := fix(p.ToolCallID); changed {
					p.ToolCallID = id
					ms[i].Parts[j] = p
				}
			}
		}
	}
}
