// File: internal/linter/forgeconv/list_filter_optional.go
//
// forgeconv-list-filter-optional — a List request's filter fields must be
// `optional`.
//
// forge documents this in every project's CLAUDE.md ("List request filter
// fields must be optional — without it, generated filters silently no-op")
// and in the proto skill, and until this rule nothing checked it. It was
// found dogfooding forge on control-plane's operator console:
// ListUsageEventsRequest carried a plain `string org_id`, `forge lint` passed,
// the born list page called the hook with no org_id, and every load failed
// with "org_id is required" — a contract the proto never stated.
//
// ── Why presence is the whole question ───────────────────────────────────
//
// A proto3 scalar without `optional` has no presence: unset and the zero
// value are the same bytes on the wire. On a filter that collapses "no
// filter" into "filter by zero", and every consumer has to guess which one
// the caller meant:
//
//   - forge's generated List op guesses per kind. A string or number skips
//     the zero value, so the zero value can never be filtered on. A bool or
//     enum is ALWAYS applied, so a caller that omits it gets only the
//     `false` / UNSPECIFIED rows — a list that silently returns a subset.
//   - the born list page and its hooks cannot tell a filter from a required
//     parameter, so a field the handler insists on is rendered as an
//     optional filter and the page's first load is an error.
//
// `optional` gives the field presence (a Go pointer, a TS `?:`), which is
// what lets "unset" mean "no filter" everywhere at once. A field that is NOT
// a filter — a parent id the list is scoped to, a required org — says so with
// `(buf.validate.field).required = true` instead. Either way the proto, not
// the handler's body, records which of the two the field is; the defect is
// the ambiguity, and that is what the rule rejects.
//
// ── What it checks ────────────────────────────────────────────────────────
//
// A message is a List request when it is named `List<X>Request` or is the
// request of an rpc named `List<X>`. Its fields are flagged when they are a
// scalar or an enum with no presence: not `optional`, not `repeated`, not a
// map, not a oneof member (oneof members have presence).
//
// Deliberately NOT flagged:
//
//   - message-typed fields (Timestamp, wrappers, nested messages): message
//     fields always have presence.
//   - pagination and ordering controls (paginationFields): they are request
//     knobs with a meaningful zero (default page size, first page, natural
//     order), not filters — forge's own generator skips the same names.
//   - a field declaring `(buf.validate.field).required = true`: that is a
//     required parameter rather than a filter, the zero value is rejected on
//     the wire, and so the ambiguity the rule exists to remove cannot occur.
//     It is also the self-documenting answer for a field like org_id.
//
// Enums are resolved across the whole proto tree, so a filter typed by an
// enum declared in proto/shared/v1/types.proto is still an enum. A type the
// tree cannot resolve at all (an import from outside proto/) is assumed to
// be a message — the direction that never produces a false finding.
//
// Severity is ERROR, like the other proto convention rules: the rule is
// documented as a "must", and the failure it prevents is silent. The usual
// `// forge:lint-disable-next-line forgeconv-list-filter-optional: <why>`
// above the field is the escape hatch.

package forgeconv

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// ruleListFilterOptional is the stable rule ID: it appears in output and is
// the name a forge:lint-disable directive or a forge.yaml lint.rules entry
// names.
const ruleListFilterOptional = "forgeconv-list-filter-optional"

// paginationFields are List-request controls rather than filters. The first
// six are exactly what forge's CRUD generator skips when it classifies
// filters (classifySkipField + order_by); the rest are the common spellings
// of the same controls on hand-written list APIs.
var paginationFields = map[string]bool{
	"page_size": true, "page_token": true, "order_by": true,
	"descending": true, "desc": true, "sort_order": true,
	"cursor": true, "limit": true, "offset": true,
	"page": true, "per_page": true, "max_results": true, "sort_by": true,
}

var (
	// listRequestNameRE matches the conventional List request name.
	listRequestNameRE = regexp.MustCompile(`^List[A-Z]\w*Request$`)
	// listRPCNameRE matches a List rpc (`ListOrders`, not `Listen`).
	listRPCNameRE = regexp.MustCompile(`^List[A-Z]`)
)

// checkListFilterOptional runs the rule over every .proto file the tree walk
// found and returns the findings keyed by the file's path relative to
// rootDir — the same frame lintProtoFile reports in, so the per-file
// suppression pass applies to both alike.
//
// The scan is per DIRECTORY because a directory is a proto package (buf's
// PACKAGE_DIRECTORY_MATCH) and the raw scanner qualifies names with one
// package. Enums and List rpcs are then indexed across every package before
// any message is judged, because a List request's filter can be typed by an
// enum declared in another package, and a List rpc can live in a different
// file from its request.
func checkListFilterOptional(rootDir string, protoFiles []string) map[string][]Finding {
	byDir := map[string][]string{}
	for _, f := range protoFiles {
		byDir[filepath.Dir(f)] = append(byDir[filepath.Dir(f)], f)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var scans []*codegen.RawProtoScan
	enums := map[string]bool{}
	listRequests := map[string]bool{}
	// Request messages of rpcs on `// forge:unmanaged` services, and of rpcs
	// on managed ones. The rule's premise is forge's GENERATED list filter,
	// and forge generates nothing for an unmanaged service — its handlers
	// read the request however they like. A request only unmanaged rpcs use
	// is therefore not this rule's to judge; the findings arrive when the
	// service is adopted, which is when they start to matter.
	unmanagedRequests := map[string]bool{}
	managedRequests := map[string]bool{}
	for _, d := range dirs {
		scan, err := codegen.ScanRawProtoFiles(byDir[d])
		if err != nil {
			// A file forge's own scanner cannot read is not this rule's to
			// report — buf lint and `forge generate` both fail on it far
			// more clearly. Skipping keeps one bad package from masking the
			// rest of the tree.
			continue
		}
		scans = append(scans, scan)
		for fq := range scan.Enums {
			enums[fq] = true
		}
		for _, rpc := range scan.RPCs {
			if rpc.Request == "" {
				continue
			}
			// A `// forge:custom` rpc is exempt for the same reason: forge
			// generates no list filter and no page for it.
			unmanaged := scan.ServiceUnmanaged(rpc.Service) || rpc.Custom
			requests := managedRequests
			if unmanaged {
				requests = unmanagedRequests
			}
			requests[rpc.Request] = true
			requests[scan.Package+"."+rpc.Request] = true
			if unmanaged || !listRPCNameRE.MatchString(rpc.Name) {
				continue
			}
			// The request may be written package-relative or fully
			// qualified; record both readings — only real names can match.
			listRequests[rpc.Request] = true
			listRequests[scan.Package+"."+rpc.Request] = true
		}
	}

	out := map[string][]Finding{}
	contents := map[string]string{}
	for _, scan := range scans {
		for _, msg := range scan.Messages {
			fq := scan.Package + "." + msg.Name
			if !listRequestNameRE.MatchString(msg.Name) && !listRequests[fq] {
				continue
			}
			if unmanagedRequests[fq] && !managedRequests[fq] {
				continue
			}
			for _, f := range msg.Fields {
				kind, ok := filterWithoutPresence(f, enums)
				if !ok {
					continue
				}
				content, seen := contents[msg.File]
				if !seen {
					raw, err := os.ReadFile(msg.File)
					if err == nil {
						content = blankProtoComments(string(raw))
					}
					contents[msg.File] = content
				}
				line, decl := locateField(content, msg, f.Name)
				rel, err := filepath.Rel(rootDir, msg.File)
				if err != nil {
					rel = msg.File
				}
				out[rel] = append(out[rel], listFilterFinding(rel, line, msg.Name, f.Name, kind, decl))
			}
		}
	}
	return out
}

// filterWithoutPresence reports whether a List-request field is a filter
// whose unset and zero states are indistinguishable, and if so what kind of
// value it is ("string", "bool", "int32", "enum", …) for the message.
func filterWithoutPresence(f codegen.SchemaFieldDef, enums map[string]bool) (string, bool) {
	if f.Optional || f.Repeated || f.Oneof != "" || f.Kind == "map" {
		return "", false // has presence, or is not a single value
	}
	if paginationFields[f.Name] {
		return "", false
	}
	if f.Validate != nil && f.Validate.Required {
		return "", false // a declared required parameter, not a filter
	}
	switch {
	case codegen.IsProtoScalarKind(f.Kind):
		return f.Kind, true
	case f.Kind == "enum":
		return "enum", true
	case f.Kind == "message" && enums[f.TypeName]:
		// The raw scanner cannot see another package's declarations, so it
		// reports a cross-package enum as a message type; the tree index
		// knows better.
		return "enum", true
	}
	return "", false
}

// zeroValueFor names, for the message, the value an unset field of this kind
// reads as.
func zeroValueFor(kind string) string {
	switch kind {
	case "string":
		return `""`
	case "bytes":
		return "empty bytes"
	case "bool":
		return "false"
	case "enum":
		return "the UNSPECIFIED (0) value"
	default:
		return "0"
	}
}

func listFilterFinding(file string, line int, msg, field, kind, decl string) Finding {
	// As a filter, a string/number skips its zero value; a bool/enum cannot
	// be skipped (its zero is a real choice), so it is always applied.
	consequence := "as a filter its zero value can never be filtered on"
	switch kind {
	case "bool":
		consequence = "as a filter it is always applied, so omitting it returns only the false rows"
	case "enum":
		consequence = "as a filter it is always applied, so omitting it returns only the UNSPECIFIED rows"
	}
	return Finding{
		Rule:     ruleListFilterOptional,
		Severity: SeverityError,
		File:     file,
		Line:     line,
		Message: fmt.Sprintf(
			"%s.%s has no field presence: unset and %s are the same value on the wire, so the proto does not say whether it is a filter or a required parameter (%s)",
			msg, field, zeroValueFor(kind), consequence),
		Remediation: fmt.Sprintf(
			"a filter: mark it `optional` — `optional %s;` — so unset means \"no filter\" end to end (Go pointer, TS `?:`, generated nil-guard). "+
				"A required parameter (a parent or org id the handler insists on): declare it with `[(buf.validate.field).required = true]`",
			decl),
	}
}

// fieldDeclRE builds the matcher for one field's declaration. It is not
// anchored to a line start because single-line bodies
// (`message X { int32 page_size = 1; string q = 2; }`) put several
// declarations on one line.
func fieldDeclRE(field string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\s;{])(?:optional\s+|repeated\s+)?([\w.]+\s+` + regexp.QuoteMeta(field) + `\s*=\s*\d+)`)
}

// locateField finds a field's declaration inside its message body and
// returns its 1-based line and its source spelling (`string org_id = 1`).
// content has had comments blanked, so a commented-out declaration is never
// mistaken for the real one. Falls back to the message's opening line —
// a usable location, never a crash — when the declaration cannot be found
// textually.
func locateField(content string, msg codegen.RawProtoMessage, field string) (int, string) {
	if content != "" && msg.BodyOpen <= msg.BodyClose && msg.BodyClose <= len(content) {
		body := content[msg.BodyOpen:msg.BodyClose]
		if m := fieldDeclRE(field).FindStringSubmatchIndex(body); m != nil {
			decl := strings.Join(strings.Fields(body[m[2]:m[3]]), " ")
			return strings.Count(content[:msg.BodyOpen+m[2]], "\n") + 1, decl
		}
	}
	line := 1
	if content != "" && msg.BodyOpen <= len(content) {
		line = strings.Count(content[:msg.BodyOpen], "\n") + 1
	}
	return line, "<type> " + field + " = <n>"
}

// blankProtoComments replaces every // and /* */ comment with spaces,
// keeping newlines and byte offsets intact so offsets from the raw scan stay
// valid. Double-quoted strings are skipped over, so a `//` inside an option
// value is not taken for a comment.
func blankProtoComments(s string) string {
	b := []byte(s)
	inString, inLine, inBlock := false, false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
			} else {
				b[i] = ' '
			}
		case inBlock:
			if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				b[i], b[i+1] = ' ', ' '
				i++
				inBlock = false
			} else if c != '\n' {
				b[i] = ' '
			}
		case inString:
			if c == '\\' && i+1 < len(b) {
				i++
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			b[i] = ' '
			inLine = true
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			b[i], b[i+1] = ' ', ' '
			i++
			inBlock = true
		}
	}
	return string(b)
}
