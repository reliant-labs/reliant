package crud

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Columns and PkColumn expose what the repo derived from the model's Bun
// tags: the order_by allowlist and the List cursor column.
func TestRepo_ColumnsAndPkColumnComeFromTheBunTags(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}

	w := NewRepo[widget]()
	if got, want := w.Columns(db), []string{"id", "name", "tags", "created_at", "updated_at", "deleted_at"}; !slices.Equal(got, want) {
		t.Errorf("widget Columns = %v, want %v", got, want)
	}
	if got := w.PkColumn(db); got != "id" {
		t.Errorf("widget PkColumn = %q, want id", got)
	}

	// A composite key has no single cursor column.
	c := NewRepo[documentCounter]()
	if got := c.PkColumn(db); got != "" {
		t.Errorf("composite-key PkColumn = %q, want empty", got)
	}
	if got, want := c.Columns(db), []string{"company_id", "kind", "next_value"}; !slices.Equal(got, want) {
		t.Errorf("composite-key Columns = %v, want %v", got, want)
	}
}

// The repo resolves a model type through pointers and interfaces.
func TestRepo_ModelTypeDereferences(t *testing.T) {
	for name, got := range map[string]reflect.Type{
		"struct":            NewRepo[widget]().modelType(),
		"pointer to struct": NewRepo[*widget]().modelType(),
	} {
		if got != reflect.TypeFor[widget]() {
			t.Errorf("%s: modelType = %v, want widget", name, got)
		}
	}
	// A nil-zero M (an interface type) falls back to the interface itself.
	if got := NewRepo[error]().modelType(); got != reflect.TypeFor[error]() {
		t.Errorf("interface M: modelType = %v, want error", got)
	}
}

// advanceVersion keeps the caller's struct in step with the stored version
// after a write that landed, for every integer width, and leaves any other
// type (and an entity without a version column) alone.
func TestRepo_AdvanceVersion(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}

	t.Run("int64", func(t *testing.T) {
		r := NewRepo[ledger]()
		r.ensureMeta(db)
		l := &ledger{ID: "x", Version: 4}
		r.advanceVersion(l)
		if l.Version != 5 {
			t.Fatalf("Version = %d, want 5", l.Version)
		}
	})

	t.Run("no version column leaves the entity alone", func(t *testing.T) {
		r := NewRepo[plainRow]()
		r.ensureMeta(db)
		p := &plainRow{ID: "x", Balance: 9}
		r.advanceVersion(p)
		if *p != (plainRow{ID: "x", Balance: 9, BaseModel: p.BaseModel}) {
			t.Fatalf("entity changed: %+v", p)
		}
	})

	type unsignedVersioned struct {
		bun.BaseModel `bun:"table:uv"`
		ID            string `bun:"id,pk"`
		Version       uint32 `bun:"version,notnull" forge:"version"`
	}
	t.Run("unsigned", func(t *testing.T) {
		r := NewRepo[unsignedVersioned]()
		r.ensureMeta(db)
		u := &unsignedVersioned{ID: "x", Version: 7}
		r.advanceVersion(u)
		if u.Version != 8 {
			t.Fatalf("Version = %d, want 8", u.Version)
		}
	})

	type stringVersioned struct {
		bun.BaseModel `bun:"table:sv"`
		ID            string `bun:"id,pk"`
		Version       string `bun:"version,notnull" forge:"version"`
	}
	t.Run("a non-integer version is left for the caller to re-read", func(t *testing.T) {
		r := NewRepo[stringVersioned]()
		r.ensureMeta(db)
		s := &stringVersioned{ID: "x", Version: "v1"}
		r.advanceVersion(s)
		if s.Version != "v1" {
			t.Fatalf("Version = %q, a type this package cannot advance must not be guessed at", s.Version)
		}
	})
}

// A miss on a read becomes the repo's NotFound; any other failure is wrapped
// with the operation and keeps its identity.
func TestRepo_MissErrClassifiesMissesOnly(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}
	r := NewRepo[widget]()
	r.ensureMeta(db)

	miss := r.missErr("get %s", orm.ErrNoRows)
	if !svcerr.IsNotFound(miss) || miss.Error() == "" {
		t.Fatalf("a miss = %v, want NotFound", miss)
	}
	boom := errors.New("connection reset")
	other := r.missErr("get %s", boom)
	if !errors.Is(other, boom) || svcerr.IsNotFound(other) {
		t.Fatalf("a non-miss = %v, want the wrapped failure and not NotFound", other)
	}
	if want := "get widgets: connection reset"; other.Error() != want {
		t.Fatalf("message = %q, want %q", other.Error(), want)
	}
}

// mapFilterErr: a refusal the filter classified keeps its code, anything else
// reads Internal naming the filters (not the response projection).
func TestMapFilterErr(t *testing.T) {
	denied := mapFilterErr(svcerr.PermissionDenied("not yours"))
	var ce *connect.Error
	if !errors.As(denied, &ce) || ce.Code() != connect.CodePermissionDenied {
		t.Fatalf("a classified refusal = %v, want PermissionDenied passed through", denied)
	}
	plain := mapFilterErr(errors.New("who is asking?"))
	if !errors.As(plain, &ce) || ce.Code() != connect.CodeInternal || ce.Message() != "list filters failed" {
		t.Fatalf("an unclassified failure = %v, want Internal %q", plain, "list filters failed")
	}
	if got := ce.Meta().Get(svcerr.ReasonHeader); got != ReasonInternal {
		t.Errorf("reason = %q, want %q", got, ReasonInternal)
	}
}

// rowCount is the sql.Result of a statement whose rows were scanned.
func TestRowCount(t *testing.T) {
	n, err := rowCount(3).RowsAffected()
	if n != 3 || err != nil {
		t.Fatalf("RowsAffected = %d, %v", n, err)
	}
	if _, err := rowCount(3).LastInsertId(); err == nil {
		t.Fatal("LastInsertId succeeded; there is no insert id for a scanned statement")
	}
}
