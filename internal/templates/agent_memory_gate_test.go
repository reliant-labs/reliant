package templates

import (
	"strings"
	"testing"
)

// guideVariants are the guides reliant.md.tmpl renders, one per project
// kind: the Go guide (a service, CLI or library, with or without a
// Taskfile), a static site, and an adopted app.
func guideVariants() map[string]ProjectMemoryData {
	return map[string]ProjectMemoryData{
		"go service": {Name: "demo", CLI: "forge"},
		"go no taskfile": {Name: "demo", CLI: "forge", Derived: true, GoModule: true,
			Gate: MemoryGate{Source: "Makefile", Runner: "make", Commands: []GateCommand{{Run: "make test", Does: "tests"}}}},
		"site":    {Name: "demo", CLI: "forge", Site: true},
		"adopted": {Name: "demo", CLI: "forge", Container: true},
	}
}

func renderGuide(t *testing.T, d ProjectMemoryData) string {
	t.Helper()
	out, err := ProjectTemplates().Render("reliant.md.tmpl", d)
	if err != nil {
		t.Fatalf("render reliant.md.tmpl: %v", err)
	}
	return string(out)
}

// TestGuideTeachesIterateScopedThenOneFullGate: agents in the forgebench
// pilot ran the whole `forge lint` (60-140s) after every edit, because the
// guide called the full gate "the gate before you call a phase done" and
// pitched `--scope` only for a shared checkout. Every kind's guide now says
// to iterate on what changed and run the full gate once, at the end, and
// keeps the read-the-last-line rule.
func TestGuideTeachesIterateScopedThenOneFullGate(t *testing.T) {
	t.Parallel()
	for name, d := range guideVariants() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := renderGuide(t, d)
			lower := strings.ToLower(body)
			if !strings.Contains(lower, "once, before you finish") {
				t.Errorf("the %s guide never says to run the full gate once, before finishing", name)
			}
			if !strings.Contains(body, "through `head`") || !strings.Contains(body, "LAST line") {
				t.Errorf("the %s guide lost \"never pipe lint through head; the verdict is the last line\"", name)
			}
			if strings.Contains(lower, "before you call a phase done") {
				t.Errorf("the %s guide still calls the full gate the per-phase gate", name)
			}
			switch name {
			case "go service", "go no taskfile":
				if !strings.Contains(body, "lint --scope <changed path>") {
					t.Errorf("the %s guide does not teach `lint --scope` for the inner loop", name)
				}
			case "site":
				if !strings.Contains(body, "Iterate with `forge lint --static-export`") {
					t.Errorf("the site guide does not name its inner-loop lint lane")
				}
			}
		})
	}
}

// TestGuideStaysSlim budgets the guide. It is resent on every model call of
// every session in the project, so each paragraph is a per-call cost; depth
// belongs in a skill the guide names. The Go guide was ~9.4k characters
// (~2.3k tokens) before it was cut to the rules plus skill pointers.
func TestGuideStaysSlim(t *testing.T) {
	t.Parallel()
	budget := map[string]int{"go service": 4800, "site": 5200}
	for name, max := range budget {
		if n := len(renderGuide(t, guideVariants()[name])); n > max {
			t.Errorf("the %s guide is %d bytes, over its %d-byte budget: move the detail into the skill it names", name, n, max)
		}
	}
}
