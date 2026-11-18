// File: internal/cli/lint/lint_go_markers.go
//
// go-markers — `forge lint --go-markers`.
//
// The Go-source twin of proto-markers and column-markers. A `//forge:*`
// comment in a .go file that forge does not read does NOTHING, and says
// nothing: reliant wrote `//forge:exclude-contract — <reason>` (an em-dash
// where the directive takes a colon), the package was not excluded, and the
// contract lint kept failing on it with no hint that the opt-out on screen had
// been read and refused. A typo'd name (`//forge:exclude-contrat: …`) is the
// same silent no-op.
//
// So every .go comment that starts a forge marker is classified against the
// Go-source registry (codegen.ClassifyGoMarkerComment, go_markers.go), and
// two verdicts fail the lint, each with file:line and the documented form:
//
//   - malformed: a known marker followed by something its recognizer does not
//     read — the em-dash case, or `//forge:outbound-io — prose` on a marker
//     that takes no argument;
//   - unknown: a directive-form `//forge:<name>` (Go's directive syntax,
//     which prose never uses) whose name no Go-source recognizer reads —
//     a typo, or a proto/column marker that does nothing in a .go file.
//
// It GATES, unlike its proto and column twins: those markers sit in free-text
// comments where `forge:` is often prose, while a Go directive comment has one
// purpose, and a gated finding is what turns "the lint fails for no reason"
// into "the lint fails because of this line". The spaced `// forge:<name>`
// form is only flagged when a lookalike separator proves it was meant as a
// directive (see ClassifyGoMarkerComment), so prose that starts with a
// marker's name is never a finding.
//
// Comments are read with go/scanner, so a `//forge:` inside a string literal
// (a test fixture's source) is never scanned. testdata/, vendor/, gen/,
// node_modules/ and hidden directories are skipped.

package lint

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// ruleGoMarkers is the rule id every finding carries, and the name a
// `forge:lint-disable-next-line` suppression names.
const ruleGoMarkers = "forge-go-markers"

// goMarkerFinding is one rejected marker comment.
type goMarkerFinding struct {
	File string
	Line int
	Use  codegen.GoMarkerUse
	// Suggestion is the nearest known Go-source marker for an unknown name.
	Suggestion string
}

func (f goMarkerFinding) message() string {
	if f.Use.Status == codegen.GoMarkerMalformed {
		return fmt.Sprintf("%s is followed by %q, which forge does not read — this marker currently does NOTHING",
			f.Use.Name, strings.TrimSpace(f.Use.Rest))
	}
	return fmt.Sprintf("%s is not a marker forge reads in a .go file — this comment currently does NOTHING", f.Use.Name)
}

func (f goMarkerFinding) fixHint() string {
	if f.Use.Status == codegen.GoMarkerMalformed {
		return fmt.Sprintf("write it as %#q (%s)", f.Use.Marker.Form, goMarkerGrammarNote(f.Use.Marker.Grammar))
	}
	var b strings.Builder
	if f.Suggestion != "" {
		fmt.Fprintf(&b, "did you mean %q? ", f.Suggestion)
	}
	if codegen.IsKnownProtoMarker(f.Use.Name) {
		fmt.Fprintf(&b, "%s is a PROTO marker — it belongs in a .proto comment. ", f.Use.Name)
	}
	fmt.Fprintf(&b, "Go-source markers: %s (see `forge project annotations --kind go`)", strings.Join(codegen.KnownGoMarkers(), ", "))
	return b.String()
}

func goMarkerGrammarNote(g codegen.GoMarkerGrammar) string {
	switch g {
	case codegen.GoMarkerBare:
		return "it takes no argument: put any explanation on the comment line above"
	case codegen.GoMarkerReasoned, codegen.GoMarkerReasonRequired:
		return "the reason follows a colon — not an em-dash, a hyphen or a space"
	case codegen.GoMarkerWordArgs:
		return "anything after it follows a colon or a space"
	case codegen.GoMarkerOneWord:
		return "one lower-case word follows it after a space, and nothing else"
	}
	return "see `forge project annotations --kind go`"
}

// collectGoMarkerFindings walks root for .go files and returns the rejected
// marker comments, suppression directives applied, as findings sorted by
// (file, line).
func collectGoMarkerFindings(root string) ([]finding.Finding, error) {
	var raw []goMarkerFinding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipGoMarkerDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		raw = append(raw, findGoMarkers(filepath.ToSlash(rel), src)...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].File != raw[j].File {
			return raw[i].File < raw[j].File
		}
		return raw[i].Line < raw[j].Line
	})
	fs := make([]finding.Finding, 0, len(raw))
	for _, f := range raw {
		fs = append(fs, finding.Finding{
			Rule:        ruleGoMarkers,
			Severity:    finding.SeverityError,
			File:        f.File,
			Line:        f.Line,
			Message:     f.message(),
			Remediation: f.fixHint(),
		})
	}
	res := suppress.ApplyFiles(root, fs, os.ReadFile)
	return append(res.Kept, res.Violations...), nil
}

// skipGoMarkerDir names the directories whose .go files are not the
// project's own source: fixtures, vendored and generated trees, and hidden
// directories (.git, .forge, a .tools cache).
func skipGoMarkerDir(name string) bool {
	switch name {
	case "testdata", "vendor", "node_modules", "gen":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// findGoMarkers scans one file's comments.
func findGoMarkers(file string, src []byte) []goMarkerFinding {
	fset := token.NewFileSet()
	tf := fset.AddFile(file, -1, len(src))
	var s scanner.Scanner
	s.Init(tf, src, func(token.Position, string) {}, scanner.ScanComments)
	var out []goMarkerFinding
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT || !strings.Contains(lit, "forge:") {
			continue
		}
		use := codegen.ClassifyGoMarkerComment(lit)
		switch use.Status {
		case codegen.GoMarkerMalformed:
			out = append(out, goMarkerFinding{File: file, Line: fset.Position(pos).Line, Use: use})
		case codegen.GoMarkerUnknown:
			if suppress.IsDirectiveToken(use.Name) {
				continue
			}
			out = append(out, goMarkerFinding{File: file, Line: fset.Position(pos).Line, Use: use, Suggestion: codegen.ClosestGoMarker(use.Name)})
		}
	}
	return out
}

// collectGoMarkersJSON maps the findings onto the JSON contract; the bool is
// the gating verdict.
func collectGoMarkersJSON(root string) ([]lintJSONFinding, bool, error) {
	findings, err := collectGoMarkerFindings(root)
	if err != nil {
		return nil, false, fmt.Errorf("go-markers lint failed: %w", err)
	}
	return findingsToJSON(findings), countErrors(findings) > 0, nil
}
