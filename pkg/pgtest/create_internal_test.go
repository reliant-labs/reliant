package pgtest

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloneStrategy(t *testing.T) {
	for _, tc := range []struct {
		version int
		fsync   string
		want    string
	}{
		{180003, "off", "FILE_COPY"},
		{160009, "off", "FILE_COPY"},
		{150000, "OFF", "FILE_COPY"},
		// fsync on: a FILE_COPY checkpoint syncs the whole server.
		{160009, "on", ""},
		// Before 15 there is no STRATEGY clause; FILE_COPY is all there is.
		{140018, "off", ""},
		// The settings could not be read.
		{0, "", ""},
	} {
		if got := cloneStrategy(tc.version, tc.fsync); got != tc.want {
			t.Errorf("cloneStrategy(%d, %q) = %q, want %q", tc.version, tc.fsync, got, tc.want)
		}
	}
}

func TestServerCreateDatabaseSQL(t *testing.T) {
	s := &server{cloneStrategy: "FILE_COPY"}
	if got, want := s.createDatabaseSQL("a", ""), `CREATE DATABASE "a"`; got != want {
		t.Errorf("empty database: %s, want %s", got, want)
	}
	if got, want := s.createDatabaseSQL("a", "t"), `CREATE DATABASE "a" TEMPLATE "t" STRATEGY = FILE_COPY`; got != want {
		t.Errorf("clone: %s, want %s", got, want)
	}
	s.cloneStrategy = ""
	if got, want := s.createDatabaseSQL("a", "t"), `CREATE DATABASE "a" TEMPLATE "t"`; got != want {
		t.Errorf("clone on a server keeping its default: %s, want %s", got, want)
	}
}

func TestCreateTimeout(t *testing.T) {
	t.Setenv(EnvCreateTimeout, "")
	if got := createTimeout(); got != DefaultCreateTimeout {
		t.Errorf("unset: %s, want %s", got, DefaultCreateTimeout)
	}
	t.Setenv(EnvCreateTimeout, "7m")
	if got := createTimeout(); got != 7*time.Minute {
		t.Errorf("7m: %s", got)
	}
	t.Setenv(EnvCreateTimeout, "soon")
	if got := createTimeout(); got != DefaultCreateTimeout {
		t.Errorf("unparsable: %s, want %s", got, DefaultCreateTimeout)
	}
}

// The embedded server runs with fsync off on 15+, so its clones copy files.
func TestEmbeddedServerClonesByFileCopy(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	t.Setenv(EnvBaseURL, "")
	s, err := boot()
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.baseDB.QueryRow(`SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	want := "FILE_COPY"
	if version < 150000 {
		want = ""
	}
	if s.cloneStrategy != want {
		t.Errorf("embedded postgres %d clones with strategy %q, want %q", version, s.cloneStrategy, want)
	}
}

// A CREATE DATABASE that cannot finish fails within createTimeout, naming
// the server's data directory and what its backend waited on, instead of
// blocking until the go test timeout. An explicit SHARE lock on pg_database
// blocks CREATE DATABASE's catalog write while still allowing the diagnostic
// query to read the catalog. LOCK TABLE is legal in a transaction block.
func TestCreateFailsWithinTheTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	t.Setenv(EnvBaseURL, "")
	s, err := boot()
	if err != nil {
		t.Fatal(err)
	}
	tmpl := scratchName(scratchTestPrefix)
	if _, err := s.baseDB.Exec("CREATE DATABASE " + tmpl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.baseDB.Exec("DROP DATABASE IF EXISTS " + tmpl + " WITH (FORCE)") })

	holder, err := open(s.baseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	tx, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("LOCK TABLE pg_database IN SHARE MODE"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = tx.Rollback() }) }
	t.Cleanup(release)

	t.Setenv(EnvCreateTimeout, "3s")
	clone := scratchName(scratchTestPrefix)
	t.Cleanup(func() { _, _ = s.baseDB.Exec("DROP DATABASE IF EXISTS " + clone) })
	start := time.Now()
	err = s.execCreate(s.createDatabaseSQL(clone, tmpl))
	took := time.Since(start)
	release()

	var te *createTimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("a held create returned %v after %s, want a createTimeoutError", err, took)
	}
	if took > time.Minute {
		t.Errorf("the create returned after %s, long past its 3s bound", took)
	}
	msg := err.Error()
	t.Log(msg)
	for _, want := range []string{
		"did not finish within 3s",
		"data dir " + runtimeDir(portOf(t, s.baseURL)),
		"Lock/",
		EnvCreateTimeout,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not say %q", msg, want)
		}
	}
	// The cancelled create left nothing behind, and the server still clones.
	var exists bool
	if err := s.baseDB.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", clone).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Errorf("the cancelled create left database %s behind", clone)
	}
	if err := s.execCreate(s.createDatabaseSQL(clone, tmpl)); err != nil {
		t.Errorf("a clone after the lock was released: %v", err)
	}
}

func portOf(t *testing.T, url string) uint32 {
	t.Helper()
	i := strings.LastIndex(url, ":")
	var port uint32
	if _, err := fmt.Sscanf(url[i+1:], "%d/", &port); err != nil {
		t.Fatalf("no port in %s: %v", url, err)
	}
	return port
}
