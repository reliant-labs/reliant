// File: internal/codegen/go_markers.go
//
// The `forge:*` GO-SOURCE marker registry — every marker forge reads out of
// a .go comment, with the ONE spelling its recognizer accepts. The twin of
// proto_markers.go (proto comments) and schemadef.KnownColumnMarkers (postgres
// catalog comments).
//
// WHY. Reliant wrote `//forge:exclude-contract — <reason>` (an em-dash) on a
// package. The recognizer accepts exactly `//forge:exclude-contract` and
// `//forge:exclude-contract: <reason>`, so the em-dash spelling was not a
// directive at all: the package was not excluded, the contract lint kept
// failing on it, and nothing said the marker had been read and refused. Every
// Go-source recognizer had the same shape — each matched its own exact text
// and treated anything else as prose — so a lookalike separator, or a typo'd
// name (`//forge:exclude-contrat: …`), was a silent no-op wherever it was
// written.
//
// The fix is one documented form per marker plus a loud error, not a parser
// that also accepts the lookalikes: a recognizer that guesses which prose was
// meant as a directive is how the next lookalike becomes a silent no-op too.
// This registry is the vocabulary `forge lint --go-markers`
// (internal/cli/lint/lint_go_markers.go) checks every .go comment against,
// and ClassifyGoMarkerComment answers, for one comment, the same question
// each recognizer answers — so the lint can say "forge read this and did NOT
// accept it" with file:line and the expected form.
//
// Each recognizer keeps its own scanning logic; each entry's Accepts mirrors
// it, and go_markers_test.go pins the two against each other for every marker
// whose recognizer is a pure function of the comment text.

package codegen

import (
	"regexp"
	"sort"
	"strings"
)

// oneWordArgRE is GoMarkerOneWord's grammar — the tail of the composition
// graph's audienceDirectiveRE.
var oneWordArgRE = regexp.MustCompile(`^\s+[a-z][a-z0-9_-]*\s*$`)

// GoMarkerGrammar is what may follow a marker's name in its comment.
type GoMarkerGrammar int

const (
	// GoMarkerBare — the marker is the whole comment — nothing may follow it
	// (forge:outbound-io, forge:optional-dep, forge:constructor, …).
	GoMarkerBare GoMarkerGrammar = iota
	// GoMarkerReasoned — bare, or `: <reason>` (forge:exclude-contract,
	// forge:not-a-component). The colon is the only separator.
	GoMarkerReasoned
	// GoMarkerReasonRequired — `: <reason>` and nothing else
	// (forge:auth-unscoped-ok, forge:placeholder, forge:custom-read-shape).
	GoMarkerReasonRequired
	// GoMarkerWordArgs — bare, `: <text>` or ` <text>` — the recognizer
	// accepts a colon or whitespace after the name (forge:no-orm,
	// forge:resolves-caller).
	GoMarkerWordArgs
	// GoMarkerOneWord — exactly one lower-case word after a space
	// (forge:audience <name>).
	GoMarkerOneWord
	// GoMarkerOwnGrammar — the marker's own parser validates what follows
	// and reports its own errors (forge:root, the forge:lint-* suppressions),
	// or forge writes it into generated files itself (forge:hash, forge:gen,
	// forge:deps). The registry knows the name; the grammar is not its job.
	GoMarkerOwnGrammar
)

// GoMarker is one Go-source marker forge reads.
type GoMarker struct {
	// Name is the marker, `forge:` prefix included.
	Name string
	// Grammar is what may follow Name.
	Grammar GoMarkerGrammar
	// Form is the documented spelling, quoted back in a malformed-marker
	// report.
	Form string
}

// goMarkers is the registry. Keep it sorted by name; TestGoMarkersSorted
// pins that so a duplicate cannot hide.
var goMarkers = []GoMarker{
	{Name: "forge:allow", Grammar: GoMarkerOwnGrammar, Form: "//forge:allow"},
	{Name: "forge:audience", Grammar: GoMarkerOneWord, Form: "//forge:audience <name>"},
	{Name: "forge:auth-unscoped-ok", Grammar: GoMarkerReasonRequired, Form: "// forge:auth-unscoped-ok: <reason>"},
	{Name: directiveConstructorMarker, Grammar: GoMarkerBare, Form: "// forge:constructor"},
	{Name: directiveContractMarker, Grammar: GoMarkerBare, Form: "// forge:contract"},
	{Name: "forge:custom-read-shape", Grammar: GoMarkerReasonRequired, Form: "// forge:custom-read-shape: <why>"},
	{Name: "forge:deps", Grammar: GoMarkerOwnGrammar, Form: "// forge:deps — consumer-declared interfaces for this package's Deps. (written by forge)"},
	{Name: directiveExcludeContract, Grammar: GoMarkerReasoned, Form: "//forge:exclude-contract: <reason>"},
	{Name: directiveExternalComponent, Grammar: GoMarkerBare, Form: "// forge:external-component"},
	{Name: "forge:gen", Grammar: GoMarkerOwnGrammar, Form: "// forge:gen <kind> <key>=<value> (written by forge)"},
	{Name: "forge:group", Grammar: GoMarkerOwnGrammar, Form: "//forge:group <word>[,<word>…]"},
	{Name: "forge:hash", Grammar: GoMarkerOwnGrammar, Form: "// forge:hash=<sha256> (written by forge)"},
	{Name: "forge:lint-disable", Grammar: GoMarkerOwnGrammar, Form: "// forge:lint-disable <rule>: <reason>"},
	{Name: "forge:lint-disable-file", Grammar: GoMarkerOwnGrammar, Form: "// forge:lint-disable-file <rule>: <reason>"},
	{Name: "forge:lint-disable-next-line", Grammar: GoMarkerOwnGrammar, Form: "// forge:lint-disable-next-line <rule>: <reason>"},
	{Name: "forge:lint-enable", Grammar: GoMarkerOwnGrammar, Form: "// forge:lint-enable <rule>"},
	{Name: directiveNoObserve, Grammar: GoMarkerBare, Form: "// forge:no-observe"},
	{Name: "forge:no-orm", Grammar: GoMarkerWordArgs, Form: "//forge:no-orm: <why>"},
	{Name: directiveNotAComponent, Grammar: GoMarkerReasoned, Form: "//forge:not-a-component: <reason>"},
	{Name: "forge:optional-checked", Grammar: GoMarkerBare, Form: "// forge:optional-checked"},
	{Name: "forge:optional-dep", Grammar: GoMarkerBare, Form: "// forge:optional-dep"},
	{Name: "forge:outbound-io", Grammar: GoMarkerBare, Form: "// forge:outbound-io"},
	{Name: "forge:placeholder", Grammar: GoMarkerReasonRequired, Form: "// forge:placeholder: <pkg>.<Type>"},
	{Name: directiveExternalComponent2, Grammar: GoMarkerBare, Form: "// forge:provided"},
	{Name: ResolvesCallerDirective, Grammar: GoMarkerWordArgs, Form: "//forge:resolves-caller"},
	{Name: "forge:root", Grammar: GoMarkerOwnGrammar, Form: "//forge:root <name> command=<word> migrate=apply|wait|none"},
	{Name: "forge:root-only", Grammar: GoMarkerOwnGrammar, Form: "//forge:root-only <root>[,<root>...]"},
	{Name: directiveServiceMarker, Grammar: GoMarkerBare, Form: "// forge:service"},
	{Name: "forge:standby", Grammar: GoMarkerOwnGrammar, Form: "//forge:standby"},
	{Name: TxOpenerMarker, Grammar: GoMarkerOwnGrammar, Form: "//forge:tx-opener level-arg=<n> [body-arg=<n>]"},
}

// TxOpenerMarker declares a project's own transaction opener — a func that
// takes an orm.TxPolicy and runs the transaction itself — so
// forgeconv-tx-lock-under-serializable judges its calls as it judges
// pkg/orm's. That lint parses the marker and reports a malformed one.
const TxOpenerMarker = "forge:tx-opener"

// KnownGoMarkers returns every Go-source marker name, sorted.
func KnownGoMarkers() []string {
	out := make([]string, len(goMarkers))
	for i, m := range goMarkers {
		out[i] = m.Name
	}
	return out
}

// LookupGoMarker returns the registry entry for name (`forge:` included).
func LookupGoMarker(name string) (GoMarker, bool) {
	i := sort.Search(len(goMarkers), func(i int) bool { return goMarkers[i].Name >= name })
	if i < len(goMarkers) && goMarkers[i].Name == name {
		return goMarkers[i], true
	}
	return GoMarker{}, false
}

// Accepts reports whether rest — the comment text after the marker name —
// is a spelling this marker's recognizer reads.
func (m GoMarker) Accepts(rest string) bool {
	switch m.Grammar {
	case GoMarkerBare:
		return strings.TrimSpace(rest) == ""
	case GoMarkerReasoned:
		return strings.TrimSpace(rest) == "" || strings.HasPrefix(rest, ":")
	case GoMarkerReasonRequired:
		return strings.HasPrefix(rest, ":")
	case GoMarkerWordArgs:
		return rest == "" || rest[0] == ':' || rest[0] == ' ' || rest[0] == '\t'
	case GoMarkerOneWord:
		return oneWordArgRE.MatchString(rest)
	default:
		return true
	}
}

// GoMarkerStatus is ClassifyGoMarkerComment's verdict.
type GoMarkerStatus int

const (
	// GoMarkerNone — not a marker attempt — ordinary prose, or prose that
	// mentions a marker.
	GoMarkerNone GoMarkerStatus = iota
	// GoMarkerOK — a marker spelled the way its recognizer reads it.
	GoMarkerOK
	// GoMarkerMalformed — a known marker followed by something its
	// recognizer does not read — the em-dash case. Forge ignores it.
	GoMarkerMalformed
	// GoMarkerUnknown — a directive-form `//forge:<name>` whose name no
	// Go-source recognizer reads — a typo, or a marker from another
	// vocabulary (a proto or column marker) that does nothing in a .go file.
	GoMarkerUnknown
)

// GoMarkerUse is one classified comment.
type GoMarkerUse struct {
	Status GoMarkerStatus
	// Name is the `forge:<name>` token as written.
	Name string
	// Rest is the text after Name.
	Rest string
	// Marker is the registry entry (zero for GoMarkerUnknown/None).
	Marker GoMarker
	// Directive is true for the unspaced `//forge:` form — Go's directive
	// syntax, which prose never uses.
	Directive bool
}

// lookalikeSeparators are what an author reaches for instead of the colon
// when giving a marker a reason. After the spaced `// forge:<name>` form they
// are the evidence that the comment was meant as a directive rather than as
// a sentence that starts with a marker's name.
var lookalikeSeparators = []string{"—", "–", "--", "-", "=", ";"}

// ClassifyGoMarkerComment classifies one raw Go comment (`// …` or `/* … */`,
// as go/ast or go/scanner hands it over).
//
// Two spellings start a marker, matching every recognizer: the unspaced
// directive form `//forge:<name>` and the spaced `// forge:<name>`. They are
// judged differently because only one of them is unambiguous:
//
//   - `//forge:<name>…` is Go's directive syntax; prose never glues the
//     slashes to a word. An unknown name is GoMarkerUnknown and a known name
//     its recognizer would not read is GoMarkerMalformed.
//   - `// forge:<name>…` is also how a sentence that starts with a marker's
//     name looks, and how doc comments quote proto markers. A known name
//     followed by a lookalike separator (an em-dash, `-`, `=`) is
//     GoMarkerMalformed — no sentence continues that way, it is the reliant
//     spelling — and a known name with no separator, or an unknown name,
//     is left alone as prose.
func ClassifyGoMarkerComment(raw string) GoMarkerUse {
	var text string
	directive := false
	switch {
	case strings.HasPrefix(raw, "//"):
		text = strings.TrimPrefix(raw, "//")
		directive = strings.HasPrefix(text, "forge:")
		text = strings.TrimLeft(text, " \t")
	case strings.HasPrefix(raw, "/*"):
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "/*"), "*/"))
	default:
		return GoMarkerUse{}
	}
	if !strings.HasPrefix(text, "forge:") {
		return GoMarkerUse{}
	}
	end := len("forge:")
	for end < len(text) && isMarkerNameByte(text[end]) {
		end++
	}
	name, rest := text[:end], strings.TrimRight(text[end:], " \t\r")
	if name == "forge:" {
		return GoMarkerUse{}
	}
	use := GoMarkerUse{Name: name, Rest: rest, Directive: directive}
	m, known := LookupGoMarker(name)
	if !known {
		if directive {
			use.Status = GoMarkerUnknown
		}
		return use
	}
	use.Marker = m
	if m.Accepts(rest) {
		use.Status = GoMarkerOK
		return use
	}
	if directive || startsWithLookalike(rest) {
		use.Status = GoMarkerMalformed
	}
	return use
}

// startsWithLookalike reports whether rest opens (after optional spaces) with
// a separator an author uses in place of the colon, followed by a space or the
// end — `— reason`, ` - reason`, `=reason` — never a hyphenated continuation
// of the name, which isMarkerNameByte already consumed.
func startsWithLookalike(rest string) bool {
	t := strings.TrimLeft(rest, " \t")
	for _, sep := range lookalikeSeparators {
		if after, ok := strings.CutPrefix(t, sep); ok {
			if sep == "=" || after == "" || after[0] == ' ' || after[0] == '\t' {
				return true
			}
		}
	}
	return false
}

func isMarkerNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b >= 'A' && b <= 'Z'
}

// ClosestGoMarker returns the known Go-source marker nearest to name, by the
// same rule ClosestProtoMarker uses, or "" when nothing is close enough.
func ClosestGoMarker(name string) string {
	return ClosestFrom(name, KnownGoMarkers())
}

// MalformedGoMarkers returns every comment in dir's non-test, non-generated
// .go files that spells marker in a form its recognizer does not read, in
// file-name then line order. Used to explain a rule that fires because a
// marker was ignored (the contract lint on a package whose
// `//forge:exclude-contract — …` was not read).
func MalformedGoMarkers(dir, marker string) []ReasonedMarker {
	var out []ReasonedMarker
	forEachPackageComment(dir, func(path string, line int, raw string) {
		if use := ClassifyGoMarkerComment(raw); use.Status == GoMarkerMalformed && use.Name == marker {
			out = append(out, ReasonedMarker{File: path, Line: line, Reason: strings.TrimSpace(use.Rest)})
		}
	})
	return out
}
