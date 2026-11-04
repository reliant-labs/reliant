//go:build integration

// File: internal/cli/db_squash_dsn_integration_test.go
//
// `forge db squash --from-dsn` end to end: build the database a goose history
// leaves, squash it, and hand the baseline to forge's migrator twice — over
// that same goose database, which it must ADOPT without running a statement,
// and over a fresh one, which it must build into the same schema.
//
// Tagged integration because it runs real docker: a postgres container
// standing in for the goose-built database, plus the pg_dump container squash
// itself runs. The test's container is removed with its volumes on cleanup.

package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// gooseBuiltDatabase is the schema a goose history built, and goose's table as
// an older goose created it — `id serial`, whose owned sequence pg_dump keeps
// unless it is excluded by name (current goose uses an identity column, which
// pg_dump drops with its table). Version 2 was applied and rolled back (an
// is_applied = false row), so it is NOT part of the history.
var gooseBuiltDatabase = []string{
	`CREATE TABLE goose_db_version (
		id serial NOT NULL PRIMARY KEY,
		version_id bigint NOT NULL, is_applied boolean NOT NULL, tstamp timestamp NULL DEFAULT now())`,
	"INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true), (20240101000001, true), (20240101000002, true), (20240101000002, false), (20240101000003, true)",
	"CREATE TYPE plan_kind AS ENUM ('free', 'pro')",
	"CREATE TABLE accounts (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL)",
	"CREATE TABLE plans (id INT PRIMARY KEY, kind plan_kind NOT NULL DEFAULT 'free', account_id BIGINT REFERENCES accounts (id))",
	"CREATE INDEX plans_account ON plans (account_id)",
	"CREATE FUNCTION account_count() RETURNS bigint LANGUAGE sql AS $$ SELECT count(*) FROM accounts $$",
	"INSERT INTO plans (id, kind) VALUES (1, 'pro')",
}

func TestDBSquash_FromDSN_AdoptsTheGooseDatabaseItWasCutFrom(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a postgres container and squash's pg_dump container")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	ctx := context.Background()
	server := startPostgresContainer(t)
	gooseDSN := server.database(t, "goose_built")
	db := openScratch(t, gooseDSN)
	for _, s := range gooseBuiltDatabase {
		mustExec(t, db, s)
	}

	// Two cuts from two directories write the same bytes.
	var baselines [2][]byte
	release := ""
	for i := range baselines {
		release = t.TempDir()
		var out bytes.Buffer
		if err := runDBSquashFromDSN(ctx, &out, squashOptions{
			FromDSN:  gooseDSN,
			Baseline: "20260101000000_baseline",
			OutDir:   release,
		}); err != nil {
			t.Fatalf("squash --from-dsn: %v\n%s", err, out.String())
		}
		if i == 0 {
			for _, want := range []string{
				"Dumping the database with pg_dump (postgres:16-alpine)",
				"It carries 1 row(s), which every fresh database will get: public.plans (1).",
				"adopts it on its next `migrate up` or AutoMigrate",
				"exactly these 2 versions",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("squash output lacks %q:\n%s", want, out.String())
				}
			}
		}
		b, err := os.ReadFile(filepath.Join(release, "20260101000000_baseline.up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		baselines[i] = b
	}
	if !bytes.Equal(baselines[0], baselines[1]) {
		t.Fatalf("two squashes of one database differ:\n%s\n----\n%s", baselines[0], baselines[1])
	}
	baseline := string(baselines[0])
	for _, want := range []string{
		"-- forge:adopts goose 20240101000001 20240101000003\n",
		"CREATE TABLE public.plans",
		"INSERT INTO public.plans VALUES (1, 'pro', NULL);",
	} {
		if !strings.Contains(baseline, want) {
			t.Errorf("baseline lacks %q:\n%s", want, baseline)
		}
	}
	// The header names goose's table and \restrict (as excluded and
	// stripped); the body, after its first blank line, carries neither.
	body := baseline[strings.Index(baseline, "\n\n")+2:]
	for _, banned := range []string{"goose_db_version", "set_config('search_path'", "\\restrict", "s3cret", "127.0.0.1"} {
		if strings.Contains(body, banned) {
			t.Errorf("baseline body carries %q:\n%s", banned, body)
		}
	}
	for _, banned := range []string{"s3cret", "127.0.0.1", server.port} {
		if strings.Contains(baseline[:len(baseline)-len(body)], banned) {
			t.Errorf("baseline header carries %q", banned)
		}
	}
	if err := os.WriteFile(filepath.Join(release, "20260102000000_add_orgs.up.sql"), []byte("CREATE TABLE orgs (id INT PRIMARY KEY);\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The goose database is adopted: the baseline's SQL never runs there
	// (its CREATE TABLEs would fail), the next migration does.
	var out bytes.Buffer
	if err := runMigrateUp(&out, gooseDSN, release); err != nil {
		t.Fatalf("migrate up over the goose database: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ADOPTED a database goose migrated: 20260101000000_baseline.up.sql recorded as applied without running its SQL (2 goose versions verified") {
		t.Errorf("the goose database was not adopted:\n%s", out.String())
	}

	// A fresh database builds the same schema from the baseline.
	freshDSN := server.database(t, "fresh")
	out.Reset()
	if err := runMigrateUp(&out, freshDSN, release); err != nil {
		t.Fatalf("migrate up over a fresh database: %v\n%s", err, out.String())
	}
	adopted, fresh := server.schema(t, "goose_built"), server.schema(t, "fresh")
	if adopted != fresh {
		t.Errorf("the adopted and the fresh database differ:\n--- adopted\n%s\n--- fresh\n%s", adopted, fresh)
	}
}

// postgresContainer is a throwaway postgres:16-alpine the test owns.
type postgresContainer struct {
	name, port string
}

const containerPassword = "s3cret"

func startPostgresContainer(t *testing.T) *postgresContainer {
	t.Helper()
	name := fmt.Sprintf("forge-squash-dsn-test-%d", time.Now().UnixNano())
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_PASSWORD="+containerPassword, "-p", "127.0.0.1::5432", "postgres:16-alpine").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", name).Run() })
	_, port, err := dockerPort(context.Background(), name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	c := &postgresContainer{name: name, port: port}
	if err := waitForPostgres(context.Background(), c.dsn("postgres"), 60*time.Second); err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *postgresContainer) dsn(db string) string {
	return fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/%s?sslmode=disable", containerPassword, c.port, db)
}

func (c *postgresContainer) database(t *testing.T, name string) string {
	t.Helper()
	if out, err := exec.Command("docker", "exec", c.name, "psql", "-U", "postgres", "-qc", "CREATE DATABASE "+name).CombinedOutput(); err != nil {
		t.Fatalf("create database %s: %v\n%s", name, err, out)
	}
	return c.dsn(name)
}

// schema is the database's schema as pg_dump writes it, without either
// migrator's bookkeeping and without pg_dump's per-run \restrict key.
func (c *postgresContainer) schema(t *testing.T, db string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", c.name, "pg_dump", "-U", "postgres", "--schema-only", "--no-owner",
		"--exclude-table=goose_db_version", "--exclude-table=goose_db_version_id_seq", "--exclude-table=schema_migrations*", db).Output()
	if err != nil {
		t.Fatalf("pg_dump %s: %v", db, err)
	}
	return regexp.MustCompile(`(?m)^\\(un)?restrict .*$`).ReplaceAllString(string(out), "")
}
