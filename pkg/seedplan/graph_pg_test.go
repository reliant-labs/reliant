package seedplan

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// graphMigration is a three-deep FK spine plus a table OFF the spine, so a
// closure that over-reaches is visible: seeding "cart_items" must bring in
// carts and products (its ancestors) and must NOT bring in shipments, which
// merely references carts.
const graphMigration = `
CREATE TABLE products (
    id TEXT PRIMARY KEY,
    sku TEXT NOT NULL,
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'live')),
    price_cents BIGINT NOT NULL CHECK (price_cents > 0)
);

CREATE TABLE carts (
    id TEXT PRIMARY KEY,
    label TEXT NOT NULL
);

CREATE TABLE cart_items (
    id TEXT PRIMARY KEY,
    cart_id TEXT NOT NULL REFERENCES carts(id),
    product_id TEXT NOT NULL REFERENCES products(id),
    quantity INTEGER NOT NULL CHECK (quantity > 0)
);

CREATE TABLE shipments (
    id TEXT PRIMARY KEY,
    cart_id TEXT NOT NULL REFERENCES carts(id)
);
`

// setupGraphDB returns a real postgres database with graphMigration applied —
// the shape a forge project's test database is in after
// app.NewMigratedTestDB(t).
func setupGraphDB(t *testing.T) *sql.DB {
	t.Helper()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(graphMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return db
}

// The headline property: SeedGraph is handed nothing but a migrated database
// and a table name, and the rows it writes are really there and really joined.
// No migrations directory, no shadow database, no generated SQL.
func TestSeedGraph_SeedsTheSpineIntoTheCallersDatabase(t *testing.T) {
	requirePG(t)
	db := setupGraphDB(t)

	g := SeedGraph(t, StdDB(db), "cart_items")

	// The root and both ancestors are present; the off-spine child is not.
	for _, want := range []string{"cart_items", "carts", "products"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + want).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", want, err)
		}
		if n != 1 {
			t.Errorf("%s has %d rows, want 1", want, n)
		}
	}
	var shipments int
	if err := db.QueryRow("SELECT count(*) FROM shipments").Scan(&shipments); err != nil {
		t.Fatalf("count shipments: %v", err)
	}
	if shipments != 0 {
		t.Errorf("shipments has %d rows, want 0 — it descends from carts and is not an ancestor of cart_items", shipments)
	}

	// The spine is CONNECTED: the seeded cart_item really points at the
	// seeded cart and product, which is the whole reason to seed a graph
	// rather than three unrelated rows.
	var cartID, productID string
	if err := db.QueryRow(`SELECT cart_id, product_id FROM cart_items`).Scan(&cartID, &productID); err != nil {
		t.Fatalf("read seeded cart_item: %v", err)
	}
	if cartID != g.PK("carts") {
		t.Errorf("cart_item.cart_id = %q, want the seeded cart %q", cartID, g.PK("carts"))
	}
	if productID != g.PK("products") {
		t.Errorf("cart_item.product_id = %q, want the seeded product %q", productID, g.PK("products"))
	}
}

// The handle has to describe the rows that were actually written, or a test
// asserting against it is asserting against fiction.
func TestSeedGraph_HandleMatchesTheRowsOnDisk(t *testing.T) {
	requirePG(t)
	db := setupGraphDB(t)

	g := SeedGraph(t, StdDB(db), "cart_items")

	var id, sku, status string
	var priceCents int64
	if err := db.QueryRow(`SELECT id, sku, status, price_cents FROM products`).Scan(&id, &sku, &status, &priceCents); err != nil {
		t.Fatalf("read seeded product: %v", err)
	}
	if got := g.PK("products"); got != id {
		t.Errorf("g.PK(\"products\") = %q, but the row on disk has id %q", got, id)
	}
	if got := g.Value("products", "sku"); got != sku {
		t.Errorf("g.Value(products.sku) = %q, but the row on disk has %q", got, sku)
	}
	// A CHECK-constrained column: the planner had to pick from the vocabulary
	// the constraint allows, and postgres accepted the row, so it did.
	if status != "draft" && status != "live" {
		t.Errorf("seeded status %q is outside the CHECK vocabulary", status)
	}
	if got := g.Value("products", "status"); got != status {
		t.Errorf("g.Value(products.status) = %q, but the row on disk has %q", got, status)
	}
}

// Rooting at a parent seeds the parent's spine and leaves the child table
// empty — the setup a CREATE-path test needs, where the child is what the RPC
// under test is supposed to make.
func TestSeedGraph_RootAtParentLeavesTheChildEmpty(t *testing.T) {
	requirePG(t)
	db := setupGraphDB(t)

	g := SeedGraph(t, StdDB(db), "carts")

	if got := g.Tables(); len(got) != 1 || got[0] != "carts" {
		t.Errorf("graph rooted at carts seeded %v, want just [carts] — carts has no ancestors", got)
	}
	var items int
	if err := db.QueryRow("SELECT count(*) FROM cart_items").Scan(&items); err != nil {
		t.Fatalf("count cart_items: %v", err)
	}
	if items != 0 {
		t.Errorf("cart_items has %d rows, want 0", items)
	}
	if g.PK("carts") == "" {
		t.Error("g.PK(\"carts\") is empty; the root's id must be readable")
	}
}

// Naming a table that does not exist is a test-authoring mistake, and the
// error has to say so with the available names rather than failing inside a
// SQL driver.
func TestSeedGraph_UnknownTableNamesTheAlternatives(t *testing.T) {
	requirePG(t)
	db := setupGraphDB(t)

	_, err := SeedGraphErr(context.Background(), StdDB(db), "carts_typo")
	if err == nil {
		t.Fatal("seeding a nonexistent table succeeded; want an error")
	}
	for _, want := range []string{"carts_typo", "cart_items", "products"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The seeded values come from the planner, so the same schema seeds the same
// spine twice — a test that reads a seeded value is reading a stable one.
func TestSeedGraph_IsDeterministicAcrossDatabases(t *testing.T) {
	requirePG(t)
	first := SeedGraph(t, StdDB(setupGraphDB(t)), "cart_items")
	second := SeedGraph(t, StdDB(setupGraphDB(t)), "cart_items")

	for _, tbl := range []string{"cart_items", "carts", "products"} {
		if first.PK(tbl) != second.PK(tbl) {
			t.Errorf("%s pk differs across runs: %q vs %q", tbl, first.PK(tbl), second.PK(tbl))
		}
	}
	if first.Value("cart_items", "quantity") != second.Value("cart_items", "quantity") {
		t.Errorf("quantity differs across runs: %q vs %q",
			first.Value("cart_items", "quantity"), second.Value("cart_items", "quantity"))
	}
}
