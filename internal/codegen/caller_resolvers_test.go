package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeclaredCallerResolvers(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/app\n\ngo 1.24\n")
	// The import path follows the directory; the package clause may differ.
	write("internal/authn/context.go", `package auth

import "context"

// UserID returns the caller.
//
//forge:resolves-caller
func UserID(ctx context.Context) string { return "" }

// Email returns the caller's email.
// forge:resolves-caller
func Email(ctx context.Context) string { return "" }

// Prose that mentions forge:resolves-caller mid-sentence is not a declaration.
func Other(ctx context.Context) string { return "" }

type S struct{}

//forge:resolves-caller
func (S) Method(ctx context.Context) string { return "" }
`)
	// Tests, hidden trees and nested modules declare nothing for this module.
	write("internal/authn/context_test.go", "package auth\n\n//forge:resolves-caller\nfunc InTest() {}\n")
	write(".hidden/x.go", "package x\n\n//forge:resolves-caller\nfunc Hidden() {}\n")
	write("tools/go.mod", "module example.com/tools\n")
	write("tools/x.go", "package tools\n\n//forge:resolves-caller\nfunc Nested() {}\n")

	got, ignored := DeclaredCallerResolvers(dir)
	if len(got) != 2 {
		t.Fatalf("resolvers = %+v, want Email and UserID", got)
	}
	for i, want := range []string{"Email", "UserID"} {
		r := got[i]
		if r.Func != want || r.ImportPath != "example.com/app/internal/authn" || r.Package != "auth" || r.File != "internal/authn/context.go" {
			t.Errorf("resolver %d = %+v, want %s in example.com/app/internal/authn (package auth)", i, r, want)
		}
	}
	if got[0].String() != "auth.Email" {
		t.Errorf("String() = %q, want auth.Email", got[0].String())
	}
	if len(ignored) != 1 || ignored[0].File != "internal/authn/context.go" {
		t.Errorf("the marker on a method must be reported as ignored, got %+v", ignored)
	}
}
