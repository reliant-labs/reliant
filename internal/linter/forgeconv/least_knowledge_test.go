package forgeconv

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/depsurface"
)

func lkFixture() *depsurface.Package {
	wide := make([]string, 12)
	for i := range wide {
		wide[i] = string(rune('A' + i))
	}
	return &depsurface.Package{
		Dir: "internal/handlers/tools",
		Fields: []depsurface.Field{
			{Name: "Repo", File: "internal/handlers/tools/service.go", Line: 10, Type: "db.Repository", Interface: true, Foreign: true, Methods: wide, Used: map[string]bool{"A": true, "B": true}},
			{Name: "Clock", File: "internal/handlers/tools/service.go", Line: 11, Type: "Clock", Interface: true, Methods: []string{"Now", "Since"}, Used: map[string]bool{"Now": true}},
			{Name: "Small", File: "internal/handlers/tools/service.go", Line: 12, Type: "events.Publisher", Interface: true, Foreign: true, Methods: []string{"Publish", "Close"}, Used: map[string]bool{"Publish": true}},
			{Name: "Passed", File: "internal/handlers/tools/service.go", Line: 13, Type: "db.Repository", Interface: true, Foreign: true, Methods: wide, Used: map[string]bool{"A": true}, Escapes: true},
			{Name: "Forwarded", File: "internal/handlers/tools/service.go", Line: 16, Type: "db.Repository", Interface: true, Foreign: true, Methods: wide, Used: map[string]bool{"A": true, "B": true, "C": true}, Roles: []depsurface.Role{{Type: "threads.Reader", Methods: []string{"B", "C"}, Embeddable: true}}},
			{Name: "Bag", File: "internal/handlers/tools/service.go", Line: 14, Type: "any", Interface: true, Container: "`any`", Used: map[string]bool{}},
			{Name: "Config", File: "internal/handlers/tools/service.go", Line: 15, Type: "*config.Config", RootConfig: true, Used: map[string]bool{}},
		},
		Downcasts: []depsurface.Downcast{{Field: "Repo", File: "internal/handlers/tools/raw.go", Line: 3, Target: "*db.Repo"}},
		EnvReads:  []depsurface.EnvRead{{File: "internal/handlers/tools/env.go", Line: 7, Call: "os.Getenv"}},
	}
}

func lkSummary(fs []Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule+":"+string(f.Severity)+"@"+f.File)
	}
	return strings.Join(out, " ")
}

// lkSeverities keys each finding by rule (deps-surface by field) to its
// severity.
func lkSeverities(fs []Finding) map[string]Severity {
	sev := map[string]Severity{}
	for _, f := range fs {
		key := f.Rule
		if f.Rule == RuleDepsSurface {
			key += "/" + strings.Fields(f.Message)[0]
		}
		sev[key] = f.Severity
	}
	return sev
}

// By default the rules carry the design's severities (§9.2): the PR 5 rules
// became errors one release after landing as warnings.
func TestLeastKnowledge_DesignSeveritiesByDefault(t *testing.T) {
	fs := leastKnowledgeFindings(lkFixture(), false)
	// Repo, Clock, Small and Forwarded are wider than their use (Forwarded
	// counting what it is passed on as); Passed escapes into something that
	// bounds nothing, so its use here is not the whole story; Bag is a
	// container; the downcast; the env read. The root config is reported
	// only under --strict.
	if got := len(fs); got != 7 {
		t.Fatalf("%d findings: %s", got, lkSummary(fs))
	}
	want := map[string]Severity{
		RuleDepsSurface + "/Deps.Repo":      SeverityError,   // foreign, 12 methods, unused ones
		RuleDepsSurface + "/Deps.Clock":     SeverityWarning, // declared here
		RuleDepsSurface + "/Deps.Small":     SeverityWarning, // foreign but narrow
		RuleDepsSurface + "/Deps.Forwarded": SeverityError,   // passed on as a role: still wide
		RuleNoContainerDeps:                 SeverityError,
		RuleDepsNoDowncast:                  SeverityError,
		RuleNoGetenv:                        SeverityWarning,
	}
	sev := lkSeverities(fs)
	for k, w := range want {
		if sev[k] != w {
			t.Errorf("%s = %q, want %q (all: %v)", k, sev[k], w, sev)
		}
	}
	if _, ok := sev[RuleDepsRootConfig]; ok {
		t.Errorf("deps-root-config is reported only under --strict while the scaffold hands every component the root config")
	}
	for _, f := range fs {
		if strings.HasPrefix(f.Message, "Deps.Forwarded ") {
			for _, want := range []string{"uses (calls, or passes it on as threads.Reader) 3 of them", "unused: D, E"} {
				if !strings.Contains(f.Message, want) {
					t.Errorf("Forwarded's finding lacks %q: %s", want, f.Message)
				}
			}
			if !strings.Contains(f.Remediation, "embedding the interfaces it passes the value on as") {
				t.Errorf("Forwarded's fix names no embedding: %s", f.Remediation)
			}
		}
	}
	if !strings.Contains(fs[0].Remediation, "forge fix narrow-deps internal/handlers/tools") {
		t.Errorf("a foreign field's fix is narrow-deps: %s", fs[0].Remediation)
	}
}

// --strict makes every least-knowledge finding an error but the root
// config, which it reports as a warning: every fresh scaffold still hands its
// components the root config, and --strict must pass on a fresh scaffold.
func TestLeastKnowledge_StrictMakesEveryFindingAnError(t *testing.T) {
	fs := leastKnowledgeFindings(lkFixture(), true)
	sev := lkSeverities(fs)
	if sev[RuleDepsRootConfig] != SeverityWarning {
		t.Errorf("deps-root-config under --strict = %q, want warning", sev[RuleDepsRootConfig])
	}
	for _, f := range fs {
		if f.Rule != RuleDepsRootConfig && f.Severity != SeverityError {
			t.Errorf("%s is %s under --strict", f.Rule, f.Severity)
		}
	}
}
