---
name: flow
description: "Flow tests — hand-write a full multi-entity RPC test: seed the FK spine with seedplan.SeedGraph, drive the real service against a migrated DB, assert the derived result, and cover the negative/gap case."
---

# Flow Tests

## What a flow test is

A **flow test** exercises a custom RPC that spans several entities end to end,
in-process, against a real migrated database. It sits at the **integration**
tier: real service, real DB, real internal collaborators — no running stack, no
network. It is the right tool for a domain operation like `PlaceOrder` that
reads a cart, prices each line, and writes an order + order items — logic no
single-entity CRUD test reaches.

Flow tests are DB-backed, so they carry the build tag:

```go
//go:build integration
```

Run them with `task test:integration`.

## The harness (already scaffolded)

Three generated helpers give you the real stack in three lines:

| Helper | What it gives |
|--------|---------------|
| `app.NewMigratedTestDB(t)` | a real-postgres `orm.Context` with `db/migrations` applied |
| `app.NewTest<Service>(t, app.WithDB(db))` | the real service, wired to that DB |
| `app.AuthedContext(t, opts...)` | a context carrying test claims (`testkit.WithRoles`, `testkit.WithOrgID`) |
| `seedplan.SeedGraph(t, db, "<table>")` | seeds `<table>` + its FK ancestors as one connected spine, returns a `*SeededGraph` |

The server mounts bare and the test controls the principal entirely through
`AuthedContext` — pass whichever claim values (`testkit.WithUserID`,
`testkit.WithRoles`, `testkit.WithOrgID`) the handler under test reads.

## Seed the FK spine — use `seedplan.SeedGraph`

A flow needs a whole foreign-key spine present before the RPC runs — a brand, a
product, a variant, a cart, a cart item. That spine is **referential setup**, not
the subject under test, so don't hand-chain a `CreateX` sequence:

```go
import "github.com/reliant-labs/forge/pkg/seedplan"

db := app.NewMigratedTestDB(t)
g := seedplan.SeedGraph(t, db, "cart_items")
```

`SeedGraph` reads the schema **out of the database you hand it** — the one
`NewMigratedTestDB` just migrated — works out which tables `cart_items`
transitively depends on, and inserts one connected happy-path spine: row 0 of
every table references row 0 of its parents. It returns a `*SeededGraph`:

- `g.PK("carts")` — the seeded cart id, to pass to the RPC.
- `g.PK("variants")` — the variant the cart item is on.
- `g.Value("cart_items", "quantity")` — the seeded quantity, so an assertion
  reads the **real** seeded input instead of hard-coding it.
- `g.Tables()` — what actually got seeded, when you're not sure of the shape.

Every seeded row satisfies the migrations' CHECK / enum / `char_length` / range
constraints, because the values come from forge's seed planner — the same one
behind `forge db seed` — run **now**, against the schema this database actually
has. Add a column or a constraint and the next test run plans around it; there
is nothing to regenerate and nothing to keep in sync.

`SeedGraph` seeds **ancestors, never descendants**. The closure answers "what
must already exist for this row to be insertable", which is exactly what a
foreign key makes mandatory. Nothing requires a row to have children, so a
child table is never dragged in.

**Root at the deepest table you want pre-seeded.** To test a CREATE path, root at
the *parent* (`SeedGraph(t, db, "carts")` seeds the spine; you then create the
cart item via its RPC). To test a read/derived/update path, root at the leaf
(`"cart_items"`) so the whole graph is present.

**Seed the spine with `SeedGraph`, insert the crux by hand.** The value your
assertion turns on — here the variant's **price type**, a `prices` row that is a
*descendant* of `variants`, not an ancestor, so it is NOT part of the spine — is a
distinct row you insert per case, so each test states its own precondition (see
the two tests below). A compact direct-`INSERT` is exactly right for that one row.

## Worked example: PlaceOrder

`PlaceOrder(cart_id)` reads the cart's items, looks up each variant's **one-time**
price, and writes an order whose `subtotal_cents` is the sum of `unit_price ×
quantity`. A variant with only a subscription price has nothing to charge, so
checkout must refuse with `FailedPrecondition`.

### Positive: the derived total is correct

```go
//go:build integration

package checkout_test

import (
	"context"
	"strconv"
	"testing"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/seedplan"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "example.com/shop/gen/checkout/v1"
	"example.com/shop/pkg/app"
)

func TestPlaceOrder_DerivesSubtotalFromOneTimePrices(t *testing.T) {
	db := app.NewMigratedTestDB(t)
	g := seedplan.SeedGraph(t, db, "cart_items") // brand → product → variant → cart → cart_item

	// The price TYPE is the domain crux, so seed it explicitly: give the spine's
	// variant a ONE-TIME price. (prices descend from variants, so they are not
	// part of the spine SeedGraph builds.)
	const unitCents = 2500
	seedPrice(t, db, g.PK("variants"), "one_time", unitCents)

	svc := app.NewTestCheckout(t, app.WithDB(db))
	ctx := app.AuthedContext(t)

	resp, err := svc.PlaceOrder(ctx, connect.NewRequest(&pb.PlaceOrderRequest{
		CartId: g.PK("carts"),
	}))
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	// Assert the DERIVED values against the REAL seeded quantity, read back from
	// the graph rather than hard-coded.
	qty, err := strconv.Atoi(g.Value("cart_items", "quantity"))
	if err != nil {
		t.Fatalf("seeded quantity: %v", err)
	}
	order := resp.Msg.GetOrder()
	if got, want := order.GetSubtotalCents(), int64(unitCents*qty); got != want {
		t.Errorf("order.subtotal_cents = %d, want %d (unit %d × qty %d)", got, want, unitCents, qty)
	}
	items := order.GetItems()
	if len(items) != 1 {
		t.Fatalf("order has %d items, want 1", len(items))
	}
	if got := items[0].GetUnitPriceCents(); got != int64(unitCents) {
		t.Errorf("order_item.unit_price_cents = %d, want %d", got, unitCents)
	}
}
```

### Negative: the gap case is not optional

A flow test that only proves the happy path is half a test. Assert the failure
mode too — here, a variant with **only** a subscription price:

```go
func TestPlaceOrder_SubscriptionOnlyVariant_FailsPrecondition(t *testing.T) {
	db := app.NewMigratedTestDB(t)
	g := seedplan.SeedGraph(t, db, "cart_items")

	// The spine's variant gets ONLY a subscription price — nothing one-time to
	// charge, so checkout must refuse rather than guess a total.
	seedPrice(t, db, g.PK("variants"), "subscription", 999)

	svc := app.NewTestCheckout(t, app.WithDB(db))
	ctx := app.AuthedContext(t)

	_, err := svc.PlaceOrder(ctx, connect.NewRequest(&pb.PlaceOrderRequest{
		CartId: g.PK("carts"),
	}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("PlaceOrder on a subscription-only variant: code = %v, want FailedPrecondition (err: %v)", got, err)
	}
}

// seedPrice inserts one price row for a variant — the domain value each flow
// case controls. A tiny hand-written insert is the right tool here: the value
// under assertion must be exact, not synthesized.
func seedPrice(t *testing.T, db orm.Context, variantID, kind string, cents int64) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`INSERT INTO prices (id, variant_id, kind, unit_amount_cents) VALUES ($1, $2, $3, $4)`,
		ulid.Make().String(), variantID, kind, cents); err != nil {
		t.Fatalf("seed %s price: %v", kind, err)
	}
}
```

## Rules

- **Seed the spine with `seedplan.SeedGraph`, insert the crux by hand.**
  `SeedGraph` builds the FK ancestor graph for a root table; a separate one- or
  two-row insert seeds the case-specific values your assertion turns on.
- **Assert derived values, not echoes.** The interesting output is what the RPC
  *computed* (`subtotal_cents`, `unit_price_cents`) from the seeded input — not
  that a field you set came back unchanged.
- **Always cover a negative.** The `FailedPrecondition` (or `NotFound` /
  `InvalidArgument`) path is where the interesting logic lives.
- **Build tag `//go:build integration`.** Flow tests touch a real DB; keep them
  out of the default unit run.
- **One graph per test.** `app.NewMigratedTestDB(t)` gives each test its own
  isolated database — no shared state, no ordering assumptions.

## Related: typed single-entity factories

When you need **one valid row of a specific entity** rather than a whole spine,
`New<Entity>(t, db, overrides…)` (in the owning handler package's
`factories_gen_test.go`) is the typed tool: it inserts one MINIMAL row — every
NOT NULL column without a DEFAULT and every FK parent satisfied, everything
else left to its DEFAULT or NULL, every CHECK honoured — gives each call a fresh
primary key, and returns the typed `*db.<Entity>`. Use `SeedGraph` for "a whole
referential spine addressed by table name", `New<Entity>` for "a valid Order I
can override two fields on", and `New<CreateRequest>(t, db, variant)` for "a
create request the schema accepts, parents seeded".

See also: `testing` (the pyramid + harness overview), `testing/integration`
(the build-tag discipline), `testing/e2e` (full-stack flows over the network).
