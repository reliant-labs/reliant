package templates

import (
	"os"
	"strings"
	"testing"
)

// testkitImportPath is what an agent must write to call testkit.Slow,
// testkit.Eventually or testkit.Receive. Guides that named only
// `testkit.Slow` sent agents grepping existing tests for the import, and
// agents who never found testkit.Eventually wrote their own poll loops.
const testkitImportPath = "github.com/reliant-labs/forge/pkg/testkit"

// TestMemoryTemplateTeachesTestkit: the guide loaded into every session
// names testkit's import path beside testkit.Slow, says a Slow test does not
// run in the -short tier (a green `task test:short` was taken for coverage of
// one), and points at testkit's event waits instead of sleeps. Both variants:
// with a Taskfile and without. The -v passthrough that lists what the inner
// loop ran and skipped moved to the testing skill, which the guide names
// (TestTestingSkillTeachesTestkit holds it there).
func TestMemoryTemplateTeachesTestkit(t *testing.T) {
	t.Parallel()
	for name, data := range map[string]ProjectMemoryData{
		"taskfile":    {Name: "demo", CLI: "forge"},
		"no taskfile": {Name: "demo", CLI: "forge", Derived: true, Gate: MemoryGate{Source: "Makefile", Runner: "make", Commands: []GateCommand{{Run: "make test", Does: "tests"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, err := ProjectTemplates().Render("reliant.md.tmpl", data)
			if err != nil {
				t.Fatalf("render reliant.md.tmpl: %v", err)
			}
			body := string(out)
			wants := []string{"`testkit.Slow(", "(`" + testkitImportPath + "`)", "skips under", "`testkit.Eventually`", "`testkit.Receive`", "(`testing`)"}
			if name == "taskfile" {
				wants = append(wants, "a green `test:short` never ran it")
			} else {
				wants = append(wants, "This project has no Taskfile")
			}
			for _, want := range wants {
				if !strings.Contains(body, want) {
					t.Errorf("reliant.md.tmpl (%s) does not say %q", name, want)
				}
			}
		})
	}
}

// TestTestingSkillTeachesTestkit holds the testing skill to the same facts,
// with room for the detail the guide has no space for.
func TestTestingSkillTeachesTestkit(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("project/skills/forge/testing/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	skill := string(raw)
	for _, want := range []string{
		"import `" + testkitImportPath + "`",
		"a green `task test:short` says nothing about a slow test",
		"task test:short -- -v ",
		"`testkit.Eventually` and `testkit.Receive` (`" + testkitImportPath,
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("the testing skill does not say %q", want)
		}
	}
}
