package generator

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

func writtenFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func TestAgentSurfacesPerHarness(t *testing.T) {
	want := map[Harness][]string{
		HarnessReliant: {"AGENTS.md"},
		HarnessCodex:   {"AGENTS.md"},
		HarnessClaude:  {".claude/skills/forge/SKILL.md", "AGENTS.md", "CLAUDE.md"},
		HarnessCursor:  {".cursor/rules/forge.mdc", ".cursorrules", "AGENTS.md"},
		HarnessCopilot: {".github/copilot-instructions.md", "AGENTS.md"},
		"":             {"AGENTS.md"},
		HarnessNone:    nil,
	}
	for h, files := range want {
		t.Run(string(h), func(t *testing.T) {
			root := t.TempDir()
			if err := writeAgentSurfaces(root, "demo", h, false); err != nil {
				t.Fatal(err)
			}
			got := writtenFiles(t, root)
			if strings.Join(got, ",") != strings.Join(files, ",") {
				t.Fatalf("files = %v, want %v", got, files)
			}
			if _, err := os.Stat(filepath.Join(root, "reliant.md")); err == nil {
				t.Fatal("reliant.md must never be written: reliant renders the guide in memory")
			}
		})
	}
}

// AGENTS.md is the one rendered copy of the guide, byte-identical to what
// reliant renders in memory, so the two cannot drift.
func TestAgentsMDIsTheSingleRenderedGuide(t *testing.T) {
	for _, site := range []bool{false, true} {
		data := ProjectMemoryData{Name: "demo", CLI: cliName(), Site: site}
		guide, err := templates.ProjectTemplates().Render("reliant.md.tmpl", data)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range ValidHarnesses() {
			if h == HarnessNone {
				continue // writes no guide at all (TestAgentSurfacesPerHarness)
			}
			root := t.TempDir()
			if err := writeAgentSurfaces(root, "demo", h, site); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
			if err != nil {
				t.Fatalf("harness %s, site %t: %v", h, site, err)
			}
			if string(got) != string(guide) {
				t.Errorf("harness %s, site %t: AGENTS.md differs from the rendered guide", h, site)
			}
		}
	}
}

func TestClaudeMDImportsAgentsMD(t *testing.T) {
	root := t.TempDir()
	if err := writeAgentSurfaces(root, "demo", HarnessClaude, false); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if !strings.Contains(string(body), "\n@AGENTS.md") {
		t.Fatalf("CLAUDE.md must import AGENTS.md on its own line, got:\n%s", body)
	}
}

// Pointer files route to the binary and never carry skill content: skills come
// from `forge skill load`, so a copy on disk would shadow the live catalog.
func TestPointerFilesCarryNoSkillBodies(t *testing.T) {
	skill, err := templates.ProjectTemplates().Get("skills/forge/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	skillLines := map[string]bool{}
	for _, l := range strings.Split(string(skill), "\n") {
		if l = strings.TrimSpace(l); len(l) >= 30 {
			skillLines[l] = true
		}
	}

	for _, h := range []Harness{HarnessClaude, HarnessCursor, HarnessCopilot} {
		root := t.TempDir()
		if err := writeAgentSurfaces(root, "demo", h, false); err != nil {
			t.Fatal(err)
		}
		for _, f := range h.agentFiles() {
			if f.path == "AGENTS.md" {
				continue
			}
			body, err := os.ReadFile(filepath.Join(root, f.path))
			if err != nil {
				t.Fatal(err)
			}
			if len(body) > 1500 {
				t.Errorf("%s is %d bytes: a pointer, not a skill body", f.path, len(body))
			}
			for _, l := range strings.Split(string(body), "\n") {
				if skillLines[strings.TrimSpace(l)] {
					t.Errorf("%s copies a line of the forge skill: %q", f.path, l)
				}
			}
			if f.path == "CLAUDE.md" {
				continue // imports AGENTS.md; names the commands there
			}
			for _, need := range []string{"skill load forge", "skill search", "project capabilities"} {
				if !strings.Contains(string(body), need) {
					t.Errorf("%s does not tell the agent to run %q", f.path, need)
				}
			}
		}
	}
}

func TestAgentSurfacesNameReliantForge(t *testing.T) {
	root := t.TempDir()
	if err := writeAgentSurfaces(root, "demo", HarnessClaude, false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"AGENTS.md", ".claude/skills/forge/SKILL.md"} {
		body, _ := os.ReadFile(filepath.Join(root, f))
		s := string(body)
		for _, need := range []string{"Reliant Forge", "github.com/reliant-labs/forge", "Atlassian Forge", "Foundry"} {
			if !strings.Contains(s, need) {
				t.Errorf("%s does not disambiguate: missing %q", f, need)
			}
		}
	}
}

// Every agent file is the user's once it exists: a rerun — a second `forge
// project new --in-place`, an adoption over a repo that already has its own
// CLAUDE.md — never touches an edited copy, for any harness. The memory a
// harness injects live (reliant) is rendered beside the user's file, never
// into it.
func TestAgentSurfacesKeepExistingFiles(t *testing.T) {
	t.Parallel()
	for _, h := range ValidHarnesses() {
		t.Run(string(h), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			mine := []byte("# mine\n\nnotes the user wrote\n")
			for _, f := range h.agentFiles() {
				dest := filepath.Join(root, f.path)
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dest, mine, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, site := range []bool{false, true} {
				if err := writeAgentSurfaces(root, "demo", h, site); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range h.agentFiles() {
				got, _ := os.ReadFile(filepath.Join(root, f.path))
				if string(got) != string(mine) {
					t.Errorf("existing %s was overwritten:\n%s", f.path, got)
				}
			}
		})
	}
}

// harness: none is the brownfield promise that adopting forge adds no agent
// file: no guide, no pointer, no MCP config — through `forge project new`
// as well as writeAgentSurfaces.
func TestHarnessNoneWritesNoAgentSurface(t *testing.T) {
	if h, err := ParseHarness("none"); err != nil || h != HarnessNone {
		t.Fatalf("ParseHarness(none) = %q, %v", h, err)
	}
	if HarnessNone.MemoryFilePath() != "" {
		t.Fatalf("none has a memory file: %q", HarnessNone.MemoryFilePath())
	}
	root := t.TempDir()
	g := NewProjectGenerator("demo", root, "example.com/demo")
	g.Harness = HarnessNone
	if err := g.writeProjectMetadata(); err != nil {
		t.Fatal(err)
	}
	for _, f := range writtenFiles(t, root) {
		if !strings.HasPrefix(f, ".reliant/") {
			t.Errorf("harness none wrote %s", f)
		}
	}
}
