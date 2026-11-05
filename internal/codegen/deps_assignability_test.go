// File: internal/codegen/deps_assignability_test.go
//
// Tests for the type-aware Deps→AppExtras matcher. The matcher
// underpins both Matcher A (inspectComponentDepsShape) and Matcher B
// (wireExpressionForApp); the test cases here cover the matcher's own
// classification surface so the wiring callers stay thin.

package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMatcherProject scaffolds the minimum go.mod + pkg/app +
// internal/<svc>/ shape the matcher loads. Returns the project root.
func writeMatcherProject(t *testing.T, appSrc, depsSrc, role, pkg string) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), `module example.com/proj

go 1.23
`)
	mustWrite(t, filepath.Join(dir, "pkg", "app", "app.go"), appSrc)
	mustWrite(t, filepath.Join(dir, role, pkg, "contract.go"), depsSrc)
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDepsAssignability_NarrowInterfaceWideConcrete covers the
// silent-drop class. AppExtras.Repo is a concrete pointer that
// implements the narrow interface Deps.Repo wants. Pre-matcher
// behavior dropped the wire (different type strings); the matcher
// should report MatchAssignable so bootstrap emits `Repo: app.Repo`.
func TestDepsAssignability_NarrowInterfaceWideConcrete(t *testing.T) {
	dir := writeMatcherProject(t,
		`package app

type Repo struct{}

func (*Repo) Log() {}

type AppExtras struct {
	RepoField *Repo
}

type App struct {
	*AppExtras
}
`,
		`package audit

type Logger interface{ Log() }

type Deps struct {
	RepoField Logger
}
`,
		"internal", "audit",
	)

	m := NewDepsAssignabilityMatcher(dir)
	// Strings differ — *Repo vs Logger — so the matcher must load
	// types to answer. Expect MatchAssignable since *Repo implements
	// Logger.
	kind := m.Match("internal", "audit", "RepoField", "Logger", "*Repo", true)
	if kind != MatchAssignable {
		t.Fatalf("Match = %v, want MatchAssignable", kind)
	}
}

// TestDepsAssignability_NameCollisionWrongType covers Matcher B's
// compile-error class. AppExtras.Foo is *foo.X but Deps.Foo wants
// *bar.Y. Same name, unrelated types. Pre-matcher Matcher B emitted
// `app.Foo` blindly → compile error. Expect MatchNameMismatch so the
// caller emits a typed-zero + loud UNRESOLVED hint instead.
func TestDepsAssignability_NameCollisionWrongType(t *testing.T) {
	dir := writeMatcherProject(t,
		`package app

type X struct{}

type AppExtras struct {
	Foo *X
}

type App struct {
	*AppExtras
}
`,
		`package audit

type Y struct{}

type Deps struct {
	Foo *Y
}
`,
		"internal", "audit",
	)

	m := NewDepsAssignabilityMatcher(dir)
	kind := m.Match("internal", "audit", "Foo", "*Y", "*X", true)
	if kind != MatchNameMismatch {
		t.Fatalf("Match = %v, want MatchNameMismatch", kind)
	}
}

// TestDepsAssignability_NoNameMatch — AppExtras has no field named
// Foo at all. The matcher must report MatchNoName so the
// optional-dep / typed-zero fallback applies.
func TestDepsAssignability_NoNameMatch(t *testing.T) {
	m := NewDepsAssignabilityMatcher(t.TempDir())
	kind := m.Match("internal", "audit", "Foo", "*Y", "", false)
	if kind != MatchNoName {
		t.Fatalf("Match = %v, want MatchNoName", kind)
	}
}

// TestDepsAssignability_ExactStringFastPath — when the pretty-printed
// strings agree byte-for-byte, the matcher short-circuits without a
// types load. This is the common case (bare-Deps trio + every
// well-typed user extension) and must stay fast.
func TestDepsAssignability_ExactStringFastPath(t *testing.T) {
	m := NewDepsAssignabilityMatcher(t.TempDir())
	kind := m.Match("internal", "audit", "Logger", "*slog.Logger", "*slog.Logger", true)
	if kind != MatchExactString {
		t.Fatalf("Match = %v, want MatchExactString (no load required)", kind)
	}
}

// TestDepsAssignability_UnavailableWhenProjectMissing — pkg/app
// doesn't exist; the matcher must degrade to MatchUnavailable so
// callers can fall back to the legacy string compare. Codegen on a
// just-scaffolded project (no pkg/app yet) must still succeed.
func TestDepsAssignability_UnavailableWhenProjectMissing(t *testing.T) {
	m := NewDepsAssignabilityMatcher(t.TempDir())
	// Strings differ AND no pkg/app — matcher must signal Unavailable.
	kind := m.Match("internal", "audit", "Repo", "Repository", "*PostgresRepo", true)
	if kind != MatchUnavailable {
		t.Fatalf("Match = %v, want MatchUnavailable (no pkg/app loadable)", kind)
	}
}

// TestInspectComponentDepsShape_NarrowInterfaceWiresAssignable is the
// end-to-end test for Matcher A's promotion to assignability. Before
// the matcher: type strings differ → silent drop → component
// constructs with a nil dep. After: matcher confirms assignability →
// AppFieldRefs gets the entry → bootstrap emits `Repo: app.Repo`.
func TestInspectComponentDepsShape_NarrowInterfaceWiresAssignable(t *testing.T) {
	dir := writeMatcherProject(t,
		`package app

type PostgresRepo struct{}

func (*PostgresRepo) Save() {}

type AppExtras struct {
	Repo *PostgresRepo
}

type App struct {
	*AppExtras
}
`,
		`package audit

type Saver interface{ Save() }

type Deps struct {
	Repo Saver
}
`,
		"internal", "audit",
	)
	components := []BootstrapComponentData{
		{Name: "audit", Package: "audit", ImportPath: "audit"},
	}
	inspectComponentDepsShape(components, dir, "internal")
	if len(components[0].AppFieldRefs) != 1 {
		t.Fatalf("AppFieldRefs = %+v, want 1 entry (narrow-interface assignability)", components[0].AppFieldRefs)
	}
	if components[0].AppFieldRefs[0].DepsField != "Repo" {
		t.Errorf("AppFieldRef DepsField = %q, want %q", components[0].AppFieldRefs[0].DepsField, "Repo")
	}
}

// TestInspectComponentDepsShape_NameCollisionStaysSilent — Matcher A
// must still skip the wire when name matches but the types are
// unrelated. The lint (post-codegen) reports the gap; the codegen
// itself must not emit `Repo: app.Repo` with incompatible types,
// since that would fail the Go build.
func TestInspectComponentDepsShape_NameCollisionStaysSilent(t *testing.T) {
	dir := writeMatcherProject(t,
		`package app

type X struct{}

type AppExtras struct {
	Repo *X
}

type App struct {
	*AppExtras
}
`,
		`package audit

type Y struct{}

type Deps struct {
	Repo *Y
}
`,
		"internal", "audit",
	)
	components := []BootstrapComponentData{
		{Name: "audit", Package: "audit", ImportPath: "audit"},
	}
	inspectComponentDepsShape(components, dir, "internal")
	if len(components[0].AppFieldRefs) != 0 {
		t.Fatalf("AppFieldRefs = %+v, want 0 (name collision, types unrelated — lint reports)", components[0].AppFieldRefs)
	}
}

// --- kalshi-trader FORGE_BACKLOG #13 regressions -------------------------
//
// The original matcher loaded pkg/app and the component package in
// SEPARATE packages.Load calls. go/types identity is pointer identity,
// so the SAME named type (or the context.Context in a method signature)
// loaded through two universes is never Identical → AssignableTo /
// Implements returned false for types that are literally identical in
// source. Worse, the failure was nondeterministic: when pkg/app
// transiently didn't type-check (structural regen mid-pipeline) the
// matcher degraded to Unavailable and the legacy name-match wired
// `app.<Field>`, while the next steady-state regen flipped the same
// field to nil — silently un-wiring production collaborators.

// settlementWorkerSrc declares a worker-local interface whose method
// signature drags in a cross-package named type (context.Context) —
// the exact shape that defeated the two-universe AssignableTo.
const settlementWorkerSrc = `package settlement_processor

import "context"

// UnsettledSource feeds the settlement loop.
type UnsettledSource interface {
	Pending(ctx context.Context) ([]string, error)
}

type Deps struct {
	// Unsettled drives the Brier-score loop.
	// forge:optional-dep
	Unsettled UnsettledSource
}
`

// TestDepsAssignability_WorkerLocalInterfaceSharedUniverse covers the
// identity case: AppExtras.Unsettled IS the worker's own named
// interface type (settlement_processor.UnsettledSource). Trivially
// assignable in source; the two-universe matcher reported
// MatchNameMismatch because the two loads produced distinct
// *types.Named. Must be MatchAssignable.
func TestDepsAssignability_WorkerLocalInterfaceSharedUniverse(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), `module example.com/proj

go 1.23
`)
	mustWrite(t, filepath.Join(dir, "internal", "workers", "settlement_processor", "contract.go"), settlementWorkerSrc)
	mustWrite(t, filepath.Join(dir, "pkg", "app", "app.go"), `package app

import "example.com/proj/internal/workers/settlement_processor"

type AppExtras struct {
	Unsettled settlement_processor.UnsettledSource
}

type App struct {
	*AppExtras
}
`)

	m := NewDepsAssignabilityMatcher(dir)
	kind := m.Match("internal/workers", "settlement_processor", "Unsettled",
		"UnsettledSource", "settlement_processor.UnsettledSource", true)
	if kind != MatchAssignable {
		t.Fatalf("Match = %v, want MatchAssignable (same named type, one universe)", kind)
	}
}

// TestDepsAssignability_CrossPackageAdapterImplementsWorkerInterface
// covers the reporter's adapter shape: AppExtras holds a CONCRETE
// adapter type from a third package that satisfies the worker-local
// interface. types.Implements must hold because both sides (and the
// context.Context in the method signature) come from one universe.
func TestDepsAssignability_CrossPackageAdapterImplementsWorkerInterface(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks or builds fixture modules via the go toolchain; runs in task test")
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), `module example.com/proj

go 1.23
`)
	mustWrite(t, filepath.Join(dir, "internal", "workers", "settlement_processor", "contract.go"), settlementWorkerSrc)
	mustWrite(t, filepath.Join(dir, "internal", "unsettledadapter", "adapter.go"), `package unsettledadapter

import "context"

// Adapter satisfies settlement_processor.UnsettledSource.
type Adapter struct{}

func (*Adapter) Pending(ctx context.Context) ([]string, error) { return nil, nil }
`)
	mustWrite(t, filepath.Join(dir, "pkg", "app", "app.go"), `package app

import "example.com/proj/internal/unsettledadapter"

type AppExtras struct {
	Unsettled *unsettledadapter.Adapter
}

type App struct {
	*AppExtras
}
`)

	m := NewDepsAssignabilityMatcher(dir)
	kind := m.Match("internal/workers", "settlement_processor", "Unsettled",
		"UnsettledSource", "*unsettledadapter.Adapter", true)
	if kind != MatchAssignable {
		t.Fatalf("Match = %v, want MatchAssignable (adapter implements worker-local interface)", kind)
	}
}

// TestMatchedAppField_UnavailableWiresNameMatch pins Matcher A's side
// of the deterministic fail-loud policy: when assignability is
// unprovable (here: no loadable pkg/app at all), a name match WIRES so
// bootstrap output can't diverge from wire_gen's on the same project
// state. The compiler arbitrates a wrong wire loudly; nil never does.
func TestMatchedAppField_UnavailableWiresNameMatch(t *testing.T) {
	m := NewDepsAssignabilityMatcher(t.TempDir())
	appByName := map[string]string{"Repo": "*PostgresRepo"}
	if !matchedAppField(m, "internal", "audit", "Repo", "Repository", appByName) {
		t.Fatal("unprovable assignability with a name match must wire (fail-loud policy)")
	}
	// No name match stays a hard no — the optional-dep invariant.
	if matchedAppField(m, "internal", "audit", "Sink", "EventSink", appByName) {
		t.Fatal("no name match must never wire")
	}
}
