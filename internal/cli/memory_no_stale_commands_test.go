package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestMemoryTemplateNamesNoRemovedCommands guards the memory file against
// instructing agents to run commands that no longer exist.
//
// reliant.md.tmpl renders the file that is auto-loaded into EVERY session in
// every directory, and the same template backs CLAUDE.md / AGENTS.md /
// .cursorrules for the other harnesses. That reach is what makes a stale
// command here worse than a stale line anywhere else in the docs: an agent
// follows it before it has read anything else, hits a hard failure on its
// first real action, and has to recover from a starting point the project
// itself handed it.
//
// `forge test` is the case that motivated this. It was removed so a second
// spelling of the test suite could not disagree with the Taskfile, the CLI
// grew a good failure message (see test_removed_test.go) — and the memory
// file kept telling agents to use it anyway.
func TestMemoryTemplateNamesNoRemovedCommands(t *testing.T) {
	t.Parallel()

	rendered, err := templates.ProjectTemplates().Render("reliant.md.tmpl", struct {
		Name string
		CLI  string
	}{Name: "demo", CLI: "forge"})
	if err != nil {
		t.Fatalf("render reliant.md.tmpl: %v", err)
	}
	body := string(rendered)

	// Each entry is a command spelling that has been removed, paired with
	// what the memory file should send people to instead. Add a row here
	// whenever a verb is retired.
	removed := []struct {
		command string
		instead string
	}{
		{command: "forge test", instead: "task test"},
	}

	for _, r := range removed {
		// Matched with the backticks the template uses for command spans, so
		// prose ABOUT the removal ("there is no `forge test`") does not trip
		// this — that sentence is exactly what we want the file to contain.
		if strings.Contains(body, "**`"+r.command+"`**") {
			t.Errorf("reliant.md.tmpl instructs the reader to use %q, which was removed — "+
				"point it at %q instead", r.command, r.instead)
		}
		if !strings.Contains(body, r.instead) {
			t.Errorf("reliant.md.tmpl never mentions %q, so a reader has nothing to use "+
				"in place of the removed %q", r.instead, r.command)
		}
	}
}
