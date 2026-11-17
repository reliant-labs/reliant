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

// TestObservedDecoratorIsTheSameForARelativeDir pins scaffold/generate
// agreement: `forge generate` hands the writer a dir relative to the project
// root, `forge scaffold` an absolute one. The relative form used to drop the
// WithMethodIDs map (filepath.Rel cannot relate an absolute module root to a
// relative dir), so scaffolding one package rewrote every other package's
// middleware_gen.go and the next generate reverted them.
func TestObservedDecoratorIsTheSameForARelativeDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "orders")
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
	render := func(dir string) string {
		t.Helper()
		cf, err := ParseContract(filepath.Join(dir, "contract.go"))
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
		return string(raw)
	}
	absolute := render(dir)
	t.Chdir(root)
	relative := render(filepath.Join("internal", "orders"))
	if relative != absolute {
		t.Fatalf("a relative dir renders a different middleware_gen.go than an absolute one\n--- absolute\n%s\n--- relative\n%s", absolute, relative)
	}
	if !strings.Contains(relative, ".WithMethodIDs(map[string]string{") {
		t.Fatalf("relative render lost WithMethodIDs:\n%s", relative)
	}
}
