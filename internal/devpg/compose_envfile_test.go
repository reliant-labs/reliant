package devpg

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// scaffoldCompose is the port mapping the scaffold emits.
const scaffoldCompose = `services:
  postgres:
    image: postgres:17-alpine
    ports:
      - "127.0.0.1:${POSTGRES_PORT:-5432}:5432"
`

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// TestComposePublishedPort_ReadsDotEnv is the reproduction of the FALSE
// REFUSAL. `docker compose` auto-loads a `.env` from the project directory
// for its own interpolation, so a developer who writes POSTGRES_PORT=5460
// there — rather than exporting it — really does get postgres published on
// 5460, and a DATABASE_URL naming 5460 is CORRECT.
//
// Reading os.Getenv alone made forge see compose's 5432 default, disagree
// with a correct DSN, and block the run. A guard that fires on a correct
// configuration trains people to work around it, which costs the protection
// it exists to provide.
func TestComposePublishedPort_ReadsDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, scaffoldCompose)
	writeFile(t, dir, ".env", "POSTGRES_PORT=5460\n")
	t.Setenv(PortEnv, "") // NOT exported — only .env sets it

	if got := ComposePublishedPort(dir); got != "5460" {
		t.Errorf("ComposePublishedPort = %q, want 5460 — compose reads .env, so postgres is really published there", got)
	}
}

// TestReconcile_DotEnvPortIsNotAMismatch is the same false positive at the
// level the user feels it: a correct project must not be refused.
func TestReconcile_DotEnvPortIsNotAMismatch(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, scaffoldCompose)
	writeFile(t, dir, ".env", "POSTGRES_PORT=5460\n")
	t.Setenv(PortEnv, "")

	dsn := "postgres://postgres:postgres@localhost:5460/pt?sslmode=disable"
	if err := Reconcile(dsn, ComposePublishedPort(dir)); err != nil {
		t.Errorf("Reconcile refused a CORRECT configuration (.env sets POSTGRES_PORT=5460, DSN names 5460):\n%v", err)
	}
}

// TestComposePublishedPort_ShellBeatsDotEnv pins compose's precedence: the
// shell environment WINS over `.env`. Verified against docker compose
// v2.34.0 — `POSTGRES_PORT=5480 docker compose config` with a `.env` saying
// 5460 publishes 5480.
func TestComposePublishedPort_ShellBeatsDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, scaffoldCompose)
	writeFile(t, dir, ".env", "POSTGRES_PORT=5460\n")
	t.Setenv(PortEnv, "5480")

	if got := ComposePublishedPort(dir); got != "5480" {
		t.Errorf("ComposePublishedPort = %q, want 5480 — the shell environment outranks .env", got)
	}
}

// TestComposePublishedPort_EmptyIsUnset mirrors compose's treatment of a
// set-but-empty value: it does NOT satisfy `${VAR:-default}`, so the default
// applies. Verified: `POSTGRES_PORT= docker compose config` with .env=5460
// publishes 5432, and an empty assignment in .env does the same.
func TestComposePublishedPort_EmptyIsUnset(t *testing.T) {
	t.Run("empty in .env falls back to the default", func(t *testing.T) {
		dir := t.TempDir()
		writeCompose(t, dir, scaffoldCompose)
		writeFile(t, dir, ".env", "POSTGRES_PORT=\n")
		t.Setenv(PortEnv, "")
		if got := ComposePublishedPort(dir); got != DefaultPort {
			t.Errorf("ComposePublishedPort = %q, want %q", got, DefaultPort)
		}
	})
	t.Run("empty in the shell does not mask .env", func(t *testing.T) {
		// compose treats a set-but-EMPTY shell var as unset for
		// `:-`, and .env is consulted next.
		dir := t.TempDir()
		writeCompose(t, dir, scaffoldCompose)
		writeFile(t, dir, ".env", "POSTGRES_PORT=5460\n")
		t.Setenv(PortEnv, "")
		if got := ComposePublishedPort(dir); got != "5460" {
			t.Errorf("ComposePublishedPort = %q, want 5460", got)
		}
	})
}

// TestComposePublishedPort_QuotedDotEnvValue: compose strips one layer of
// quotes from a dotenv value.
func TestComposePublishedPort_QuotedDotEnvValue(t *testing.T) {
	dir := t.TempDir()
	writeCompose(t, dir, scaffoldCompose)
	writeFile(t, dir, ".env", "POSTGRES_PORT=\"5461\"\n")
	t.Setenv(PortEnv, "")
	if got := ComposePublishedPort(dir); got != "5461" {
		t.Errorf("ComposePublishedPort = %q, want 5461 (quotes stripped)", got)
	}
}

// TestReconcile_StillCatchesGenuineDisagreement is the anti-regression for
// the WHOLE POINT of the guard. Widening interpolation to read `.env` must
// not weaken the refusal: when the DSN and the published port really do
// disagree, forge must still stop before it creates, migrates and seeds a
// database inside another stack's postgres.
func TestReconcile_StillCatchesGenuineDisagreement(t *testing.T) {
	cases := []struct {
		name, dotenv, shellPort, dsnPort, wantComposePort string
	}{
		{
			name:            "port set in .env, DSN left on the old default",
			dotenv:          "POSTGRES_PORT=5460\n",
			dsnPort:         "5432",
			wantComposePort: "5460",
		},
		{
			name:            "shell override moves compose, .env and DSN stale",
			dotenv:          "POSTGRES_PORT=5460\n",
			shellPort:       "15433",
			dsnPort:         "5460",
			wantComposePort: "15433",
		},
		{
			name:            "no .env at all — DSN on a foreign port",
			dsnPort:         "5434",
			wantComposePort: DefaultPort,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCompose(t, dir, scaffoldCompose)
			if tc.dotenv != "" {
				writeFile(t, dir, ".env", tc.dotenv)
			}
			t.Setenv(PortEnv, tc.shellPort)

			got := ComposePublishedPort(dir)
			if got != tc.wantComposePort {
				t.Fatalf("ComposePublishedPort = %q, want %q", got, tc.wantComposePort)
			}
			dsn := "postgres://postgres:postgres@localhost:" + tc.dsnPort + "/pt?sslmode=disable"
			err := Reconcile(dsn, got)
			if err == nil {
				t.Fatalf("Reconcile ACCEPTED a genuine disagreement (DSN :%s vs compose :%s) — "+
					"this is the path that writes a project's tables into another stack's database",
					tc.dsnPort, tc.wantComposePort)
			}
			var me *MismatchError
			if !errors.As(err, &me) {
				t.Fatalf("Reconcile error = %T, want *MismatchError", err)
			}
			if me.DSNPort != tc.dsnPort || me.ComposePort != tc.wantComposePort {
				t.Errorf("MismatchError{DSN:%s, Compose:%s}, want {DSN:%s, Compose:%s}",
					me.DSNPort, me.ComposePort, tc.dsnPort, tc.wantComposePort)
			}
		})
	}
}
