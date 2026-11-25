package generator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestPnpmVersionIsPinnedEverywhere holds every place that names the pnpm a
// scaffolded frontend uses to ONE release, templates.PnpmVersion: each
// frontend template's package.json `packageManager` (which the scaffolded
// Dockerfile's corepack and the scaffolded workflows' pnpm/action-setup both
// read), and forge's own workflows that install pnpm to drive a scaffold. A
// workflow on another pnpm than the scaffold's lockfile was written by is
// how a frozen install fails on a lockfile nobody changed.
func TestPnpmVersionIsPinnedEverywhere(t *testing.T) {
	want := templates.PnpmVersion
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(want) {
		t.Fatalf("templates.PnpmVersion = %q, want an exact X.Y.Z release", want)
	}
	root := forgeRepoRoot(t)

	manifests, err := filepath.Glob(filepath.Join(root, "internal", "templates", "frontend", "*", "package.json.tmpl"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no frontend package.json templates found (err=%v) — the pin guard would inspect nothing", err)
	}
	for _, path := range manifests {
		rel, _ := filepath.Rel(root, path)
		out, err := templates.FrontendTemplates().Render(strings.TrimPrefix(filepath.ToSlash(rel), "internal/templates/frontend/"),
			templates.FrontendTemplateData{FrontendName: "web", ProjectName: "pin-probe"})
		if err != nil {
			t.Fatalf("%s: render: %v", rel, err)
		}
		var manifest struct {
			PackageManager string `json:"packageManager"`
		}
		if err := json.Unmarshal(out, &manifest); err != nil {
			t.Fatalf("%s: rendered package.json does not parse: %v", rel, err)
		}
		if got := manifest.PackageManager; got != "pnpm@"+want {
			t.Errorf("%s: packageManager %q, want pnpm@%s", rel, got, want)
		}
	}

	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	installs := 0
	for _, path := range workflows {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		var wf struct {
			Jobs map[string]struct {
				Steps []struct {
					Uses string            `yaml:"uses"`
					With map[string]string `yaml:"with"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &wf); err != nil {
			t.Fatalf("%s: parse: %v", rel, err)
		}
		for job, j := range wf.Jobs {
			for _, s := range j.Steps {
				if !strings.HasPrefix(s.Uses, "pnpm/action-setup@") {
					continue
				}
				installs++
				if got := s.With["version"]; got != want {
					t.Errorf("%s (job %s): pnpm/action-setup version %q, want %s", rel, job, got, want)
				}
			}
		}
	}
	if installs == 0 {
		t.Error(".github/workflows: no pnpm/action-setup found — the pin guard inspected nothing")
	}
}
