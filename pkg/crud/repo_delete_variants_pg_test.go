package crud

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Delete and DeleteByPK take a different statement per soft-delete flavour: a
// legacy TEXT deleted_at stamped with CURRENT_TIMESTAMP, Bun's native
// ,soft_delete stamped with a bound time, a hard DELETE. Each must tombstone
// (or remove) exactly the addressed LIVE row, and answer a miss — a missing id,
// an already-tombstoned one — with the same NotFound.

// hookedTombstone has a Bun model hook, which turns parameter binding off for
// the repo (hasModelHooks): the soft-delete UPDATE then goes through Bun's own
// rewriting of NewDelete rather than the repo's bound statement.
type hookedTombstone struct {
	bun.BaseModel `bun:"table:hooked_tombstones,alias:hooked_tombstones"`

	ID        string     `bun:"id,pk"`
	Name      string     `bun:"name,notnull"`
	DeletedAt *time.Time `bun:"deleted_at,soft_delete,nullzero"`
}

func (*hookedTombstone) BeforeAppendModel(context.Context, bun.Query) error { return nil }

// tombstoneKey is a composite-key entity with a native soft delete, for
// DeleteByPK's soft-delete arm.
type tombstoneKey struct {
	bun.BaseModel `bun:"table:tombstone_keys,alias:tombstone_keys"`

	Tenant    string     `bun:"tenant,pk"`
	Slug      string     `bun:"slug,pk"`
	Title     string     `bun:"title,notnull"`
	DeletedAt *time.Time `bun:"deleted_at,soft_delete,nullzero"`
}

// legacyKey is the composite-key twin with the legacy TEXT soft delete.
type legacyKey struct {
	bun.BaseModel `bun:"table:legacy_keys,alias:legacy_keys"`

	Tenant    string  `bun:"tenant,pk"`
	Slug      string  `bun:"slug,pk"`
	DeletedAt *string `bun:"deleted_at" forge:"soft-delete"`
}

func execAll(t *testing.T, db orm.Context, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func rawInt(t *testing.T, db orm.Context, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestRepoDelete_LegacyTextSoftDeleteStampsOnlyLiveRows(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	execAll(t, db, `CREATE TABLE legacy_tombstones (id TEXT PRIMARY KEY, name TEXT NOT NULL, deleted_at TEXT)`)
	repo := NewRepo[legacyTombstone]()
	for _, id := range []string{"a", "b"} {
		if err := repo.Create(ctx, db, &legacyTombstone{ID: id, Name: id}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}

	if err := repo.Delete(ctx, db, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := rawInt(t, db, `SELECT count(*) FROM legacy_tombstones WHERE id = 'a' AND deleted_at IS NOT NULL`); n != 1 {
		t.Fatalf("the row was not tombstoned in place (%d tombstones)", n)
	}
	if n := rawInt(t, db, `SELECT count(*) FROM legacy_tombstones WHERE id = 'b' AND deleted_at IS NULL`); n != 1 {
		t.Fatal("Delete touched a row it was not asked to")
	}
	// Already tombstoned and never existed answer alike.
	for _, id := range []string{"a", "missing"} {
		err := repo.Delete(ctx, db, id)
		if !svcerr.IsNotFound(err) || !errors.Is(err, orm.ErrNoRows) {
			t.Errorf("Delete(%q) = %v, want NotFound", id, err)
		}
	}
	if _, err := repo.Get(ctx, db, "a"); !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Get of a tombstoned row = %v, want not found", err)
	}
}

func TestRepoDelete_NativeSoftDeleteWithModelHooks(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	execAll(t, db, `CREATE TABLE hooked_tombstones (id TEXT PRIMARY KEY, name TEXT NOT NULL, deleted_at TIMESTAMPTZ)`)
	repo := NewRepo[hookedTombstone]()
	if err := repo.Create(ctx, db, &hookedTombstone{ID: "h", Name: "hooked"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if repo.m.bindParams {
		t.Fatal("precondition: a model hook must turn the repo's parameter binding off")
	}
	if err := repo.Delete(ctx, db, "h"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := rawInt(t, db, `SELECT count(*) FROM hooked_tombstones WHERE deleted_at IS NOT NULL`); n != 1 {
		t.Fatalf("native soft delete did not stamp (%d tombstones)", n)
	}
	if err := repo.Delete(ctx, db, "h"); !svcerr.IsNotFound(err) {
		t.Errorf("a second Delete = %v, want NotFound", err)
	}
}

func TestRepoDeleteByPK_EveryFlavourAndTheMiss(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	execAll(t, db,
		`CREATE TABLE tombstone_keys (tenant TEXT, slug TEXT, title TEXT NOT NULL, deleted_at TIMESTAMPTZ, PRIMARY KEY (tenant, slug))`,
		`CREATE TABLE legacy_keys (tenant TEXT, slug TEXT, deleted_at TEXT, PRIMARY KEY (tenant, slug))`,
		`INSERT INTO tombstone_keys (tenant, slug, title) VALUES ('t', 'a', 'A'), ('t', 'b', 'B')`,
		`INSERT INTO legacy_keys (tenant, slug) VALUES ('t', 'a'), ('t', 'b')`,
	)

	t.Run("native soft delete stamps one row of the composite key", func(t *testing.T) {
		repo := NewRepo[tombstoneKey]()
		if err := repo.DeleteByPK(ctx, db, &tombstoneKey{Tenant: "t", Slug: "a"}); err != nil {
			t.Fatalf("DeleteByPK: %v", err)
		}
		if n := rawInt(t, db, `SELECT count(*) FROM tombstone_keys WHERE deleted_at IS NOT NULL`); n != 1 {
			t.Fatalf("%d rows tombstoned, want only (t, a)", n)
		}
		if n := rawInt(t, db, `SELECT count(*) FROM tombstone_keys WHERE slug = 'a' AND deleted_at IS NOT NULL`); n != 1 {
			t.Fatal("the wrong row was tombstoned")
		}
		if err := repo.DeleteByPK(ctx, db, &tombstoneKey{Tenant: "t", Slug: "a"}); !svcerr.IsNotFound(err) {
			t.Errorf("deleting a tombstoned key again = %v, want NotFound", err)
		}
	})

	t.Run("legacy text soft delete", func(t *testing.T) {
		repo := NewRepo[legacyKey]()
		if err := repo.DeleteByPK(ctx, db, &legacyKey{Tenant: "t", Slug: "b"}); err != nil {
			t.Fatalf("DeleteByPK: %v", err)
		}
		if n := rawInt(t, db, `SELECT count(*) FROM legacy_keys WHERE slug = 'b' AND deleted_at IS NOT NULL`); n != 1 {
			t.Fatal("the legacy row was not tombstoned")
		}
		if err := repo.DeleteByPK(ctx, db, &legacyKey{Tenant: "t", Slug: "nope"}); !svcerr.IsNotFound(err) {
			t.Errorf("a missing key = %v, want NotFound", err)
		}
	})
}

// The unexported helpers the repo stamps with, on every shape the generator
// projects a managed timestamp as: time.Time, *time.Time, string, *string.
func TestManagedTimestampStamping_EveryProjectedShape(t *testing.T) {
	type stamped struct {
		T  time.Time
		PT *time.Time
		S  string
		PS *string
	}
	now := time.Date(2026, 11, 26, 10, 11, 12, 13, time.UTC)
	var e stamped
	v := reflect.ValueOf(&e).Elem()
	for i, c := range []struct {
		field    string
		isPtr    bool
		isString bool
	}{{"T", false, false}, {"PT", true, false}, {"S", false, true}, {"PS", true, true}} {
		sf := stampField{index: i, isPtr: c.isPtr, isString: c.isString}
		if !stampIsEmpty(v.Field(i), sf) {
			t.Errorf("%s: a fresh field is not empty", c.field)
		}
		writeStamp(v.Field(i), sf, now)
		if stampIsEmpty(v.Field(i), sf) {
			t.Errorf("%s: still empty after writeStamp", c.field)
		}
	}
	if !e.T.Equal(now) || e.PT == nil || !e.PT.Equal(now) {
		t.Errorf("time stamps: %v %v", e.T, e.PT)
	}
	want := now.Format(time.RFC3339Nano)
	if e.S != want || e.PS == nil || *e.PS != want {
		t.Errorf("string stamps: %q %v, want %q", e.S, e.PS, want)
	}

	// A caller-set value is not empty, so Create keeps it.
	set := time.Unix(1, 0)
	e2 := stamped{PT: &set, S: "given"}
	v2 := reflect.ValueOf(&e2).Elem()
	if stampIsEmpty(v2.Field(1), stampField{index: 1, isPtr: true}) || stampIsEmpty(v2.Field(2), stampField{index: 2, isString: true}) {
		t.Error("a caller-set stamp reads as empty")
	}

	// An unaddressable field is left alone rather than panicking.
	frozen := reflect.ValueOf(stamped{})
	writeStamp(frozen.Field(0), stampField{index: 0}, now)
	if !frozen.Field(0).Interface().(time.Time).IsZero() {
		t.Error("writeStamp wrote through an unsettable value")
	}
}

func TestStampFieldFor_OnlyStampableTypes(t *testing.T) {
	type model struct {
		bun.BaseModel `bun:"table:m"`
		At            time.Time  `bun:"at"`
		PAt           *time.Time `bun:"pat"`
		Str           string     `bun:"str"`
		N             int64      `bun:"n"`
		Raw           []byte     `bun:"raw"`
	}
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}
	tbl := db.bdb.Dialect().Tables().Get(reflect.TypeFor[model]())
	for col, want := range map[string]struct {
		ok       bool
		isPtr    bool
		isString bool
	}{
		"at":  {true, false, false},
		"pat": {true, true, false},
		"str": {true, false, true},
		"n":   {false, false, false},
		"raw": {false, false, false},
	} {
		sf, ok := stampFieldFor(tbl.FieldMap[col])
		if ok != want.ok || (ok && (sf.isPtr != want.isPtr || sf.isString != want.isString)) {
			t.Errorf("stampFieldFor(%s) = %+v, %v; want ok=%v ptr=%v string=%v", col, sf, ok, want.ok, want.isPtr, want.isString)
		}
	}
}
