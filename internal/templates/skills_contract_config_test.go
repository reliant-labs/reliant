package templates_test

import (
	"regexp"
	"testing"
)

func TestContractSkillsDoNotRecommendRemovedSettings(t *testing.T) {
	t.Parallel()
	skills := shippedSkills(t)
	// Match settings in YAML blocks and inline prose recommendations (such as
	// "Set `contracts.strict: true`"), not prose naming keys to remove.
	removed := regexp.MustCompile("(?m)(?:^\\s*(?:- )?|`)(?:contracts\\.)?(?:strict|allow_exported_vars|allow_exported_funcs):\\s*(?:true|false)\\b")
	for _, recommendation := range []string{
		"contracts:\n  strict: true",
		"- `allow_exported_funcs: false`",
		"Set `contracts.strict: true` from day one.",
		"Use `contracts.allow_exported_vars: true`.",
	} {
		if !removed.MatchString(recommendation) {
			t.Fatalf("removed-setting matcher misses recommendation: %s", recommendation)
		}
	}
	for _, name := range []string{"contracts", "migration", "migration-cli", "migration-service"} {
		path := "forge/" + name + "/SKILL.md"
		if bad := removed.FindString(skills[path]); bad != "" {
			t.Errorf("%s recommends a removed setting: %s", path, bad)
		}
	}
}
