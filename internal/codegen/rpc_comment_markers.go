// File: internal/codegen/rpc_comment_markers.go
//
// The rpc-level comment markers read on EVERY generate (not at birth):
// `// forge:mutation` and `// forge:query-scope <Entity>`. Two passes read
// them — the frontend hook generator (internal/cli/generate_frontend_hooks.go)
// and `forge lint --idempotency` (internal/cli/lint/lint_idempotency.go) — and
// they must agree on which rpc a marker labels, or the lint would accept a
// `forge:mutation` the hook generator never sees (the rpc stays a useQuery)
// or refuse one it honours. So the attachment walk lives here, once.

package codegen

import (
	"regexp"
	"strings"
)

// ProtoMarkerMutationRE matches the ProtoMarkerMutation token anywhere in a
// line. A word character right after it is a longer token
// (`forge:mutations`), which never matches.
var ProtoMarkerMutationRE = regexp.MustCompile(regexp.QuoteMeta(ProtoMarkerMutation) + `\b`)

// mutationReasonRE captures the prose after a forge:mutation token: the
// reason the author gives for an rpc that writes. Leading `:`, `—`, `–` or
// `-` separators are not part of the reason.
var mutationReasonRE = regexp.MustCompile(regexp.QuoteMeta(ProtoMarkerMutation) + `\b\s*(?::|—|–|-)?\s*(.*)$`)

// protoMarkerQueryScopeRE matches `forge:query-scope [<Entity>]` and captures
// the entity. A hyphen or word character right after the token is a
// different, longer token, so it never matches; a bare marker captures "".
var protoMarkerQueryScopeRE = regexp.MustCompile(regexp.QuoteMeta(ProtoMarkerQueryScope) +
	`(?:[ \t]+([A-Za-z_]\w*))?(?:[^\w-]|$)`)

// rpcDeclLineRE matches a proto `rpc <Name>(` service-method declaration at
// the start of a line and captures the method name.
var rpcDeclLineRE = regexp.MustCompile(`^\s*rpc\s+([A-Za-z_]\w*)\s*\(`)

// RPCCommentMarkers is what the comments of one .proto file say about its
// rpcs, keyed by rpc name.
type RPCCommentMarkers struct {
	// Mutation: the rpc carries `// forge:mutation`.
	Mutation map[string]bool
	// MutationReason: the prose after the forge:mutation token on the line
	// that carries it (`// forge:mutation — holds a seat` → "holds a seat");
	// "" for a bare marker.
	MutationReason map[string]string
	// QueryScope: the entity `// forge:query-scope <Entity>` names ("" for a
	// marker naming none).
	QueryScope map[string]string
	// Lines: the 1-based line of each rpc's `rpc` keyword.
	Lines map[string]int
}

// ScanRPCCommentMarkers reads the rpc comment markers out of one .proto
// file's source.
//
// A marker attaches like a protoc leading doc-comment: it counts when it
// appears in the contiguous comment block directly above the `rpc` line, or
// as a trailing comment on the `rpc` line itself. A blank (non-comment) line
// between a comment and the rpc breaks the attachment, matching how protoc
// associates leading comments — so a marker can't leak onto an unrelated rpc
// further down the file.
func ScanRPCCommentMarkers(content string) RPCCommentMarkers {
	out := RPCCommentMarkers{
		Mutation:       map[string]bool{},
		MutationReason: map[string]string{},
		QueryScope:     map[string]string{},
		Lines:          map[string]int{},
	}
	pendingMutation, pendingReason := false, ""
	pendingScope, havePendingScope := "", false
	reset := func() {
		pendingMutation, pendingReason = false, ""
		pendingScope, havePendingScope = "", false
	}
	for i, line := range strings.Split(content, "\n") {
		if m := rpcDeclLineRE.FindStringSubmatch(line); m != nil {
			name := m[1]
			out.Lines[name] = i + 1
			// Marker in the attached comment block above, or trailing on the
			// rpc line itself.
			switch {
			case ProtoMarkerMutationRE.MatchString(line):
				out.Mutation[name] = true
				out.MutationReason[name] = mutationReason(line)
			case pendingMutation:
				out.Mutation[name] = true
				out.MutationReason[name] = pendingReason
			}
			if sm := protoMarkerQueryScopeRE.FindStringSubmatch(line); sm != nil {
				out.QueryScope[name] = sm[1]
			} else if havePendingScope {
				out.QueryScope[name] = pendingScope
			}
			reset()
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "//"),
			strings.HasPrefix(trimmed, "/*"),
			strings.HasPrefix(trimmed, "*"):
			// Comment line within (or opening) the block above the rpc.
			if ProtoMarkerMutationRE.MatchString(trimmed) {
				pendingMutation = true
				if r := mutationReason(trimmed); r != "" || pendingReason == "" {
					pendingReason = r
				}
			}
			if sm := protoMarkerQueryScopeRE.FindStringSubmatch(trimmed); sm != nil {
				pendingScope, havePendingScope = sm[1], true
			}
		default:
			// A blank line breaks leading-comment attachment, and any
			// other statement resets it too.
			reset()
		}
	}
	return out
}

// mutationReason is the prose after the forge:mutation token on line, with
// a closing `*/` and surrounding space removed.
func mutationReason(line string) string {
	m := mutationReasonRE.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), "*/"))
}
