package templates_test

import (
	"strings"
	"testing"
)

// TestSkillsSpellPackageScaffoldOneWay keeps the skills on one spelling of
// the package scaffold. `forge package new` and `forge scaffold package` are
// the same command (internal/cli/deprecated_aliases.go names the second as
// the current spelling), but the services skill taught the first while the
// forge skill and AGENTS.md taught the second, so a reader could not tell
// they were one thing (forge friction #33, Ranger). The old spelling keeps
// working; the skills just never teach it.
func TestSkillsSpellPackageScaffoldOneWay(t *testing.T) {
	t.Parallel()
	for rel, content := range shippedSkills(t) {
		if strings.Contains(content, "forge package new") {
			t.Errorf("skills/%s says `forge package new`; teach `forge scaffold package` (the same command, the spelling every other skill uses)", rel)
		}
	}
}
