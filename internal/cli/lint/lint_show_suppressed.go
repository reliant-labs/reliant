package lint

// `forge lint --show-suppressed` — every suppression the project declares,
// in one list.
//
// A suppression mechanism whose output is invisible is how a codebase ends up
// disabling everything one line at a time (internal/linter/suppress). Each
// suppression is visible at its own site, but nothing showed them TOGETHER: a
// reviewer asking "what has this project told forge lint to ignore, and why"
// had to grep five spellings across five file types and forge.yaml.
//
// It is read-only: it runs no lane, fixes nothing, and exits 0. It is an
// audit, not a gate; a reasonless suppression of a gating rule is already a
// finding of its own (forge-suppression-missing-reason,
// forge-exclude-contract-reason).
//
// What it lists, each read by the code that honours it, so the list and the
// linters cannot disagree:
//
//   - forge.yaml: lint.rules entries set to off or warn, and contracts.exclude
//     paths;
//   - //forge:exclude-contract package markers (codegen.FindExcludeContractMarker);
//   - forge:lint-disable-file, forge:lint-disable-next-line,
//     forge:lint-disable … forge:lint-enable and //nolint: directives
//     (suppress.Declarations), in Go, proto, SQL, TypeScript/JavaScript and
//     YAML;
//   - the rule-specific pragmas: -- forge:allow-destructive,
//     -- forge:allow-unsafe-not-null and -- forge:allow-in-transaction in SQL
//     (migrationlint.Allowances), and
//     // forge:optional-checked in Go (the optional-deps guard).
//
// Hand-written files only: a generated file's directives are forge's, and the
// next generate rewrites any edit to them. testdata, dependency and build
// trees are skipped. golangci-lint's own .golangci.yml exclusions are not
// forge's to report.

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/migrationlint"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// declaredSuppression is one rule turned off somewhere, as the inventory
// reports it. It is also the --json element.
type declaredSuppression struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Rule string `json:"rule"`
	// Mechanism is how the suppression is spelled: the directive token, the
	// forge.yaml key, or the marker.
	Mechanism string `json:"mechanism"`
	// Covers is what it silences, in words ("line 12", "the file",
	// "package internal/algos").
	Covers string `json:"covers"`
	Reason string `json:"reason,omitempty"`
}

// contractRules names what a package-level contract opt-out silences: the
// --contract lane's rules, for the whole package.
const contractRules = "contract"

// directiveExts are the file types the shared suppression mechanism is
// written in (internal/linter/suppress: "forge lints Go, proto, SQL,
// TypeScript and YAML").
var directiveExts = map[string]bool{
	".go": true, ".proto": true, ".sql": true,
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".yaml": true, ".yml": true,
}

// skippedDirs are trees no forge linter reads as the project's own source.
// Hidden directories (.git, .forge, .next) are skipped too. Build-output
// names (build, dist, out) are not: a Go package may be called build.
var skippedDirs = map[string]bool{"node_modules": true, "vendor": true, "testdata": true}

// generatedHeaderRe is the canonical generated-file banner in any of the
// comment syntaxes above, looked for near the top of a file.
var generatedHeaderRe = regexp.MustCompile(`(?m)^\s*(//|#|--)\s*Code generated .*DO NOT EDIT`)

// runShowSuppressed prints the inventory for the project the command runs in.
func runShowSuppressed(w io.Writer, jsonOut bool, rawScope []string) error {
	root, err := cmdutil.FindProjectRoot()
	if err != nil {
		return err
	}
	if root == "" {
		if root, err = cmdutil.ResolutionRoot(); err != nil {
			return err
		}
	}
	scope, err := parseLintScope(root, rawScope)
	if err != nil {
		return err
	}
	all, err := collectSuppressions(root)
	if err != nil {
		return err
	}
	var entries []declaredSuppression
	for _, e := range all {
		if scope.contains(e.File) {
			entries = append(entries, e)
		}
	}
	if jsonOut {
		return renderSuppressionsJSON(w, entries)
	}
	renderSuppressionsText(w, entries, scope)
	return nil
}

// collectSuppressions walks root and returns every declared suppression,
// sorted by file and line. Files are reported project-relative.
func collectSuppressions(root string) ([]declaredSuppression, error) {
	out, err := configSuppressions(root)
	if err != nil {
		return nil, err
	}
	excludeDirs := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (skippedDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !directiveExts[filepath.Ext(name)] || strings.HasSuffix(name, "_gen.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(body)
		if generatedHeaderRe.MatchString(content[:min(len(content), 4096)]) {
			return nil
		}
		rel := relSlash(root, path)
		for _, d := range suppress.Declarations(content) {
			out = append(out, declaredSuppression{
				File: rel, Line: d.Line, Rule: d.Rule, Mechanism: d.Directive, Covers: d.Covers, Reason: d.Reason,
			})
		}
		switch filepath.Ext(name) {
		case ".sql":
			for _, a := range migrationlint.Allowances(content) {
				for _, rule := range a.Rules {
					out = append(out, declaredSuppression{
						File: rel, Line: a.Line, Rule: rule, Mechanism: a.Pragma, Covers: "the file", Reason: a.Reason,
					})
				}
			}
		case ".go":
			out = append(out, optionalCheckedSuppressions(rel, path, content)...)
			if !strings.HasSuffix(name, "_test.go") && strings.Contains(content, "forge:exclude-contract") {
				excludeDirs[filepath.Dir(path)] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s for suppressions: %w", root, err)
	}
	for dir := range excludeDirs {
		m, ok := codegen.FindExcludeContractMarker(dir)
		if !ok {
			continue
		}
		out = append(out, declaredSuppression{
			File: relSlash(root, m.File), Line: m.Line, Rule: contractRules, Mechanism: "forge:exclude-contract",
			Covers: "package " + relSlash(root, dir) + ", with its mock and decorator", Reason: m.Reason,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// optionalCheckedSuppressions lists the `// forge:optional-checked` comments
// in one Go file, recognised exactly as the optional-deps guard recognises
// them (commentDirectiveText). The guard silences the comment's own line and
// the line below it.
func optionalCheckedSuppressions(rel, path, content string) []declaredSuppression {
	if !strings.Contains(content, optionalCheckedDirective) {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		// The guard skips a file that does not parse, so nothing in it
		// is suppressed either.
		return nil
	}
	var out []declaredSuppression
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if commentDirectiveText(c.Text) != optionalCheckedDirective {
				continue
			}
			line := fset.Position(c.Pos()).Line
			out = append(out, declaredSuppression{
				File: rel, Line: line, Rule: "forge-optional-deps-guard", Mechanism: optionalCheckedDirective,
				Covers: fmt.Sprintf("lines %d-%d", line, line+1),
			})
		}
	}
	return out
}

// configSuppressions reads the repo-wide suppressions from forge.yaml, with
// the line each is written on: lint.rules entries that turn a rule off or
// down to a warning, and contracts.exclude paths. No forge.yaml, none.
func configSuppressions(root string) ([]declaredSuppression, error) {
	body, err := os.ReadFile(filepath.Join(root, "forge.yaml"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse forge.yaml: %w", err)
	}
	var out []declaredSuppression
	if rules := yamlPath(&doc, "lint", "rules"); rules != nil && rules.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(rules.Content); i += 2 {
			k, v := rules.Content[i], rules.Content[i+1]
			sev := strings.ToLower(strings.TrimSpace(v.Value))
			covers := "every finding in the project: not reported"
			if sev != suppress.SeverityOff {
				// Only a downgrade suppresses anything; error (or a
				// value lint.rules validation refuses) does not.
				parsed, ok := finding.ParseSeverity(sev)
				if !ok || parsed == finding.SeverityError {
					continue
				}
				covers = "every finding in the project: reported as " + string(parsed) + ", never gating"
			}
			out = append(out, declaredSuppression{
				File: "forge.yaml", Line: k.Line, Rule: k.Value, Mechanism: "lint.rules: " + sev, Covers: covers,
				Reason: yamlComment(k.HeadComment, v.LineComment),
			})
		}
	}
	if excl := yamlPath(&doc, "contracts", "exclude"); excl != nil && excl.Kind == yaml.SequenceNode {
		for _, item := range excl.Content {
			out = append(out, declaredSuppression{
				File: "forge.yaml", Line: item.Line, Rule: contractRules, Mechanism: "contracts.exclude",
				Covers: "packages matching " + item.Value + ", with their mocks and decorators",
				Reason: yamlComment(item.HeadComment, item.LineComment),
			})
		}
	}
	return out, nil
}

// yamlComment is the comment written above or beside a forge.yaml entry —
// the closest thing that file has to a stated reason — as one line.
func yamlComment(parts ...string) string {
	var words []string
	for _, p := range parts {
		for _, line := range strings.Split(p, "\n") {
			if line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#")); line != "" {
				words = append(words, line)
			}
		}
	}
	return strings.Join(words, " ")
}

// yamlPath descends a document through mapping keys.
func yamlPath(n *yaml.Node, keys ...string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for _, key := range keys {
		if n.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				next = n.Content[i+1]
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

func relSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func renderSuppressionsText(w io.Writer, entries []declaredSuppression, scope *lintScope) {
	where := "this project"
	if scope != nil {
		where = scope.String()
	}
	if len(entries) == 0 {
		_, _ = fmt.Fprintf(w, "No suppressions declared in %s.\n", where)
		return
	}
	_, _ = fmt.Fprintf(w, "Suppressions declared in %s (read-only: nothing was linted):\n\n", where)
	noReason := 0
	byRule := map[string]int{}
	for _, e := range entries {
		reason := e.Reason
		if reason == "" {
			reason = "(no reason given)"
			noReason++
		}
		byRule[e.Rule]++
		_, _ = fmt.Fprintf(w, "%s:%d  %s  [%s; %s]\n    %s\n", e.File, e.Line, e.Rule, e.Mechanism, e.Covers, reason)
	}
	rules := make([]string, 0, len(byRule))
	for r := range byRule {
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool {
		if byRule[rules[i]] != byRule[rules[j]] {
			return byRule[rules[i]] > byRule[rules[j]]
		}
		return rules[i] < rules[j]
	})
	counts := make([]string, len(rules))
	for i, r := range rules {
		counts[i] = fmt.Sprintf("%s %d", r, byRule[r])
	}
	_, _ = fmt.Fprintf(w, "\n%s, %d with no reason. By rule: %s\n", plural(len(entries), "suppression"), noReason, strings.Join(counts, ", "))
}

func renderSuppressionsJSON(w io.Writer, entries []declaredSuppression) error {
	noReason := 0
	for _, e := range entries {
		if e.Reason == "" {
			noReason++
		}
	}
	if entries == nil {
		entries = []declaredSuppression{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Suppressions  []declaredSuppression `json:"suppressions"`
		Total         int                   `json:"total"`
		WithoutReason int                   `json:"without_reason"`
	}{entries, len(entries), noReason})
}
