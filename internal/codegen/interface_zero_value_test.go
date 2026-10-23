// File: internal/codegen/interface_zero_value_test.go
//
// Regression tests for the auto-stub zero-value emitter. A composite
// literal is legal Go ONLY for a struct; `T{}` where T is an interface
// does not compile. The generated helpers_gen_test.go is Tier-1
// regenerated with a forge:hash, so a user cannot hand-edit their way
// out of it — the generator has to be right.
//
// The trigger in the wild is every generated store: they all carry
// `WithTx(orm.Context) T`, a method whose result is the interface
// itself. So any db.*Store on a handler's Deps produced an
// uncompilable test helper, and switching to narrow per-entity stores
// did not dodge it. Both halves are pinned here: `nil` for an
// interface result, and a compiling zero for a named struct.
//
// The named-struct expectation is `*new(T)` rather than `T{}` since
// the emitter stopped guessing a literal per type family — see
// named_type_zero_value_test.go, which covers the rest of that family
// by type-checking the generated stub instead of matching its text.

package codegen

import (
	"strings"
	"testing"
)

// selfReturningStoreFixture mirrors the real shape: an imported
// package declaring an interface with a self-returning method
// (WithTx) alongside a named struct result (Stats), so one fixture
// pins both the fix and the behavior it must not regress.
func selfReturningStoreFixture(t *testing.T) string {
	t.Helper()
	return writeStubFixtureModule(t, "internal/handlers/crews", map[string]string{
		"internal/orm/orm.go": `package orm

// Context is the transaction handle WithTx takes.
type Context struct{ Tx string }
`,
		"internal/store/store.go": `package store

import "example.com/proj/internal/orm"

// Snapshot is a named STRUCT result — the case that must keep
// generating a composite literal.
type Snapshot struct{ N int }

// CrewStore is the generated-store shape: WithTx returns the
// interface itself.
type CrewStore interface {
	WithTx(ctx orm.Context) CrewStore
	Stats() Snapshot
}
`,
		"internal/handlers/crews/service.go": `package crews

import (
	"log/slog"

	"example.com/proj/internal/store"
)

type Deps struct {
	Logger *slog.Logger
	Crews  store.CrewStore
}
`,
	})
}

// TestResolveCrossPkgInterface_SelfReturningMethodEmitsNil is the
// regression for the uncompilable helpers_gen_test.go. `WithTx`
// returns the interface, so the stub body must `return nil`;
// `return store.CrewStore{}` is not valid Go.
func TestResolveCrossPkgInterface_SelfReturningMethodEmitsNil(t *testing.T) {
	handlerDir := selfReturningStoreFixture(t)

	res, ok := ResolveCrossPkgInterface(handlerDir, "store", "CrewStore")
	if !ok {
		t.Fatal("expected resolver to succeed for store.CrewStore")
	}

	byName := map[string]InterfaceMethod{}
	for _, m := range res.Methods {
		byName[m.Name] = m
	}

	withTx, ok := byName["WithTx"]
	if !ok {
		t.Fatalf("missing WithTx in resolved methods %v", res.Methods)
	}
	if withTx.ReturnStatement != "return nil" {
		t.Errorf("WithTx returns the INTERFACE, so the stub must emit nil.\n got: %q\nwant: %q\n(a composite literal is only legal for a struct — %q does not compile)",
			withTx.ReturnStatement, "return nil", "return store.CrewStore{}")
	}

	// The good case must survive: a named struct result still gets a
	// composite literal, which is both valid and the useful zero value.
	stats, ok := byName["Stats"]
	if !ok {
		t.Fatalf("missing Stats in resolved methods %v", res.Methods)
	}
	if stats.ReturnStatement != "return *new(store.Snapshot)" {
		t.Errorf("named STRUCT result should get the universal zero form.\n got: %q\nwant: %q",
			stats.ReturnStatement, "return *new(store.Snapshot)")
	}
}

// TestComputeAutoStubs_SelfReturningMethodEmitsNil pins the same fix
// one level up, at the emitter that actually feeds the template — the
// resolver being right is only useful if the stub carries it through.
func TestComputeAutoStubs_SelfReturningMethodEmitsNil(t *testing.T) {
	handlerDir := selfReturningStoreFixture(t)

	stubs, unresolved := computeAutoStubs(handlerDir, "")
	if len(unresolved) != 0 {
		t.Fatalf("store.CrewStore should resolve, got unresolved %v", unresolved)
	}
	if len(stubs) != 1 {
		t.Fatalf("expected exactly one auto-stub (Crews), got %d: %v", len(stubs), stubs)
	}
	for _, m := range stubs[0].Methods {
		if m.Name != "WithTx" {
			continue
		}
		if strings.Contains(m.ReturnStatement, "{}") {
			t.Errorf("auto-stub WithTx must not emit a composite literal for an interface: %q", m.ReturnStatement)
		}
	}
}

// TestParseLocalInterfaces_SelfReturningMethodEmitsNil covers the
// locally-declared half. ParseLocalInterfaces has only the AST, but it
// already knows every interface NAME declared in the package before it
// renders any method — which is exactly the fact needed here.
func TestParseLocalInterfaces_SelfReturningMethodEmitsNil(t *testing.T) {
	handlerDir := writeStubFixtureModule(t, "internal/handlers/crews", map[string]string{
		"internal/handlers/crews/service.go": `package crews

import "context"

// Stats is a named STRUCT result declared in the same package.
type Stats struct{ N int }

// Repository is declared LOCALLY and returns itself from WithTx.
type Repository interface {
	WithTx(ctx context.Context) Repository
	Snapshot() Stats
}

type Deps struct {
	Repo Repository
}
`,
	})

	locals, err := ParseLocalInterfaces(handlerDir)
	if err != nil {
		t.Fatalf("ParseLocalInterfaces: %v", err)
	}
	iface, ok := locals["Repository"]
	if !ok {
		t.Fatalf("Repository not parsed; got %v", locals)
	}

	byName := map[string]InterfaceMethod{}
	for _, m := range iface.Methods {
		byName[m.Name] = m
	}

	if got := byName["WithTx"].ReturnStatement; got != "return nil" {
		t.Errorf("locally-declared self-returning method must emit nil.\n got: %q\nwant: %q",
			got, "return nil")
	}
	if got := byName["Snapshot"].ReturnStatement; got != "return *new(Stats)" {
		t.Errorf("named STRUCT result should get the universal zero form.\n got: %q\nwant: %q",
			got, "return *new(Stats)")
	}
}
