package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

var oneBreak = []Break{{Pkg: "pkg/widget", Message: "Close: removed"}}

func TestParseDeclaration(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		labels   []string
		declared bool
		remedies []Remedy
	}{
		{name: "nothing", text: "just a PR body\nwith lines"},
		{name: "label only", labels: []string{"bug", "api-break"}, declared: true},
		{name: "marker no remedy", text: "API-BREAK:", declared: true},
		{
			name: "marker with codemod and playbook", declared: true,
			text:     "Summary\n\nAPI-BREAK: codemod=tx-isolation playbook=migrations/v0.1.48\n",
			remedies: []Remedy{{Kind: "codemod", Value: "tx-isolation"}, {Kind: "playbook", Value: "migrations/v0.1.48"}},
		},
		{
			name: "case-insensitive, indented, in a commit body", declared: true,
			text:     "feat(orm)!: x\n\n  api-break: codemod=a\n",
			remedies: []Remedy{{Kind: "codemod", Value: "a"}},
		},
		{name: "mid-line mention is not a marker", text: "see the API-BREAK: codemod=x convention"},
		{name: "label match is exact", labels: []string{"api-break-ish"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := ParseDeclaration(tc.text, tc.labels)
			if d.Declared != tc.declared {
				t.Fatalf("Declared = %v, want %v", d.Declared, tc.declared)
			}
			if len(d.Remedies) != len(tc.remedies) {
				t.Fatalf("Remedies = %v, want %v", d.Remedies, tc.remedies)
			}
			for i := range tc.remedies {
				if d.Remedies[i] != tc.remedies[i] {
					t.Fatalf("Remedies = %v, want %v", d.Remedies, tc.remedies)
				}
			}
		})
	}
}

func TestEvaluateNoBreaksPasses(t *testing.T) {
	v := Evaluate(nil, Declaration{}, nil, t.TempDir())
	if !v.OK {
		t.Fatalf("no breaks must pass: %s", v.Reason)
	}
}

func TestEvaluateUndeclaredBreakFails(t *testing.T) {
	v := Evaluate(oneBreak, Declaration{}, nil, t.TempDir())
	if v.OK {
		t.Fatal("an undeclared break must fail")
	}
	for _, want := range []string{"pkg/widget: Close: removed", "API-BREAK:", "api-break", "codemod"} {
		if !strings.Contains(v.Reason, want) {
			t.Errorf("reason does not mention %q:\n%s", want, v.Reason)
		}
	}
}

func TestEvaluateDeclaredWithoutRemedyFails(t *testing.T) {
	root := t.TempDir()
	changed := []string{"pkg/widget/widget.go", "internal/codemod/helper_test.go", "docs/x.md"}
	v := Evaluate(oneBreak, Declaration{Declared: true}, changed, root)
	if v.OK {
		t.Fatal("declared but shipping no codemod or playbook must fail")
	}
	if !strings.Contains(v.Reason, "ships no codemod") {
		t.Errorf("reason: %s", v.Reason)
	}
}

func TestEvaluateLabelWithCodemodInDiffPasses(t *testing.T) {
	v := Evaluate(oneBreak, Declaration{Declared: true},
		[]string{"pkg/widget/widget.go", "internal/codemod/widgetclose.go"}, t.TempDir())
	if !v.OK {
		t.Fatalf("label + a codemod in the diff must pass: %s", v.Reason)
	}
}

func TestEvaluateLabelWithPlaybookInDiffPasses(t *testing.T) {
	v := Evaluate(oneBreak, Declaration{Declared: true},
		[]string{"pkg/widget/widget.go", "internal/templates/project/skills/forge/migrations/v0.1.48/SKILL.md"}, t.TempDir())
	if !v.OK {
		t.Fatalf("label + a playbook entry in the diff must pass: %s", v.Reason)
	}
}

func TestEvaluateNamedCodemodMustBeRegisteredAndInDiff(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "internal/codemod/widgetclose.go", "package codemod\n\nfunc init() {\n\tregister(Codemod{\n\t\tName: \"widget-close\",\n\t})\n}\n")
	decl := Declaration{Declared: true, Remedies: []Remedy{{Kind: "codemod", Value: "widget-close"}}}

	v := Evaluate(oneBreak, decl, []string{"pkg/widget/widget.go", "internal/codemod/widgetclose.go"}, root)
	if !v.OK {
		t.Fatalf("a registered codemod that is in the diff must pass: %s", v.Reason)
	}

	v = Evaluate(oneBreak, decl, []string{"pkg/widget/widget.go"}, root)
	if v.OK {
		t.Fatal("a codemod registered by an earlier PR is not shipped by this one")
	}

	decl.Remedies[0].Value = "no-such-codemod"
	v = Evaluate(oneBreak, decl, []string{"pkg/widget/widget.go", "internal/codemod/widgetclose.go"}, root)
	if v.OK || !strings.Contains(v.Reason, "no-such-codemod") {
		t.Fatalf("an unregistered codemod name must fail and be named: ok=%v %s", v.OK, v.Reason)
	}
}

func TestEvaluateNamedPlaybookMustExistAndBeInDiff(t *testing.T) {
	root := t.TempDir()
	rel := "internal/templates/project/skills/forge/migrations/v0.1.48/SKILL.md"
	writeFile(t, root, rel, "# v0.1.48\n")
	decl := Declaration{Declared: true, Remedies: []Remedy{{Kind: "playbook", Value: "migrations/v0.1.48"}}}

	if v := Evaluate(oneBreak, decl, []string{rel}, root); !v.OK {
		t.Fatalf("an existing playbook in the diff must pass: %s", v.Reason)
	}
	if v := Evaluate(oneBreak, decl, []string{"pkg/widget/widget.go"}, root); v.OK {
		t.Fatal("a playbook this PR did not touch is not shipped by it")
	}
	missing := Declaration{Declared: true, Remedies: []Remedy{{Kind: "playbook", Value: "migrations/v9.9.9"}}}
	if v := Evaluate(oneBreak, missing, []string{rel}, root); v.OK {
		t.Fatal("a playbook that does not exist must fail")
	}
	escape := Declaration{Declared: true, Remedies: []Remedy{{Kind: "playbook", Value: "../../../etc/passwd"}}}
	if v := Evaluate(oneBreak, escape, []string{rel}, root); v.OK {
		t.Fatal("a playbook path must stay inside the migrations skill directory")
	}
}

func TestUnknownRemedyKindFails(t *testing.T) {
	decl := Declaration{Declared: true, Remedies: []Remedy{{Kind: "vibes", Value: "x"}}}
	if v := Evaluate(oneBreak, decl, []string{"internal/codemod/a.go"}, t.TempDir()); v.OK {
		t.Fatal("an unknown remedy kind must fail rather than be ignored")
	}
}
