package crud

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// shipment is a row whose lifecycle a custom RPC owns. status and shipped_at
// are read-only ON THE WIRE (forge:read-only), which is deliberately NOT a
// storage fact: their struct tags are plain, because the code that owns them
// writes them through this same repository. The other columns cover every
// way a write can leave a column unset — a forge:immutable column
// (,skipupdate), a GENERATED one, and the managed timestamps.
type shipment struct {
	bun.BaseModel `bun:"table:shipments,alias:shipments"`

	ID        string     `bun:"id,pk"`
	Title     string     `bun:"title,notnull"`
	Status    string     `bun:"status,notnull"`
	ShippedAt *time.Time `bun:"shipped_at"`
	Qty       int64      `bun:"qty,notnull"`
	Total     int64      `bun:"total,notnull,nullzero,skipupdate" forge:"generated"`
	OwnerID   string     `bun:"owner_id,notnull,skipupdate"`
	CreatedAt time.Time  `bun:"created_at,notnull,default:now()"`
	UpdatedAt time.Time  `bun:"updated_at,notnull,default:now()"`
}

// shipmentReadOnly is what the generated op carries for this entity.
var shipmentReadOnly = []string{"status", "shipped_at"}

func shipmentSchema(ctx context.Context, t *testing.T, db orm.Context) {
	t.Helper()
	if _, err := db.Exec(ctx, `
CREATE TABLE shipments (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    shipped_at TIMESTAMPTZ,
    qty BIGINT NOT NULL DEFAULT 0,
    total BIGINT GENERATED ALWAYS AS (qty * 10) STORED NOT NULL,
    owner_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

// seedShipped creates a row and moves it to shipped the way a custom RPC
// does: a repo-level masked write naming the read-only columns. That write
// landing is itself the first assertion — Preserve and the op's refusal must
// not have reached the repository's masked path.
func seedShipped(ctx context.Context, t *testing.T, db orm.Context, repo *Repo[shipment]) (*shipment, time.Time) {
	t.Helper()
	if err := repo.Create(ctx, db, &shipment{ID: "s1", Title: "crate", Qty: 2, OwnerID: "acme"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	shippedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := repo.UpdateMasked(ctx, db, &shipment{ID: "s1", Status: "shipped", ShippedAt: &shippedAt},
		[]string{"status", "shipped_at"}); err != nil {
		t.Fatalf("app-code masked write of the read-only columns: %v", err)
	}
	stored := readShipment(ctx, t, db)
	if stored.Status != "shipped" || stored.ShippedAt == nil || !stored.ShippedAt.Equal(shippedAt) {
		t.Fatalf("app-code masked write did not land: %+v", stored)
	}
	return stored, shippedAt
}

func readShipment(ctx context.Context, t *testing.T, db orm.Context) *shipment {
	t.Helper()
	got := &shipment{}
	if err := db.Bun().NewSelect().Model(got).Where("id = ?", "s1").Scan(ctx); err != nil {
		t.Fatalf("read back: %v", err)
	}
	return got
}

// Preserve holds the named columns out of one full replace, and the entity
// comes back carrying the row as stored: the held-back columns, the
// ,skipupdate column, the GENERATED column recomputed from what this write
// changed, and created_at — every column the SET did not name.
func TestPreserve_FullReplaceLeavesColumnsAndReadsThemBack(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	shipmentSchema(ctx, t, db)
	repo := NewRepo[shipment]()
	before, shippedAt := seedShipped(ctx, t, db, repo)

	// A client round-trip that did not echo the read-only fields: status
	// empty, shipped_at nil — and owner/total/created_at zero as well.
	repl := &shipment{ID: "s1", Title: "crate-renamed", Qty: 5}
	if err := repo.Update(ctx, db, repl, Preserve(shipmentReadOnly...)); err != nil {
		t.Fatalf("update: %v", err)
	}

	stored := readShipment(ctx, t, db)
	if stored.Status != "shipped" || stored.ShippedAt == nil || !stored.ShippedAt.Equal(shippedAt) {
		t.Errorf("preserved columns were written: status=%q shipped_at=%v", stored.Status, stored.ShippedAt)
	}
	if stored.Title != "crate-renamed" || stored.Qty != 5 {
		t.Errorf("ordinary columns must still be written: %+v", stored)
	}

	// The entity reports the stored row, not the zeros it was built with.
	if repl.Status != "shipped" || repl.ShippedAt == nil || !repl.ShippedAt.Equal(shippedAt) {
		t.Errorf("preserved columns not read back: status=%q shipped_at=%v", repl.Status, repl.ShippedAt)
	}
	if repl.OwnerID != "acme" {
		t.Errorf("owner_id = %q, want the stored acme — a ,skipupdate column must be read back", repl.OwnerID)
	}
	if repl.Total != 50 {
		t.Errorf("total = %d, want 50 — the GENERATED column must be read back after the write that changed qty", repl.Total)
	}
	if !repl.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("created_at = %v, want the stored %v", repl.CreatedAt, before.CreatedAt)
	}
}

// A `// forge:generated` field is read-only on the wire, so its GENERATED
// column rides in the generated op's ReadOnly and therefore in its Preserve
// list. Preserve must accept it as the no-op it is — postgres computes the
// column and no full replace writes it — and the recomputed value must still
// be read back. Refusing it as "not updatable" would fail every client
// Update of every entity that uses the marker.
func TestPreserve_GeneratedColumnIsANoOp(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	shipmentSchema(ctx, t, db)
	repo := NewRepo[shipment]()
	seedShipped(ctx, t, db, repo)

	repl := &shipment{ID: "s1", Title: "crate", Qty: 7, Total: 1}
	if err := repo.Update(ctx, db, repl, Preserve(append([]string{"total"}, shipmentReadOnly...)...)); err != nil {
		t.Fatalf("Preserve naming a GENERATED column must be accepted: %v", err)
	}
	if repl.Total != 70 {
		t.Errorf("total = %d, want 70 — the GENERATED column recomputed from qty, read back", repl.Total)
	}
	if stored := readShipment(ctx, t, db); stored.Total != 70 || stored.Qty != 7 {
		t.Errorf("stored row = %+v, want qty 7 and total 70", stored)
	}
}

// Without Preserve, a full replace still writes the read-only columns: the
// policy is the CALLER's, and app code doing a full replace keeps the
// semantics it always had.
func TestUpdate_WithoutPreserveWritesEveryUpdatableColumn(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	shipmentSchema(ctx, t, db)
	repo := NewRepo[shipment]()
	seedShipped(ctx, t, db, repo)

	e := readShipment(ctx, t, db)
	e.Status = "returned"
	if err := repo.Update(ctx, db, e); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := readShipment(ctx, t, db).Status; got != "returned" {
		t.Errorf("status = %q, want returned — app code's full replace must write it", got)
	}
}

// A masked write reads back every column it did not name, so a caller that
// sent a sparse entity holds the full stored row afterwards.
func TestUpdateMasked_ReadsBackUnmaskedColumns(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	shipmentSchema(ctx, t, db)
	repo := NewRepo[shipment]()
	seedShipped(ctx, t, db, repo)

	sparse := &shipment{ID: "s1", Qty: 7, Status: "client-guess"}
	if err := repo.UpdateMasked(ctx, db, sparse, []string{"qty"}); err != nil {
		t.Fatalf("masked update: %v", err)
	}
	if sparse.Status != "shipped" || sparse.Title != "crate" || sparse.OwnerID != "acme" {
		t.Errorf("unmasked columns not read back: %+v", sparse)
	}
	if sparse.Total != 70 {
		t.Errorf("total = %d, want 70 recomputed from the masked qty", sparse.Total)
	}
	if got := readShipment(ctx, t, db).Status; got != "shipped" {
		t.Errorf("status = %q — an unmasked column was written", got)
	}
}

// A misspelled Preserve must not silently drop its protection.
func TestPreserve_UnknownColumnIsAnError(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	shipmentSchema(ctx, t, db)
	repo := NewRepo[shipment]()
	seedShipped(ctx, t, db, repo)

	err := repo.Update(ctx, db, &shipment{ID: "s1", Title: "x"}, Preserve("stauts"))
	if err == nil {
		t.Fatal("Preserve naming no column must fail the write")
	}
	if got := readShipment(ctx, t, db); got.Title != "crate" || got.Status != "shipped" {
		t.Errorf("a refused write touched the row: %+v", got)
	}
}

// RETURNING switches Bun from Exec to a scan; zero rows must still read as
// the missing row it is.
func TestUpdate_ReadBackOnMissingRowIsNotFound(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	shipmentSchema(ctx, t, db)
	repo := NewRepo[shipment]()

	ghost := &shipment{ID: "nope", Title: "x", Status: "kept"}
	if err := repo.Update(ctx, db, ghost, Preserve(shipmentReadOnly...)); !errors.Is(err, orm.ErrNoRows) {
		t.Fatalf("update of a missing row: err = %v, want orm.ErrNoRows", err)
	}
	if err := repo.UpdateMasked(ctx, db, ghost, []string{"title"}); !errors.Is(err, orm.ErrNoRows) {
		t.Fatalf("masked update of a missing row: err = %v, want orm.ErrNoRows", err)
	}
	if ghost.Status != "kept" {
		t.Errorf("a write that matched no row rewrote the entity: status = %q", ghost.Status)
	}
}

// shipmentReq stands in for the generated Update<Entity>Request: the whole
// entity, plus the update_mask paths.
type shipmentReq struct {
	Shipment *shipment
	Mask     []string
}
type shipmentResp struct{ Shipment *shipment }

// shipmentUpdateOp is wired the way the generated crud<Update>Op is:
// ReadOnly from the wire markers, Persist preserving the same columns,
// PersistMasked straight onto the repository.
func shipmentUpdateOp(db orm.Context, repo *Repo[shipment]) UpdateOp[shipmentReq, shipmentResp, *shipment] {
	return UpdateOp[shipmentReq, shipmentResp, *shipment]{
		EntityLower:    "shipment",
		EntityFieldLow: "shipment",
		ReadOnly:       shipmentReadOnly,
		Entity: func(_ context.Context, r *shipmentReq) (*shipment, error) {
			if r.Shipment == nil {
				return nil, ErrEntityRequired
			}
			return r.Shipment, nil
		},
		Persist: func(ctx context.Context, e *shipment, _ ...orm.QueryOption) error {
			return repo.Update(ctx, db, e, Preserve(shipmentReadOnly...))
		},
		Mask: func(r *shipmentReq) []string { return r.Mask },
		PersistMasked: func(ctx context.Context, e *shipment, fields []string, _ ...orm.QueryOption) error {
			return repo.UpdateMasked(ctx, db, e, fields)
		},
		Pack: func(e *shipment) (*shipmentResp, error) { return &shipmentResp{Shipment: e}, nil },
	}
}

// The client path end to end against postgres: neither a full replace nor
// a mask lets a client write a read-only column, and every response
// reports the stored value instead of the request's.
func TestHandleUpdate_ReadOnlyColumnsThroughRepo(t *testing.T) {
	ctx := context.Background()
	db := newRepoTestDB(t)
	shipmentSchema(ctx, t, db)
	repo := NewRepo[shipment]()
	_, shippedAt := seedShipped(ctx, t, db, repo)
	h := HandleUpdate(shipmentUpdateOp(db, repo))

	wantShipped := func(label string, got *shipment) {
		t.Helper()
		if got.Status != "shipped" || got.ShippedAt == nil || !got.ShippedAt.Equal(shippedAt) {
			t.Errorf("%s: status=%q shipped_at=%v, want shipped @ %v", label, got.Status, got.ShippedAt, shippedAt)
		}
	}

	// Maskless, read-only fields omitted (the reset case).
	resp, err := h(ctx, connect.NewRequest(&shipmentReq{Shipment: &shipment{ID: "s1", Title: "a", Qty: 1}}))
	if err != nil {
		t.Fatalf("maskless update: %v", err)
	}
	wantShipped("stored after maskless update omitting them", readShipment(ctx, t, db))
	wantShipped("response to maskless update omitting them", resp.Msg.Shipment)

	// Maskless, read-only fields carrying values (the bypass case).
	if _, err := h(ctx, connect.NewRequest(&shipmentReq{
		Shipment: &shipment{ID: "s1", Title: "b", Qty: 1, Status: "delivered"},
	})); err != nil {
		t.Fatalf("maskless update carrying read-only values: %v", err)
	}
	wantShipped("stored after maskless update carrying them", readShipment(ctx, t, db))

	// A mask naming one.
	_, err = h(ctx, connect.NewRequest(&shipmentReq{
		Shipment: &shipment{ID: "s1", Status: "delivered"},
		Mask:     []string{"status"},
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument || ce.Meta().Get(svcerr.ReasonHeader) != ReasonUnknownField {
		t.Fatalf("mask naming a read-only column: err = %v, want InvalidArgument / %s", err, ReasonUnknownField)
	}
	wantShipped("stored after a refused mask", readShipment(ctx, t, db))

	// A mask of editable columns: written, and the response reports the
	// stored read-only values rather than the request's.
	resp, err = h(ctx, connect.NewRequest(&shipmentReq{
		Shipment: &shipment{ID: "s1", Title: "c", Status: "client-guess"},
		Mask:     []string{"title"},
	}))
	if err != nil {
		t.Fatalf("masked update of an editable column: %v", err)
	}
	if got := readShipment(ctx, t, db).Title; got != "c" {
		t.Errorf("title = %q, want c", got)
	}
	wantShipped("response to an editable mask", resp.Msg.Shipment)
}
