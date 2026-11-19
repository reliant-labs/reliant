package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateCmdCommands pins the write-once contract of the
// user-owned extension point: first call scaffolds userCommands();
// later calls never overwrite.
func TestGenerateCmdCommands(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateCmdCommands(dir, "proj"); err != nil {
		t.Fatalf("GenerateCmdCommands: %v", err)
	}
	dest := filepath.Join(dir, "cmd", "proj", "cmd", "commands.go")
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read commands.go: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "func userCommands(deps Deps) []*cobra.Command {") {
		t.Errorf("commands.go missing the userCommands hook:\n%s", content)
	}
	if !strings.Contains(content, "yours: scaffolded once") {
		t.Error("commands.go missing the user-owned banner")
	}
	fset := token.NewFileSet()
	if _, perr := parser.ParseFile(fset, "commands.go", content, parser.AllErrors); perr != nil {
		t.Fatalf("commands.go does not parse: %v\n%s", perr, content)
	}

	// Write-once: a user edit survives regeneration.
	sentinel := []byte("package main\n\n// user edit sentinel\n")
	if err := os.WriteFile(dest, sentinel, 0644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateCmdCommands(dir, "proj"); err != nil {
		t.Fatalf("GenerateCmdCommands (second call): %v", err)
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(sentinel) {
		t.Error("GenerateCmdCommands overwrote a user-owned cmd/<bin>/cmd/commands.go")
	}
}
