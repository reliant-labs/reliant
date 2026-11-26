package migratekit

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"
)

// TestAwaitSchemaWaitsForTheApplier pins migrate=wait's primitive: a waiter
// over a fresh database blocks — creating nothing — until another process
// applies every migration the waiter embeds, then returns.
func TestAwaitSchemaWaitsForTheApplier(t *testing.T) {
	dsn := requirePG(t)
	waiterDB := openRecordDB(t, dsn)
	applierDB := openRecordDB(t, dsn)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- AwaitSchema(ctx, waiterDB, release(olderFiles), "migrations", AwaitOptions{Timeout: time.Minute, Poll: 20 * time.Millisecond})
	}()

	select {
	case err := <-done:
		t.Fatalf("AwaitSchema returned %v before any migration was applied", err)
	case <-time.After(300 * time.Millisecond):
	}
	var exists bool
	if err := waiterDB.QueryRowContext(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("the waiter created schema_migrations: a waiter changes nothing")
	}

	if err := AutoMigrate(release(olderFiles), "migrations")(applierDB, nil); err != nil {
		t.Fatalf("applier: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AwaitSchema after the applier finished: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("AwaitSchema still waiting 30s after the schema became current")
	}
}

// TestAwaitSchemaVerdicts: a schema behind the waiter's migrations times out
// naming both versions; one at or past them returns at once; ctx ends it.
func TestAwaitSchemaVerdicts(t *testing.T) {
	dsn := requirePG(t)
	db := openRecordDB(t, dsn)
	ctx := context.Background()
	if err := AutoMigrate(release(olderFiles), "migrations")(db, nil); err != nil {
		t.Fatal(err)
	}
	fast := AwaitOptions{Timeout: 200 * time.Millisecond, Poll: 20 * time.Millisecond}

	// The previous release meets the next one's schema: current enough.
	if err := AwaitSchema(ctx, db, release(map[string]string{"1_accounts.up.sql": olderFiles["1_accounts.up.sql"]}), "migrations", fast); err != nil {
		t.Errorf("a schema past the embedded migrations must satisfy the waiter: %v", err)
	}
	if err := AwaitSchema(ctx, db, release(olderFiles), "migrations", fast); err != nil {
		t.Errorf("a schema at the embedded migrations must satisfy the waiter: %v", err)
	}
	for _, none := range []fstest.MapFS{nil, {"migrations/README.md": {Data: []byte("none yet")}}} {
		var fsys fs.FS
		if none != nil {
			fsys = none
		}
		if err := AwaitSchema(ctx, db, fsys, "migrations", fast); err != nil {
			t.Errorf("a binary embedding no migrations has nothing to wait for: %v", err)
		}
	}

	err := AwaitSchema(ctx, db, release(merged(olderFiles, compatibleThird)), "migrations", fast)
	var pending *SchemaPendingError
	if !errors.As(err, &pending) || pending.Want != 3 || pending.State.Version != 2 {
		t.Fatalf("a schema behind the embedded migrations must time out naming both versions; got %v", err)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := AwaitSchema(cctx, db, release(merged(olderFiles, compatibleThird)), "migrations", AwaitOptions{Timeout: time.Minute}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait returns ctx's error; got %v", err)
	}
}
