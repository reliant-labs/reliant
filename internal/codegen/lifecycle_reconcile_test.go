package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// The regression these tests pin: `forge scaffold worker nightly` scaffolds
// cmd/<bin>/cmd/workers/nightly.go, which calls c.WorkerNightly() — an
// accessor that lives in internal/app/lifecycle.go. lifecycle.go was
// scaffold-once with NO reconciler, so nobody ever wrote the accessor and the
// tree stopped compiling:
//
//	cmd/svcdemo/cmd/workers/nightly.go:40:87: c.WorkerNightly undefined
//
// GenerateLifecycle now reconciles the owned file additively against the
// discovered supervised set, the same way GenerateCompose already did for
// services.

func readLifecycle(t *testing.T, projectDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, "internal", "app", "lifecycle.go"))
	if err != nil {
		t.Fatalf("read lifecycle.go: %v", err)
	}
	return string(data)
}

func lifecycleInput(dir string, workers []BootstrapWorkerData, operators []BootstrapOperatorData) InjectGenInput {
	return InjectGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Workers:    workers,
		Operators:  operators,
	}
}

func mustGenerateLifecycle(t *testing.T, in InjectGenInput) {
	t.Helper()
	if err := GenerateLifecycle(in); err != nil {
		t.Fatalf("GenerateLifecycle: %v", err)
	}
}

// assertValidGo fails unless src parses AND names every package it imports.
// An unused import is a compile error, not a style nit: a lifecycle.go that
// imported lifecyclekit with no worker to wrap failed every fresh project's
// first generate ("lifecyclekit imported and not used"), while a parse-only
// check passed it.
func assertValidGo(t *testing.T, name, src string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s is not valid Go: %v\n----\n%s", name, err, src)
	}
	used := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		// The package name is the alias when there is one, else the last path
		// element: true of every import these files carry (the hyphenated
		// controller-runtime is always aliased).
		pkg := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			pkg = imp.Name.Name
		}
		if pkg == "_" || pkg == "." {
			continue
		}
		if !used[pkg] {
			t.Fatalf("%s imports %q and never uses it (a compile error):\n----\n%s", name, path, src)
		}
	}
}

// TestGenerateLifecycle_ReconcileAddsWorkerAfterScaffold walks the exact
// sequence the bug report describes: scaffold a zero-worker project, then add
// a worker, then regenerate.
func TestGenerateLifecycle_ReconcileAddsWorkerAfterScaffold(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")

	// First emit: zero supervised components (a fresh `forge project new`).
	mustGenerateLifecycle(t, lifecycleInput(dir, nil, nil))
	first := readLifecycle(t, dir)
	if !strings.Contains(first, "func (c *Components) AllWorkers() []serverkit.Worker") {
		t.Fatalf("scaffold emit missing AllWorkers:\n%s", first)
	}
	if strings.Contains(first, "WorkerNightly") {
		t.Fatalf("scaffold emit must not carry a worker yet:\n%s", first)
	}
	assertValidGo(t, "lifecycle.go", first)

	// `forge scaffold worker nightly` — the worker package appears on disk.
	writeComponentDeps(t, dir, "internal/workers", "nightly", "nightly",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	workers, err := WorkerDataFromSpecs(DiscoverWorkerSpecs(dir), dir)
	if err != nil {
		t.Fatalf("DiscoverWorkerSpecs: %v", err)
	}
	if len(workers) != 1 || workers[0].Name != "nightly" {
		t.Fatalf("discovery did not find the worker: %+v", workers)
	}
	mustGenerateLifecycle(t, lifecycleInput(dir, workers, nil))
	out := readLifecycle(t, dir)

	if !strings.Contains(out, "func (c *Components) WorkerNightly() serverkit.Worker") {
		t.Fatalf("reconcile did not add the WorkerNightly accessor:\n%s", out)
	}
	if !strings.Contains(out, `lifecyclekit.WrapWorker("nightly", c.Nightly)`) {
		t.Fatalf("accessor does not weld the label to the typed field:\n%s", out)
	}
	if !strings.Contains(out, "c.WorkerNightly(),") {
		t.Fatalf("reconcile did not add the worker to AllWorkers:\n%s", out)
	}
	assertValidGo(t, "lifecycle.go", out)

	// Idempotent: the same discovered set must produce byte-identical output.
	mustGenerateLifecycle(t, lifecycleInput(dir, workers, nil))
	if again := readLifecycle(t, dir); again != out {
		t.Fatalf("reconcile is not idempotent; second run diverged:\n%s", again)
	}
}

// TestGenerateLifecycle_ReconcileAddsOperatorAfterScaffold is the operator-side
// analog: the accessor, its package import, and the AllOperators entry.
func TestGenerateLifecycle_ReconcileAddsOperatorAfterScaffold(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")
	mustGenerateLifecycle(t, lifecycleInput(dir, nil, nil))

	writeComponentDeps(t, dir, "internal/operators", "fleet", "fleet",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	operators, err := OperatorDataFromSpecs(DiscoverOperatorSpecs(dir), dir)
	if err != nil {
		t.Fatalf("DiscoverOperatorSpecs: %v", err)
	}
	mustGenerateLifecycle(t, lifecycleInput(dir, nil, operators))
	out := readLifecycle(t, dir)

	if !strings.Contains(out, "func (c *Components) OperatorFleet() OperatorEntry") {
		t.Fatalf("reconcile did not add the OperatorFleet accessor:\n%s", out)
	}
	if !strings.Contains(out, `"example.com/proj/internal/operators/fleet"`) {
		t.Fatalf("reconcile did not add the operator package import:\n%s", out)
	}
	if !strings.Contains(out, "c.OperatorFleet(),") {
		t.Fatalf("reconcile did not add the operator to AllOperators:\n%s", out)
	}
	assertValidGo(t, "lifecycle.go", out)

	mustGenerateLifecycle(t, lifecycleInput(dir, nil, operators))
	if again := readLifecycle(t, dir); again != out {
		t.Fatalf("reconcile is not idempotent; second run diverged:\n%s", again)
	}
}

// TestGenerateLifecycle_ReconcilePreservesCustomization: a project that has
// hand-edited the owned parts of lifecycle.go (the OperatorEntry shape and the
// RunOperators body — exactly what the real downstream control-plane does)
// still gets the new accessor, and keeps every one of its edits.
func TestGenerateLifecycle_ReconcilePreservesCustomization(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")
	mustGenerateLifecycle(t, lifecycleInput(dir, nil, nil))

	path := filepath.Join(dir, "internal", "app", "lifecycle.go")
	src := readLifecycle(t, dir)
	src = strings.Replace(src,
		"type OperatorEntry struct {\n\tName             string",
		"type OperatorEntry struct {\n\t// HandOwned is per-app policy forge cannot derive.\n\tHandOwned        bool\n\tName             string", 1)
	src = strings.Replace(src,
		"func RunOperators(ctx context.Context",
		"// RunOperators is HAND-CUSTOMIZED for this app.\nfunc RunOperators(ctx context.Context", 1)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	writeComponentDeps(t, dir, "internal/workers", "sweeper", "sweeper",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	workers, err := WorkerDataFromSpecs(DiscoverWorkerSpecs(dir), dir)
	if err != nil {
		t.Fatalf("DiscoverWorkerSpecs: %v", err)
	}
	mustGenerateLifecycle(t, lifecycleInput(dir, workers, nil))
	out := readLifecycle(t, dir)

	for _, want := range []string{
		"HandOwned        bool",
		"// RunOperators is HAND-CUSTOMIZED for this app.",
		"func (c *Components) WorkerSweeper() serverkit.Worker",
		"c.WorkerSweeper(),",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("reconciled lifecycle.go missing %q:\n%s", want, out)
		}
	}
	assertValidGo(t, "lifecycle.go", out)
}

// TestGenerateLifecycle_ReconcileLeavesCustomAggregateAlone: a project whose
// AllWorkers body is no longer the shape forge emits keeps its body verbatim —
// WHICH workers the aggregate reports is the user's decision — but the
// accessor still lands, so the per-worker subcommand compiles.
func TestGenerateLifecycle_ReconcileLeavesCustomAggregateAlone(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")
	mustGenerateLifecycle(t, lifecycleInput(dir, nil, nil))

	path := filepath.Join(dir, "internal", "app", "lifecycle.go")
	src := readLifecycle(t, dir)
	src = strings.Replace(src,
		"func (c *Components) AllWorkers() []serverkit.Worker {\n\t_ = c\n\treturn nil\n}",
		"func (c *Components) AllWorkers() []serverkit.Worker {\n\t// this project supervises workers per-command, never in bulk\n\tpanic(\"use the per-worker accessors\")\n}", 1)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	writeComponentDeps(t, dir, "internal/workers", "sweeper", "sweeper",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	workers, err := WorkerDataFromSpecs(DiscoverWorkerSpecs(dir), dir)
	if err != nil {
		t.Fatalf("DiscoverWorkerSpecs: %v", err)
	}
	mustGenerateLifecycle(t, lifecycleInput(dir, workers, nil))
	out := readLifecycle(t, dir)

	if !strings.Contains(out, `panic("use the per-worker accessors")`) {
		t.Errorf("a hand-rewritten AllWorkers body must be left alone:\n%s", out)
	}
	if !strings.Contains(out, "func (c *Components) WorkerSweeper() serverkit.Worker") {
		t.Errorf("the accessor must still land so the subcommand compiles:\n%s", out)
	}
	assertValidGo(t, "lifecycle.go", out)
}

// TestGenerateLifecycle_DisownedIsNeverTouched: `forge project disown` is a
// one-way transfer of the bytes. Not even the additive path may write.
func TestGenerateLifecycle_DisownedIsNeverTouched(t *testing.T) {
	dir := newInjectProject(t)
	writeInfra(t, dir, "\tLog *slog.Logger\n\tCfg *config.Config")
	mustGenerateLifecycle(t, lifecycleInput(dir, nil, nil))

	before := readLifecycle(t, dir)
	cs := &checksums.FileChecksums{}
	if err := cs.DisownPaths(dir, []string{"internal/app/lifecycle.go"}, "owned by this app"); err != nil {
		t.Fatalf("DisownPaths: %v", err)
	}

	writeComponentDeps(t, dir, "internal/workers", "sweeper", "sweeper",
		"\tLogger *slog.Logger\n\tConfig *config.Config")
	workers, err := WorkerDataFromSpecs(DiscoverWorkerSpecs(dir), dir)
	if err != nil {
		t.Fatalf("DiscoverWorkerSpecs: %v", err)
	}
	in := lifecycleInput(dir, workers, nil)
	in.Checksums = cs
	mustGenerateLifecycle(t, in)

	if after := readLifecycle(t, dir); after != before {
		t.Fatalf("disowned lifecycle.go was modified:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestGenerateCompose_DisownedIsNotEvenResolved: a disowned compose.go must
// not fail the generate run over wiring forge would never write.
//
// The owner disowns compose.go precisely BECAUSE the construction is bespoke —
// deps forge cannot resolve by type, wired by hand off Infra. Resolving them
// anyway and returning a MissingProvider error would break `forge generate`
// for exactly the projects that took ownership to avoid it.
func TestGenerateCompose_DisownedIsNotEvenResolved(t *testing.T) {
	dir := newInjectProject(t)
	// A worker whose collaborator has no producer and no Infra field: the
	// shape that raises MissingProvider.
	writeComponentDeps(t, dir, "internal/workers", "sweeper", "sweeper", "\tStripe StripeClient")
	appendType(t, dir, "internal/workers/sweeper", "type StripeClient interface{ Charge() }")
	workers, err := WorkerDataFromSpecs(DiscoverWorkerSpecs(dir), dir)
	if err != nil {
		t.Fatalf("DiscoverWorkerSpecs: %v", err)
	}
	in := lifecycleInput(dir, workers, nil)

	// Sanity: without a disown record this IS the loud error.
	if err := GenerateCompose(in); err == nil {
		t.Fatalf("expected a MissingProvider error for an unresolvable worker dep")
	}

	// Hand-owned bytes + a disown record: forge computes nothing and writes
	// nothing.
	appDir := filepath.Join(dir, "internal", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(appDir, "compose.go")
	owned := "package app\n\n// hand-owned composition.\ntype Components struct{}\n\nfunc NewComponents(infra *Infra) (*Components, error) { return &Components{}, nil }\n"
	if err := os.WriteFile(composePath, []byte(owned), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := &checksums.FileChecksums{}
	if err := cs.DisownPaths(dir, []string{"internal/app/compose.go"}, "bespoke construction"); err != nil {
		t.Fatalf("DisownPaths: %v", err)
	}
	in.Checksums = cs
	if err := GenerateCompose(in); err != nil {
		t.Fatalf("disowned compose.go must not fail the run: %v", err)
	}
	got, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != owned {
		t.Fatalf("disowned compose.go was modified:\n%s", got)
	}
}

// TestDiscoverSupervisedSpecs_SkipsNonPackages: discovery reads real Go
// packages, not every directory that happens to sit under the role root.
func TestDiscoverSupervisedSpecs_SkipsNonPackages(t *testing.T) {
	dir := t.TempDir()
	writeComponentDeps(t, dir, "internal/workers", "real", "real", "\tLogger *slog.Logger")
	// A dir with only a test file, and a dir with no Go at all.
	for _, leaf := range []string{"testonly", "empty"} {
		if err := os.MkdirAll(filepath.Join(dir, "internal", "workers", leaf), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "workers", "testonly", "x_test.go"),
		[]byte("package testonly\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := DiscoverWorkerSpecs(dir)
	if len(got) != 1 || got[0].Name != "real" {
		t.Fatalf("DiscoverWorkerSpecs = %+v, want just {real}", got)
	}
	if ops := DiscoverOperatorSpecs(dir); len(ops) != 0 {
		t.Fatalf("DiscoverOperatorSpecs on a project with no operators = %+v, want none", ops)
	}
}
