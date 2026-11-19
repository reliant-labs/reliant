// Package suppress is the single suppression mechanism shared by every
// forge-owned linter (forgeconv, scaffolds, migrationlint, and the
// deadcode/vacuous guards once they are exposed to user projects).
//
// # WHY THIS EXISTS, AND WHY IT IS ONE PACKAGE
//
// Every linter forge ships needs the same three escape hatches — turn a
// rule off for the repo, for a file, or for a line — and before this
// package there was exactly one: `contracts.exclude` in forge.yaml, a
// path-glob list that only the contract linter honored. A rule with no
// way to say "not here, and here is why" is a rule authors route around
// by deleting the check, not by narrowing it.
//
// The three levels are deliberately NOT interchangeable, and the reason
// is the same one the golangci template gives for preferring //nolint
// over a path glob: a glob silently swallows every FUTURE finding under
// that path, including the one you would have wanted to see. So the
// levels narrow as they get more local, and the most local one is the
// one meant to be reached for:
//
//	forge.yaml lint.rules   whole repo, per rule, off|warn|error
//	file directive          one file, per rule
//	line directive          one line, per rule   ← prefer this
//
// PIGGYBACKING ON //nolint
//
// Go authors already have muscle memory for `//nolint:name`, and a
// forge rule that demanded its own spelling for the same job would be
// one more thing to remember for no benefit. So `//nolint:<rule>` is
// honored for forge rules too, with golangci's own positional
// semantics (trailing on a code line = that line; alone on a line =
// the line below).
//
// This is safe in both directions. golangci-lint ignores unknown names
// in a nolint list, so `//nolint:forgeconv-list-filter-optional` does
// not make golangci complain — UNLESS the project enables `nolintlint`,
// which flags nolint directives naming linters it does not know. That
// is the one configuration where the forge spelling is the better
// choice, which is why both exist rather than only the borrowed one.
//
// The forge-native spelling is `forge:lint-disable...`, matching the
// ~25 other `forge:*` directives the toolchain already reads
// (forge:optional-dep, forge:read-only, forge:exclude-contract, …).
//
// # COMMENT SYNTAX IS NOT PARSED, ON PURPOSE
//
// Directives are found by scanning each line for the directive token,
// not by parsing comments. forge lints Go, proto, SQL, TypeScript and
// YAML — five comment syntaxes (`//`, `--`, `#`) — and a per-language
// comment parser here would be five parsers to keep correct for a
// feature whose entire job is finding a keyword. The cost of the
// shortcut is that a directive token inside a string literal would be
// honored; the benefit is that one scanner serves every linter forge
// will ever add. Given the token is `forge:lint-disable`, a false hit
// is a curiosity, not a hazard.
//
// # REASONS
//
// A suppression of an error-severity rule must carry a reason (`: why`
// after the rule list). This is not ceremony. The audience for forge's
// opinionated rules is authors who do not yet know why the rule exists,
// and the moment they suppress one is exactly the moment they either
// learn it or route around it. A reason turns the suppression into a
// decision a reviewer can evaluate; without one it is a shrug that
// reads identically to giving up. Warning-severity rules do not
// require one — they do not gate, so silencing them is cheap by design.
package suppress

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// Directive tokens. The forge-native spelling and the borrowed golangci
// one are both real; see the package doc for when each is the better
// choice.
const (
	// TokenDisableFile turns a rule off for the remainder of the file
	// it appears in, regardless of where in the file it appears.
	TokenDisableFile = "forge:lint-disable-file"
	// TokenDisableNextLine turns a rule off for the single line
	// following the directive.
	TokenDisableNextLine = "forge:lint-disable-next-line"
	// TokenDisable opens a suppressed block, closed by TokenEnable or
	// by end-of-file.
	TokenDisable = "forge:lint-disable"
	// TokenEnable closes a block opened by TokenDisable.
	TokenEnable = "forge:lint-enable"
	// TokenNolint is golangci-lint's directive, honored for forge
	// rules so authors need not learn a second spelling.
	TokenNolint = "nolint:"
)

// Wildcard suppresses every rule at the scope it is written on. Spelled
// the same in forge.yaml and in directives.
const Wildcard = "*"

// Suppression records one honored directive: which rule it silenced,
// where, and why. Retained (rather than being applied and forgotten) so a
// caller can render what a run chose NOT to tell you. A suppression
// mechanism whose output is invisible is how a codebase ends up disabling
// everything one line at a time — which is also why `forge lint
// --show-suppressed` lists every directive as written (Declarations),
// whether or not a finding reached it.
type Suppression struct {
	Rule   string `json:"rule"`
	File   string `json:"file"`
	Line   int    `json:"line"`   // line of the SUPPRESSED finding
	Reason string `json:"reason"` // "" when none was given
	// Directive is the token that did the suppressing, so a reviewer
	// can tell a deliberate line-scoped silence from a file-wide one.
	Directive string `json:"directive"`
}

// Result is the outcome of applying a file's directives to its
// findings: what survived, and what did not.
type Result struct {
	Kept       []finding.Finding
	Suppressed []Suppression
	// Violations are findings ABOUT the suppressions themselves — an
	// error-severity rule silenced with no reason. They are emitted as
	// ordinary findings so they travel through the same reporting path
	// as everything else.
	Violations []finding.Finding
}

// RuleMissingReason is the rule ID reported when an error-severity
// finding is suppressed without a reason. It is itself suppressible
// (with a reason), which is the only consistent choice — but doing so
// requires writing down why you want unexplained suppressions, which is
// the point.
const RuleMissingReason = "forge-suppression-missing-reason"

// directive is one parsed directive occurrence.
type directive struct {
	token  string
	rules  []string // lowercased; may contain Wildcard
	reason string
	line   int // 1-indexed line the directive appeared on
}

// fileDirectives is the parsed suppression state of a single file.
type fileDirectives struct {
	// fileScope are rules disabled for the whole file.
	fileScope map[string]directive
	// lineScope maps a target line to the directives covering it.
	lineScope map[int][]directive
	// blocks are half-open [start, end) ranges of disabled rules.
	blocks []blockRange
}

type blockRange struct {
	rule       string
	start, end int // end == 0 means "to end of file"
	reason     string
	line       int
}

// ParseFile extracts every suppression directive from the source of one
// file. It never fails: a malformed directive is simply not a
// directive, because the alternative — failing a lint run over the
// syntax of the thing that turns lint off — is a trap with no upside.
func ParseFile(content string) *fileDirectives { //nolint:revive // unexported return is deliberate; callers only pass it back to Apply
	fd := &fileDirectives{
		fileScope: map[string]directive{},
		lineScope: map[int][]directive{},
	}
	// open tracks blocks awaiting a matching forge:lint-enable.
	open := map[string]blockRange{}

	lines := strings.Split(content, "\n")
	for i, raw := range lines {
		lineNo := i + 1
		for _, d := range parseLine(raw, lineNo) {
			switch d.token {
			case TokenDisableFile:
				for _, r := range d.rules {
					fd.fileScope[r] = d
				}
			case TokenDisableNextLine:
				for _, target := range nextLineTargets(lines, lineNo) {
					fd.lineScope[target] = append(fd.lineScope[target], d)
				}
			case TokenNolint:
				// golangci semantics: trailing on a line of code
				// applies to THAT line; alone on its own line applies
				// to what follows it, the same span a next-line
				// directive covers.
				targets := []int{lineNo}
				if isDirectiveOnlyLine(raw, TokenNolint) {
					targets = nextLineTargets(lines, lineNo)
				}
				for _, target := range targets {
					fd.lineScope[target] = append(fd.lineScope[target], d)
				}
			case TokenDisable:
				for _, r := range d.rules {
					// A re-opened block keeps the first opener's line
					// and reason — the outermost intent wins.
					if _, already := open[r]; !already {
						open[r] = blockRange{rule: r, start: lineNo, reason: d.reason, line: d.line}
					}
				}
			case TokenEnable:
				for _, r := range d.rules {
					if br, ok := open[r]; ok {
						br.end = lineNo
						fd.blocks = append(fd.blocks, br)
						delete(open, r)
					}
				}
			}
		}
	}
	// Unclosed blocks run to end of file.
	for _, br := range open {
		fd.blocks = append(fd.blocks, br)
	}
	sort.Slice(fd.blocks, func(i, j int) bool {
		if fd.blocks[i].start != fd.blocks[j].start {
			return fd.blocks[i].start < fd.blocks[j].start
		}
		return fd.blocks[i].rule < fd.blocks[j].rule
	})
	return fd
}

// parseLine returns every directive occurring on one source line. A
// line may legitimately carry more than one (a trailing //nolint on a
// line that also opens a block), so this returns a slice.
func parseLine(raw string, lineNo int) []directive {
	var out []directive
	// Order matters: the longer tokens are prefixes of the shorter one
	// (forge:lint-disable-file starts with forge:lint-disable), so the
	// specific spellings must be tested before the general one.
	for _, tok := range []string{TokenDisableFile, TokenDisableNextLine, TokenEnable, TokenDisable, TokenNolint} {
		idx := strings.Index(raw, tok)
		if idx < 0 {
			continue
		}
		rest := raw[idx+len(tok):]
		rules, reason := parseRulesAndReason(rest, tok)
		if len(rules) == 0 {
			continue
		}
		out = append(out, directive{token: tok, rules: rules, reason: reason, line: lineNo})
		// One directive per token per line is enough; a second
		// occurrence of the SAME token on one line is not a shape
		// worth supporting.
	}
	return out
}

// parseRulesAndReason splits a directive tail into its rule list and
// optional reason. The accepted shapes:
//
//	forge:lint-disable rule-a, rule-b: because X
//	forge:lint-disable rule-a
//	nolint:rule-a,rule-b // because X
//
// The `nolint:` form takes its rules immediately after the colon with
// no space (golangci's spelling), so it is split differently from the
// space-delimited forge spellings.
func parseRulesAndReason(rest, tok string) (rules []string, reason string) {
	// Separate the reason first. `:` delimits it for the forge
	// spellings; for nolint the colon is already consumed by the token
	// itself, so a reason there is whatever follows a comment marker.
	body := rest
	if tok == TokenNolint {
		// Rules run until whitespace or a comment marker.
		body = strings.TrimLeft(body, " \t")
		if cut := strings.IndexAny(body, " \t"); cut >= 0 {
			reason = strings.TrimSpace(body[cut:])
			body = body[:cut]
		}
		reason = strings.TrimSpace(strings.TrimLeft(reason, "/-#"))
	} else {
		// Require a separator so `forge:lint-disabled-thing` (a word
		// that merely starts with the token) is not read as a
		// directive with rules.
		if body != "" && !strings.ContainsAny(body[:1], " \t") {
			return nil, ""
		}
		if colon := strings.Index(body, ":"); colon >= 0 {
			reason = strings.TrimSpace(body[colon+1:])
			body = body[:colon]
		}
	}

	for _, part := range strings.Split(body, ",") {
		r := strings.ToLower(strings.TrimSpace(part))
		// The wildcard is itself punctuation, so it has to be
		// recognized BEFORE trailing comment characters are trimmed —
		// otherwise `*` trims to "" and the broadest directive
		// silently becomes a no-op.
		if r != Wildcard {
			// Trim trailing comment punctuation an author might leave
			// on the last rule (`//nolint:foo */`).
			r = strings.TrimRight(r, "*/-# \t")
		}
		if r == "" {
			continue
		}
		if !isPlausibleRuleName(r) {
			continue
		}
		rules = append(rules, r)
	}
	return rules, reason
}

// isPlausibleRuleName keeps prose out of the rule list. Rule IDs are
// lowercase words joined by hyphens (forgeconv-handler-file-size), plus
// the wildcard. Anything with spaces or punctuation is a sentence that
// wandered in, not a rule.
func isPlausibleRuleName(s string) bool {
	if s == Wildcard {
		return true
	}
	if s == "" || strings.ContainsAny(s, " \t()\"'") {
		return false
	}
	for _, r := range s {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if !isLower && !isDigit && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// nextLineTargets is the span a directive on line directiveLine means by
// "the next line": the line below it, and, when that line is a comment,
// every comment line after it up to and including the first line that is
// not a comment. In other words, the next line of CODE, plus the comment
// lines between the directive and that code.
//
// A literal "directive line + 1" is not what authors mean, and two
// ordinary things break it:
//
//   - gofmt moves `//forge:` directive lines to the END of a Go doc
//     comment (they are Go directive syntax, like `//go:build`), past any
//     prose, and separates them from prose with a blank `//`. A directive
//     written directly above a declaration can end up several comment
//     lines above it, or above a marker it no longer touches.
//   - Two directives aimed at one target (a marker drawing two refusals,
//     each with its own reasoned allowance) necessarily stack, so the
//     upper one's literal next line is the lower one, not the target.
//
// golangci-lint's own `//nolint` has the same shape: it applies to the
// whole declaration a doc comment belongs to, not to a line number.
//
// The span never crosses a blank line or a line of code, so it cannot
// reach anything the author could not see directly beneath the directive.
// A finding anchored ON a comment line inside the span (an
// exclude-contract marker) is covered too, which is what makes stacked
// allowances work.
func nextLineTargets(lines []string, directiveLine int) []int {
	targets := []int{directiveLine + 1}
	for l := directiveLine + 1; l <= len(lines) && isCommentOnlyLine(lines[l-1]); l++ {
		targets = append(targets, l+1)
	}
	return targets
}

// isCommentOnlyLine reports whether raw is entirely a line comment in
// one of the syntaxes forge lints: `//` (Go, proto, TypeScript), `#`
// (YAML, shell) or `--` (SQL). The `#` and `--` forms require a
// following space or end of line, so a TypeScript private field
// (`#count = 0`) or a decrement is never mistaken for a comment.
func isCommentOnlyLine(raw string) bool {
	t := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(t, "//"):
		return true
	case t == "#" || t == "--":
		return true
	case strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "#\t") || strings.HasPrefix(t, "##"):
		return true
	case strings.HasPrefix(t, "-- ") || strings.HasPrefix(t, "--\t"):
		return true
	}
	return false
}

// isDirectiveOnlyLine reports whether the directive is the only thing
// on the line (ignoring leading whitespace and the comment marker).
// This is what distinguishes golangci's "applies to the next line" form
// from its "applies to this line" trailing form.
func isDirectiveOnlyLine(raw, tok string) bool {
	idx := strings.Index(raw, tok)
	if idx < 0 {
		return false
	}
	before := strings.TrimSpace(raw[:idx])
	before = strings.TrimRight(before, "/-#")
	return strings.TrimSpace(before) == ""
}

// covers reports whether the parsed directives silence rule at line,
// and returns the directive that did it.
func (fd *fileDirectives) covers(rule string, line int) (directive, bool) {
	rule = strings.ToLower(rule)
	if d, ok := fd.fileScope[rule]; ok {
		return d, true
	}
	if d, ok := fd.fileScope[Wildcard]; ok {
		return d, true
	}
	for _, d := range fd.lineScope[line] {
		if matches(d.rules, rule) {
			return d, true
		}
	}
	for _, br := range fd.blocks {
		if br.rule != rule && br.rule != Wildcard {
			continue
		}
		if line >= br.start && (br.end == 0 || line < br.end) {
			return directive{token: TokenDisable, rules: []string{br.rule}, reason: br.reason, line: br.line}, true
		}
	}
	return directive{}, false
}

func matches(rules []string, rule string) bool {
	for _, r := range rules {
		if r == rule || r == Wildcard {
			return true
		}
	}
	return false
}

// Apply filters findings for one file against that file's directives.
// A finding whose line is 0 (file-level findings, which several forge
// linters emit) is matched only by file-scope directives — a
// line-scoped directive cannot silence something that has no line.
func Apply(content string, findings []finding.Finding) Result {
	fd := ParseFile(content)
	var res Result
	for _, f := range findings {
		d, silenced := fd.covers(f.Rule, f.Line)
		if !silenced {
			res.Kept = append(res.Kept, f)
			continue
		}
		res.Suppressed = append(res.Suppressed, Suppression{
			Rule:      f.Rule,
			File:      f.File,
			Line:      f.Line,
			Reason:    d.reason,
			Directive: d.token,
		})
		// Silencing a gating rule without saying why is itself a
		// finding — see the package doc. It is an ERROR: a suppression
		// is how a gating rule stops gating, so a reasonless one that
		// merely warned would make "a reason is required" advice rather
		// than a rule, and a bare directive would turn any gate green.
		if f.Severity == finding.SeverityError && strings.TrimSpace(d.reason) == "" {
			if _, exempt := fd.covers(RuleMissingReason, d.line); !exempt {
				res.Violations = append(res.Violations, finding.Finding{
					Rule:     RuleMissingReason,
					Severity: finding.SeverityError,
					File:     f.File,
					Line:     d.line,
					Message: fmt.Sprintf(
						"%q gates the build and was suppressed without a reason", f.Rule),
					Remediation: fmt.Sprintf(
						"say why: `%s %s: <reason>` — a suppression with a reason is a decision "+
							"a reviewer can evaluate; without one it is indistinguishable from giving up",
						d.token, f.Rule),
				})
			}
		}
	}
	return res
}

// ApplyFiles runs Apply over findings that may span many files, reading
// each file once through read. A finding's File is resolved against root
// unless it is already absolute; root "" means the working directory.
//
// A finding with no File, or whose file cannot be read, is kept as-is:
// failing to read the file that would carry a directive is not evidence
// that the finding was suppressed. Order is preserved within each file,
// and files appear in the order they first occur.
//
// read is a parameter (callers pass os.ReadFile) so this package stays a
// policy over text rather than an owner of I/O.
func ApplyFiles(root string, findings []finding.Finding, read func(path string) ([]byte, error)) Result {
	var res Result
	byFile := map[string][]finding.Finding{}
	var order []string
	for _, f := range findings {
		if f.File == "" {
			res.Kept = append(res.Kept, f)
			continue
		}
		if _, seen := byFile[f.File]; !seen {
			order = append(order, f.File)
		}
		byFile[f.File] = append(byFile[f.File], f)
	}
	for _, file := range order {
		fs := byFile[file]
		path := file
		if root != "" && !filepath.IsAbs(path) {
			path = filepath.Join(root, filepath.FromSlash(file))
		}
		content, err := read(path)
		if err != nil {
			res.Kept = append(res.Kept, fs...)
			continue
		}
		one := Apply(string(content), fs)
		res.Kept = append(res.Kept, one.Kept...)
		res.Suppressed = append(res.Suppressed, one.Suppressed...)
		res.Violations = append(res.Violations, one.Violations...)
	}
	return res
}

// IsDirectiveToken reports whether token (a `forge:`-prefixed word as it
// appears in a comment) is one of this package's directives. Linters that
// flag unrecognised `forge:*` comments consult it, so a suppression is
// never itself reported as an unknown marker.
func IsDirectiveToken(token string) bool {
	switch token {
	case TokenDisableFile, TokenDisableNextLine, TokenDisable, TokenEnable:
		return true
	}
	return false
}
