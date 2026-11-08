package codegen

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/templates"
)

// GenerateCmdCommands scaffolds cmd/<bin>/cmd/commands.go — the user-owned
// cobra extension point newRootCmd consumes (userCommands(deps)). Written
// ONCE; never overwritten (Tier-2: the user owns the file the moment it
// exists). Second binaries register here as code with opt-in serverkit
// pieces instead of a parallel hand-rolled main().
//
// bin is the primary binary name (the cmd/<bin>/cmd directory leaf). The
// template references it for accurate doc paths and the {{.Name}} display.
func GenerateCmdCommands(targetDir, bin string) error {
	cmdDir := filepath.Join(targetDir, "cmd", bin, "cmd")
	dest := filepath.Join(cmdDir, "commands.go")
	rel := filepath.Join("cmd", bin, "cmd", "commands.go")

	// Written once and never again — this is user-owned code, and that
	// covers deleting it as much as editing it. A project with no extra
	// cobra commands can remove this file and it stays removed.
	if !checksums.ScaffoldOnceDecision(targetDir, rel) {
		return nil
	}

	if err := os.MkdirAll(cmdDir, 0755); err != nil {
		return err
	}

	content, err := templates.ProjectTemplates().Render("cmd-tree-commands.go.tmpl", struct{ Name string }{Name: bin})
	if err != nil {
		return fmt.Errorf("render cmd-tree-commands.go.tmpl: %w", err)
	}

	if err := writeUserScaffold(dest, content); err != nil {
		return err
	}
	checksums.RecordScaffold(targetDir, rel)
	return nil
}
