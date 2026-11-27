package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// The step against the real template: the guide `forge project new` wrote,
// under the user's notes, becomes the guide `forge project memory` renders
// for the project, the notes stay byte for byte, and a second run is a no-op.
func TestStepAgentsGuide_RealGuideRefreshesAndConverges(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	born, err := templates.ProjectTemplates().Render("reliant.md.tmpl", templates.ProjectMemoryData{Name: "demo", CLI: "forge"})
	if err != nil {
		t.Fatal(err)
	}
	bornGuide, _, ok := generator.SplitAgentsGuide(born)
	if !ok {
		t.Fatal("the rendered guide does not split at `## Project Notes`: the template lost its header or its heading")
	}
	notes := "## Project Notes\n\nOur deploys go through `task ship`.\n"
	path := filepath.Join(root, generator.AgentsGuidePath)
	if err := os.WriteFile(path, append(append([]byte{}, bornGuide...), notes...), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &pipelineContext{ProjectDir: root, Cfg: &config.ProjectConfig{Name: "demo"}}
	for run := 1; run <= 2; run++ {
		if err := stepAgentsGuide(ctx); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := renderAgentsGuides(root, "demo", "forge")
	if err != nil {
		t.Fatal(err)
	}
	wantGuide, _, _ := generator.SplitAgentsGuide(current)
	if want := string(wantGuide) + notes; string(got) != want {
		t.Fatalf("AGENTS.md is not the current guide over the user's notes:\n%s", got)
	}
	if !strings.Contains(string(got), "driven by the `forge` CLI") {
		t.Errorf("the guide lost the CLI it was written with:\n%s", got)
	}
}

// `forge project disown AGENTS.md` is the way to keep a hand-edited guide,
// so disown must accept the unmarked file.
func TestIsAgentsGuideRelPath(t *testing.T) {
	for p, want := range map[string]bool{"AGENTS.md": true, "./AGENTS.md": true, "docs/AGENTS.md": false, "CLAUDE.md": false} {
		if got := isAgentsGuideRelPath(p); got != want {
			t.Errorf("isAgentsGuideRelPath(%q) = %t, want %t", p, got, want)
		}
	}
}
