package crud

import (
	"context"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// inviteRow is an entity with a NON-PK `forge:fill=ulid` column — a share
// token or invite code the client never sends and the DB has no DEFAULT
// for. The `forge:"fill=ulid"` struct tag is what the generator projects
// from a `forge:fill=ulid` catalog-comment marker (see
// internal/generator/plan_orm_gen.go's structTag).
type inviteRow struct {
	bun.BaseModel `bun:"table:invites,alias:invites"`

	ID    string `bun:"id,pk"`
	Email string `bun:"email,notnull"`
	Token string `bun:"token,notnull" forge:"fill=ulid"`
}

// TestFillULIDColumnClassification is the hermetic half: the column is
// DETECTED off the struct tag with no database needed, mirroring
// TestVersionColumnClassification's shape for forge:version.
func TestFillULIDColumnClassification(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}

	repo := NewRepo[inviteRow]()
	repo.ensureMeta(db)

	if len(repo.m.fillULIDFields) != 1 {
		t.Fatalf("fillULIDFields = %v, want exactly one field index (token) — the forge:\"fill=ulid\" struct tag was not read", repo.m.fillULIDFields)
	}
}

// TestFillULIDColumnClassification_PlainRowUnaffected pins the negative:
// an entity with no fill=ulid column must classify to an empty set, not a
// stray index from unrelated fields.
func TestFillULIDColumnClassification_PlainRowUnaffected(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}

	repo := NewRepo[plainRow]()
	repo.ensureMeta(db)

	if len(repo.m.fillULIDFields) != 0 {
		t.Errorf("plainRow declares no forge:fill=ulid column; got fillULIDFields = %v", repo.m.fillULIDFields)
	}
}

// TestRepo_Create_FillsULIDColumn is the real-postgres half: Create must
// ULID-generate the fill=ulid column when the caller left it empty, and
// must NOT overwrite a value the caller explicitly supplied.
func TestRepo_Create_FillsULIDColumn(t *testing.T) {
	db := newRepoTestDB(t)

	ctx := context.Background()
	if _, err := db.Bun().NewCreateTable().Model((*inviteRow)(nil)).IfNotExists().Exec(ctx); err != nil {
		t.Fatalf("create invites table: %v", err)
	}

	repo := NewRepo[inviteRow]()

	// Empty token → forge generates one.
	row := &inviteRow{ID: "row-1", Email: "a@example.com"}
	if err := repo.Create(ctx, db, row); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if row.Token == "" {
		t.Error("Create must ULID-generate an empty forge:fill=ulid column")
	}

	// Caller-supplied token → left alone.
	row2 := &inviteRow{ID: "row-2", Email: "b@example.com", Token: "MY-OWN-TOKEN"}
	if err := repo.Create(ctx, db, row2); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if row2.Token != "MY-OWN-TOKEN" {
		t.Errorf("Create must not overwrite a caller-supplied fill=ulid value; got %q", row2.Token)
	}
}
