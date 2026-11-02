package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The declaration a pull request makes about an incompatible change to
// forge/pkg. Two spellings, either one declares:
//
//	label  api-break
//	marker a line in the PR body (or a commit body) of the form
//	       API-BREAK: [codemod=<forge fix name>] [playbook=migrations/<vX.Y.Z>]
//
// A declaration alone never passes: the PR must also SHIP the remedy — a
// codemod registered in internal/codemod, or a migration playbook under
// internal/templates/project/skills/forge/migrations — in the same diff.
const (
	BreakLabel  = "api-break"
	markerToken = "API-BREAK:"

	codemodDir    = "internal/codemod/"
	playbookRoot  = "internal/templates/project/skills/forge/"
	playbookGroup = "migrations/"
)

// Remedy is one codemod or playbook a marker names.
type Remedy struct {
	// Kind is "codemod" or "playbook" (anything else is rejected by Evaluate).
	Kind string
	// Value is the `forge fix` name, or the skill path under skills/forge/.
	Value string
}

// Declaration is what a PR said about its incompatible changes.
type Declaration struct {
	Declared bool
	Remedies []Remedy
}

// markerLine matches a marker at the START of a line (after indentation), so
// prose that mentions the convention mid-sentence declares nothing.
var markerLine = regexp.MustCompile(`(?mi)^[ \t]*API-BREAK:(.*)$`)

// ParseDeclaration reads the PR text (body plus commit bodies) and labels.
func ParseDeclaration(text string, labels []string) Declaration {
	var d Declaration
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), BreakLabel) {
			d.Declared = true
		}
	}
	for _, m := range markerLine.FindAllStringSubmatch(text, -1) {
		d.Declared = true
		for _, field := range strings.Fields(m[1]) {
			k, v, ok := strings.Cut(field, "=")
			if !ok || v == "" {
				continue
			}
			d.Remedies = append(d.Remedies, Remedy{Kind: strings.ToLower(k), Value: v})
		}
	}
	return d
}

// Verdict is the gate's decision and, when it fails, the full explanation.
type Verdict struct {
	OK     bool
	Reason string
}

// Evaluate decides whether the incompatible changes may merge. changed is the
// PR's changed files (slash paths relative to the module root); root is the
// head checkout, read to confirm a named codemod or playbook really exists.
func Evaluate(breaks []Break, decl Declaration, changed []string, root string) Verdict {
	if len(breaks) == 0 {
		return Verdict{OK: true}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d incompatible change(s) to forge/pkg:\n", len(breaks))
	for _, br := range breaks {
		fmt.Fprintf(&b, "  - %s\n", br)
	}
	fail := func(format string, args ...any) Verdict {
		b.WriteString("\n")
		fmt.Fprintf(&b, format, args...)
		return Verdict{Reason: strings.TrimRight(b.String(), "\n")}
	}

	if !decl.Declared {
		return fail("forge may break freely, but a breaking PR pays for it: declare the break and ship its codemod.\n"+
			"  1. Add the `%s` label to the PR, or put a line in the PR body:\n"+
			"       %s codemod=<forge fix name>        (or playbook=migrations/<vX.Y.Z>)\n"+
			"  2. Ship the remedy in this PR: a `forge fix <name>` codemod registered in %s, or a\n"+
			"     migration playbook entry under %s%s<vX.Y.Z>/.\n"+
			"     (docs: dev-docs/api-breaks.md)", BreakLabel, markerToken, codemodDir, playbookRoot, playbookGroup)
	}

	if len(decl.Remedies) == 0 {
		if !shipsCodemod(changed) && !shipsPlaybook(changed) {
			return fail("the break is declared but this PR ships no codemod and no playbook entry.\n"+
				"  Add a codemod (a non-test file in %s that registers a `forge fix` name) or a playbook\n"+
				"  (a file under %s%s), then name it: %s codemod=<name>", codemodDir, playbookRoot, playbookGroup, markerToken)
		}
		return Verdict{OK: true}
	}

	var problems []string
	for _, r := range decl.Remedies {
		var err error
		switch r.Kind {
		case "codemod":
			err = checkCodemod(r.Value, changed, root)
		case "playbook":
			err = checkPlaybook(r.Value, changed, root)
		default:
			err = fmt.Errorf("unknown remedy %q: use codemod=<name> or playbook=<migrations/vX.Y.Z>", r.Kind+"="+r.Value)
		}
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fail("the declared remedy does not check out:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return Verdict{OK: true}
}

func isCodemodSource(f string) bool {
	return strings.HasPrefix(f, codemodDir) && strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") &&
		!strings.Contains(strings.TrimPrefix(f, codemodDir), "/")
}

func shipsCodemod(changed []string) bool {
	for _, f := range changed {
		if isCodemodSource(f) {
			return true
		}
	}
	return false
}

func shipsPlaybook(changed []string) bool {
	for _, f := range changed {
		if strings.HasPrefix(f, playbookRoot+playbookGroup) {
			return true
		}
	}
	return false
}

func checkCodemod(name string, changed []string, root string) error {
	re := regexp.MustCompile(`Name:\s*"` + regexp.QuoteMeta(name) + `"`)
	registered := false
	for _, f := range changed {
		if !isCodemodSource(f) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		if re.Match(b) {
			return nil
		}
	}
	// Distinguish "not registered anywhere" from "registered by an earlier PR".
	entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(codemodDir)))
	for _, e := range entries {
		f := codemodDir + e.Name()
		if e.IsDir() || !isCodemodSource(f) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f))); err == nil && re.Match(b) {
			registered = true
		}
	}
	if registered {
		return fmt.Errorf("codemod %q is registered, but not by this PR: a break must ship its own codemod (or name a playbook)", name)
	}
	return fmt.Errorf("codemod %q is not registered in %s (no `Name: %q` in a non-test file there)", name, codemodDir, name)
}

func checkPlaybook(value string, changed []string, root string) error {
	clean := path.Clean(value)
	if !strings.HasPrefix(clean, playbookGroup) || strings.Contains(clean, "..") {
		return fmt.Errorf("playbook %q must be a path under %s (for example %sv1.0.0, the release NEXT_VERSION names)", value, playbookGroup, playbookGroup)
	}
	dir := playbookRoot + clean + "/"
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir+"SKILL.md"))); err != nil {
		return fmt.Errorf("playbook %q has no %sSKILL.md", value, dir)
	}
	for _, f := range changed {
		if strings.HasPrefix(f, dir) {
			return nil
		}
	}
	return fmt.Errorf("playbook %q exists but this PR does not touch it: add this break's section to %s", value, dir)
}
