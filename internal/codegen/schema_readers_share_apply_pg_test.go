package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/shadowdb"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// TestSchemaReaders_OneRunAppliesTheMigrationsOnce drives every generate-time
// reader of the applied schema in this package — the entity join (behind the
// ORM, CRUD wiring, pages, nav and seed steps), the CRUD-test fixture model,
// the test factories and the frontend mock dataset — first each on its own
// shadow, as they ran before, then all inside ONE run.
//
// Inside the run the migrations must be applied exactly once, and every
// reader must produce exactly what it produced from its private apply: the
// same entity model, the same seed plans, the same factory file byte for
// byte. That is the whole claim of the run scope — the readers share the
// work and nothing else.
func TestSchemaReaders_OneRunAppliesTheMigrationsOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("applies migrations to a real postgres several times; runs in task test")
	}
	projectDir := t.TempDir()
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "shop")
	migDir := filepath.Join(projectDir, "db", "migrations")
	for _, d := range []string{handlerDir, migDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(handlerDir, "service.go"), []byte("package shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	migration := `
CREATE TABLE brands (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
CREATE TABLE products (
    id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    contact_email TEXT NOT NULL CHECK (contact_email ~* '^[^@]+@[^@]+\.[^@]+$'),
    currency TEXT NOT NULL CHECK (char_length(currency) = 3),
    status TEXT NOT NULL CHECK (status IN ('PRODUCT_STATUS_ACTIVE', 'PRODUCT_STATUS_DISCONTINUED')),
    brand_id TEXT NOT NULL REFERENCES brands(id),
    sku TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    price_cents BIGINT NOT NULL CHECK (price_cents >= 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(migration), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, methods := productSvcAndMethods()
	svc.Methods = []Method{
		{Name: "CreateProduct", InputType: "CreateProductRequest", OutputType: "CreateProductResponse"},
		{Name: "GetProduct", InputType: "GetProductRequest", OutputType: "GetProductResponse"},
		{Name: "UpdateProduct", InputType: "UpdateProductRequest", OutputType: "UpdateProductResponse"},
	}
	// The entity join admits Product only when the service declares its
	// wire message.
	svc.Schemas["shop.v1.Product"] = []SchemaFieldDef{
		{Name: "id", Kind: "string"},
		{Name: "contact_email", Kind: "string"},
		{Name: "currency", Kind: "string"},
		{Name: "status", Kind: "enum", TypeName: "shop.v1.ProductStatus"},
		{Name: "brand_id", Kind: "string"},
		{Name: "sku", Kind: "string"},
		{Name: "name", Kind: "string"},
		{Name: "price_cents", Kind: "int64"},
	}
	services := []ServiceDef{svc}
	factoryPath := filepath.Join(handlerDir, entityFactoryFile)

	type readings struct {
		entities  []EntityDef
		seed      *SeedProjection
		fixtures  map[string]string // table → the fixture plan's parent seed SQL
		factories string
	}
	readAll := func(t *testing.T) readings {
		t.Helper()
		var r readings
		var err error
		if r.entities, err = BuildSchemaEntities(projectDir, services); err != nil {
			t.Fatalf("BuildSchemaEntities: %v", err)
		}
		if len(r.entities) != 1 {
			t.Fatalf("precondition: one entity (Product), got %d", len(r.entities))
		}
		if r.seed, err = BuildSeedProjection(projectDir, seedplan.DefaultConfig()); err != nil || r.seed == nil {
			t.Fatalf("BuildSeedProjection: %v (nil=%v)", err, r.seed == nil)
		}
		fx, err := loadCRUDTestFixtures(projectDir, methods)
		if err != nil || fx == nil {
			t.Fatalf("loadCRUDTestFixtures: fx=%v err=%v", fx, err)
		}
		r.fixtures = map[string]string{}
		for table, plan := range fx.plans {
			r.fixtures[table] = plan.seedSQL
		}
		fx.close()
		if err := os.Remove(factoryPath); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := GenerateEntityFactories(projectDir, "example.com/test", services, nil); err != nil {
			t.Fatalf("GenerateEntityFactories: %v", err)
		}
		raw, err := os.ReadFile(factoryPath)
		if err != nil {
			t.Fatalf("GenerateEntityFactories wrote no %s: %v", entityFactoryFile, err)
		}
		r.factories = string(raw)
		return r
	}

	private := readAll(t)
	if len(private.fixtures) == 0 || private.fixtures["products"] == "" {
		t.Fatalf("precondition: the fixture model seeds the products entity's brand parent, got %v", private.fixtures)
	}

	end := shadowdb.BeginRun()
	defer end()
	start := shadowdb.CurrentStats()
	shared := readAll(t)
	stats := shadowdb.StatsSince(start)

	if stats.Applies != 1 {
		t.Errorf("one run applied the migrations %d times; every reader must share one apply", stats.Applies)
	}
	// Four readers: the entity join and the mock dataset read the model, the
	// fixture model and the factories each open a live copy.
	if stats.Reuses != 3 {
		t.Errorf("one run served %d readers from the shared apply, want 3 (four readers, one apply)", stats.Reuses)
	}
	if !reflect.DeepEqual(shared.entities, private.entities) {
		t.Errorf("the entity model differs when read from the shared apply:\n got %+v\nwant %+v", shared.entities, private.entities)
	}
	if !reflect.DeepEqual(shared.seed, private.seed) {
		t.Error("the frontend mock dataset differs when read from the shared apply")
	}
	if !reflect.DeepEqual(shared.fixtures, private.fixtures) {
		t.Errorf("the CRUD-test fixture plans differ when read from the shared apply:\n got %v\nwant %v", shared.fixtures, private.fixtures)
	}
	if shared.factories != private.factories {
		t.Errorf("factories_gen_test.go differs when generated from the shared apply:\n--- shared\n%s\n--- private\n%s", shared.factories, private.factories)
	}
}
