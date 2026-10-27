//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// privatePostgresOverrides is deliberately narrower than libpq/pgx. Clone
// helpers rewrite the URI path, not query aliases or service configuration.
// Validate BEFORE any parser that could load service files and before any SQL.
func privatePostgresOverrides(environ []string) (map[string]string, error) {
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PG") && value != "" {
			return nil, fmt.Errorf("private fixture rejects PostgreSQL environment configuration")
		}
	}
	canonical := make(map[string]string)
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if name != "CP_TEST_POSTGRES_URL" && name != "FORGE_TEST_POSTGRES_URL" {
			continue
		}
		if value == "" {
			continue
		}
		if _, exists := canonical[name]; exists {
			return nil, fmt.Errorf("private fixture rejects duplicate Postgres override")
		}
		dsn, err := privateMaintenanceURI(value)
		if err != nil {
			return nil, err
		}
		canonical[name] = dsn
	}
	return canonical, nil
}

func privateMaintenanceURI(raw string) (string, error) {
	denied := fmt.Errorf("private Postgres override requires literal loopback, explicit safe port/user, /postgres, and only sslmode=disable")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "/postgres" || u.User == nil || u.User.Username() == "" {
		return "", denied
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 || port == 5434 || ip == nil || !ip.IsLoopback() {
		return "", denied
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 1 || len(query["sslmode"]) != 1 || query.Get("sslmode") != "disable" {
		return "", denied
	}
	// Reconstruct instead of retaining query spellings/aliases. The clone's
	// path substitution must be the ONLY way the database can be selected.
	safe := &url.URL{Scheme: "postgres", User: u.User, Host: net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10)), Path: "/postgres", RawQuery: "sslmode=disable"}
	cfg, err := pgx.ParseConfig(safe.String())
	if err != nil || !privateEffectiveConfig(cfg, safe.Hostname(), uint16(port)) {
		return "", denied
	}
	return safe.String(), nil
}

func privateEffectiveConfig(cfg *pgx.ConnConfig, host string, port uint16) bool {
	if cfg == nil || cfg.Database != "postgres" || cfg.Host != host || cfg.Port != port || len(cfg.RuntimeParams) != 0 {
		return false
	}
	safe := func(h string, p uint16) bool {
		ip := net.ParseIP(h)
		return ip != nil && ip.IsLoopback() && h == host && p == port && p != 0 && p != 5434
	}
	if !safe(cfg.Host, cfg.Port) {
		return false
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback == nil || !safe(fallback.Host, fallback.Port) {
			return false
		}
	}
	return true
}

func guardPrivatePostgres(t *testing.T) {
	t.Helper()
	overrides, err := privatePostgresOverrides(os.Environ())
	if err != nil {
		// Never include the rejected DSN, username, password or environment value.
		t.Fatal(err)
	}
	for name, canonical := range overrides {
		t.Setenv(name, canonical)
	}
}

// Pure parser tests: no SQL driver, dial, database helper or migrations run.
func TestPrivatePostgresGuard(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PG") {
			t.Setenv(name, "")
		}
	}
	valid := "postgres://postgres:fixture@127.0.0.1:5433/postgres?sslmode=disable"
	// Pin the actual driver semantics behind the regression, without dialing:
	// the old net/url port/path checks disagreed with these effective targets.
	portOverride, err := pgx.ParseConfig(valid + "&port=5434")
	if err != nil || portOverride.Port != 5434 {
		t.Fatal("pgx query-port regression premise changed")
	}
	for _, alias := range []string{"dbname", "database", "%64bname"} {
		dbOverride, err := pgx.ParseConfig(valid + "&" + alias + "=existing_product_db")
		if err != nil || dbOverride.Database != "existing_product_db" {
			t.Fatal("pgx database-alias regression premise changed")
		}
	}
	for _, override := range []string{"CP_TEST_POSTGRES_URL", "FORGE_TEST_POSTGRES_URL"} {
		for _, dsn := range []string{
			valid + "&port=5434", valid + "&dbname=reliant", valid + "&database=reliant",
			valid + "&%64bname=reliant", valid + "&%70ort=5434", valid + "&service=prod",
			valid + "&servicefile=/tmp/pg_service.conf", valid + "&options=-csearch_path=public",
			valid + "&host=127.0.0.1,127.0.0.1&port=5433,5434", valid + "&sslmode=disable",
			"postgres://postgres:fixture@127.0.0.1:5434/postgres?sslmode=disable",
			"postgres://postgres:fixture@127.0.0.1:5433/reliant?sslmode=disable",
			"postgres://postgres:fixture@127.0.0.1:5433/%70ostgres?sslmode=disable",
			"postgres://postgres:fixture@localhost:5433/postgres?sslmode=disable",
			"postgres://postgres:fixture@127.0.0.1:5433,127.0.0.1:5434/postgres?sslmode=disable",
			"host=127.0.0.1 port=5433 dbname=postgres service=prod", valid + "#fragment",
		} {
			if _, err := privatePostgresOverrides([]string{override + "=" + dsn}); err == nil {
				t.Fatalf("accepted unsafe override for %s (value withheld)", override)
			}
		}
		for _, name := range []string{"PGDATABASE", "PGHOST", "PGHOSTADDR", "PGPORT", "PGSERVICE", "PGSERVICEFILE", "PGOPTIONS", "PGPASSFILE", "PGSSLMODE", "PGPASSWORD"} {
			if _, err := privatePostgresOverrides([]string{override + "=" + valid, name + "=unsafe"}); err == nil {
				t.Fatalf("accepted PostgreSQL environment %s", name)
			}
		}
		out, err := privatePostgresOverrides([]string{override + "=" + valid})
		if err != nil || out[override] != valid {
			t.Fatalf("valid private compose URI rejected: %v", err)
		}
		// Prove actual pgx clone selection is the new path, not an alias.
		u, _ := url.Parse(out[override])
		u.Path = "/private_clone_test"
		cfg, err := pgx.ParseConfig(u.String())
		if err != nil || cfg.Database != "private_clone_test" || cfg.Port != 5433 {
			t.Fatal("canonical URI does not preserve clone database selection")
		}
	}
	cfg, err := pgx.ParseConfig(valid)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Fallbacks = []*pgconn.FallbackConfig{{Host: "127.0.0.1", Port: 5434}}
	if privateEffectiveConfig(cfg, "127.0.0.1", 5433) {
		t.Fatal("effective pgx fallback on dev port accepted")
	}
	cfg.Fallbacks = []*pgconn.FallbackConfig{{Host: "192.0.2.1", Port: 5433}}
	if privateEffectiveConfig(cfg, "127.0.0.1", 5433) {
		t.Fatal("effective pgx nonloopback fallback accepted")
	}
	cfg.Fallbacks = nil
	cfg.RuntimeParams = map[string]string{"options": "unsafe"}
	if privateEffectiveConfig(cfg, "127.0.0.1", 5433) {
		t.Fatal("effective pgx runtime parameters accepted")
	}
	if _, err := privatePostgresOverrides([]string{"PGSERVICE=unsafe"}); err == nil {
		t.Fatal("environment without URI override escaped validation")
	}
}
