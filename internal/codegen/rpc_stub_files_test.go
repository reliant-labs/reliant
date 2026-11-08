// File: internal/codegen/rpc_stub_files_test.go
//
// One custom RPC, one handler file — the property that lets N authors
// implement N RPCs of the same service concurrently with nothing to merge.
//
// Forge already births ONE FILE PER RPC for the scaffold TESTS
// (handlers_scaffold_<rpc>_test.go, ScaffoldTestFileName) and for
// `forge scaffold rpc`'s not-yet-in-proto path (rpc_<rpc>.go). The
// generate pipeline's stub emitter was the last path that piled every
// RPC into one shared handlers.go, so every fan-out over a multi-RPC
// service had to hand-split it first.
//
// These assert the property through the shipped emitters, and type-check
// the result in-module so "one file per RPC" cannot be satisfied by
// files that do not compile on their own.

package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// threeCustomRPCs is the shape the defect was measured on: a service with
// several custom (non-CRUD) RPCs, each of which a different author owns.
// One streaming shape is included so the split covers more than the unary
// template branch.
func threeCustomRPCs() ServiceDef {
	return ServiceDef{
		Name:       "OrdersService",
		Package:    "orders.v1",
		GoPackage:  "example.com/app/gen/services/orders/v1",
		PkgName:    "ordersv1",
		ProtoFile:  "proto/services/orders/v1/orders.proto",
		ModulePath: "example.com/app",
		Methods: []Method{
			{Name: "SubmitOrder", InputType: "SubmitOrderRequest", OutputType: "SubmitOrderResponse"},
			{Name: "CancelOrder", InputType: "CancelOrderRequest", OutputType: "CancelOrderResponse"},
			{Name: "TailOrderEvents", InputType: "TailOrderEventsRequest", OutputType: "TailOrderEventsResponse", ServerStreaming: true},
		},
	}
}

// handlerStubFilesByMethod parses every non-test, non-_gen .go file in dir
// and returns method name → the file that declares it, for *Service methods
// only. Files are keyed by bare name so the assertions read as a layout.
func handlerStubFilesByMethod(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		file, perr := parser.ParseFile(fset, name, src, parser.ParseComments)
		if perr != nil {
			t.Fatalf("emitted %s does not parse: %v\n%s", name, perr, src)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name == nil || !receiverIsService(fd) {
				continue
			}
			if prev, dup := out[fd.Name.Name]; dup {
				t.Fatalf("method %s declared in BOTH %s and %s", fd.Name.Name, prev, name)
			}
			out[fd.Name.Name] = name
		}
	}
	return out
}

// assertOneFilePerRPC is the whole property, asserted the same way for both
// emitter paths: every RPC lands in its own rpc_<snake>.go, that file holds
// that RPC and nothing else, and the unwired-stub marker survives the split
// (it is the machine-readable handoff the next phase queries).
func assertOneFilePerRPC(t *testing.T, dir string, svc ServiceDef) {
	t.Helper()

	byMethod := handlerStubFilesByMethod(t, dir)
	for _, m := range svc.Methods {
		want := RPCHandlerFileName(m.Name)
		got, ok := byMethod[m.Name]
		if !ok {
			t.Errorf("RPC %s has no *Service method anywhere in %s (layout: %v)", m.Name, dir, byMethod)
			continue
		}
		if got != want {
			t.Errorf("RPC %s landed in %s, want its own %s — a shared file is what forces every "+
				"fan-out to hand-split before it can run two RPCs in parallel", m.Name, got, want)
		}
	}

	// Each per-RPC file declares exactly ONE handler method. A file holding
	// two RPCs is the same collision with a different name.
	perFile := map[string]int{}
	for _, f := range byMethod {
		perFile[f]++
	}
	for f, n := range perFile {
		if strings.HasPrefix(f, "rpc_") && n != 1 {
			t.Errorf("%s declares %d handler methods, want exactly 1", f, n)
		}
	}

	// The stub file the pipeline used to pile everything into must not exist
	// for a service whose RPCs are all freshly stubbed.
	if _, err := os.Stat(filepath.Join(dir, "handlers.go")); err == nil {
		names := make([]string, 0, len(byMethod))
		for m := range byMethod {
			names = append(names, m)
		}
		sort.Strings(names)
		t.Errorf("handlers.go still exists after scaffolding %v — the shared stub file is the "+
			"thing being removed", names)
	}

	// The marker is the handoff between scaffold_and_verify and build_mvp.
	// Assert it through the canonical reader, not a substring.
	marked := ScanUnwiredStubMethods(dir)
	for _, m := range svc.Methods {
		if !marked[m.Name] {
			t.Errorf("RPC %s lost its %q marker in the split (scanned: %v)",
				m.Name, UnwiredStubMarkerPrefix, keysOf(marked))
		}
	}
}

// TestGenerateServiceStub_OneFilePerRPC covers the fresh-service path:
// `forge project new` / `forge scaffold service` on a proto that already
// declares several custom RPCs.
func TestGenerateServiceStub_OneFilePerRPC(t *testing.T) {
	svc := threeCustomRPCs()
	targetDir := filepath.Join(t.TempDir(), "app", "internal", "handlers", "orders")

	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}
	assertOneFilePerRPC(t, targetDir, svc)
}

// TestGenerateMissingHandlerStubs_OneFilePerRPC covers the incremental path
// — the one the measured run hit: the RPCs were declared in the proto and
// `forge generate` appended a stub for each into the one shared handlers.go.
func TestGenerateMissingHandlerStubs_OneFilePerRPC(t *testing.T) {
	svc := threeCustomRPCs()
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "orders")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A handler dir that already exists (service.go born, no RPCs yet) is
	// what makes this the append path rather than the fresh-file one.
	if err := os.WriteFile(filepath.Join(targetDir, "service.go"),
		[]byte("package orders\n\ntype Service struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := GenerateMissingHandlerStubs(svc, projectDir, targetDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs: %v", err)
	}
	if len(res.NewMethods) != 3 {
		t.Fatalf("stubbed %v, want all three RPCs", res.NewMethods)
	}
	assertOneFilePerRPC(t, targetDir, svc)
}

// TestGenerateMissingHandlerStubs_IncrementalRPCGetsItsOwnFile is the
// second wave: the service already has an implemented RPC in a hand-owned
// file, and a NEW RPC arrives. The new one must get its own file rather
// than being appended into whatever the user is holding open.
func TestGenerateMissingHandlerStubs_IncrementalRPCGetsItsOwnFile(t *testing.T) {
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "orders")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userOwned := `package orders

import (
	"context"

	"connectrpc.com/connect"
)

// SubmitOrder is the user's own implementation.
func (s *Service) SubmitOrder(ctx context.Context, req *connect.Request[any]) (*connect.Response[any], error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(targetDir, "handlers.go"), []byte(userOwned), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := threeCustomRPCs()
	res, err := GenerateMissingHandlerStubs(svc, projectDir, targetDir, nil, nil)
	if err != nil {
		t.Fatalf("GenerateMissingHandlerStubs: %v", err)
	}
	sort.Strings(res.NewMethods)
	if want := []string{"CancelOrder", "TailOrderEvents"}; strings.Join(res.NewMethods, ",") != strings.Join(want, ",") {
		t.Fatalf("stubbed %v, want %v (SubmitOrder is already implemented)", res.NewMethods, want)
	}

	byMethod := handlerStubFilesByMethod(t, targetDir)
	if byMethod["SubmitOrder"] != "handlers.go" {
		t.Errorf("the user's SubmitOrder moved to %s — forge must never relocate user code",
			byMethod["SubmitOrder"])
	}
	for _, m := range []string{"CancelOrder", "TailOrderEvents"} {
		if got, want := byMethod[m], RPCHandlerFileName(m); got != want {
			t.Errorf("new RPC %s landed in %s, want %s — appending into the user's open file is "+
				"exactly the collision the split removes", m, got, want)
		}
	}
	after, err := os.ReadFile(filepath.Join(targetDir, "handlers.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "CancelOrder") {
		t.Errorf("forge appended into the user's handlers.go:\n%s", after)
	}
}

// TestGenerateMissingHandlerStubs_ExistingRPCFileIsNotStomped: the one file
// forge would target already exists and does NOT declare the method (the
// user emptied it, or `forge scaffold rpc` wrote it and the method was
// renamed away). Forge must merge into it, never overwrite it — losing the
// user's bytes is worse than a fat file ever was.
func TestGenerateMissingHandlerStubs_ExistingRPCFileIsNotStomped(t *testing.T) {
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "orders")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := "// keepMeIAmTheUsersNote is load-bearing to this test."
	preexisting := "package orders\n\n" + sentinel + "\nfunc keepMeIAmTheUsersNote() {}\n"
	target := filepath.Join(targetDir, RPCHandlerFileName("CancelOrder"))
	if err := os.WriteFile(target, []byte(preexisting), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := threeCustomRPCs()
	if _, err := GenerateMissingHandlerStubs(svc, projectDir, targetDir, nil, nil); err != nil {
		t.Fatalf("GenerateMissingHandlerStubs: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "keepMeIAmTheUsersNote") {
		t.Errorf("forge stomped a pre-existing %s:\n%s", RPCHandlerFileName("CancelOrder"), got)
	}
	if !strings.Contains(string(got), "func (s *Service) CancelOrder(") {
		t.Errorf("the CancelOrder stub was dropped instead of merged — the service would not "+
			"satisfy the Connect handler interface:\n%s", got)
	}
	if !ScanUnwiredStubMethods(targetDir)["CancelOrder"] {
		t.Error("the merged CancelOrder stub carries no unwired-stub marker")
	}
}

// TestExciseUnwiredStubs_RemovesTheEmptiedPerRPCFile: when an RPC becomes
// entity-backed, CRUD gen excises its pristine stub. With one file per RPC
// that empties the file, and an orphan `package x` husk is litter forge
// wrote and must therefore clean up.
func TestExciseUnwiredStubs_RemovesTheEmptiedPerRPCFile(t *testing.T) {
	svc := threeCustomRPCs()
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "orders")
	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}

	removed, err := ExciseUnwiredStubs(projectDir, targetDir, map[string]bool{"CancelOrder": true})
	if err != nil {
		t.Fatalf("ExciseUnwiredStubs: %v", err)
	}
	if len(removed) != 1 || removed[0] != "CancelOrder" {
		t.Fatalf("excised %v, want [CancelOrder]", removed)
	}
	if _, err := os.Stat(filepath.Join(targetDir, RPCHandlerFileName("CancelOrder"))); !os.IsNotExist(err) {
		body, _ := os.ReadFile(filepath.Join(targetDir, RPCHandlerFileName("CancelOrder")))
		t.Errorf("excision left an empty %s husk behind:\n%s", RPCHandlerFileName("CancelOrder"), body)
	}
	// The siblings are untouched.
	for _, m := range []string{"SubmitOrder", "TailOrderEvents"} {
		if _, err := os.Stat(filepath.Join(targetDir, RPCHandlerFileName(m))); err != nil {
			t.Errorf("excision removed the wrong file: %s is gone", RPCHandlerFileName(m))
		}
	}
}

// TestScaffoldedRPCStubs_TypeCheck COMPILES the per-RPC files. "One file
// per RPC" is only true if each file stands alone: its own package clause
// and its own import block. A split that leaves the imports behind in the
// file it split away from parses fine and does not build.
//
// The stubs are type-checked in-module via a go/packages overlay against
// the REAL connectrpc.com/connect and forge/pkg/svcerr (both dependencies
// of this repo), with the project-side `pb` package synthesized into the
// overlay at exactly the import path the emitter wrote.
func TestScaffoldedRPCStubs_TypeCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checking emitted handlers loads the full dependency graph (>2s)")
	}

	repoRoot := forgeRepoRoot(t)
	// The synthetic module path makes the emitter's own
	// `pb "<module>/gen/<proto pkg>/v1"` import resolve inside this repo,
	// so the import line under test is the one that gets compiled.
	synthModule := "github.com/reliant-labs/forge/internal/codegen/zz_rpcsplit_typecheck"
	svc := threeCustomRPCs()
	svc.ModulePath = synthModule
	svc.GoPackage = synthModule + "/gen/services/orders/v1"

	targetDir := filepath.Join(t.TempDir(), "app", "internal", "handlers", "orders")
	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}

	synthDir := filepath.Join(repoRoot, "internal", "codegen", "zz_rpcsplit_typecheck", "orders")
	overlay := map[string][]byte{}
	var goFiles []string

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "rpc_") || !strings.HasSuffix(name, ".go") {
			continue
		}
		body, rerr := os.ReadFile(filepath.Join(targetDir, name))
		if rerr != nil {
			t.Fatal(rerr)
		}
		p := filepath.Join(synthDir, name)
		overlay[p] = body
		goFiles = append(goFiles, p)
	}
	if len(goFiles) != len(svc.Methods) {
		t.Fatalf("found %d rpc_*.go files, want %d", len(goFiles), len(svc.Methods))
	}
	sort.Strings(goFiles)

	// The `*Service` receiver lives in the scaffolded service.go, which
	// imports the project's app packages; supply the minimum the stubs
	// need instead so the compile is about the STUBS.
	overlay[filepath.Join(synthDir, "zz_service.go")] = []byte("package orders\n\ntype Service struct{}\n")

	// The pb package the emitted import names.
	var pb strings.Builder
	pb.WriteString("package v1\n")
	for _, m := range svc.Methods {
		fmt.Fprintf(&pb, "\ntype %s struct{}\ntype %s struct{}\n", m.InputType, m.OutputType)
	}
	overlay[filepath.Join(repoRoot, "internal", "codegen", "zz_rpcsplit_typecheck",
		"gen", "services", "orders", "v1", "pb.go")] = []byte(pb.String())

	cfg := &packages.Config{
		Mode:    packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
		Dir:     repoRoot,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, "file="+goFiles[0])
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("packages.Load returned no packages")
	}
	failed := false
	for _, p := range pkgs {
		for _, e := range p.Errors {
			failed = true
			t.Errorf("emitted per-RPC handler does not compile: %v", e)
		}
	}
	if failed {
		for _, f := range goFiles {
			t.Logf("==== %s ====\n%s", filepath.Base(f), overlay[f])
		}
	}
}
