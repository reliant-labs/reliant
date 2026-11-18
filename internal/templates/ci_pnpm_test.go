package templates

import (
	"strings"
	"testing"
)

// Existing npm frontends keep their lockfile and command when CI is
// scaffolded beside a new pnpm frontend. This is a default flip, not an npm
// support removal.
func TestCIWorkflowMixedPackageManagers(t *testing.T) {
	out, err := CITemplates("github").Render("ci.yml.tmpl", CIWorkflowData{
		ProjectName: "demo", HasFrontends: true,
		Frontends: []FrontendCIConfig{
			{Name: "old", Path: "frontends/old", PackageManager: "npm"},
			{Name: "web", Path: "frontends/web", PackageManager: "pnpm"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pnpm/action-setup@v4", `package_json_file: "frontends/web/package.json"`, "frontends/old/package-lock.json", "frontends/web/pnpm-lock.yaml", "run: npm ci", "run: pnpm install --frozen-lockfile"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("mixed CI missing %q", want)
		}
	}
}
