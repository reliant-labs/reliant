package crud

import (
	"context"
	"regexp"
	"testing"

	"github.com/uptrace/bun"
)

// Who supplies a primary key on Create is the applied schema's answer, which
// the generator carries as ,autoincrement (internal/generator.serverAllocatedKey):
// present only when the key has an identity or a DEFAULT. These pin the
// repo's half against real postgres.

// callerKeyed is reliant's legacy_credential_copy: `integer PRIMARY KEY` with
// no DEFAULT. The key is the caller's; Create must insert it.
type callerKeyed struct {
	bun.BaseModel `bun:"table:caller_keyed,alias:caller_keyed"`

	ID   int32  `bun:"id,pk"`
	Note string `bun:"note,notnull"`
}

// serialKeyed is a serial key: the database allocates it.
type serialKeyed struct {
	bun.BaseModel `bun:"table:serial_keyed,alias:serial_keyed"`

	ID   int32  `bun:"id,pk,autoincrement"`
	Note string `bun:"note,notnull"`
}

// uuidKeyed is a string key the database allocates (gen_random_uuid()), so
// an empty key is the database's to fill — a ULID is not a uuid.
type uuidKeyed struct {
	bun.BaseModel `bun:"table:uuid_keyed,alias:uuid_keyed"`

	ID   string `bun:"id,pk,autoincrement"`
	Note string `bun:"note,notnull"`
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestRepoCreate_KeyAllocationFollowsTheSchema(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE caller_keyed (id integer PRIMARY KEY CHECK (id = 1), note TEXT NOT NULL)`,
		`CREATE TABLE serial_keyed (id serial PRIMARY KEY, note TEXT NOT NULL)`,
		`CREATE TABLE uuid_keyed (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), note TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(ctx, stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	t.Run("a key with no default is inserted as the caller set it", func(t *testing.T) {
		repo := NewRepo[callerKeyed]()
		row := &callerKeyed{ID: 1, Note: "done"}
		if err := repo.Create(ctx, db, row); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := repo.Get(ctx, db, int32(1))
		if err != nil {
			t.Fatalf("Get(1): %v", err)
		}
		if got.Note != "done" {
			t.Errorf("stored note = %q, want done", got.Note)
		}
	})

	t.Run("a serial key left zero is allocated and read back", func(t *testing.T) {
		repo := NewRepo[serialKeyed]()
		a, b := &serialKeyed{Note: "a"}, &serialKeyed{Note: "b"}
		for _, row := range []*serialKeyed{a, b} {
			if err := repo.Create(ctx, db, row); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
		if a.ID == 0 || b.ID != a.ID+1 {
			t.Errorf("allocated ids = %d, %d; want the sequence's consecutive values", a.ID, b.ID)
		}
	})

	t.Run("a serial key the caller set is inserted as set", func(t *testing.T) {
		repo := NewRepo[serialKeyed]()
		row := &serialKeyed{ID: 100, Note: "explicit"}
		if err := repo.Create(ctx, db, row); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if row.ID != 100 {
			t.Errorf("id after Create = %d, want 100", row.ID)
		}
		if _, err := repo.Get(ctx, db, int32(100)); err != nil {
			t.Errorf("Get(100): %v", err)
		}
	})

	t.Run("a uuid key left empty is the database's, not a ULID", func(t *testing.T) {
		repo := NewRepo[uuidKeyed]()
		row := &uuidKeyed{Note: "db"}
		if err := repo.Create(ctx, db, row); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !uuidRE.MatchString(row.ID) {
			t.Errorf("id after Create = %q, want the uuid gen_random_uuid() assigned", row.ID)
		}
	})

	t.Run("a uuid key the caller set is kept", func(t *testing.T) {
		repo := NewRepo[uuidKeyed]()
		const id = "0b6f1b8e-2d1c-4a8f-9a59-1d2f7c3e4b5a"
		row := &uuidKeyed{ID: id, Note: "caller"}
		if err := repo.Create(ctx, db, row); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if row.ID != id {
			t.Errorf("id after Create = %q, want %q", row.ID, id)
		}
		if _, err := repo.Get(ctx, db, id); err != nil {
			t.Errorf("Get(%s): %v", id, err)
		}
	})
}
