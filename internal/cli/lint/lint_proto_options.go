// File: internal/cli/lint/lint_proto_options.go
//
// proto-options — `forge lint --proto-options`.
//
// The OPTION-field twin of the comment-marker check next door
// (lint_proto_markers.go). That one catches a misspelled `// forge:*`
// COMMENT marker; this one catches a `(forge.v1.*)` annotation naming an
// option FIELD that forge's compiled descriptor does not define.
//
// The two are the same silent-failure shape arriving by different routes,
// and the second route is the one that did real damage. A project's
// vendored forge.proto had diverged from forge's own (see
// lint_vendored_protos.go): it still declared method options
// `authz_public`, `required_roles`, `authz_custom` and `default_roles` on
// field numbers upstream had since RESERVED. So:
//
//   - `buf` compiled the annotations happily — the LOCAL forge.proto
//     declared those fields, so they are valid proto.
//   - forge read its OWN compiled descriptor, where those numbers are
//     reserved and the fields do not exist, and found nothing.
//
// 104 annotations across 14 service protos declared an authorization
// posture that was enforced by nothing. No error, no warning, no output
// of any kind. A migration agent eventually found them by hand.
//
// ── Source of truth ───────────────────────────────────────────────────
//
// The compiled descriptors linked into the running forge binary, reached
// through protoregistry — the SAME descriptors `forge project annotations`
// dumps and the same ones forge's own readers consult. Not a hand-kept
// list: a hand-kept list is how the vocabulary drifts, which is the bug.
//
// This gives two properties for free:
//
//   - RESERVED field numbers are absent from a descriptor, so a retired
//     option is "unknown" automatically. There is no separate
//     RemovedProtoOptions map to maintain, and none can go stale.
//   - A forge binary that ADDS an option immediately accepts it, and one
//     that has not yet gained it says so, rather than guessing.
//
// Suggestions reuse codegen.ClosestProtoMarker — the Levenshtein helper
// the marker check already uses — rather than a parallel implementation.
//
// ── Severity ──────────────────────────────────────────────────────────
//
// WARNING, deliberately, even though the failure it names is severe.
//
// The scan is a source-level parse, not a buf compile: it reads `.proto`
// text without resolving imports, so it cannot know whether a project has
// legitimately extended forge's option messages in its own vendored copy
// (which the sibling vendored-protos check reports at error severity, in
// the one place where that judgment can be made correctly). Reporting a
// hard failure on a parse that cannot see the whole picture is how a lint
// earns a project-wide `--no-verify` habit. The finding names the field,
// the annotation, the closest match and the dump command; that is enough
// to act on, and the vendored-protos check gates the root cause.
//
// ── False positives this deliberately avoids ──────────────────────────
//
//   - COMMENTS. forge.proto's own header, and every scaffolded
//     config.proto, document annotations in `//` comment blocks. Comments
//     are stripped before scanning; a documented example is not a
//     declaration.
//   - THE VENDORED forge.proto ITSELF. It DEFINES these messages, so its
//     `bool auth_required = 1;` lines are definitions, not uses. Every
//     project contains this file, so mistaking definitions for uses would
//     fire everywhere. Skipped by path.
//   - FOREIGN EXTENSIONS. `(buf.validate.field)` is protovalidate's
//     vocabulary, not forge's, and is never checked against forge's
//     descriptors. Only `(forge.v1.*)` is in scope.
//   - STRING AND ENUM VALUES. `errors: ["NotFound"]` — the contents of a
//     string literal are values; the scanner never mines them for names.
//   - NESTED SUBMESSAGES resolve against the nested message's own
//     descriptor (`auth: { auth_required: true }` checks AuthConfig, not
//     ServiceOptions), so a valid nested field is not flagged for being
//     absent from the outer message.
//   - An UNRESOLVABLE extension — a `(forge.v1.something)` this binary has
//     never heard of — yields ONE finding naming the extension, and its
//     body is not then scanned into a cascade of field findings.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/reliant-labs/forge/pkg/forgepb" // registers the forge.v1 extensions

	"github.com/reliant-labs/forge/internal/codegen"
)

// forgeExtensionPrefix scopes the check to forge's own annotations.
const forgeExtensionPrefix = "forge.v1."

// protoOptionDumpCmd prints the authoritative option surface, read from
// the same compiled descriptors this check validates against.
const protoOptionDumpCmd = "forge project annotations"

// protoOptionFinding is one `(forge.v1.*)` option field that forge's
// compiled descriptor does not define. File is as walked.
type protoOptionFinding struct {
	File string
	Line int
	// Extension is the annotation carrying the field, e.g. "forge.v1.method".
	Extension string
	// Message is the descriptor the field was resolved against, e.g.
	// "forge.v1.MethodOptions" — the NESTED message for a nested field.
	// Empty when the extension itself is unknown.
	Message string
	// Field is the offending option field name, verbatim. Empty when the
	// EXTENSION rather than a field is what forge does not define.
	Field string
	// Known lists the field names the message does define, for the hint.
	Known []string
	// Suggestion is the closest known field by edit distance, when there
	// is an obvious near-match; empty otherwise.
	Suggestion string
}

// protoOptionFixHint renders the remediation as a runbook: what is
// inert, the near-match when there is one, and the fields that DO exist —
// listed from the descriptor, so the hint can never advertise a field
// forge does not have.
func protoOptionFixHint(f protoOptionFinding) string {
	if f.Field == "" {
		return fmt.Sprintf(
			"(%s) is not an annotation this forge binary defines — the whole option is read by NOTHING. "+
				"If your project's vendored proto/forge/v1/forge.proto declares it, that copy has drifted: "+
				"run `forge lint --vendored-protos`, then `forge project upgrade` to re-copy it. "+
				"See `%s` for the annotations forge actually reads.",
			f.Extension, protoOptionDumpCmd)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "(%s).%s is not a field %s defines — this annotation compiles but is read by NOTHING. ",
		f.Extension, f.Field, f.Message)
	if f.Suggestion != "" {
		fmt.Fprintf(&b, "Did you mean %q? ", f.Suggestion)
	} else {
		b.WriteString("A field forge RETIRED is absent from its descriptor, so a stale annotation reads exactly like a typo: " +
			"if your project's vendored proto/forge/v1/forge.proto still declares it, that copy has drifted " +
			"(`forge lint --vendored-protos`). ")
	}
	if len(f.Known) > 0 {
		fmt.Fprintf(&b, "Fields of %s: %s. ", f.Message, strings.Join(f.Known, ", "))
	}
	fmt.Fprintf(&b, "Remove the field or replace it; see `%s`.", protoOptionDumpCmd)
	return b.String()
}

// collectProtoOptionFindings is the shared engine behind text mode and
// `forge lint --json`. It walks protoDir for *.proto files and validates
// every `(forge.v1.*)` annotation's field names against the compiled
// descriptors. Findings come back sorted by (file, line).
//
// A missing or empty proto directory yields no findings.
func collectProtoOptionFindings(protoDir string) ([]protoOptionFinding, error) {
	if _, err := os.Stat(protoDir); os.IsNotExist(err) {
		return nil, nil
	}

	var files []string
	if err := filepath.WalkDir(protoDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".proto") {
			return nil
		}
		// The vendored forge.proto DEFINES these options. Its field
		// declarations are definitions, not uses.
		if isVendoredForgeProto(path) {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk %s: %w", protoDir, err)
	}
	sort.Strings(files)

	var findings []protoOptionFinding
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		findings = append(findings, findUnknownProtoOptions(file, string(data))...)
	}
	return findings, nil
}

// isVendoredForgeProto reports whether path is the vendored copy of
// forge's own annotation definitions, matched on the import path shape
// (`forge/v1/forge.proto`) that every project vendors it at.
func isVendoredForgeProto(path string) bool {
	return strings.HasSuffix(filepath.ToSlash(path), "forge/v1/forge.proto")
}

// findUnknownProtoOptions scans one .proto file for `(forge.v1.*)`
// annotations and validates their field names.
func findUnknownProtoOptions(file, content string) []protoOptionFinding {
	stripped := stripProtoComments(content)

	var findings []protoOptionFinding
	for _, site := range findForgeAnnotationSites(stripped) {
		md, ok := forgeExtensionMessage(site.extension)
		if !ok {
			findings = append(findings, protoOptionFinding{
				File:      file,
				Line:      lineOf(stripped, site.offset),
				Extension: site.extension,
			})
			continue
		}
		if md == nil {
			// A scalar-valued extension (none today) has no fields to check.
			continue
		}
		findings = append(findings, checkOptionBody(file, stripped, site, md)...)
	}

	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Line < findings[j].Line })
	return findings
}

// annotationSite is one `(forge.v1.x)` occurrence and the extent of its
// value in the stripped source.
type annotationSite struct {
	extension string
	offset    int // byte offset of the `(`
	bodyStart int // byte offset just after the opening `{`, -1 when none
	bodyEnd   int // byte offset of the matching `}`
}

// findForgeAnnotationSites locates every `(forge.v1.*)` annotation and
// the brace-delimited body it assigns, if any.
//
// Two annotation shapes exist and only one carries field names:
//
//	option (forge.v1.method) = { auth_required: true };   ← a body
//	[(forge.v1.field).pk = true]                          ← a field PATH
//
// The path form names its field with a `.` selector directly after the
// closing paren; it is captured as a single-field body so the same
// validation covers it.
func findForgeAnnotationSites(s string) []annotationSite {
	var sites []annotationSite
	for i := 0; i < len(s); i++ {
		if s[i] != '(' {
			continue
		}
		close := strings.IndexByte(s[i:], ')')
		if close < 0 {
			break
		}
		name := strings.TrimSpace(s[i+1 : i+close])
		if !strings.HasPrefix(name, forgeExtensionPrefix) || strings.ContainsAny(name, " \t\n{}") {
			continue
		}
		site := annotationSite{extension: name, offset: i, bodyStart: -1}

		rest := i + close + 1
		// A `.field` selector immediately after the paren is the path form.
		if j := skipSpace(s, rest); j < len(s) && s[j] == '.' {
			k := j + 1
			for k < len(s) && (isIdentByte(s[k]) || s[k] == '.') {
				k++
			}
			site.bodyStart, site.bodyEnd = j+1, k
			sites = append(sites, site)
			i = k - 1
			continue
		}
		// Otherwise look for `= {` and take the balanced body.
		j := skipSpace(s, rest)
		if j < len(s) && s[j] == '=' {
			j = skipSpace(s, j+1)
			if j < len(s) && s[j] == '{' {
				if end, ok := matchBrace(s, j); ok {
					site.bodyStart, site.bodyEnd = j+1, end
					sites = append(sites, site)
					i = end
					continue
				}
			}
		}
		// An annotation with no body (or an unparseable one) names no
		// fields; nothing to validate.
		i = rest - 1
	}
	return sites
}

// checkOptionBody validates the field names in one annotation body
// against md, recursing into nested submessage bodies so a nested field
// resolves against ITS message.
func checkOptionBody(file, s string, site annotationSite, md protoreflect.MessageDescriptor) []protoOptionFinding {
	if site.bodyStart < 0 {
		return nil
	}
	return checkFieldsIn(file, s, site.bodyStart, site.bodyEnd, site.extension, md)
}

// checkFieldsIn walks `name:` / `name {` / `name.sub` pairs in s[start:end],
// validating each against md and recursing into message-typed fields.
func checkFieldsIn(file, s string, start, end int, extension string, md protoreflect.MessageDescriptor) []protoOptionFinding {
	var findings []protoOptionFinding

	i := start
	for i < end {
		switch {
		case s[i] == '"' || s[i] == '\'':
			i = skipString(s, i)
			continue
		case s[i] == '{':
			// A brace not introduced by a field name (a list element
			// `[{...}]`) — validate its contents against the same message.
			if inner, ok := matchBrace(s, i); ok && inner <= end {
				findings = append(findings, checkFieldsIn(file, s, i+1, inner, extension, md)...)
				i = inner + 1
				continue
			}
			i++
			continue
		case !isIdentStartByte(s[i]):
			i++
			continue
		}

		// An identifier: a field name only if followed by `:` or `{` or `.`.
		j := i
		for j < end && isIdentByte(s[j]) {
			j++
		}
		name := s[i:j]
		k := skipSpace(s, j)
		if k >= end || (s[k] != ':' && s[k] != '{' && s[k] != '.') {
			// An enum value, a bareword — not a field name.
			i = j
			continue
		}

		fd := md.Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			findings = append(findings, protoOptionFinding{
				File:       file,
				Line:       lineOf(s, i),
				Extension:  extension,
				Message:    string(md.FullName()),
				Field:      name,
				Known:      fieldNames(md),
				Suggestion: closestFieldName(name, md),
			})
			// Skip the value so its contents are not mined against a
			// message we already know does not describe them.
			i = skipValue(s, k, end)
			continue
		}

		// A nested body resolves against the nested message.
		vstart := k
		if s[vstart] == ':' {
			vstart = skipSpace(s, vstart+1)
		}
		if s[vstart] == '.' {
			// Path form: `(forge.v1.field).pk` — a sub-selector on a
			// message-typed field.
			if sub := fd.Message(); sub != nil {
				m := vstart + 1
				for m < end && isIdentByte(s[m]) {
					m++
				}
				findings = append(findings, checkFieldsIn(file, s, vstart+1, m, extension, sub)...)
				i = m
				continue
			}
			i = j
			continue
		}
		if vstart < end && s[vstart] == '{' {
			if inner, ok := matchBrace(s, vstart); ok && inner <= end {
				if sub := fd.Message(); sub != nil {
					findings = append(findings, checkFieldsIn(file, s, vstart+1, inner, extension, sub)...)
				}
				i = inner + 1
				continue
			}
		}
		i = skipValue(s, k, end)
	}
	return findings
}

// forgeExtensionMessage resolves a `forge.v1.x` extension name to the
// message descriptor of its value, from the global registry the running
// binary linked. Reports ok=false when this forge does not define the
// extension at all.
func forgeExtensionMessage(name string) (protoreflect.MessageDescriptor, bool) {
	xt, err := protoregistry.GlobalTypes.FindExtensionByName(protoreflect.FullName(name))
	if err != nil {
		return nil, false
	}
	return xt.TypeDescriptor().Message(), true
}

// fieldNames lists a message's field names in declaration order.
func fieldNames(md protoreflect.MessageDescriptor) []string {
	out := make([]string, 0, md.Fields().Len())
	for i := range md.Fields().Len() {
		out = append(out, string(md.Fields().Get(i).Name()))
	}
	return out
}

// closestFieldName returns the nearest field name by edit distance, or ""
// when nothing is close enough. It reuses codegen.ClosestProtoMarker —
// the same Levenshtein helper and the same length-scaled cutoff the
// comment-marker check uses — by borrowing its scoring over this
// message's vocabulary, so the two checks cannot disagree about what
// counts as a near-miss.
func closestFieldName(name string, md protoreflect.MessageDescriptor) string {
	return codegen.ClosestFrom(name, fieldNames(md))
}

// stripProtoComments blanks out `//` line comments and `/* */` block
// comments, preserving every other byte and all newlines so offsets and
// line numbers still refer to the original source.
func stripProtoComments(s string) string {
	b := []byte(s)
	out := make([]byte, len(b))
	copy(out, b)

	for i := 0; i < len(b); {
		switch {
		case b[i] == '"' || b[i] == '\'':
			end := skipString(s, i)
			i = end
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				out[i] = ' '
				i++
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			for i < len(b) && !(b[i] == '*' && i+1 < len(b) && b[i+1] == '/') {
				if b[i] != '\n' {
					out[i] = ' '
				}
				i++
			}
			for j := 0; j < 2 && i < len(b); j++ {
				out[i] = ' '
				i++
			}
		default:
			i++
		}
	}
	return string(out)
}

// skipString returns the offset just past the string literal starting at
// i (s[i] is the quote), honoring backslash escapes.
func skipString(s string, i int) int {
	quote := s[i]
	i++
	for i < len(s) {
		if s[i] == '\\' {
			i += 2
			continue
		}
		if s[i] == quote {
			return i + 1
		}
		i++
	}
	return len(s)
}

// matchBrace returns the offset of the `}` matching the `{` at i,
// skipping string literals.
func matchBrace(s string, i int) (int, bool) {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '"', '\'':
			j = skipString(s, j) - 1
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return j, true
			}
		}
	}
	return 0, false
}

// skipBracketed advances past one `{...}` or `[...]` group starting at i,
// returning the index just past its closing bracket. Nested groups are
// counted, and quoted strings are skipped whole so a bracket inside a string
// literal cannot close the group early. An unterminated group consumes to end.
func skipBracketed(s string, i, end int) int {
	depth := 0
	for i < end {
		if s[i] == '"' || s[i] == '\'' {
			i = skipString(s, i)
			continue
		}
		switch s[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return i
}

// skipValue advances past one option value beginning at or after i,
// stopping at the delimiter that ends it.
func skipValue(s string, i, end int) int {
	i = skipSpace(s, i)
	if i < end && s[i] == ':' {
		i = skipSpace(s, i+1)
	}
	for i < end {
		switch s[i] {
		case '"', '\'':
			i = skipString(s, i)
			continue
		case '{', '[':
			return skipBracketed(s, i, end)
		case ',', ';', '\n':
			return i + 1
		case '}':
			return i
		}
		i++
	}
	return i
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func isIdentStartByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentByte(c byte) bool {
	return isIdentStartByte(c) || (c >= '0' && c <= '9')
}

// lineOf returns the 1-indexed line containing byte offset off.
func lineOf(s string, off int) int {
	if off > len(s) {
		off = len(s)
	}
	return strings.Count(s[:off], "\n") + 1
}
