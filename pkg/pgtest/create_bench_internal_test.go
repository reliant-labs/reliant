package pgtest

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// benchTemplateTables is the size of the template the clone benchmarks
// copy: a few hundred tables, each with indexes, as a real project's
// migrations build.
const benchTemplateTables = 200

// buildWideSchema fills db with n tables shaped like an application's: a
// uuid key, a handful of columns and three indexes each.
func buildWideSchema(db *sql.DB, n int) error {
	for i := 0; i < n; i++ {
		for _, stmt := range []string{
			fmt.Sprintf(`CREATE TABLE t%d (
				id uuid PRIMARY KEY, name text NOT NULL, body text, owner_id uuid,
				state int NOT NULL DEFAULT 0, created_at timestamptz NOT NULL DEFAULT now(),
				updated_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz)`, i),
			fmt.Sprintf(`CREATE INDEX t%d_owner ON t%d (owner_id)`, i, i),
			fmt.Sprintf(`CREATE INDEX t%d_live ON t%d (created_at) WHERE deleted_at IS NULL`, i, i),
			fmt.Sprintf(`CREATE UNIQUE INDEX t%d_name ON t%d (name, owner_id)`, i, i),
		} {
			if _, err := db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	return nil
}

// BenchmarkCloneTemplate clones a 200-table template concurrently (8
// goroutines per GOMAXPROCS) with each CREATE DATABASE strategy, on the
// shared embedded server, each clone dropped again. It is the measurement
// behind cloneStrategy:
//
//	go test -run '^$' -bench CloneTemplate -benchtime 100x ./pkg/pgtest/
//
// On a 48-CPU host running other test fan-outs (postgres 16.9, 16-40
// concurrent clones), WAL_LOG made 4-8 clones a second and FILE_COPY 12-21,
// with each clone's CREATE DATABASE 3-6 times faster at the median.
func BenchmarkCloneTemplate(b *testing.B) {
	b.Setenv(EnvBaseURL, "")
	s, err := boot()
	if err != nil {
		b.Fatal(err)
	}
	var version int
	if err := s.baseDB.QueryRow(`SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		b.Fatal(err)
	}
	if version < 150000 {
		b.Skipf("postgres %d has no CREATE DATABASE STRATEGY", version)
	}
	tmpl, cleanup, err := NewURL()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(cleanup)
	tdb, err := open(tmpl)
	if err != nil {
		b.Fatal(err)
	}
	if err := buildWideSchema(tdb, benchTemplateTables); err != nil {
		b.Fatal(err)
	}
	_ = tdb.Close()
	tmplName := dbNameOf(tmpl)

	admin, err := open(s.baseURL)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = admin.Close() })
	admin.SetMaxOpenConns(128)

	for _, strategy := range []string{"WAL_LOG", "FILE_COPY"} {
		b.Run(strategy, func(b *testing.B) {
			b.SetParallelism(8)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					name := scratchName(scratchTestPrefix)
					if _, err := admin.Exec("CREATE DATABASE " + name + " TEMPLATE " + tmplName + " STRATEGY = " + strategy); err != nil {
						b.Error(err)
						return
					}
					_, _ = admin.Exec("DROP DATABASE " + name)
				}
			})
		})
	}
}

// dbNameOf is the database a postgres:// DSN names.
func dbNameOf(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}
