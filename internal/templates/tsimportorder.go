// Canonical import ordering for emitted TypeScript/TSX — the frontend
// analogue of the gofmt/goimports pass the Tier-1 writer runs over every .go
// render before stamping it (internal/checksums/goformat.go).
//
// ── Why the emitter cannot hand-order these imports ───────────────────
//
// A scaffolded CRUD page emits a CONDITIONAL import set. Whether
// `lucide-react`, the shared runtime, an enum module, or the debounce hook
// appears at all depends on the entity's shape — has a Create RPC, has an
// enum column, has a scalar filter — and two of the specifiers are not known
// until render time: the service's generated hooks module
// (`@/hooks/<service>-service-hooks`) and the protobuf-es module the entity's
// enums live in (`@/gen/services/<service>/v1/<file>_pb`).
//
// Any hand-written line order is therefore correct for exactly the entity
// shapes its author happened to try and wrong for the rest:
// `@/hooks/catalog-service-hooks` sorts BEFORE `@/hooks/use-query-resource`
// and `@/hooks/warehouse-service-hooks` sorts AFTER it, so no static
// arrangement of template lines satisfies both. Patching the lines that a
// particular project's entity happened to expose fixes that project and
// leaves the next one warning.
//
// So the order is DERIVED from the specifiers that were actually emitted:
// the page writer runs its render through CanonicalTSImportOrder, which
// reads the leading import block, classifies each statement into eslint's
// configured groups, sorts within the group, and rewrites the block. The
// template author lists the imports a page needs and never thinks about
// order again.
//
// ── One source of truth ───────────────────────────────────────────────
//
// The grouping/sorting rules below MIRROR the `import/order` rule and the
// `import/internal-regex` setting in
// internal/templates/frontend/nextjs/eslint.config.mjs — the config the
// scaffold's own `npm run lint` enforces. The config is JavaScript; parsing
// it to derive these constants would mean shipping a JS parser to read four
// values. Instead TestTSImportOrderMatchesESLintConfig re-reads the emitted
// config and FAILS when the two disagree, so a rule change cannot silently
// leave the emitter behind.
//
// The classification itself mirrors eslint-plugin-import's importType()
// (lib/core/importType.js) and its alphabetize comparator (getSorter in
// lib/rules/order.js) — notably the SEGMENT-WISE path comparison, under
// which "next/link" sorts before "next-auth" even though the raw strings
// compare the other way.
package templates

import (
	"regexp"
	"sort"
	"strings"
)

// tsImportGroups mirrors the `groups` array of the import/order rule in the
// scaffold's eslint config. A specifier's rank is its index here; anything
// eslint classifies outside these names (an absolute path, an unresolvable
// specifier) ranks last, matching how the rule treats omitted types.
var tsImportGroups = []string{
	"builtin", "external", "internal", "parent", "sibling", "index", "object", "type",
}

// tsInternalRegex mirrors settings["import/internal-regex"]: the tsconfig
// path alias that makes project-local modules "internal" rather than
// "external". Without it eslint cannot resolve the alias and generated code
// lints differently on different machines.
var tsInternalRegex = regexp.MustCompile(`^@/`)

// tsNodeBuiltins is the set of Node core modules, keyed by the FIRST path
// segment — eslint-plugin-import's isBuiltIn() tests the base module, so
// "fs/promises" is builtin because "fs" is. A `node:` prefix is builtin
// unconditionally and needs no entry.
var tsNodeBuiltins = map[string]bool{
	"assert": true, "async_hooks": true, "buffer": true, "child_process": true,
	"cluster": true, "console": true, "constants": true, "crypto": true,
	"dgram": true, "diagnostics_channel": true, "dns": true, "domain": true,
	"events": true, "fs": true, "http": true, "http2": true, "https": true,
	"inspector": true, "module": true, "net": true, "os": true, "path": true,
	"perf_hooks": true, "process": true, "punycode": true, "querystring": true,
	"readline": true, "repl": true, "sea": true, "sqlite": true, "stream": true,
	"string_decoder": true, "sys": true, "test": true, "timers": true, "tls": true,
	"trace_events": true, "tty": true, "url": true, "util": true, "v8": true,
	"vm": true, "wasi": true, "worker_threads": true, "zlib": true,
}

var (
	// tsImportFromRe matches a complete `import ... from "<path>"` statement,
	// with an optional trailing line comment.
	tsImportFromRe = regexp.MustCompile(`^import\s[^;]*?\sfrom\s*["']([^"']+)["']\s*;?\s*(//.*)?$`)
	// tsSideEffectImportRe matches an unassigned import (`import "./x";`).
	// Those are evaluated for their side effects, so their relative order is
	// semantic — a block containing one is left exactly as written.
	tsSideEffectImportRe = regexp.MustCompile(`^import\s*["'][^"']+["']\s*;?\s*(//.*)?$`)
	// tsTypeOnlyImportRe matches `import type ...` — the whole statement is
	// type-only, which the rule ranks in the "type" group. An inline
	// specifier (`import { type Foo }`) is NOT type-only and must not match.
	tsTypeOnlyImportRe = regexp.MustCompile(`^import\s+type\s`)
	// tsDirectiveRe matches a directive prologue ("use client";).
	tsDirectiveRe = regexp.MustCompile(`^["'][^"']*["']\s*;?$`)
)

// tsMaxStatementLines bounds how far CanonicalTSImportOrder will look for the
// end of one import statement. A block that does not terminate within it is
// not something this function understands, and it declines rather than
// guesses.
const tsMaxStatementLines = 40

// tsImportStmt is one parsed import statement plus the comment lines that
// immediately precede it, which travel with it when it moves.
type tsImportStmt struct {
	comments []string
	lines    []string
	path     string
	typeOnly bool
}

// CanonicalTSImportOrder rewrites the leading import block of TypeScript
// source so it satisfies the scaffold's eslint `import/order` rule: grouped
// in the configured order, alphabetised within each group, exactly one blank
// line between groups and none inside one.
//
// It is a fixed point — running it over its own output changes nothing — and
// it is conservative: source it does not fully understand (no leading import
// block, a side-effect import whose evaluation order is semantic, an
// unterminated statement) is returned verbatim rather than rearranged.
//
// Comments BEFORE the first import belong to the file's banner and stay put;
// comments BETWEEN imports annotate the import that follows and move with it.
func CanonicalTSImportOrder(src []byte) []byte {
	lines := strings.Split(string(src), "\n")

	start := tsImportBlockStart(lines)
	if start < 0 {
		return src
	}

	stmts, end, ok := parseTSImportBlock(lines, start)
	if !ok || len(stmts) < 2 {
		return src
	}

	out := make([]string, 0, len(lines)+len(tsImportGroups))
	out = append(out, lines[:start]...)
	out = append(out, renderTSImportBlock(stmts)...)
	out = append(out, lines[end:]...)
	return []byte(strings.Join(out, "\n"))
}

// tsImportBlockStart returns the index of the first line of the leading
// import block, or -1 when the file does not open with one. Only a directive
// prologue, comments, and blank lines may precede it — the first line of real
// code ends the search, so an `await import(...)` deeper in the file can
// never be mistaken for the block.
func tsImportBlockStart(lines []string) int {
	inBlockComment := false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case inBlockComment:
			if strings.Contains(line, "*/") {
				inBlockComment = false
			}
		case line == "", strings.HasPrefix(line, "//"):
		case strings.HasPrefix(line, "/*"):
			if !strings.Contains(line, "*/") {
				inBlockComment = true
			}
		case tsDirectiveRe.MatchString(line):
		case strings.HasPrefix(line, "import "):
			return i
		default:
			return -1
		}
	}
	return -1
}

// parseTSImportBlock reads the import statements starting at line `start`,
// returning them in source order and the index of the first line after the
// block. ok=false means the block holds something this function will not
// reorder (a side-effect import, an unterminated statement).
func parseTSImportBlock(lines []string, start int) (stmts []tsImportStmt, end int, ok bool) {
	var pending []string // comment lines awaiting the import they annotate
	i := start
	end = start
	inBlockComment := false

	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		switch {
		case inBlockComment:
			pending = append(pending, lines[i])
			if strings.Contains(line, "*/") {
				inBlockComment = false
			}
			i++
			continue
		case line == "":
			pending = append(pending, lines[i])
			i++
			continue
		case strings.HasPrefix(line, "//"):
			pending = append(pending, lines[i])
			i++
			continue
		case strings.HasPrefix(line, "/*"):
			pending = append(pending, lines[i])
			if !strings.Contains(line, "*/") {
				inBlockComment = true
			}
			i++
			continue
		case !strings.HasPrefix(line, "import "):
			// First line of real code: the block ended before it, and the
			// pending comment/blank run belongs to what follows.
			return stmts, end, true
		}

		stmt, next, complete := readTSImportStmt(lines, i)
		if !complete {
			return nil, 0, false
		}
		if tsSideEffectImportRe.MatchString(strings.TrimSpace(strings.Join(stmt.lines, " "))) {
			return nil, 0, false
		}
		stmt.comments = trimBlankEdges(pending)
		pending = nil
		stmts = append(stmts, stmt)
		i = next
		end = next
	}
	return stmts, end, true
}

// readTSImportStmt reads one import statement beginning at line i, joining
// continuation lines until the statement terminates. complete=false when it
// does not terminate within tsMaxStatementLines or carries no module path.
func readTSImportStmt(lines []string, i int) (stmt tsImportStmt, next int, complete bool) {
	for n := 0; n < tsMaxStatementLines && i+n < len(lines); n++ {
		stmt.lines = append(stmt.lines, lines[i+n])
		joined := strings.TrimSpace(strings.Join(stmt.lines, " "))
		if m := tsImportFromRe.FindStringSubmatch(joined); m != nil {
			stmt.path = m[1]
			stmt.typeOnly = tsTypeOnlyImportRe.MatchString(joined)
			return stmt, i + n + 1, true
		}
		if tsSideEffectImportRe.MatchString(joined) {
			return stmt, i + n + 1, true
		}
	}
	return tsImportStmt{}, 0, false
}

// renderTSImportBlock emits the statements grouped and sorted, with exactly
// one blank line between adjacent non-empty groups (newlines-between:
// "always" forbids blank lines inside a group).
func renderTSImportBlock(stmts []tsImportStmt) []string {
	byGroup := make([][]tsImportStmt, len(tsImportGroups)+1)
	for _, s := range stmts {
		rank := tsImportRank(s)
		byGroup[rank] = append(byGroup[rank], s)
	}

	var out []string
	for _, group := range byGroup {
		if len(group) == 0 {
			continue
		}
		sort.SliceStable(group, func(a, b int) bool {
			return compareTSImportPaths(group[a].path, group[b].path) < 0
		})
		if len(out) > 0 {
			out = append(out, "")
		}
		for _, s := range group {
			out = append(out, s.comments...)
			out = append(out, s.lines...)
		}
	}
	return out
}

// tsImportRank is the statement's index in tsImportGroups. Type-only imports
// rank in the "type" group regardless of their path (import/order's behaviour
// whenever "type" appears in `groups`); anything eslint classifies outside
// the configured names ranks last.
func tsImportRank(s tsImportStmt) int {
	name := "type"
	if !s.typeOnly {
		name = tsImportType(s.path)
	}
	for i, g := range tsImportGroups {
		if g == name {
			return i
		}
	}
	return len(tsImportGroups)
}

// tsImportType classifies a module specifier the way eslint-plugin-import's
// importType() does, in the same order of tests: the internal-regex alias
// wins over everything, then Node core, then relative paths, then anything
// package-looking is external.
func tsImportType(path string) string {
	switch {
	case tsInternalRegex.MatchString(path):
		return "internal"
	case tsIsNodeBuiltin(path):
		return "builtin"
	case path == "..", strings.HasPrefix(path, "../"):
		return "parent"
	case path == ".", path == "./", path == "./index", path == "./index.js":
		return "index"
	case strings.HasPrefix(path, "./"):
		return "sibling"
	default:
		return "external"
	}
}

// tsIsNodeBuiltin reports whether the specifier names a Node core module,
// matching on the base module: `node:`-prefixed always, otherwise the first
// path segment (or `@scope/pkg` for a scoped name, which is never core).
func tsIsNodeBuiltin(path string) bool {
	if strings.HasPrefix(path, "node:") {
		return true
	}
	if strings.HasPrefix(path, "@") {
		return false
	}
	base, _, _ := strings.Cut(path, "/")
	return tsNodeBuiltins[base]
}

// compareTSImportPaths reproduces import/order's alphabetize comparator with
// {order: "asc", caseInsensitive: true}: lowercase both, then compare
// SEGMENT-WISE on "/" when either has a segment separator, shorter path first
// on a tie. Segment-wise is not the same as comparing the raw strings —
// "next/link" precedes "next-auth" because "next" < "next-auth", while a raw
// compare puts "next-auth" first.
func compareTSImportPaths(a, b string) int {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if !strings.Contains(a, "/") && !strings.Contains(b, "/") {
		return strings.Compare(a, b)
	}
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		// A leading "." / ".." is a group discriminator, not a name: the rule
		// skips comparing it when both sides are relative.
		if i == 0 && (as[i] == "." || as[i] == "..") && (bs[i] == "." || bs[i] == "..") {
			if as[i] != bs[i] {
				break
			}
			continue
		}
		if c := strings.Compare(as[i], bs[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	default:
		return 0
	}
}

// trimBlankEdges drops leading and trailing blank lines from a comment run —
// the blank lines that separated groups in the input, which the emitter
// regenerates itself.
func trimBlankEdges(lines []string) []string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	if start == end {
		return nil
	}
	return lines[start:end]
}
