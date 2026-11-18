package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStubFixtureModule writes an arbitrary file set into a temp module rooted at
// example.com/proj and returns the absolute path of handlerRel.
//
// Distinct from writeTestModule (cross_pkg_interface_test.go), which is an
// OVERRIDE helper: it writes only the paths in its own defaults map, so a
// fixture that needs new files — as these tests do — has to write them itself.
func writeStubFixtureModule(t *testing.T, handlerRel string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	all := map[string]string{"go.mod": "module example.com/proj\n\ngo 1.22\n"}
	for k, v := range files {
		all[k] = v
	}
	for path, content := range all {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return filepath.Join(root, filepath.FromSlash(handlerRel))
}

// TestComputeAutoStubs_LocalInterfaceCarriesSignatureImports is the
// regression test for the control-plane `proxy_authz` build break.
//
// The shape: an interface declared LOCALLY in the handler package (so
// computeAutoStubs takes the local branch, not the cross-package one)
// whose METHOD SIGNATURES reference types from another package. The
// generated stub renders `func (stub) Authorize(ctx context.Context,
// in proxyauthz.Input) proxyauthz.Decision`, so the helper file must
// import `internal/proxyauthz` — otherwise the generated test helper
// does not compile (`undefined: proxyauthz`).
//
// Before the fix the local branch emitted no ExtraImports at all.
func TestComputeAutoStubs_LocalInterfaceCarriesSignatureImports(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/proxy_authz", map[string]string{
		"internal/proxyauthz/decider.go": `package proxyauthz

type Input struct{ Path string }
type Decision struct{ Allow bool }
`,
		"internal/handlers/proxy_authz/service.go": `package proxy_authz

import (
	"context"
	"log/slog"

	"example.com/proj/internal/proxyauthz"
)

// AccessDecider is LOCAL to this package, but its method signature
// reaches into another package for its parameter and result types.
type AccessDecider interface {
	Authorize(ctx context.Context, in proxyauthz.Input) proxyauthz.Decision
}

type Deps struct {
	Logger *slog.Logger
	Decider AccessDecider
}
`,
	})

	stubs, unresolved := computeAutoStubs(handlerDir, "proxy_authz")
	if len(unresolved) != 0 {
		t.Errorf("expected zero unresolved stubs, got %v", unresolved)
	}
	if len(stubs) != 1 {
		t.Fatalf("got %d stubs, want 1", len(stubs))
	}
	s := stubs[0]

	// The signature the template will render — this is what needs imports.
	if len(s.Methods) != 1 || !strings.Contains(s.Methods[0].Params, "proxyauthz.Input") {
		t.Fatalf("unexpected rendered methods: %+v", s.Methods)
	}

	var paths []string
	for _, ei := range s.ExtraImports {
		paths = append(paths, ei.Path)
	}
	if !containsPath(paths, "example.com/proj/internal/proxyauthz") {
		t.Errorf("local stub is missing the import its signature references; got %v", paths)
	}
	// `context` is referenced too, and must be offered so renderComponentTestHelpers
	// can decide (via filterAlreadyImported) whether the template already has it.
	if !containsPath(paths, "context") {
		t.Errorf("local stub is missing the context import; got %v", paths)
	}
	// Nothing the signature does NOT reference may be emitted — an unused
	// import is just as much a build failure as a missing one.
	if containsPath(paths, "log/slog") {
		t.Errorf("local stub emitted an unreferenced import (log/slog); got %v", paths)
	}
	// And never a self-import of the stub's own package.
	if containsPath(paths, "example.com/proj/internal/handlers/proxy_authz") {
		t.Errorf("local stub self-imported its own package; got %v", paths)
	}
}

// TestComputeAutoStubs_LocalInterfaceAliasedImport covers the aliased
// case: the rendered signature uses the ALIAS, so the emitted import
// line must declare that same alias or the stub won't compile.
func TestComputeAutoStubs_LocalInterfaceAliasedImport(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/billing", map[string]string{
		"internal/gen/userv1/user.go": `package userv1

type GetUserRequest struct{ ID string }
`,
		"internal/handlers/billing/service.go": `package billing

import (
	"log/slog"

	pb "example.com/proj/internal/gen/userv1"
)

type Fetcher interface {
	Fetch(req *pb.GetUserRequest) error
}

type Deps struct {
	Logger  *slog.Logger
	Fetcher Fetcher
}
`,
	})

	stubs, _ := computeAutoStubs(handlerDir, "billing")
	if len(stubs) != 1 {
		t.Fatalf("got %d stubs, want 1", len(stubs))
	}
	var found bool
	for _, ei := range stubs[0].ExtraImports {
		if ei.Path == "example.com/proj/internal/gen/userv1" {
			found = true
			if ei.Alias != "pb" {
				t.Errorf("aliased import emitted alias %q, want %q — the rendered "+
					"signature says `*pb.GetUserRequest`", ei.Alias, "pb")
			}
		}
	}
	if !found {
		t.Errorf("aliased import not emitted; got %v", stubs[0].ExtraImports)
	}
}

// TestComputeAutoStubs_LocalInterfaceNoImportsWhenBuiltinOnly guards the
// common case: an interface whose signatures use only builtins must not
// grow a spurious import block.
func TestComputeAutoStubs_LocalInterfaceNoImportsWhenBuiltinOnly(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/billing", map[string]string{
		"internal/handlers/billing/service.go": `package billing

import "log/slog"

type Repository interface {
	GetByID(id string) error
}

type Deps struct {
	Logger *slog.Logger
	Repo   Repository
}
`,
	})

	stubs, _ := computeAutoStubs(handlerDir, "billing")
	if len(stubs) != 1 {
		t.Fatalf("got %d stubs, want 1", len(stubs))
	}
	if len(stubs[0].ExtraImports) != 0 {
		t.Errorf("builtin-only signature should need no imports, got %v", stubs[0].ExtraImports)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
