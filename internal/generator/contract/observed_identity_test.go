package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

func TestObservedDecoratorCarriesCatalogMethodIdentity(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal/orders")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/shop\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "contract.go")
	if err := os.WriteFile(path, []byte("package orders\nimport \"context\"\ntype Service interface {Get(context.Context) error}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cf, err := ParseContract(path)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := WriteObservedDecorator(cf, dir, "Service", "NewServiceWithForgeMiddleware", "forgeMiddlewareService", "orders", &DefaultChain{Scope: "example.com/shop/internal/orders", Name: "orders"}, Options{})
	if err != nil || !ok {
		t.Fatalf("generated=%v err=%v", ok, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "middleware_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".WithMethodIDs(map[string]string{", servicecatalog.MethodID("example.com/shop/internal/orders", "Service", "Get"), `o.chain.Run(p0, "orders.Get"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %q in %s", want, raw)
		}
	}
}
