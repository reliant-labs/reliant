//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EORMKeyAllocationFollowsTheAppliedSchema pins who supplies a primary
// key on the generated Create<E>: the applied schema, never the key's type.
//
// reliant's legacy_credential_copy is
//
//	id integer PRIMARY KEY CHECK (id = 1)
//
// with no DEFAULT and no sequence. Forge projected every integer key as
// ,autoincrement, so CreateLegacyCredentialCopy left the caller's id out of
// the INSERT and failed NOT NULL. A key is server-allocated only when the
// schema gives it a value — serial, an identity column, or any DEFAULT — and
// this walks the real path: migration → introspection → `forge generate` →
// the generated Create against the migrated database.
func TestE2EORMKeyAllocationFollowsTheAppliedSchema(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "pkapp", "--mod", "example.com/pkapp", "--service", "widget")
	projectDir := filepath.Join(dir, "pkapp")
	addCorpusForgePkgReplace(t, projectDir)

	up := `CREATE TABLE ledger_markers (
    id   integer PRIMARY KEY CHECK (id = 1),
    note text NOT NULL
);
CREATE TABLE counters (
    id   serial PRIMARY KEY,
    note text NOT NULL
);
CREATE TABLE identity_rows (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    note text NOT NULL
);
CREATE TABLE tokens (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    note text NOT NULL
);
`
	if err := os.WriteFile(nextMigrationPathE2E(t, projectDir, "pk_shapes"), []byte(up), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "generate")

	dbDir := filepath.Join(projectDir, "internal", "db")
	for file, want := range map[string]string{
		"ledger_marker_orm_gen.go": "`bun:\"id,pk\"`",
		"counter_orm_gen.go":       "`bun:\"id,pk,autoincrement\"`",
		"identity_row_orm_gen.go":  "`bun:\"id,pk,autoincrement\"`",
		"token_orm_gen.go":         "`bun:\"id,pk,autoincrement\"`",
	} {
		if src := readFileE2E(t, filepath.Join(dbDir, file)); !strings.Contains(src, want) {
			t.Errorf("%s: want the key tagged %s:\n%s", file, want, src)
		}
	}

	// The generated Create, against the project's own migrations.
	probe := `package db_test

import (
	"context"
	"testing"

	forgedb "example.com/pkapp/db"
	"example.com/pkapp/internal/db"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestKeyAllocation(t *testing.T) {
	ctx := context.Background()
	conn := testkit.NewMigratedPostgresDB(t, forgedb.MigrationsFS)

	// No DEFAULT: the caller's id is the row's id.
	if err := db.CreateLedgerMarker(ctx, conn, &db.LedgerMarker{ID: 1, Note: "done"}); err != nil {
		t.Fatalf("CreateLedgerMarker: %v", err)
	}
	if got, err := db.GetLedgerMarkerByID(ctx, conn, 1); err != nil || got.Note != "done" {
		t.Fatalf("GetLedgerMarkerByID(1) = %+v, %v", got, err)
	}

	// serial: a zero id is allocated by the sequence and read back.
	a, b := &db.Counter{Note: "a"}, &db.Counter{Note: "b"}
	for _, c := range []*db.Counter{a, b} {
		if err := db.CreateCounter(ctx, conn, c); err != nil {
			t.Fatalf("CreateCounter: %v", err)
		}
	}
	if a.ID == 0 || b.ID != a.ID+1 {
		t.Fatalf("serial ids = %d, %d; want consecutive sequence values", a.ID, b.ID)
	}

	// identity: allocated though the catalog reports no DEFAULT.
	r := &db.IdentityRow{Note: "r"}
	if err := db.CreateIdentityRow(ctx, conn, r); err != nil {
		t.Fatalf("CreateIdentityRow: %v", err)
	}
	if r.ID == 0 {
		t.Fatal("identity id was not read back")
	}

	// uuid DEFAULT gen_random_uuid(): an empty id is the database's.
	tok := &db.Token{Note: "t"}
	if err := db.CreateToken(ctx, conn, tok); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if len(tok.ID) != 36 {
		t.Fatalf("token id = %q, want the uuid the column DEFAULT assigned", tok.ID)
	}
}
`
	if err := os.WriteFile(filepath.Join(dbDir, "key_allocation_probe_test.go"), []byte(probe), 0o644); err != nil {
		t.Fatalf("write probe test: %v", err)
	}
	runCmd(t, projectDir, "go", "test", "-count=1", "-run", "^TestKeyAllocation$", "./internal/db/")
}
