package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

// TestMockOutcomeLine_NamesTheWrittenFile: generate's progress line named
// internal/handlers/mocks/<svc>_mock.go long after the file it wrote
// became <svc>_mock_gen.go. Both now read codegen.MockRelPath.
func TestMockOutcomeLine_NamesTheWrittenFile(t *testing.T) {
	svc := codegen.ServiceDef{Name: "ToolCallService"}
	for _, res := range []codegen.MockResult{
		{Wrote: true, Changed: true},
		{Wrote: true},
		{Skipped: "no RPCs"},
	} {
		line := mockOutcomeLine(svc, res)
		if !strings.Contains(line, "internal/handlers/mocks/tool_call_mock_gen.go") {
			t.Errorf("%+v: %q does not name the file GenerateMock writes", res, line)
		}
	}
}

// TestGenerateServiceMocks_ReportsOnlyChangedMocks: generate printed
// "✅ Updated internal/handlers/mocks/…" for every mock on every run,
// though a re-run renders each one byte-identical (the run's closing
// summary already counted them unchanged). A mock is reported as updated
// only when its bytes changed; an unchanged one is a --verbose line.
func TestGenerateServiceMocks_ReportsOnlyChangedMocks(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: proj\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checksums.ResetSkipWrite()
	t.Cleanup(checksums.ResetSkipWrite)

	svcs := []codegen.ServiceDef{{
		Name:       "OrdersService",
		Package:    "orders.v1",
		GoPackage:  "github.com/test/proj/gen/services/orders/v1",
		PkgName:    "ordersv1",
		ProtoFile:  "services/orders/v1/orders.proto",
		ModulePath: "github.com/test/proj",
		Methods:    []codegen.Method{{Name: "CreateOrder", InputType: "CreateOrderRequest", OutputType: "CreateOrderResponse"}},
	}}
	const updated = "✅ Updated internal/handlers/mocks/orders_mock_gen.go"
	run := func() string {
		return captureStdout(t, func() {
			if err := generateServiceMocks(svcs, projectDir, nil); err != nil {
				t.Fatalf("generateServiceMocks: %v", err)
			}
		})
	}

	if out := run(); !strings.Contains(out, updated) {
		t.Fatalf("first run must report the new mock:\n%s", out)
	}
	if out := run(); strings.Contains(out, "✅ Updated") {
		t.Fatalf("re-run reported a mock whose bytes did not change:\n%s", out)
	}

	restore := setGenerateVerbosity(true)
	out := run()
	restore()
	if !strings.Contains(out, "internal/handlers/mocks/orders_mock_gen.go unchanged") {
		t.Errorf("--verbose re-run should name the unchanged mock:\n%s", out)
	}

	svcs[0].Methods = append(svcs[0].Methods, codegen.Method{Name: "CancelOrder", InputType: "CancelOrderRequest", OutputType: "CancelOrderResponse"})
	if out := run(); !strings.Contains(out, updated) {
		t.Fatalf("a changed mock must be reported:\n%s", out)
	}
}

// TestInternalContracts_ReportOnlyChangedPackages is the contract-package
// twin: "✅ Generated mock + middleware for internal/<pkg>/" printed for
// every contract package on every run. On forge itself a run that changed
// one mock_gen.go printed eight of them.
func TestInternalContracts_ReportOnlyChangedPackages(t *testing.T) {
	resetCleanupRunState(t)
	dir := t.TempDir()
	writeMockContractPackage(t, dir, "climate")
	const line = "✅ Generated mock + middleware for internal/climate/"
	run := func() string {
		return captureStdout(t, func() {
			if err := generateInternalPackageContracts(dir, nil, &checksums.FileChecksums{}); err != nil {
				t.Fatalf("generateInternalPackageContracts: %v", err)
			}
		})
	}

	if out := run(); !strings.Contains(out, line) {
		t.Fatalf("first run must report the new mock:\n%s", out)
	}
	if out := run(); strings.Contains(out, "✅") {
		t.Fatalf("re-run reported a package whose generated files did not change:\n%s", out)
	}

	mustWrite(t, filepath.Join(dir, "internal", "climate", "contract.go"),
		"package climate\n\nimport \"context\"\n\n// Service is the package contract.\n"+
			"type Service interface {\n\tGet(ctx context.Context, id string) (string, error)\n\tDelete(ctx context.Context, id string) error\n}\n")
	if out := run(); !strings.Contains(out, line) {
		t.Fatalf("a changed contract must be reported:\n%s", out)
	}
}
