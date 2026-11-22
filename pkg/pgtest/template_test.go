package pgtest_test

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

func uniqueKey(t *testing.T) string { return fmt.Sprintf("t%d", time.Now().UnixNano()) }

// The schema is built once per key however many callers there are, every caller
// gets its own writable clone, and one caller's writes never reach another's.
func TestNewFromTemplate_BuildsOnceAndClonesIsolated(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	key := uniqueKey(t)
	var builds atomic.Int32
	build := func(db *sql.DB, _ string) error {
		builds.Add(1)
		_, err := db.Exec(`CREATE TABLE seeded (v int); INSERT INTO seeded VALUES (42)`)
		return err
	}

	const n = 6
	dbs := make([]*sql.DB, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, _, cleanup, err := pgtest.NewFromTemplate(key, build)
			if err != nil {
				t.Errorf("NewFromTemplate %d: %v", i, err)
				return
			}
			t.Cleanup(cleanup)
			dbs[i] = db
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("schema built %d times, want exactly once", got)
	}
	for i, db := range dbs {
		var v int
		if err := db.QueryRow(`SELECT v FROM seeded`).Scan(&v); err != nil || v != 42 {
			t.Fatalf("clone %d lost the template's rows: v=%d err=%v", i, v, err)
		}
	}
	if _, err := dbs[0].Exec(`INSERT INTO seeded VALUES (7)`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := dbs[1].QueryRow(`SELECT count(*) FROM seeded`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("a write in clone 0 reached clone 1: count=%d err=%v", count, err)
	}
}

// A build that fails leaves nothing behind: the error comes back, no template is
// published, and the next caller with the same key builds again.
func TestNewFromTemplate_FailedBuildIsNotPublished(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	key := uniqueKey(t)
	boom := errors.New("boom")
	if _, _, _, err := pgtest.NewFromTemplate(key, func(*sql.DB, string) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the build error", err)
	}
	db, _, cleanup, err := pgtest.NewFromTemplate(key, func(db *sql.DB, _ string) error {
		_, err := db.Exec(`CREATE TABLE ok (id int)`)
		return err
	})
	if err != nil {
		t.Fatalf("a failed build must not poison the key: %v", err)
	}
	defer cleanup()
	if _, err := db.Exec(`INSERT INTO ok VALUES (1)`); err != nil {
		t.Fatal(err)
	}
}

// NewFromTemplate hands out URLs as well as handles: build gets the template's
// DSN, for a builder that connects itself (a migrator that takes a URL), and
// the caller gets the clone's, for a second pool or a subprocess. Both name the
// database the handle beside them is connected to, and a session opened on the
// clone's DSN does not keep cleanup from dropping it.
func TestNewFromTemplate_ReturnsTheCloneDSN(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	key := uniqueKey(t)
	build := func(db *sql.DB, dsn string) error {
		// Build through the URL alone, as a migrator would.
		other, err := sql.Open(pgtest.DriverName, dsn)
		if err != nil {
			return err
		}
		defer other.Close()
		var viaURL, viaHandle string
		if err := other.QueryRow(`SELECT current_database()`).Scan(&viaURL); err != nil {
			return err
		}
		if err := db.QueryRow(`SELECT current_database()`).Scan(&viaHandle); err != nil {
			return err
		}
		if viaURL != viaHandle {
			return fmt.Errorf("build DSN names %s, the build handle %s", viaURL, viaHandle)
		}
		_, err = other.Exec(`CREATE TABLE built_by_url (v int); INSERT INTO built_by_url VALUES (9)`)
		return err
	}
	db, dsn, cleanup, err := pgtest.NewFromTemplate(key, build)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dsn, "postgres://") {
		t.Fatalf("dsn = %q, want a postgres:// URL like NewURL's", dsn)
	}
	second, err := sql.Open(pgtest.DriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var viaDSN, viaHandle string
	if err := second.QueryRow(`SELECT current_database()`).Scan(&viaDSN); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT current_database()`).Scan(&viaHandle); err != nil {
		t.Fatal(err)
	}
	if viaDSN != viaHandle {
		t.Fatalf("the returned DSN names %s, the returned handle %s", viaDSN, viaHandle)
	}
	var v int
	if err := second.QueryRow(`SELECT v FROM built_by_url`).Scan(&v); err != nil || v != 9 {
		t.Fatalf("the clone lacks what build wrote through its DSN: v=%d err=%v", v, err)
	}
	if _, err := second.Exec(`INSERT INTO built_by_url VALUES (10)`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM built_by_url`).Scan(&v); err != nil || v != 2 {
		t.Fatalf("a write through the DSN is not visible on the handle: count=%d err=%v", v, err)
	}

	cleanup()
	probe, _, probeCleanup, err := pgtest.NewFromTemplate(key, build)
	if err != nil {
		t.Fatal(err)
	}
	defer probeCleanup()
	var n int
	if err := probe.QueryRow(`SELECT count(*) FROM pg_database WHERE datname = $1`, viaHandle).Scan(&n); err != nil || n != 0 {
		t.Fatalf("clone %s survived cleanup while a second pool held it open (n=%d, err=%v)", viaHandle, n, err)
	}
}

// The clone is dropped by its cleanup.
func TestNewFromTemplate_CleanupDropsTheClone(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	key := uniqueKey(t)
	build := func(db *sql.DB, _ string) error { _, err := db.Exec(`CREATE TABLE x (id int)`); return err }
	db, _, cleanup, err := pgtest.NewFromTemplate(key, build)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	cleanup()
	other, _, cleanup2, err := pgtest.NewFromTemplate(key, build)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	var n int
	if err := other.QueryRow(`SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n); err != nil || n != 0 {
		t.Fatalf("clone %s still exists after cleanup (n=%d, err=%v)", name, n, err)
	}
}
