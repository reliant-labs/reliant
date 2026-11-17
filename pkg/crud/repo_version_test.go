package crud

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// ledger is an entity that opted into optimistic concurrency control by
// declaring one column `forge:version` in a migration. The generator projects
// that declaration onto TWO tags: `,skipupdate` (Bun's own guard, so a
// hand-rolled full-struct query can't write the column either) and
// `forge:"version"` in forge's own tag namespace, which is what
// Repo.ensureMeta reads to find the column at runtime.
type ledger struct {
	bun.BaseModel `bun:"table:ledgers,alias:ledgers"`

	ID      string `bun:"id,pk"`
	Balance int64  `bun:"balance,notnull"`
	Note    string `bun:"note,notnull"`
	Version int64  `bun:"version,notnull,skipupdate" forge:"version"`
}

// plainRow is ledger's control: same shape, no version column at all. Every
// assertion made against it is a claim that adding OCC to forge changed
// nothing for the entities that did not ask for it.
type plainRow struct {
	bun.BaseModel `bun:"table:plain_rows,alias:plain_rows"`

	ID      string `bun:"id,pk"`
	Balance int64  `bun:"balance,notnull"`
	Note    string `bun:"note,notnull"`
}

func versionedSchema(ctx context.Context, t *testing.T, db orm.Context) {
	t.Helper()
	if _, err := db.Exec(ctx, `
CREATE TABLE ledgers (
    id      TEXT PRIMARY KEY,
    balance BIGINT NOT NULL,
    note    TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 0
);
COMMENT ON COLUMN ledgers.version IS 'forge:version';

CREATE TABLE plain_rows (
    id      TEXT PRIMARY KEY,
    balance BIGINT NOT NULL,
    note    TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
}

// The hermetic half: the version column is DETECTED off the struct tag and
// excluded from both caller-settable column sets. No database needed — this
// is the whole classification mechanism.
func TestVersionColumnClassification(t *testing.T) {
	db := dialectOnlyDB{bdb: bun.NewDB(nil, pgdialect.New())}

	repo := NewRepo[ledger]()
	repo.ensureMeta(db)

	if repo.m.versionColumn != "version" {
		t.Fatalf("versionColumn = %q, want \"version\" — the forge:\"version\" struct tag was not read", repo.m.versionColumn)
	}
	// The repo owns the column: it must appear in NEITHER the full-replace
	// SET list nor the masked-update allowlist. The masked exclusion is the
	// stricter one and the one that matters — a client able to name the
	// version in an update_mask could pick the value its own predicate is
	// compared against, which is the guarantee gone.
	if containsCol(repo.m.updatable, "version") {
		t.Errorf("full-replace SET list must exclude the version column, got %v", repo.m.updatable)
	}
	if repo.m.updatableSet["version"] {
		t.Error("masked-update allowlist must exclude the version column: a client must never set it")
	}
	// Surgical: ordinary columns are untouched.
	if !containsCol(repo.m.updatable, "balance") || !repo.m.updatableSet["note"] {
		t.Errorf("ordinary columns lost their write policy: updatable=%v", repo.m.updatable)
	}

	// The control entity resolves no version column, so every guard in the
	// write path short-circuits.
	plain := NewRepo[plainRow]()
	plain.ensureMeta(db)
	if plain.m.versionColumn != "" {
		t.Errorf("an entity with no forge:version column must have versionColumn == \"\", got %q", plain.m.versionColumn)
	}
	if !containsCol(plain.m.updatable, "balance") || !containsCol(plain.m.updatable, "note") {
		t.Errorf("unversioned entity lost updatable columns: %v", plain.m.updatable)
	}
}

// Happy path: a write carrying the CURRENT version lands and moves the
// version forward, both in the database and on the caller's entity.
func TestVersionedUpdate_SucceedsAndIncrements(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	versionedSchema(ctx, t, db)

	repo := NewRepo[ledger]()
	row := &ledger{ID: "l1", Balance: 100, Note: "opening"}
	if err := repo.Create(ctx, db, row); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if row.Version != 0 {
		t.Fatalf("a fresh row should start at the column DEFAULT 0, got %d", row.Version)
	}

	read, err := repo.Get(ctx, db, "l1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	read.Balance = 250
	if err := repo.Update(ctx, db, read); err != nil {
		t.Fatalf("Update with the current version must succeed: %v", err)
	}

	stored, err := repo.Get(ctx, db, "l1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if stored.Balance != 250 {
		t.Errorf("balance = %d, want 250 — the write did not land", stored.Balance)
	}
	if stored.Version != 1 {
		t.Errorf("stored version = %d, want 1 — the UPDATE did not increment it", stored.Version)
	}
	// The in-memory entity tracks the increment, so the same caller can write
	// again without re-reading. Without this, its next Update would conflict
	// with a row nobody else had touched.
	if read.Version != 1 {
		t.Errorf("in-memory version = %d, want 1 — a second write by this caller would falsely conflict", read.Version)
	}
	read.Balance = 300
	if err := repo.Update(ctx, db, read); err != nil {
		t.Fatalf("a second Update by the same caller must succeed without a re-read: %v", err)
	}
}

// The whole point: two writers read the same row, both write, and the SECOND
// one is refused rather than silently overwriting the first.
func TestVersionedUpdate_ConcurrentWriteIsAborted(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	versionedSchema(ctx, t, db)

	repo := NewRepo[ledger]()
	if err := repo.Create(ctx, db, &ledger{ID: "l1", Balance: 100, Note: "opening"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Two readers, same row, same version — the interleaving that used to
	// lose a write with no error anywhere.
	first, err := repo.Get(ctx, db, "l1")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	second, err := repo.Get(ctx, db, "l1")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}

	first.Balance = 500
	first.Note = "first writer"
	if err := repo.Update(ctx, db, first); err != nil {
		t.Fatalf("the first writer must succeed: %v", err)
	}

	second.Balance = 999
	second.Note = "second writer"
	err = repo.Update(ctx, db, second)
	if err == nil {
		t.Fatal("the second writer must be refused — last-writer-wins is the bug this closes")
	}
	if !errors.Is(err, svcerr.ErrAborted) {
		t.Errorf("second writer error = %v, want errors.Is(err, svcerr.ErrAborted)", err)
	}
	if errors.Is(err, orm.ErrNoRows) {
		t.Error("a version conflict must NOT report not-found: the row is still there, and a client told 'deleted' would act on a lie")
	}
	// The code a client actually routes on, through the RPC mapping the
	// generated handlers use.
	if got := connect.CodeOf(mapRepoErr("update", "ledger", err)); got != connect.CodeAborted {
		t.Errorf("wire code = %v, want %v (re-read and retry)", got, connect.CodeAborted)
	}

	// A refused write must not have written ANYTHING — a conflict that
	// half-applied would be worse than the lost update it replaced.
	stored, err := repo.Get(ctx, db, "l1")
	if err != nil {
		t.Fatalf("Get after conflict: %v", err)
	}
	if stored.Balance != 500 || stored.Note != "first writer" {
		t.Errorf("row = {balance:%d note:%q}, want {500 \"first writer\"} — the losing write mutated the row",
			stored.Balance, stored.Note)
	}
	if stored.Version != 1 {
		t.Errorf("version = %d, want 1 — the losing write incremented the version it did not earn", stored.Version)
	}

	// And the prescribed recovery actually works: re-read, re-apply, retry.
	fresh, err := repo.Get(ctx, db, "l1")
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	fresh.Balance = 999
	if err := repo.Update(ctx, db, fresh); err != nil {
		t.Fatalf("retry after re-read must succeed — Aborted promises exactly this: %v", err)
	}
}

// The disambiguation, from the other side: a missing row is still NotFound on
// a versioned entity. Collapsing the two would make "it's gone" and "someone
// else edited it" indistinguishable, which is the failure this design exists
// to avoid.
func TestVersionedUpdate_MissingRowStaysNotFound(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	versionedSchema(ctx, t, db)

	repo := NewRepo[ledger]()
	ghost := &ledger{ID: "never-existed", Balance: 1, Note: "x", Version: 0}

	err := repo.Update(ctx, db, ghost)
	if !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Update of an id that never existed = %v, want orm.ErrNoRows", err)
	}
	if errors.Is(err, svcerr.ErrAborted) {
		t.Error("a nonexistent row must NOT report Aborted: there is nothing to re-read, so retrying is futile")
	}
	if got := connect.CodeOf(mapRepoErr("update", "ledger", err)); got != connect.CodeNotFound {
		t.Errorf("wire code = %v, want %v", got, connect.CodeNotFound)
	}

	// The same discrimination for the masked path.
	err = repo.UpdateMasked(ctx, db, ghost, []string{"balance"})
	if !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("UpdateMasked of an id that never existed = %v, want orm.ErrNoRows", err)
	}
	if errors.Is(err, svcerr.ErrAborted) {
		t.Error("a nonexistent row must NOT report Aborted from the masked path either")
	}

	// A STALE version against a row that exists is the other answer, proving
	// the two branches are actually distinguished rather than one of them
	// being unreachable.
	if err := repo.Create(ctx, db, &ledger{ID: "real", Balance: 1, Note: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stale := &ledger{ID: "real", Balance: 2, Note: "y", Version: 42}
	if err := repo.Update(ctx, db, stale); !errors.Is(err, svcerr.ErrAborted) {
		t.Errorf("stale version against an existing row = %v, want svcerr.ErrAborted", err)
	}
}

// A masked write is version-checked too: rewriting one field of a row someone
// else has since changed is the same lost update as replacing the whole row.
func TestVersionedUpdateMasked_IsVersionChecked(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	versionedSchema(ctx, t, db)

	repo := NewRepo[ledger]()
	if err := repo.Create(ctx, db, &ledger{ID: "l1", Balance: 100, Note: "opening"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	first, _ := repo.Get(ctx, db, "l1")
	second, _ := repo.Get(ctx, db, "l1")

	first.Note = "first writer"
	if err := repo.UpdateMasked(ctx, db, first, []string{"note"}); err != nil {
		t.Fatalf("the first masked write must succeed: %v", err)
	}
	if first.Version != 1 {
		t.Errorf("masked write did not advance the in-memory version: got %d, want 1", first.Version)
	}

	second.Balance = 999
	err := repo.UpdateMasked(ctx, db, second, []string{"balance"})
	if !errors.Is(err, svcerr.ErrAborted) {
		t.Errorf("stale masked write = %v, want svcerr.ErrAborted", err)
	}
	if got := connect.CodeOf(mapRepoErr("update", "ledger", err)); got != connect.CodeAborted {
		t.Errorf("wire code = %v, want %v", got, connect.CodeAborted)
	}

	stored, _ := repo.Get(ctx, db, "l1")
	if stored.Balance != 100 {
		t.Errorf("balance = %d, want 100 — the losing masked write mutated a column", stored.Balance)
	}
	if stored.Note != "first writer" {
		t.Errorf("note = %q, want \"first writer\"", stored.Note)
	}

	// The version column is not a mask path a client may name: it is absent
	// from updatableSet, so naming it is the same caller error as naming the
	// PK. Anything else would let a client choose the value its own
	// concurrency check is made against.
	var unknown *orm.UnknownFieldError
	if err := repo.UpdateMasked(ctx, db, stored, []string{"version"}); !errors.As(err, &unknown) {
		t.Errorf("update_mask naming the version column = %v, want *orm.UnknownFieldError", err)
	}
}

// The regression guard: an entity that declared NO version column behaves
// exactly as it did before OCC existed — no predicate, no conflict, no extra
// query, last-writer-wins preserved for anyone who did not opt in.
func TestUnversionedEntity_Unaffected(t *testing.T) {
	db := newRepoTestDB(t)
	ctx := context.Background()
	versionedSchema(ctx, t, db)

	repo := NewRepo[plainRow]()
	if err := repo.Create(ctx, db, &plainRow{ID: "p1", Balance: 100, Note: "opening"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The exact interleaving that conflicts on a versioned entity: two reads,
	// two writes. Here the second must simply win, silently, as it always has.
	first, _ := repo.Get(ctx, db, "p1")
	second, _ := repo.Get(ctx, db, "p1")

	first.Note = "first writer"
	if err := repo.Update(ctx, db, first); err != nil {
		t.Fatalf("first Update: %v", err)
	}
	second.Note = "second writer"
	if err := repo.Update(ctx, db, second); err != nil {
		t.Fatalf("an unversioned entity must not gain concurrency control: %v", err)
	}
	stored, _ := repo.Get(ctx, db, "p1")
	if stored.Note != "second writer" {
		t.Errorf("note = %q, want \"second writer\" — last-writer-wins must be preserved for entities that did not opt in", stored.Note)
	}

	// The masked path is equally untouched.
	stored.Balance = 7
	if err := repo.UpdateMasked(ctx, db, stored, []string{"balance"}); err != nil {
		t.Fatalf("unversioned masked update: %v", err)
	}

	// And the not-found answer is unchanged: still ErrNoRows, never Aborted.
	err := repo.Update(ctx, db, &plainRow{ID: "missing", Balance: 1, Note: "x"})
	if !errors.Is(err, orm.ErrNoRows) {
		t.Errorf("Update of a missing unversioned row = %v, want orm.ErrNoRows", err)
	}
	if errors.Is(err, svcerr.ErrAborted) {
		t.Error("an unversioned entity must never produce Aborted")
	}
}
