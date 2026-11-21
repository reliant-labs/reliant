package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/scaffolds"
)

// The emitters below each hand-write their own header, and two of them
// stopped at the forge-owned banner — so a freshly scaffolded project
// reported `gen-missing-source` on db/embed_gen.go and on every service
// mock, warning about files the user is told never to edit. These pin each
// emitter to the exact rule `forge lint` applies; internal/tierguard holds
// the whole rendered Tier-1 set to it.

func TestGenerateMockPassesScaffoldOwnershipLint(t *testing.T) {
	mockDir := filepath.Join(t.TempDir(), "mocks")
	svc := ServiceDef{
		Name:       "OrdersService",
		Package:    "orders.v1",
		GoPackage:  "github.com/test/proj/gen/proto/services/orders/v1",
		PkgName:    "ordersv1",
		Methods:    []Method{{Name: "CancelOrder", InputType: "CancelOrderRequest", OutputType: "CancelOrderResponse"}},
		ProtoFile:  "proto/services/orders/v1/orders.proto",
		ModulePath: "github.com/test/proj",
	}
	if _, err := GenerateMock(svc, "", mockDir, nil); err != nil {
		t.Fatalf("GenerateMock: %v", err)
	}
	assertPassesScaffoldOwnershipLint(t, filepath.Join(mockDir, "orders_mock_gen.go"), "internal/handlers/mocks/orders_mock_gen.go")
}

func TestGenerateMigrateEmbedPassesScaffoldOwnershipLint(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateMigrate(dir, "example.com/proj", true, nil); err != nil {
		t.Fatalf("GenerateMigrate: %v", err)
	}
	for _, rel := range []string{"db/embed_gen.go", "db/source_gen.go"} {
		assertPassesScaffoldOwnershipLint(t, filepath.Join(dir, filepath.FromSlash(rel)), rel)
	}
}

func assertPassesScaffoldOwnershipLint(t *testing.T, path, rel string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if fs := scaffolds.LintGeneratedHeader(rel, data); len(fs) > 0 {
		t.Errorf("%s fails forge's own scaffold-ownership lint: %+v", rel, fs)
	}
}
