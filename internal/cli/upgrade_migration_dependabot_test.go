package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestDependabotGeneratorsMigration_Detection runs the SHIPPED v0.1.44
// detection against the dependabot.yml shapes it has to tell apart.
// dependabot.yml is scaffold-once, so this migration is the only way an
// existing project learns its Dependabot config bumps a version-stamping
// generator (protoc-gen-es, protoc-gen-go) and fails Verify Generated Code
// on every such PR.
//
// The "current" cases render the real template, so a template that stops
// emitting an ignore the detection looks for fails here instead of offering
// the migration to every project forever.
func TestDependabotGeneratorsMigration_Detection(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	var m migrationMeta
	for _, candidate := range metas {
		if candidate.ID == "v0.1.44" {
			m = candidate
		}
	}
	if m.ID == "" {
		t.Fatal("no shipped v0.1.44 migration")
	}

	render := func(data templates.DependabotData) string {
		t.Helper()
		b, err := templates.CITemplates("github").Render("dependabot.yml.tmpl", data)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const (
		bufGenGo   = "version: v2\nplugins:\n  - local: protoc-gen-go\n    out: gen\n  - local: protoc-gen-connect-go\n    out: gen\n"
		npmNoIgn   = "version: 2\nupdates:\n  - package-ecosystem: npm\n    directory: /frontends/web\n    schedule:\n      interval: weekly\n"
		gomodOnly  = "version: 2\nupdates:\n  - package-ecosystem: gomod\n    directory: /\n    ignore:\n      - dependency-name: github.com/reliant-labs/forge\n"
		npmIgnored = "version: 2\nupdates:\n  - package-ecosystem: npm\n    directory: /frontends/web\n    ignore:\n      - dependency-name: \"@bufbuild/protoc-gen-es\"\n      - dependency-name: \"@bufbuild/protobuf\"\n"
	)
	for _, tc := range []struct {
		name       string
		dependabot string // "" = no .github/dependabot.yml
		bufGen     string // "" = no buf.gen.yaml
		want       bool
	}{
		{"npm entry that lets protoc-gen-es through", npmNoIgn, "", true},
		{"npm entry that already ignores protoc-gen-es", npmIgnored, "", false},
		{"gomod entry that lets protobuf through, protoc-gen-go in use", gomodOnly, bufGenGo, true},
		{"gomod entry, no protoc-gen-go in the pipeline", gomodOnly, "", false},
		{"no dependabot.yml at all", "", bufGenGo, false},
		{"today's template, frontend + protoc-gen-go", render(templates.DependabotData{FrontendName: "web", RunsProtocGenGo: true}), bufGenGo, false},
		{"today's template, no frontend, no protoc-gen-go", render(templates.DependabotData{}), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.dependabot != "" {
				if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := writeFile(filepath.Join(dir, ".github", "dependabot.yml"), tc.dependabot); err != nil {
					t.Fatal(err)
				}
			}
			if tc.bufGen != "" {
				if err := writeFile(filepath.Join(dir, "buf.gen.yaml"), tc.bufGen); err != nil {
					t.Fatal(err)
				}
			}
			if got := migrationApplies(m, "v0.1.43", dir); got != tc.want {
				t.Errorf("migrationApplies = %v, want %v", got, tc.want)
			}
		})
	}
}
