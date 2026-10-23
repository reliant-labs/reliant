package pgtest

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestSplitAppDSN pins EnsureDatabase's pure parse step: an application DSN is
// split into its database NAME and the maintenance SERVER DSN (same
// host/port/credentials, database forced to "postgres"), so the app's own
// database is never opened to create it. Every reject path (unparseable, no
// host, no database) is an error, not a silent empty.
func TestSplitAppDSN(t *testing.T) {
	tests := []struct {
		name       string
		dsn        string
		wantName   string
		wantServer string
		wantErr    bool
	}{
		{
			name:       "scaffolded dev dsn",
			dsn:        "postgres://postgres:postgres@localhost:5434/myapp?sslmode=disable",
			wantName:   "myapp",
			wantServer: "postgres://postgres:postgres@localhost:5434/postgres?sslmode=disable",
		},
		{
			name:       "no sslmode defaults to disable on the maintenance url",
			dsn:        "postgres://u:p@db.internal:5432/orders",
			wantName:   "orders",
			wantServer: "postgres://u:p@db.internal:5432/postgres?sslmode=disable",
		},
		{
			name:     "hyphenated database name is preserved",
			dsn:      "postgres://postgres:postgres@localhost:5434/control-plane?sslmode=disable",
			wantName: "control-plane",
		},
		{name: "no database segment", dsn: "postgres://postgres@localhost:5432/", wantErr: true},
		{name: "no host", dsn: "postgres:///myapp", wantErr: true},
		{name: "unparseable", dsn: "://not a url", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, server, err := splitAppDSN(tc.dsn)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitAppDSN(%q) = (%q,%q,nil), want error", tc.dsn, name, server)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitAppDSN(%q): unexpected error %v", tc.dsn, err)
			}
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if tc.wantServer != "" && server != tc.wantServer {
				t.Errorf("server = %q, want %q", server, tc.wantServer)
			}
			// The maintenance server must ALWAYS target the "postgres" db and
			// never the app database — the invariant that keeps the app's own
			// database from being opened just to create it.
			if !strings.Contains(server, "/postgres") {
				t.Errorf("server %q does not target the maintenance database", server)
			}
			if strings.Contains(server, "/"+tc.wantName+"?") || strings.HasSuffix(server, "/"+tc.wantName) {
				t.Errorf("server %q must not target the app database %q", server, tc.wantName)
			}
		})
	}
}

// TestEnsureDatabase_IdempotentCreate proves the runtime create-if-absent
// behavior against a REAL server: the first EnsureDatabase creates the named
// database and a SECOND call is a no-op (not an error), and — unlike NewAtURL —
// it is not dropped. Skipped under -short: it boots the shared real postgres.
func TestEnsureDatabase_IdempotentCreate(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	s, err := boot()
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	name := fmt.Sprintf("forge_ensure_test_%d_%d", os.Getpid(), dbCounter.Add(1))
	// Reduce the shared maintenance URL to an APP DSN naming our target db, the
	// shape a scaffolded dev DSN has.
	appDSN := replaceDBName(s.baseURL, name)
	t.Cleanup(func() {
		_, _ = s.baseDB.Exec("DROP DATABASE IF EXISTS " + quoteIdent(name))
	})

	if err := EnsureDatabase(appDSN); err != nil {
		t.Fatalf("first EnsureDatabase: %v", err)
	}
	// Idempotent: a second call against the now-existing database is a no-op.
	if err := EnsureDatabase(appDSN); err != nil {
		t.Fatalf("second EnsureDatabase (already-exists must be a no-op): %v", err)
	}

	var exists bool
	if err := s.baseDB.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", name,
	).Scan(&exists); err != nil {
		t.Fatalf("verify existence: %v", err)
	}
	if !exists {
		t.Fatalf("database %q was not created (or was dropped)", name)
	}
}

// TestQuoteIdent pins the identifier quoting CREATE DATABASE relies on so a
// name that is not a bare identifier is created verbatim: wrapped in double
// quotes, with any embedded double quote doubled.
func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"myapp":         `"myapp"`,
		"control-plane": `"control-plane"`,
		`we"ird`:        `"we""ird"`,
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}
