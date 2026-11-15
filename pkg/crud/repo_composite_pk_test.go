package crud

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
)

// documentCounter mirrors the motivating shape from the bug report: a
// composite PRIMARY KEY (company_id, kind) where company_id is ALSO a
// foreign key. Both key columns are strings, so pre-fix code (which read
// tbl.PKs[0] and set pkIsString unconditionally) misclassified company_id
// as a ULID-generatable string PK.
type documentCounter struct {
	bun.BaseModel `bun:"table:document_counters,alias:document_counters"`

	CompanyID string `bun:"company_id,pk"`
	Kind      string `bun:"kind,pk"`
	NextValue int64  `bun:"next_value,notnull"`
}

func createDocumentCountersTable(t *testing.T, db orm.Context) {
	t.Helper()
	_, err := db.Bun().NewCreateTable().Model((*documentCounter)(nil)).
		IfNotExists().Exec(context.Background())
	if err != nil {
		t.Fatalf("create document_counters table: %v", err)
	}
}

// TestEnsureMeta_CompositePK_DisablesULIDAndCursor is the hermetic unit for
// the fix: a composite-PK model must NOT classify off tbl.PKs[0] the way a
// single-PK model does. No live database is needed — ensureMeta's
// classification never touches one.
func TestEnsureMeta_CompositePK_DisablesULIDAndCursor(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}
	repo := NewRepo[documentCounter]()
	repo.ensureMeta(db)

	if repo.m.pkIsString {
		t.Error("composite PK must not set pkIsString — it would ULID-generate over one key column (possibly a foreign key) on Create")
	}
	if repo.m.pkColumn != "" {
		t.Errorf("composite PK must leave pkColumn empty (the existing PK-cursor-pagination-disabled escape hatch), got %q", repo.m.pkColumn)
	}
	if repo.m.pkAutoInc {
		t.Error("composite PK must not set pkAutoInc")
	}
}

// TestCreate_CompositePK_DoesNotULIDOverwriteFirstKeyColumn is the runtime
// proof against real embedded postgres: Create on a composite-PK entity
// must leave BOTH caller-supplied key columns untouched. Pre-fix, forge
// read tbl.PKs[0] (company_id), saw a string type, and ULID-generated over
// it whenever it looked empty on Create — and even when non-empty, the
// entity was reporting pkIsString=true for a column that, in a real
// schema, is also a foreign key: exactly the corruption this guards
// against. This test pins the caller-supplied value surviving Create
// unmodified.
func TestCreate_CompositePK_DoesNotULIDOverwriteFirstKeyColumn(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createDocumentCountersTable(t, db)

	repo := NewRepo[documentCounter]()

	row := &documentCounter{CompanyID: "company-42", Kind: "invoice", NextValue: 1}
	if err := repo.Create(ctx, db, row); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if row.CompanyID != "company-42" {
		t.Errorf("Create overwrote the composite key's first column: got %q, want %q (a real schema would have this column ALSO be a foreign key, so a ULID here silently corrupts the reference)", row.CompanyID, "company-42")
	}
	if row.Kind != "invoice" {
		t.Errorf("Create overwrote the composite key's second column: got %q, want %q", row.Kind, "invoice")
	}
}

// normalPKRow is the contrasting case: a single-string-PK entity, which
// must KEEP generating a ULID on Create when the caller leaves the PK
// empty. This is the behavior the composite-PK fix must not regress.
type normalPKRow struct {
	bun.BaseModel `bun:"table:normal_pk_rows,alias:normal_pk_rows"`

	ID   string `bun:"id,pk"`
	Name string `bun:"name,notnull"`
}

func createNormalPKRowsTable(t *testing.T, db orm.Context) {
	t.Helper()
	_, err := db.Bun().NewCreateTable().Model((*normalPKRow)(nil)).
		IfNotExists().Exec(context.Background())
	if err != nil {
		t.Fatalf("create normal_pk_rows table: %v", err)
	}
}

// TestCreate_SingleStringPK_StillGeneratesULID is the contrast case: an
// ordinary single-column string PK must still get a ULID minted on Create
// when left empty — the composite-PK guard must be surgical, not a
// blanket regression of ULID generation.
func TestCreate_SingleStringPK_StillGeneratesULID(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createNormalPKRowsTable(t, db)

	repo := NewRepo[normalPKRow]()

	row := &normalPKRow{Name: "alpha"} // ID left empty on purpose
	if err := repo.Create(ctx, db, row); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if row.ID == "" {
		t.Error("Create must ULID-generate a single-column string PK left empty by the caller")
	}
}

// A composite-key entity is a full store (reliant P5b, F12.3): read,
// rewritten, upserted and deleted by its WHOLE key. Every one of these used
// to address the row by a single pkColumn — "" for a composite key — so
// the only safe verbs were Create and List.
func TestCompositePK_FullLifecycleByTheWholeKey(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	createDocumentCountersTable(t, db)
	repo := NewRepo[documentCounter]()

	for _, row := range []*documentCounter{
		{CompanyID: "c1", Kind: "invoice", NextValue: 1},
		{CompanyID: "c1", Kind: "estimate", NextValue: 10},
		{CompanyID: "c2", Kind: "invoice", NextValue: 100},
	} {
		if err := repo.Create(ctx, db, row); err != nil {
			t.Fatalf("Create %+v: %v", row, err)
		}
	}

	got, err := repo.GetByPK(ctx, db, &documentCounter{CompanyID: "c1", Kind: "estimate"})
	if err != nil || got.NextValue != 10 {
		t.Fatalf("GetByPK(c1, estimate) = %+v, %v; want next_value 10", got, err)
	}
	if _, err := repo.GetByPK(ctx, db, &documentCounter{CompanyID: "c3", Kind: "invoice"}); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("GetByPK of a missing key: err = %v, want orm.ErrNoRows", err)
	}

	got.NextValue = 11
	if err := repo.Update(ctx, db, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := repo.UpdateMasked(ctx, db, &documentCounter{CompanyID: "c2", Kind: "invoice", NextValue: 101}, []string{"next_value"}); err != nil {
		t.Fatalf("UpdateMasked: %v", err)
	}
	if err := repo.Upsert(ctx, db, &documentCounter{CompanyID: "c1", Kind: "invoice", NextValue: 2}); err != nil {
		t.Fatalf("Upsert on the composite key: %v", err)
	}
	want := map[string]int64{"c1/invoice": 2, "c1/estimate": 11, "c2/invoice": 101}
	rows, err := repo.List(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("List = %d rows, want %d (an Update or Upsert that matched by one key column would touch, or add, others)", len(rows), len(want))
	}
	for _, r := range rows {
		if w := want[r.CompanyID+"/"+r.Kind]; r.NextValue != w {
			t.Errorf("%s/%s next_value = %d, want %d", r.CompanyID, r.Kind, r.NextValue, w)
		}
	}

	if err := repo.DeleteByPK(ctx, db, &documentCounter{CompanyID: "c1", Kind: "invoice"}); err != nil {
		t.Fatalf("DeleteByPK: %v", err)
	}
	if n, _ := repo.Count(ctx, db); n != 2 {
		t.Errorf("after DeleteByPK, %d rows remain, want 2 (only the one key)", n)
	}
	if err := repo.DeleteByPK(ctx, db, &documentCounter{CompanyID: "c1", Kind: "invoice"}); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("DeleteByPK of a missing key: err = %v, want orm.ErrNoRows", err)
	}
	if err := repo.Update(ctx, db, &documentCounter{CompanyID: "zz", Kind: "none", NextValue: 1}); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Update of a missing key: err = %v, want orm.ErrNoRows", err)
	}
}

// The composite-key by-PK statements bind every key column (F12.8's rule
// for the single-key ones): one prepared statement per shape, however many
// distinct keys pass through.
func TestCompositePK_ByKeyStatementsBindTheirKey(t *testing.T) {
	db, sqldb := newPgxRepoTestDB(t)
	ctx := context.Background()
	createDocumentCountersTable(t, db)
	repo := NewRepo[documentCounter]()

	const n = 15
	for i := range n {
		if err := repo.Create(ctx, db, &documentCounter{CompanyID: fmt.Sprintf("c%d", i), Kind: "invoice", NextValue: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	preparedStatements(t, sqldb)
	before := preparedStatements(t, sqldb)
	for i := range n {
		key := &documentCounter{CompanyID: fmt.Sprintf("c%d", i), Kind: "invoice"}
		row, err := repo.GetByPK(ctx, db, key)
		if err != nil {
			t.Fatalf("GetByPK: %v", err)
		}
		row.NextValue++
		if err := repo.Update(ctx, db, row); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if err := repo.DeleteByPK(ctx, db, key); err != nil {
			t.Fatalf("DeleteByPK: %v", err)
		}
	}
	if got := preparedStatements(t, sqldb) - before; got > 3 {
		t.Fatalf("%d by-key calls over %d distinct keys prepared %d statements, want at most 3 (one per shape)", 3*n, n, got)
	}
}
