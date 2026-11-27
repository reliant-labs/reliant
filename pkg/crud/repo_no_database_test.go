package crud

import (
	"context"
	"errors"
	"strings"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// mockDatabase has the shape forge's contract generator gives a mock of a
// Deps interface that embeds orm.Context (a store package's MockDatabase):
// a BunFunc field, and with it unset, Bun returns bun.IDB's zero value,
// because Bun has no error result to return contractkit.MockNotSet on. The
// embedded orm.Context is nil: a repo reaching past Bun panics the test.
type mockDatabase struct {
	orm.Context
	BunFunc func() bun.IDB
}

func (m *mockDatabase) Bun() bun.IDB {
	if m.BunFunc != nil {
		return m.BunFunc()
	}
	return *new(bun.IDB)
}

// Every query method refuses a handle with no database with ErrNoDatabase
// instead of panicking on db.Bun()'s nil, and the refusal leaves the
// repo's one-time metadata underived, so the next call with a real handle
// still derives it.
func TestRepo_NoDatabaseIsAnErrorNotAPanic(t *testing.T) {
	handles := map[string]orm.Context{
		"mock with BunFunc unset": &mockDatabase{},
		"nil orm.Context":         nil,
	}
	for name, db := range handles {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo := NewRepo[vault]()
			calls := map[string]func() error{
				"Create": func() error { return repo.Create(ctx, db, &vault{Name: "a"}) },
				"Upsert": func() error { return repo.Upsert(ctx, db, &vault{ID: "v1"}) },
				"Get":    func() error { _, err := repo.Get(ctx, db, "v1"); return err },
				"GetByPK": func() error {
					_, err := repo.GetByPK(ctx, db, &vault{ID: "v1"})
					return err
				},
				"List":     func() error { _, err := repo.List(ctx, db); return err },
				"Count":    func() error { _, err := repo.Count(ctx, db); return err },
				"ListAll":  func() error { _, err := repo.ListAll(ctx, db); return err },
				"Update":   func() error { return repo.Update(ctx, db, &vault{ID: "v1"}) },
				"Delete":   func() error { return repo.Delete(ctx, db, "v1") },
				"DeleteBy": func() error { return repo.DeleteByPK(ctx, db, &vault{ID: "v1"}) },
				"UpdateMasked": func() error {
					return repo.UpdateMasked(ctx, db, &vault{ID: "v1"}, []string{"name"})
				},
				"UpdateWhere": func() error {
					_, err := repo.UpdateWhere(ctx, db, &vault{Name: "b"}, []string{"name"}, orm.WhereEq("id", "v1"))
					return err
				},
			}
			for method, call := range calls {
				err := call()
				if !errors.Is(err, ErrNoDatabase) {
					t.Errorf("%s = %v, want ErrNoDatabase", method, err)
				}
				if cause := svcerr.Cause(err); cause == nil || !strings.Contains(cause.Error(), "vault") || !strings.Contains(cause.Error(), "BunFunc unset") {
					t.Errorf("%s cause = %v, want entity and configuration diagnostic", method, cause)
				}
				if msg := svcerr.ToConnect(err).Message(); msg != "internal server error" {
					t.Errorf("%s client message = %q, want generic internal error", method, msg)
				}
			}
			if cols := repo.Columns(db); cols != nil {
				t.Errorf("Columns = %v, want nil", cols)
			}
			if pk := repo.PkColumn(db); pk != "" {
				t.Errorf("PkColumn = %q, want empty", pk)
			}

			real := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}
			if cols := repo.Columns(real); !containsCol(cols, "name") {
				t.Fatalf("Columns after a refused call = %v: the refusal poisoned the repo's metadata", cols)
			}
		})
	}
}

// The generated CRUD lifecycle returns CodeInternal without disclosing the
// server-side configuration diagnostic to the client.
func TestCreate_NoDatabaseReachesTheClientAsInternal(t *testing.T) {
	db := &mockDatabase{}
	repo := NewRepo[vault]()
	h := Create(CreateOp[createReq, createResp, *vault]{
		EntityLower: "vault",
		Entity:      func(_ context.Context, r *createReq) (*vault, error) { return &vault{Name: r.Name}, nil },
		Persist:     func(ctx context.Context, e *vault) error { return repo.Create(ctx, db, e) },
		Pack:        func(*vault) (*createResp, error) { return &createResp{}, nil },
	})
	_, err := h(context.Background(), &createReq{Name: "a"})
	if got := svcerr.CodeV2(err); got != connectv2.CodeInternal {
		t.Fatalf("code = %v, want Internal (err: %v)", got, err)
	}
	if msg := svcerr.ToConnect(err).Message(); msg != "internal server error" {
		t.Fatalf("client message = %q, want generic internal error", msg)
	}
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("server-side error = %v, want ErrNoDatabase", err)
	}
}
