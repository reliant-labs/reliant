// skill_headroom_test.go — the early warning before a skill's hard size cap.
package templates

import (
	"fmt"
	"strings"
	"testing"
)

// SkillHeadroomPercent is where a skill is reported as due for a split: a
// warning, not a failure, so the split happens in its own PR while there is
// still room, instead of under a hard cap that ejects whichever unrelated PR
// adds the next line (three were ejected in one week: #1759, #1770, …).
//
// Exported from a _test.go file so the external templates_test package (the
// load-budget guard in skills_size_test.go) shares it with this package's
// frontend working-cap test.
const SkillHeadroomPercent = 85

// SkillHeadroomWarning returns the warning for a skill of size bytes against
// a cap of budget bytes, or "" while it is at or under SkillHeadroomPercent.
// The line starts with a GitHub Actions workflow command, so
// scripts/ci-test-shard.sh surfaces it as an annotation on a passing run;
// locally it is visible under -v or -json.
func SkillHeadroomWarning(skill string, size, budget int) string {
	if size*100 <= budget*SkillHeadroomPercent {
		return ""
	}
	return fmt.Sprintf("::warning title=Skill headroom::%s is %d of %d bytes (%d%%, over the %d%% headroom line): "+
		"split a separable section into a sub-skill before it reaches the cap",
		skill, size, budget, size*100/budget, SkillHeadroomPercent)
}

func TestSkillHeadroomWarning(t *testing.T) {
	t.Parallel()
	const budget = 1000
	if w := SkillHeadroomWarning("x", 850, budget); w != "" {
		t.Errorf("exactly %d%% must not warn, got %q", SkillHeadroomPercent, w)
	}
	w := SkillHeadroomWarning("deploy", 851, budget)
	for _, want := range []string{"deploy", "851 of 1000", "85%"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not contain %q", w, want)
		}
	}
	if !strings.HasPrefix(w, "::warning") {
		t.Errorf("warning must start with the workflow command so CI can surface it: %q", w)
	}
}
