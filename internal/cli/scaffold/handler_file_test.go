// File: internal/cli/scaffold/handler_file_test.go
//
// Tests for `forge scaffold handler-file <svc> <name>`. They exercise:
//
//   - happy path: writes a stub file, prints the right hints
//   - rejection: handler directory does not exist
//   - rejection: target file already exists (don't clobber)
//   - .go suffix on <name> is tolerated (stripped)
//   - subcommand is registered on `forge scaffold`

package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalServiceForgeYAML is a project-global forge.yaml: it carries no
// `kind:` field, because kind is read off the project's real sources. Callers
// that need the service kind pair it with markServiceProject, which stamps the
// pkg/app composition root so requireServiceKind passes.
const minimalServiceForgeYAML = `name: testproj
module_path: example.com/testproj
version: 0.1.0
`

func TestRunAddHandlerFile_HappyPath(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	handlerDir := filepath.Join(dir, "internal", "handlers", "billing")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir handlers/billing: %v", err)
	}

	if err := runHandlerFile("billing", "payment_methods"); err != nil {
		t.Fatalf("runHandlerFile: %v", err)
	}

	want := filepath.Join(handlerDir, "payment_methods.go")
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read scaffolded file: %v", err)
	}
	content := string(body)

	if !strings.HasPrefix(content, "package billing\n") {
		t.Errorf("scaffolded file should declare `package billing`, got prefix:\n%s", content[:40])
	}
	if !strings.Contains(content, "Add RPC method implementations here.") {
		t.Error("scaffolded file should carry the marker comment")
	}
	if !strings.Contains(content, "mock_gen.go") {
		t.Error("scaffolded file should mention mock_gen.go discovery convention")
	}
}

func TestRunAddHandlerFile_MissingHandlerDir(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	// No handlers/billing dir on disk.

	err := runHandlerFile("billing", "extras")
	if err == nil {
		t.Fatal("expected error when handler directory is missing, got nil")
	}
	if !strings.Contains(err.Error(), "no handler directory") {
		t.Errorf("error should explain the missing directory, got: %v", err)
	}
}

func TestRunAddHandlerFile_FileAlreadyExists(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	handlerDir := filepath.Join(dir, "internal", "handlers", "billing")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir handlers/billing: %v", err)
	}
	existing := filepath.Join(handlerDir, "extras.go")
	if err := os.WriteFile(existing, []byte("package billing\n// user-edited\n"), 0o644); err != nil {
		t.Fatalf("seed existing file: %v", err)
	}

	err := runHandlerFile("billing", "extras")
	if err == nil {
		t.Fatal("expected error when target file already exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should explain the conflict, got: %v", err)
	}

	// Critically: the existing user-edited file must NOT have been overwritten.
	body, _ := os.ReadFile(existing)
	if !strings.Contains(string(body), "user-edited") {
		t.Error("existing file was clobbered; refusal path is broken")
	}
}

func TestRunAddHandlerFile_TolerateGoSuffix(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	handlerDir := filepath.Join(dir, "internal", "handlers", "billing")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir handlers/billing: %v", err)
	}

	// The user types `extras.go` even though the subcommand owns the
	// extension. We strip it cleanly so we don't end up with extras.go.go.
	if err := runHandlerFile("billing", "extras.go"); err != nil {
		t.Fatalf("runHandlerFile: %v", err)
	}

	if _, err := os.Stat(filepath.Join(handlerDir, "extras.go")); err != nil {
		t.Errorf("expected extras.go to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(handlerDir, "extras.go.go")); err == nil {
		t.Error("found extras.go.go — the .go suffix strip is broken")
	}
}

// TestAddHandlerFileSubcommandRegistered confirms the subcommand is
// actually wired into `forge scaffold`. Without this the run function
// could exist but never be reachable from the CLI.
func TestHandlerFileSubcommandRegistered(t *testing.T) {
	root := newScaffoldCmd(testFactory())
	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "handler-file" {
			found = true
			if sub.Args == nil {
				t.Error("handler-file subcommand should declare cobra.ExactArgs(2)")
			}
			break
		}
	}
	if !found {
		t.Fatal("handler-file subcommand not registered on `forge scaffold`")
	}

	// And the parent's Long string should advertise it so users
	// reading `forge scaffold --help` discover the new verb.
	if !strings.Contains(root.Long, "handler-file") {
		t.Error("`forge scaffold --help` Long string should advertise the handler-file subcommand")
	}
}

// TestBuildHandlerFileStub directly exercises the pure helper so a
// regression in the stub format surfaces without filesystem I/O.
func TestBuildHandlerFileStub(t *testing.T) {
	got := buildHandlerFileStub("billing", "payment_methods")
	if !strings.HasPrefix(got, "package billing\n") {
		t.Errorf("stub should start with `package billing\\n`, got:\n%s", got)
	}
	if !strings.Contains(got, "payment_methods.go") {
		t.Errorf("stub should reference its own filename, got:\n%s", got)
	}
	if !strings.Contains(got, "mock_gen.go") {
		t.Errorf("stub should explain the mock_gen.go convention, got:\n%s", got)
	}
}
