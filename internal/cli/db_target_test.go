// File: internal/cli/db_target_test.go
//
// The env/DSN reconciliation gate.
//
// `requireDevSeedTarget` classifies the ENVIRONMENT by reading
// deploy/kcl/<env>/config.k for `environment = "development"`. It is properly
// fail-closed and has no override. But the DSN was resolved on a completely
// separate path (`resolveDSN`: --dsn, else $DATABASE_URL, else error) that
// never consulted --env at all.
//
// So the two halves of the decision never met:
//
//	forge db seed reset --env dev --dsn postgres://prod-host/app
//
// passed the dev check — because the DEV CONFIG FILE says development — and
// then acted on production. A stale exported DATABASE_URL does the same thing
// with no flag at all. Validating "is this env dev?" while acting on a
// connection string nobody checked is a guard in name only, and behind a
// `DROP DATABASE` it is unacceptable.
//
// These tests pin the close: a claimed DSN must be reconcilable with the DSN
// the environment itself declares, or the command refuses.

package cli

import (
	"strings"
	"testing"
)

// The headline property: a DSN naming a DIFFERENT SERVER than the env
// declares is refused, even though the env itself is a genuine dev env. This
// is the exact hole — `--env dev --dsn postgres://prod-host/app`.
func TestReconcileClaimedDSN_RefusesADSNThatIsNotTheEnvs(t *testing.T) {
	const declared = "postgres://postgres:postgres@localhost:5470/docvault3?sslmode=disable"

	cases := []struct {
		name    string
		claimed string
		wantIn  []string
	}{
		{
			name:    "different host entirely (the reported hole)",
			claimed: "postgres://app:hunter2@prod-host.example.com:5432/docvault3?sslmode=require",
			wantIn:  []string{"prod-host.example.com:5432", "localhost:5470"},
		},
		{
			name:    "same host, different PORT — another stack's postgres on this machine",
			claimed: "postgres://postgres:postgres@localhost:5460/docvault3?sslmode=disable",
			wantIn:  []string{"localhost:5460", "localhost:5470"},
		},
		{
			name:    "same server, different DATABASE — a sibling project's db",
			claimed: "postgres://postgres:postgres@localhost:5470/roofers2?sslmode=disable",
			wantIn:  []string{"roofers2", "docvault3"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := reconcileClaimedDSN(tc.claimed, declared, "dev")
			if err == nil {
				t.Fatalf("reconcileClaimedDSN(%q, %q) = nil; a DSN the env does not declare MUST be refused", tc.claimed, declared)
			}
			got := err.Error()
			for _, want := range tc.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("refusal must name %q so the user can see which database is which; got:\n%s", want, got)
				}
			}
			// The refusal must never print a password.
			if strings.Contains(got, "hunter2") {
				t.Errorf("refusal leaked a password:\n%s", got)
			}
		})
	}
}

// The other half: a DSN that IS the env's database must pass, including the
// spellings that differ only cosmetically. A guard that refuses a CORRECT
// configuration teaches people to route around it, and the workaround is
// permanent while the false alarm was not.
func TestReconcileClaimedDSN_AcceptsTheEnvsOwnDatabase(t *testing.T) {
	const declared = "postgres://postgres:postgres@localhost:5470/docvault3?sslmode=disable"

	cases := []struct {
		name    string
		claimed string
	}{
		{"byte-identical", declared},
		{"127.0.0.1 is localhost", "postgres://postgres:postgres@127.0.0.1:5470/docvault3?sslmode=disable"},
		{"docker host-gateway alias denotes this machine", "postgres://postgres:postgres@host.docker.internal:5470/docvault3?sslmode=disable"},
		{"k3d host-gateway alias denotes this machine", "postgres://postgres:postgres@host.k3d.internal:5470/docvault3?sslmode=disable"},
		{"credentials may differ (a superuser connection to the same db)", "postgres://admin:other@localhost:5470/docvault3?sslmode=disable"},
		{"query parameters may differ", "postgres://postgres:postgres@localhost:5470/docvault3?sslmode=prefer&connect_timeout=5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := reconcileClaimedDSN(tc.claimed, declared, "dev"); err != nil {
				t.Errorf("reconcileClaimedDSN(%q, %q) = %v; want nil — this IS the env's database", tc.claimed, declared, err)
			}
		})
	}
}

// When the environment declares no DSN of its own, forge has NO EVIDENCE
// either way about the connection string it was handed. For a destructive
// verb that is a refusal, not a pass: "I could not check" and "I checked and
// it is fine" are different answers, and only one of them may precede a
// DROP DATABASE.
func TestReconcileClaimedDSN_RefusesWhenTheEnvDeclaresNothing(t *testing.T) {
	err := reconcileClaimedDSN("postgres://postgres:postgres@localhost:5470/docvault3?sslmode=disable", "", "dev")
	if err == nil {
		t.Fatal("reconcileClaimedDSN with no declared DSN = nil; forge cannot confirm the target belongs to the env, so it must refuse rather than guess")
	}
	got := err.Error()
	// The refusal has to be actionable — it must say where a DSN is declared.
	for _, want := range []string{"dev", "DATABASE_URL"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal must mention %q so the user knows how to fix it; got:\n%s", want, got)
		}
	}
}

// An unparseable claimed DSN is refused rather than compared as an opaque
// string — a string compare would let `postgres://prod/app ` (trailing space)
// through on one path and not another.
func TestReconcileClaimedDSN_RefusesAnUnparseableTarget(t *testing.T) {
	const declared = "postgres://postgres:postgres@localhost:5470/docvault3?sslmode=disable"
	for _, claimed := range []string{"://not a url", "postgres:///nohost", "postgres://localhost:5470/"} {
		if err := reconcileClaimedDSN(claimed, declared, "dev"); err == nil {
			t.Errorf("reconcileClaimedDSN(%q, ...) = nil; a DSN that names no server+database cannot be reconciled", claimed)
		}
	}
}

// databaseIdentity is the comparison key: SERVER (normalized host + port) and
// DATABASE NAME, and nothing else. Credentials and query parameters are not
// part of WHICH DATABASE a DSN names.
func TestDatabaseIdentity_NormalizesHostButKeepsServerAndName(t *testing.T) {
	cases := []struct {
		dsn  string
		want string
	}{
		{"postgres://postgres:postgres@localhost:5470/docvault3?sslmode=disable", "localhost:5470/docvault3"},
		{"postgres://u:p@127.0.0.1:5470/docvault3", "localhost:5470/docvault3"},
		{"postgres://u:p@host.k3d.internal:5470/docvault3", "localhost:5470/docvault3"},
		{"postgres://u:p@db.example.com:5432/app", "db.example.com:5432/app"},
		{"postgres://u:p@localhost/app", "localhost:5432/app"}, // implicit port
		{"://not a url", ""},
		{"postgres://localhost:5432/", ""}, // no database
	}
	for _, tc := range cases {
		if got := databaseIdentity(tc.dsn); got != tc.want {
			t.Errorf("databaseIdentity(%q) = %q, want %q", tc.dsn, got, tc.want)
		}
	}
}
