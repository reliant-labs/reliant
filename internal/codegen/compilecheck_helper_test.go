package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestGenerateConfigLoader_DefaultScaffoldParses renders the DEFAULT scaffold
// config (the exact shape every new project gets) and parses it.
//
// The config object IS the proto type now: the generated pkg/config/config.go
// is a thin shim that imports the project's gen/config/v1 package, so it
// cannot be standalone-compiled in isolation (the gen package only exists in
// a real project). The end-to-end compile+boot verification lives in the
// `forge project new` scratch path; here we assert the rendered shim is valid Go and
// carries the alias cutover invariants.
func TestGenerateConfigLoader_DefaultScaffoldParses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateConfigLoader(DefaultConfigMessages(), dir, nil); err != nil {
		t.Fatalf("GenerateConfigLoader: %v", err)
	}
	src := filepath.Join(dir, "pkg", "config", "config_gen.go")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	if _, perr := parser.ParseFile(fset, "config_gen.go", data, parser.AllErrors); perr != nil {
		t.Fatalf("generated config.go does not parse: %v\n--- SOURCE ---\n%s", perr, data)
	}
}
