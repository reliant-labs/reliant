package contract

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildSandboxGoMod returns a go.mod body for a temp-dir test project
// that pulls contractkit (and its otel deps) from the local pkg
// sub-module via a replace directive. Without this, sandboxed builds
// fail because they cannot reach the network and the local module is
// not yet published.
//
// Indirect otel deps are listed explicitly so go-mod-tidy is not needed
// in the sandbox (which has no network access).
//
// Both the go directive and those indirect versions are READ from forge's
// go.mod rather than written here. The sandbox requires forge and copies its
// go.sum for checksum verification, so any literal pinned in this file is a
// second copy of a version go.mod already owns — and a dependency bump lands
// in exactly one of the two. When they drift the build fails with `missing
// go.sum entry`, which names the module but not the stale literal in this
// helper that actually caused it.
func buildSandboxGoMod(modName string) string {
	abs := localForgeRoot()

	indirect := []string{
		"github.com/cespare/xxhash/v2",
		"github.com/go-logr/logr",
		"github.com/go-logr/stdr",
		"go.opentelemetry.io/auto/sdk",
		"go.opentelemetry.io/otel",
		"go.opentelemetry.io/otel/metric",
		"go.opentelemetry.io/otel/trace",
	}
	var requires strings.Builder
	for _, module := range indirect {
		requires.WriteString(fmt.Sprintf("\t%s %s // indirect\n", module, pkgRequireVersion(module)))
	}

	return fmt.Sprintf(`module %s

go %s

require github.com/reliant-labs/forge v0.0.0

require (
%s)

replace github.com/reliant-labs/forge => %s
`, modName, pkgGoDirective(), requires.String(), abs)
}

// pkgRequireVersion returns the version forge's go.mod requires for module.
// It panics rather than guessing: a module the sandbox needs that forge no
// longer requires is a real drift between the two, and a fabricated
// version would surface it later as an opaque checksum error.
func pkgRequireVersion(module string) string {
	data, err := os.ReadFile(filepath.Join(localForgeRoot(), "go.mod"))
	if err != nil {
		panic(fmt.Sprintf("read forge go.mod for sandbox require %q: %v", module, err))
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == module {
			return fields[1]
		}
	}
	panic(fmt.Sprintf("forge's go.mod does not require %q, which the contract-generator build sandbox needs", module))
}

// pkgGoDirective returns the `go` version forge's go.mod declares, so the
// sandbox tracks it automatically. Falling back to the running
// toolchain's version keeps the failure legible if that go.mod ever
// becomes unreadable: the build then fails on the real problem rather
// than on a go.mod this helper guessed wrong.
func pkgGoDirective() string {
	data, err := os.ReadFile(filepath.Join(localForgeRoot(), "go.mod"))
	if err != nil {
		return strings.TrimPrefix(runtime.Version(), "go")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return strings.TrimSpace(v)
		}
	}
	return strings.TrimPrefix(runtime.Version(), "go")
}

// localForgeRoot returns the absolute path to the forge checkout: three
// levels above this package's source directory, the working directory `go
// test` runs it in. Exposed as a separate helper so tests can also copy
// go.sum from it.
//
// Not runtime.Caller: under -trimpath (the workspace image's GOFLAGS default)
// that reports "github.com/reliant-labs/forge/internal/...", a module-relative
// path that is not a file on disk. internal/trimpathguard holds the tree to it.
func localForgeRoot() string {
	abs, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	return abs
}

// writeSandboxGoSum copies forge's go.sum into dir so that `go build` in
// the sandbox can verify module checksums. The sandbox only depends on
// transitive otel deps already present there, so a straight copy is
// sufficient.
func writeSandboxGoSum(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join(localForgeRoot(), "go.sum")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read pkg go.sum: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), data, 0644); err != nil {
		t.Fatalf("write sandbox go.sum: %v", err)
	}
}

func TestParseContract_Simple(t *testing.T) {
	cf, err := ParseContract("testdata/simple/contract.go")
	if err != nil {
		t.Fatalf("ParseContract() error = %v", err)
	}

	if cf.Package != "simple" {
		t.Errorf("Package = %q, want %q", cf.Package, "simple")
	}

	if len(cf.Interfaces) != 1 {
		t.Fatalf("len(Interfaces) = %d, want 1", len(cf.Interfaces))
	}

	iface := cf.Interfaces[0]
	if iface.Name != "Service" {
		t.Errorf("Interface name = %q, want %q", iface.Name, "Service")
	}
	if len(iface.Methods) != 2 {
		t.Fatalf("len(Methods) = %d, want 2", len(iface.Methods))
	}

	// Get(ctx context.Context, id string) (string, error)
	get := iface.Methods[0]
	if get.Name != "Get" {
		t.Errorf("Method[0].Name = %q, want %q", get.Name, "Get")
	}
	if len(get.Params) != 2 {
		t.Errorf("Get params = %d, want 2", len(get.Params))
	}
	if len(get.Results) != 2 {
		t.Errorf("Get results = %d, want 2", len(get.Results))
	}
	if get.Results[0].TypeExpr != "string" {
		t.Errorf("Get result[0].TypeExpr = %q, want %q", get.Results[0].TypeExpr, "string")
	}
	if get.Results[1].TypeExpr != "error" {
		t.Errorf("Get result[1].TypeExpr = %q, want %q", get.Results[1].TypeExpr, "error")
	}
}

func TestParseContract_Complex(t *testing.T) {
	cf, err := ParseContract("testdata/complex/contract.go")
	if err != nil {
		t.Fatalf("ParseContract() error = %v", err)
	}

	if len(cf.Interfaces) != 1 {
		t.Fatalf("len(Interfaces) = %d, want 1", len(cf.Interfaces))
	}

	iface := cf.Interfaces[0]
	if iface.Name != "Store" {
		t.Errorf("Interface name = %q, want %q", iface.Name, "Store")
	}
	if len(iface.Methods) != 4 {
		t.Fatalf("len(Methods) = %d, want 4", len(iface.Methods))
	}

	// Query — variadic args
	query := iface.Methods[0]
	if query.Name != "Query" {
		t.Errorf("Method[0].Name = %q, want %q", query.Name, "Query")
	}
	lastParam := query.Params[len(query.Params)-1]
	if !lastParam.Variadic {
		t.Error("Query last param should be variadic")
	}
	if !strings.Contains(lastParam.TypeExpr, "...any") {
		t.Errorf("Query last param type = %q, want contains ...any", lastParam.TypeExpr)
	}

	// Query returns *sql.Rows
	if !strings.Contains(query.Results[0].TypeExpr, "*sql.Rows") {
		t.Errorf("Query result[0] = %q, want *sql.Rows", query.Results[0].TypeExpr)
	}

	// Subscribe — channel return
	subscribe := iface.Methods[2]
	if subscribe.Name != "Subscribe" {
		t.Errorf("Method[2].Name = %q, want %q", subscribe.Name, "Subscribe")
	}
	if !strings.Contains(subscribe.Results[0].TypeExpr, "<-chan") {
		t.Errorf("Subscribe result[0] = %q, want contains <-chan", subscribe.Results[0].TypeExpr)
	}

	// Transform — func type param
	transform := iface.Methods[3]
	if transform.Name != "Transform" {
		t.Errorf("Method[3].Name = %q, want %q", transform.Name, "Transform")
	}
	fnParam := transform.Params[1]
	if !strings.Contains(fnParam.TypeExpr, "func(") {
		t.Errorf("Transform param[1] = %q, want contains func(", fnParam.TypeExpr)
	}
}

func TestParseContract_Multi(t *testing.T) {
	cf, err := ParseContract("testdata/multi/contract.go")
	if err != nil {
		t.Fatalf("ParseContract() error = %v", err)
	}

	if len(cf.Interfaces) != 2 {
		t.Fatalf("len(Interfaces) = %d, want 2", len(cf.Interfaces))
	}

	if cf.Interfaces[0].Name != "Reader" {
		t.Errorf("Interfaces[0].Name = %q, want %q", cf.Interfaces[0].Name, "Reader")
	}
	if cf.Interfaces[1].Name != "Writer" {
		t.Errorf("Interfaces[1].Name = %q, want %q", cf.Interfaces[1].Name, "Writer")
	}
	if len(cf.Interfaces[0].Methods) != 2 {
		t.Errorf("Reader methods = %d, want 2", len(cf.Interfaces[0].Methods))
	}
	if len(cf.Interfaces[1].Methods) != 2 {
		t.Errorf("Writer methods = %d, want 2", len(cf.Interfaces[1].Methods))
	}
}

func TestParseContract_Empty(t *testing.T) {
	cf, err := ParseContract("testdata/empty/contract.go")
	if err != nil {
		t.Fatalf("ParseContract() error = %v", err)
	}

	if len(cf.Interfaces) != 1 {
		t.Fatalf("len(Interfaces) = %d, want 1", len(cf.Interfaces))
	}

	if cf.Interfaces[0].Name != "Marker" {
		t.Errorf("Interface name = %q, want %q", cf.Interfaces[0].Name, "Marker")
	}
	if len(cf.Interfaces[0].Methods) != 0 {
		t.Errorf("Methods = %d, want 0", len(cf.Interfaces[0].Methods))
	}
}

func TestGenerate_Simple(t *testing.T) {
	dir := copyTestdata(t, "testdata/simple")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Verify mock_gen.go exists and is valid Go.
	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "Code generated by forge. DO NOT EDIT.")
	assertContains(t, mockPath, "MockService")
	assertContains(t, mockPath, "GetFunc")
	assertContains(t, mockPath, "SetFunc")
	assertContains(t, mockPath, "var _ Service = (*MockService)(nil)")

	// The middleware/tracing/metrics wrappers are no longer generated —
	// observability lives in forge/pkg/observe (Connect interceptors at
	// the handler boundary; opt-in helpers for inner-call instrumentation).
	// Generate must not leave stale copies behind.
	for _, gone := range []string{"middleware_gen.go", "tracing_gen.go", "metrics_gen.go"} {
		assertFileMissing(t, filepath.Join(dir, gone))
	}
}

func TestGenerate_Complex(t *testing.T) {
	dir := copyTestdata(t, "testdata/complex")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "MockStore")
	assertContains(t, mockPath, "QueryFunc")
	assertContains(t, mockPath, "ExecFunc")
	assertContains(t, mockPath, "SubscribeFunc")
	assertContains(t, mockPath, "TransformFunc")
	assertContains(t, mockPath, "args...")
	assertContains(t, mockPath, `"database/sql"`)

	for _, gone := range []string{"middleware_gen.go", "tracing_gen.go", "metrics_gen.go"} {
		assertFileMissing(t, filepath.Join(dir, gone))
	}
}

func TestGenerate_Multi(t *testing.T) {
	dir := copyTestdata(t, "testdata/multi")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "MockReader")
	assertContains(t, mockPath, "MockWriter")
	assertContains(t, mockPath, "var _ Reader = (*MockReader)(nil)")
	assertContains(t, mockPath, "var _ Writer = (*MockWriter)(nil)")

	for _, gone := range []string{"middleware_gen.go", "tracing_gen.go", "metrics_gen.go"} {
		assertFileMissing(t, filepath.Join(dir, gone))
	}
}

func TestGenerate_Empty(t *testing.T) {
	dir := copyTestdata(t, "testdata/empty")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "MockMarker")
	assertContains(t, mockPath, "var _ Marker = (*MockMarker)(nil)")

	for _, gone := range []string{"middleware_gen.go", "tracing_gen.go", "metrics_gen.go"} {
		assertFileMissing(t, filepath.Join(dir, gone))
	}
}

// TestGenerate_ZeroValues verifies that mock_gen.go for a contract returning
// many shapes (struct value, pointer, slice, map, channel, func, any, basic
// types) actually compiles — not just parses. Regression test for the bug
// where `zeroValue()` returned "nil" for non-pointer named struct returns.
func TestGenerate_ZeroValues(t *testing.T) {
	dir := copyTestdata(t, "testdata/zerovalues")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)

	// The struct return must use composite literal, not nil. Post-
	// contractkit migration the trailing error is contractkit.MockNotSet.
	assertContains(t, mockPath, "return LocalStruct{}, contractkit.MockNotSet")
	// The non-error single-return struct must also use composite literal.
	assertContains(t, mockPath, "return LocalStruct{}")
	// A cross-package named scalar gets the universal zero value.
	assertContains(t, mockPath, "return *new(time.Duration), contractkit.MockNotSet")

	// The middleware/tracing/metrics wrappers are no longer generated, so
	// the build sandbox only needs to type-check contract.go + mock_gen.go.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(buildSandboxGoMod("zerovaluestest")), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeSandboxGoSum(t, dir)

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build of generated mock failed: %v\n%s", err, string(out))
	}
}

// TestGenerate_InterfaceReturns is the regression test for bug #16:
// methods returning interface-typed values must emit "nil" in the mock
// fallback, not the invalid composite literal "T{}". Covers both
// locally-defined interfaces (Debugger) and cross-package interfaces
// from the allow-list (io.Reader). Also confirms struct returns still
// produce "Result{}" — bug #15's fix must not regress.
func TestGenerate_InterfaceReturns(t *testing.T) {
	dir := copyTestdata(t, "testdata/interfaces")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)

	// Local-interface returns must produce "nil", not "Debugger{}". Post-
	// contractkit migration the not-set error is contractkit.MockNotSet.
	assertContains(t, mockPath, `return nil, contractkit.MockNotSet("MockService", "NewDebugger")`)
	assertContains(t, mockPath, "return nil")
	assertNotContains(t, mockPath, "Debugger{}")

	// Cross-package interface (io.Reader) from the allow-list — must be nil.
	assertContains(t, mockPath, `return nil, contractkit.MockNotSet("MockService", "OpenReader")`)
	assertNotContains(t, mockPath, "io.Reader{}")

	// Bug #15 regression: struct return still uses composite literal.
	assertContains(t, mockPath, "return Result{}, contractkit.MockNotSet")

	// The middleware/tracing/metrics wrappers are no longer generated, so
	// the build sandbox only needs to type-check contract.go + mock_gen.go.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(buildSandboxGoMod("interfacestest")), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeSandboxGoSum(t, dir)

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build of generated mock failed: %v\n%s", err, string(out))
	}
}

// TestGenerate_RecordMethodShadowsRecorder is the regression pin for
// the Recorder-shadowing bug the fixture corpus caught: a contract
// method literally named Record makes the generated
// MockService.Record shadow the promoted contractkit.Recorder.Record,
// so a bare `m.Record("Record", ...)` inside the mock body is a
// self-call — wrong arity (compile error) or, with a matching
// signature, infinite recursion. The template must qualify the call
// as `m.Recorder.Record(...)`.
func TestGenerate_RecordMethodShadowsRecorder(t *testing.T) {
	dir := copyTestdata(t, "testdata/recordshadow")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, `m.Recorder.Record("Record", ctx, entry)`)

	// Type-check the generated mock — the shadowing failure mode is an
	// arity error `go build` catches, not a parse error.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(buildSandboxGoMod("recordshadowtest")), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeSandboxGoSum(t, dir)

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build of generated mock failed (Recorder shadowing regressed): %v\n%s", err, string(out))
	}
}

// TestGenerate_ExtraInterfaceTypes verifies the forge.yaml extension hook:
// a project-local cross-package interface declared via Options.
// ExtraInterfaceTypes flips the mock fallback from the invalid composite
// literal "pkg.T{}" to "nil". Regression test for the cp-forge v2
// svcbilling repro where a contract method returned billing.MeterClient
// — an interface the built-in crossPackageInterfaces list does not (and
// should not) know about.
//
// Plain Generate (no extras) cannot know billing.MeterClient is an
// interface, so it emits the universal "*new(billing.MeterClient)" —
// which compiles for every type family (it used to emit the invalid
// "billing.MeterClient{}"). The hook still turns it into the more
// readable "nil"; both halves are pinned.
func TestGenerate_ExtraInterfaceTypes(t *testing.T) {
	// Baseline: without the extras hook, an unknown cross-package
	// interface return falls through to the qualified-named-type branch
	// and emits the universal zero value, never a composite literal.
	baselineDir := copyTestdata(t, "testdata/extra_iface")
	if err := Generate(filepath.Join(baselineDir, "contract.go")); err != nil {
		t.Fatalf("Generate() baseline error = %v", err)
	}
	baselineMock := filepath.Join(baselineDir, "mock_gen.go")
	assertFileExists(t, baselineMock)
	assertContains(t, baselineMock, "*new(billing.MeterClient)")
	assertNotContains(t, baselineMock, "billing.MeterClient{}")

	// With the extras hook: declare "billing.MeterClient" as a mockable
	// interface and the mock fallback collapses to "nil".
	extraDir := copyTestdata(t, "testdata/extra_iface")
	opts := Options{
		ExtraInterfaceTypes: map[string]bool{
			"billing.MeterClient": true,
		},
	}
	if err := GenerateWithOptions(filepath.Join(extraDir, "contract.go"), opts); err != nil {
		t.Fatalf("GenerateWithOptions() error = %v", err)
	}
	extraMock := filepath.Join(extraDir, "mock_gen.go")
	assertFileExists(t, extraMock)
	assertValidGo(t, extraMock)
	// The Meter fallback returns (billing.MeterClient, error). With the
	// extra declared, the interface return collapses to nil; the error
	// stays as contractkit.MockNotSet.
	assertContains(t, extraMock, `return nil, contractkit.MockNotSet("MockService", "Meter")`)
	assertNotContains(t, extraMock, "billing.MeterClient{}")
}

// TestGenerate_SiblingFileInterface verifies that an interface declared in
// a sibling .go file (not contract.go) is still recognized as an interface
// by zeroValue. Without sibling-file scanning the generator emits the
// invalid "return Handle{}" instead of "return nil".
func TestGenerate_SiblingFileInterface(t *testing.T) {
	dir := copyTestdata(t, "testdata/sibling_iface")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "return nil")
	assertNotContains(t, mockPath, "Handle{}")
}

// TestGenerate_PrimitiveAlias is the regression test for the named-
// primitive-alias zero-value bug: contracts returning a `type X string`
// (or int/bool/float) must emit the underlying primitive's zero (`""`,
// `0`, `false`) rather than the invalid composite literal `X{}`.
// Covers both same-file aliases (BalanceCapReason / AttemptCount /
// Enabled / Score) and sibling-file aliases (SiblingTag declared in
// aliases.go).
func TestGenerate_PrimitiveAlias(t *testing.T) {
	dir := copyTestdata(t, "testdata/primitive_alias")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)

	// Same-package string alias: must emit "" not BalanceCapReason{}.
	assertContains(t, mockPath, `return 0, ""`)
	assertNotContains(t, mockPath, "BalanceCapReason{}")

	// int / bool / float aliases — must emit primitive zeros, not T{}.
	assertNotContains(t, mockPath, "AttemptCount{}")
	assertNotContains(t, mockPath, "Enabled{}")
	assertNotContains(t, mockPath, "Score{}")

	// Sibling-file string alias must also be resolved.
	assertNotContains(t, mockPath, "SiblingTag{}")

	// Build the generated mock in a sandbox to confirm it actually
	// compiles end-to-end (the original repro was a build error).
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(buildSandboxGoMod("primitivealiastest")), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeSandboxGoSum(t, dir)

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build of generated mock failed: %v\n%s", err, string(out))
	}
}

func TestParseContract_Embedded(t *testing.T) {
	cf, err := ParseContract("testdata/embedded/contract.go")
	if err != nil {
		t.Fatalf("ParseContract() error = %v", err)
	}

	if len(cf.Interfaces) != 3 {
		t.Fatalf("len(Interfaces) = %d, want 3", len(cf.Interfaces))
	}

	// CommandPublisher has 1 method
	if cf.Interfaces[0].Name != "CommandPublisher" {
		t.Errorf("Interfaces[0].Name = %q, want %q", cf.Interfaces[0].Name, "CommandPublisher")
	}
	if len(cf.Interfaces[0].Methods) != 1 {
		t.Errorf("CommandPublisher methods = %d, want 1", len(cf.Interfaces[0].Methods))
	}

	// EventPublisher has 1 method
	if cf.Interfaces[1].Name != "EventPublisher" {
		t.Errorf("Interfaces[1].Name = %q, want %q", cf.Interfaces[1].Name, "EventPublisher")
	}
	if len(cf.Interfaces[1].Methods) != 1 {
		t.Errorf("EventPublisher methods = %d, want 1", len(cf.Interfaces[1].Methods))
	}

	// Service should have 3 methods: Close + PublishCommand + PublishEvent
	svc := cf.Interfaces[2]
	if svc.Name != "Service" {
		t.Errorf("Interfaces[2].Name = %q, want %q", svc.Name, "Service")
	}
	if len(svc.Methods) != 3 {
		t.Fatalf("Service methods = %d, want 3; got: %v", len(svc.Methods), methodNames(svc.Methods))
	}

	// Verify all expected methods are present
	names := methodNames(svc.Methods)
	for _, want := range []string{"Close", "PublishCommand", "PublishEvent"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Service missing method %q, has: %v", want, names)
		}
	}
}

func TestGenerate_Embedded(t *testing.T) {
	dir := copyTestdata(t, "testdata/embedded")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Verify mock_gen.go has all methods for Service. Embedded interfaces
	// (CommandPublisher, EventPublisher) get their methods promoted onto
	// MockService — the original purpose of this test.
	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)
	assertContains(t, mockPath, "MockService")
	assertContains(t, mockPath, "CloseFunc")
	assertContains(t, mockPath, "PublishCommandFunc")
	assertContains(t, mockPath, "PublishEventFunc")
	assertContains(t, mockPath, "var _ Service = (*MockService)(nil)")

	for _, gone := range []string{"middleware_gen.go", "tracing_gen.go", "metrics_gen.go"} {
		assertFileMissing(t, filepath.Join(dir, gone))
	}
}

func methodNames(methods []MethodDef) []string {
	names := make([]string, len(methods))
	for i, m := range methods {
		names[i] = m.Name
	}
	return names
}

func TestMethodDef_HasContext(t *testing.T) {
	withCtx := MethodDef{
		Params: []ParamDef{{Name: "ctx", TypeExpr: "context.Context"}},
	}
	if !withCtx.HasContext() {
		t.Error("HasContext() = false, want true")
	}

	withoutCtx := MethodDef{
		Params: []ParamDef{{Name: "id", TypeExpr: "string"}},
	}
	if withoutCtx.HasContext() {
		t.Error("HasContext() = true, want false")
	}

	noParams := MethodDef{}
	if noParams.HasContext() {
		t.Error("HasContext() = true for no params, want false")
	}
}

func TestMethodDef_ParamSignature(t *testing.T) {
	m := MethodDef{
		Name: "Query",
		Params: []ParamDef{
			{Name: "ctx", TypeExpr: "context.Context"},
			{Name: "query", TypeExpr: "string"},
			{Name: "args", TypeExpr: "...any", Variadic: true},
		},
	}
	got := m.ParamSignature()
	want := "ctx context.Context, query string, args ...any"
	if got != want {
		t.Errorf("ParamSignature() = %q, want %q", got, want)
	}
}

func TestMethodDef_CallArgs_Variadic(t *testing.T) {
	m := MethodDef{
		Params: []ParamDef{
			{Name: "ctx", TypeExpr: "context.Context"},
			{Name: "args", TypeExpr: "...any", Variadic: true},
		},
	}
	got := m.CallArgs()
	want := "ctx, args..."
	if got != want {
		t.Errorf("CallArgs() = %q, want %q", got, want)
	}
}

func TestMethodDef_FuncFieldType(t *testing.T) {
	m := MethodDef{
		Name: "Get",
		Params: []ParamDef{
			{Name: "ctx", TypeExpr: "context.Context"},
			{Name: "id", TypeExpr: "string"},
		},
		Results: []ParamDef{
			{TypeExpr: "string"},
			{TypeExpr: "error"},
		},
	}
	got := m.FuncFieldType()
	want := "func(context.Context, string) (string, error)"
	if got != want {
		t.Errorf("FuncFieldType() = %q, want %q", got, want)
	}
}

func TestMethodDef_ResultSignature_Single(t *testing.T) {
	m := MethodDef{
		Results: []ParamDef{{TypeExpr: "error"}},
	}
	got := m.ResultSignature()
	if got != "error" {
		t.Errorf("ResultSignature() = %q, want %q", got, "error")
	}
}

func TestMethodDef_ResultSignature_Multiple(t *testing.T) {
	m := MethodDef{
		Results: []ParamDef{
			{TypeExpr: "string"},
			{TypeExpr: "error"},
		},
	}
	got := m.ResultSignature()
	if got != "(string, error)" {
		t.Errorf("ResultSignature() = %q, want %q", got, "(string, error)")
	}
}

func TestMethodDef_ResultSignature_None(t *testing.T) {
	m := MethodDef{}
	got := m.ResultSignature()
	if got != "" {
		t.Errorf("ResultSignature() = %q, want empty", got)
	}
}

// TestGenerate_ReceiverCollision was the regression test for bug #18: the
// removed middleware/tracing/metrics templates must not name their
// receiver "w", because contract methods commonly have a parameter named
// "w" (e.g. an io.Writer). After the wrappers were dropped in favour of
// observe.* the failure mode no longer applies, but the testdata
// fixture is a useful "method with parameter named w" smoke for the
// surviving mock template — keep the test, scope it down to mock_gen.go.
func TestGenerate_ReceiverCollision(t *testing.T) {
	dir := copyTestdata(t, "testdata/receivercollision")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	mockPath := filepath.Join(dir, "mock_gen.go")
	assertFileExists(t, mockPath)
	assertValidGo(t, mockPath)
	// The mock receiver is "m"; a parameter named "w" must not collide.
	assertNotContains(t, mockPath, "func (w *MockService)")

	for _, gone := range []string{"middleware_gen.go", "tracing_gen.go", "metrics_gen.go"} {
		assertFileMissing(t, filepath.Join(dir, gone))
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// copyTestdata copies a testdata directory into a temp directory so generated
// files don't pollute the source tree.
func copyTestdata(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", src, err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0644); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}
	}
	return dir
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("expected file %s to exist", path)
	}
}

// assertFileMissing fails when path exists. Used to verify Generate's
// cleanup of legacy middleware_gen.go / tracing_gen.go / metrics_gen.go
// — a stale wrapper whose imports reference now-removed contractkit
// helpers would break user builds.
func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected file %s to be absent (Generate must not emit it)", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func assertValidGo(t *testing.T, path string) {
	t.Helper()
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
	if err != nil {
		data, _ := os.ReadFile(path)
		t.Fatalf("generated file %s is not valid Go:\n%v\n\nContent:\n%s", path, err, string(data))
	}
}

func assertContains(t *testing.T, path, substr string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if !strings.Contains(string(data), substr) {
		t.Errorf("file %s does not contain %q\n\nContent:\n%s", path, substr, string(data))
	}
}

func assertNotContains(t *testing.T, path, substr string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	if strings.Contains(string(data), substr) {
		t.Errorf("file %s should NOT contain %q\n\nContent:\n%s", path, substr, string(data))
	}
}
