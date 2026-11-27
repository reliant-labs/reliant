package shadowdb

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

const deadServer = "postgres://postgres:postgres@127.0.0.1:1/postgres?sslmode=disable"

// errEmbedded stands in for whatever embedded postgres would answer: the
// tests below only care that the reader got there.
var errEmbedded = errors.New("reached embedded postgres")

// fallbackProject writes a one-migration project and returns its directories.
func fallbackProject(t *testing.T) (dir, migDir string) {
	t.Helper()
	dir = t.TempDir()
	migDir = filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte("CREATE TABLE t (id TEXT PRIMARY KEY);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, migDir
}

// serverGoneErr is what a server stopped mid-apply returns.
func serverGoneErr() error {
	return fmt.Errorf("apply 00001_init.up.sql: %w", &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"})
}

func TestServerGoneClassifiesARefusedDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	db, err := sql.Open("pgx", "postgres://postgres:postgres@"+addr+"/postgres?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	pingErr := db.Ping()
	if pingErr == nil {
		t.Fatalf("ping of the closed port %s succeeded", addr)
	}
	if !serverGone(fmt.Errorf("pgtest: connect to x: %w", pingErr)) {
		t.Errorf("serverGone(%v) = false, want true for a refused dial", pingErr)
	}
}

func TestServerGoneKeepsMigrationErrors(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"admin shutdown":  {serverGoneErr(), true},
		"crash shutdown":  {&pgconn.PgError{Code: "57P02"}, true},
		"starting up":     {&pgconn.PgError{Code: "57P03"}, true},
		"undefined table": {fmt.Errorf("apply: %w", &pgconn.PgError{Code: "42P01"}), false},
		"check violation": {&pgconn.PgError{Code: "23514"}, false},
		"plain error":     {errors.New("syntax error at or near"), false},
	}
	for name, tc := range cases {
		if got := serverGone(tc.err); got != tc.want {
			t.Errorf("%s: serverGone = %v, want %v", name, got, tc.want)
		}
	}
}

// A run whose probed server goes away moves to embedded postgres, and so
// does every reader after it.
func TestRunMovesToEmbeddedWhenItsServerGoesAway(t *testing.T) {
	t.Setenv(pgtest.EnvBaseURL, "")
	dir, migDir := fallbackProject(t)
	var servers []string
	orig := applyTemplate
	applyTemplate = func(_ *schemadef.MigrationSet, server string) (*schemadef.Template, error) {
		servers = append(servers, server)
		if server == deadServer {
			return nil, serverGoneErr()
		}
		return nil, errEmbedded
	}
	t.Cleanup(func() { applyTemplate = orig })

	end := BeginRun()
	defer end()
	run.mu.Lock()
	run.servers[projectKey(dir)] = deadServer // what Resolve probed, before it went away
	run.mu.Unlock()

	if _, _, err := runTemplate(dir, migDir); !errors.Is(err, errEmbedded) {
		t.Fatalf("runTemplate error = %v, want the embedded attempt's", err)
	}
	if want := []string{deadServer, ""}; !reflect.DeepEqual(servers, want) {
		t.Fatalf("applied on %q, want %q", servers, want)
	}
	if got, _ := runServer(dir); got != "" {
		t.Errorf("run server after the fallback = %q, want embedded (\"\")", got)
	}
}

// Outside a run, a private apply retries on embedded postgres.
func TestPrivateApplyMovesToEmbeddedWhenItsServerGoesAway(t *testing.T) {
	t.Setenv(pgtest.EnvBaseURL, "")
	dir, migDir := fallbackProject(t)
	servers := stubApplyShadow(t)

	if _, _, err := applyPrivate(dir, migDir, deadServer); !errors.Is(err, errEmbedded) {
		t.Fatalf("applyPrivate error = %v, want the embedded attempt's", err)
	}
	if want := []string{deadServer, ""}; !reflect.DeepEqual(*servers, want) {
		t.Fatalf("applied on %q, want %q", *servers, want)
	}
}

// A failed private apply can return no shadow. Tables must pass through
// the error, including its own cleanup after the failed attempt.
func TestTablesReturnsPrivateApplyErrorWithoutShadow(t *testing.T) {
	t.Setenv(pgtest.EnvBaseURL, deadServer)
	dir, migDir := fallbackProject(t)
	servers := stubApplyShadow(t)

	tables, err := Tables(dir, migDir)
	var pgErr *pgconn.PgError
	if tables != nil || !errors.As(err, &pgErr) || pgErr.Code != "57P01" {
		t.Fatalf("Tables = %v, %v; want only the override's own failure", tables, err)
	}
	if want := []string{deadServer}; !reflect.DeepEqual(*servers, want) {
		t.Fatalf("applied on %q, want %q", *servers, want)
	}
}

// The FORGE_TEST_POSTGRES_URL override fails loudly; it is never swapped for
// embedded postgres behind the caller's back.
func TestOverrideServerIsNeverReplaced(t *testing.T) {
	t.Setenv(pgtest.EnvBaseURL, deadServer)
	dir, migDir := fallbackProject(t)
	servers := stubApplyShadow(t)

	_, _, err := applyPrivate(dir, migDir, deadServer)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "57P01" {
		t.Fatalf("applyPrivate error = %v, want the override's own failure", err)
	}
	if want := []string{deadServer}; !reflect.DeepEqual(*servers, want) {
		t.Fatalf("applied on %q, want %q", *servers, want)
	}
}

// stubApplyShadow makes deadServer fail as a stopped server and embedded
// postgres answer errEmbedded, recording each server applied to.
func stubApplyShadow(t *testing.T) *[]string {
	t.Helper()
	var servers []string
	orig := applyShadow
	applyShadow = func(_ *schemadef.MigrationSet, server string) ([]schemadef.Table, *schemadef.Shadow, error) {
		servers = append(servers, server)
		if server == deadServer {
			return nil, nil, serverGoneErr()
		}
		return nil, nil, errEmbedded
	}
	t.Cleanup(func() { applyShadow = orig })
	return &servers
}
