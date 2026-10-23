package devpg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDSNFollowsPostgresPort is the unit-level reproduction of the
// data-corruption bug: the scaffolded DATABASE_URL must name the port
// docker-compose publishes, not a hardcoded one. Before the fix the scaffold
// wrote :5434 no matter what POSTGRES_PORT said, so `POSTGRES_PORT=15433
// forge run` connected the app to another stack's postgres.
func TestDSNFollowsPostgresPort(t *testing.T) {
	t.Setenv(PortEnv, "15433")
	got := DSN("pt")
	if p := PortOf(got); p != "15433" {
		t.Errorf("DSN(%q) = %q, port %q; want port 15433 (the port compose publishes)", "pt", got, p)
	}
	if !strings.Contains(got, "/pt?") {
		t.Errorf("DSN should name the project database: %s", got)
	}
}

// TestDSNDefaultMatchesComposeDefault: with POSTGRES_PORT unset the DSN must
// use compose's OWN default (5432). The pre-fix scaffold hardcoded 5434,
// which disagreed with compose's `${POSTGRES_PORT:-5432}` even on a plain
// scaffold with no env override at all — the default case was already broken.
func TestDSNDefaultMatchesComposeDefault(t *testing.T) {
	t.Setenv(PortEnv, "")
	if p := PortOf(DSN("myapp")); p != DefaultPort {
		t.Errorf("default DSN port = %q, want %q (compose's ${POSTGRES_PORT:-5432})", p, DefaultPort)
	}
}

func TestDSNHonorsUserAndPassword(t *testing.T) {
	t.Setenv(PortEnv, "5599")
	t.Setenv(UserEnv, "alice")
	t.Setenv(PasswordEnv, "s3cret")
	got := DSN("shop")
	if !strings.Contains(got, "alice:s3cret@localhost:5599") {
		t.Errorf("DSN did not follow POSTGRES_USER/POSTGRES_PASSWORD: %s", got)
	}
}

// TestReconcileCatchesDisagreement is the runtime half of the fix: even a
// correctly-scaffolded project can be RUN later under a different
// POSTGRES_PORT, at which point the DSN and compose diverge again. That must
// be a loud refusal, never a silent write into another stack's database.
func TestReconcileCatchesDisagreement(t *testing.T) {
	err := Reconcile("postgres://postgres:postgres@localhost:5434/pt?sslmode=disable", "15433")
	if err == nil {
		t.Fatal("Reconcile accepted a DSN on :5434 while compose publishes :15433 — this is the silent-corruption path")
	}
	var mm *MismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("want *MismatchError, got %T", err)
	}
	// The error must be a runbook: both values and the literal fix.
	msg := err.Error()
	for _, want := range []string{"5434", "15433", "pt", "forge secret set", PortEnv} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message must name %q; got:\n%s", want, msg)
		}
	}
}

func TestReconcileAcceptsAgreement(t *testing.T) {
	if err := Reconcile("postgres://postgres:postgres@localhost:15433/pt?sslmode=disable", "15433"); err != nil {
		t.Errorf("Reconcile rejected matching ports: %v", err)
	}
}

// TestReconcileIgnoresNonLocalAndUnknown: the reconcile governs the LOCAL
// compose stack only. A dev DSN pointed at a managed/remote postgres, or a
// project with no compose port to compare against, is a deliberate setup and
// must not be blocked.
func TestReconcileIgnoresNonLocalAndUnknown(t *testing.T) {
	cases := []struct{ name, dsn, composePort string }{
		{"remote host", "postgres://u:p@db.example.com:5432/pt", "15433"},
		{"no compose port", "postgres://u:p@localhost:5434/pt", ""},
		{"no port in dsn", "postgres://u:p@localhost/pt", "15433"},
		{"unparseable dsn", "::not a dsn::", "15433"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Reconcile(tc.dsn, tc.composePort); err != nil {
				t.Errorf("Reconcile(%q, %q) = %v; want nil (nothing to reconcile)", tc.dsn, tc.composePort, err)
			}
		})
	}
}

func TestComposePublishedPort(t *testing.T) {
	const compose = `services:
  postgres:
    image: postgres:17-alpine
    ports:
      - "127.0.0.1:${POSTGRES_PORT:-5432}:5432"
  app:
    ports:
      - "8080:8080"
`
	t.Run("default", func(t *testing.T) {
		dir := t.TempDir()
		writeCompose(t, dir, compose)
		t.Setenv(PortEnv, "")
		if got := ComposePublishedPort(dir); got != "5432" {
			t.Errorf("ComposePublishedPort = %q, want 5432", got)
		}
	})
	t.Run("override expands like compose does", func(t *testing.T) {
		dir := t.TempDir()
		writeCompose(t, dir, compose)
		t.Setenv(PortEnv, "15433")
		if got := ComposePublishedPort(dir); got != "15433" {
			t.Errorf("ComposePublishedPort = %q, want 15433", got)
		}
	})
	t.Run("no compose file", func(t *testing.T) {
		if got := ComposePublishedPort(t.TempDir()); got != "" {
			t.Errorf("ComposePublishedPort = %q, want empty", got)
		}
	})
	t.Run("no postgres service", func(t *testing.T) {
		dir := t.TempDir()
		writeCompose(t, dir, "services:\n  app:\n    ports:\n      - \"8080:8080\"\n")
		if got := ComposePublishedPort(dir); got != "" {
			t.Errorf("ComposePublishedPort = %q, want empty", got)
		}
	})
	t.Run("ephemeral mapping is not a coordinate", func(t *testing.T) {
		dir := t.TempDir()
		writeCompose(t, dir, "services:\n  postgres:\n    ports:\n      - \"5432\"\n")
		if got := ComposePublishedPort(dir); got != "" {
			t.Errorf("ComposePublishedPort = %q, want empty for an ephemeral mapping", got)
		}
	})
}

func TestHostPortOf(t *testing.T) {
	cases := map[string]string{
		"5433:5432":               "5433",
		"127.0.0.1:5433:5432":     "5433",
		"127.0.0.1:5433:5432/tcp": "5433",
		"5432":                    "",
		"0:5432":                  "",
		"8000-8010:8000-8010":     "",
		"":                        "",
	}
	for in, want := range cases {
		if got := hostPortOf(in); got != want {
			t.Errorf("hostPortOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeCompose(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ComposeFileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
}
