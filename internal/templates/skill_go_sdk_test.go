package templates_test

import (
	"strings"
	"testing"
)

func TestGoSDKSkillPublishesTheNestedModule(t *testing.T) {
	t.Parallel()
	skills := shippedSkills(t)
	if !strings.Contains(skills["forge/migration-cli/SKILL.md"], "`migration-cli/go-sdk`") {
		t.Error("migration-cli must point SDK authors at the release recipe")
	}
	body := skills["forge/migration-cli/go-sdk/SKILL.md"]
	for _, want := range []string{
		"git push origin refs/tags/gen/v0.3.0",
		"go mod edit -require=github.com/acme/dispatch/gen@v0.3.0",
		"dependencies' `replace` directives are\nignored",
		"outside** the repo",
		"export GOWORK=off",
		"go get github.com/acme/dispatch/pkg/client@v0.3.0",
		"go list -m github.com/acme/dispatch/gen",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Go SDK release recipe missing %q", want)
		}
	}
}
