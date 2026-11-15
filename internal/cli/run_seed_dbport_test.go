package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devpg"
)

// composeAt writes a scaffold-shaped docker-compose.yml (postgres published
// on ${POSTGRES_PORT:-5432}) into dir and chdirs there, so the reconcile
// resolves the project dir the way `forge env up` does.
func composeAt(t *testing.T, dir string) {
	t.Helper()
	body := "services:\n  postgres:\n    image: postgres:17-alpine\n    ports:\n      - \"127.0.0.1:${POSTGRES_PORT:-5432}:5432\"\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	// forge.yaml makes projectDirForKCL resolve to this dir.
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: pt\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
}

// TestReconcileDevDatabasePort_RefusesWrongPort is the guard that makes the
// reported corruption impossible: a DSN whose port is not the port this
// project's compose publishes must be REFUSED before forge issues CREATE
// DATABASE, because on a multi-stack dev machine that port reliably belongs
// to another project's postgres.
func TestReconcileDevDatabasePort_RefusesWrongPort(t *testing.T) {
	composeAt(t, t.TempDir())
	t.Setenv(devpg.PortEnv, "15433") // compose publishes 15433

	// ...but the committed DSN still names the old hardcoded 5434.
	err := reconcileDevDatabasePort("postgres://postgres:postgres@localhost:5434/pt?sslmode=disable", nil)
	if err == nil {
		t.Fatal("reconcileDevDatabasePort accepted a DSN on :5434 while compose publishes :15433 — " +
			"this is exactly the path that wrote a project's tables into another stack's database")
	}
	msg := err.Error()
	for _, want := range []string{"5434", "15433", "pt"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must name %q so the developer can act on it; got:\n%s", want, msg)
		}
	}
}

func TestReconcileDevDatabasePort_AcceptsMatchingPort(t *testing.T) {
	composeAt(t, t.TempDir())
	t.Setenv(devpg.PortEnv, "15433")

	if err := reconcileDevDatabasePort("postgres://postgres:postgres@localhost:15433/pt?sslmode=disable", nil); err != nil {
		t.Errorf("reconcileDevDatabasePort rejected an agreeing DSN: %v", err)
	}
}

// TestReconcileDevDatabasePort_ScaffoldDefaultAgrees: the scaffold-time and
// run-time halves must agree by construction — a DSN produced by the
// scaffold emitter passes the runtime reconcile under the same environment.
// If this ever fails the two derivations have drifted apart.
func TestReconcileDevDatabasePort_ScaffoldDefaultAgrees(t *testing.T) {
	for _, port := range []string{"", "5432", "15433", "5440"} {
		t.Run("POSTGRES_PORT="+port, func(t *testing.T) {
			composeAt(t, t.TempDir())
			t.Setenv(devpg.PortEnv, port)
			if err := reconcileDevDatabasePort(devpg.DSN("pt"), nil); err != nil {
				t.Errorf("scaffolded DSN failed the runtime reconcile: %v", err)
			}
		})
	}
}

// TestReconcileDevDatabasePort_DotEnvIsNotAMismatch is the false-positive
// regression. `docker compose` auto-loads `.env` from the project directory,
// so a developer who sets POSTGRES_PORT there rather than exporting it gets
// postgres on that port for real — and a DATABASE_URL naming it is CORRECT.
// Forge used to read os.Getenv only, see compose's 5432 default, and refuse.
//
// This is the failure mode the guard must never have: refusing a correct
// configuration trains people to work around the guard, and the workaround
// outlives the false alarm.
func TestReconcileDevDatabasePort_DotEnvIsNotAMismatch(t *testing.T) {
	dir := t.TempDir()
	composeAt(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("POSTGRES_PORT=5460\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv(devpg.PortEnv, "") // never exported — .env alone sets it

	dsn := "postgres://postgres:postgres@localhost:5460/pt?sslmode=disable"
	if err := reconcileDevDatabasePort(dsn, nil); err != nil {
		t.Errorf("refused a correct project (.env sets POSTGRES_PORT=5460, DSN names 5460):\n%v", err)
	}
}

// TestReconcileDevDatabasePort_DotEnvStillCatchesMismatch: reading `.env`
// must not blunt the guard. With `.env` moving compose to 5460, a DSN still
// on the old default is a GENUINE disagreement and must be refused loudly.
func TestReconcileDevDatabasePort_DotEnvStillCatchesMismatch(t *testing.T) {
	dir := t.TempDir()
	composeAt(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("POSTGRES_PORT=5460\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv(devpg.PortEnv, "")

	err := reconcileDevDatabasePort("postgres://postgres:postgres@localhost:5432/pt?sslmode=disable", nil)
	if err == nil {
		t.Fatal("accepted a DSN on :5432 while .env publishes compose on :5460 — " +
			"this is the path that writes a project's tables into another stack's database")
	}
	for _, want := range []string{"5432", "5460"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q; got:\n%s", want, err)
		}
	}
}

// TestReconcileDevDatabasePort_UnreadableEnvFileStandsDown: when forge will
// pass a `--env-file` it cannot read, it cannot reproduce compose's
// interpolation. Prefer NOT refusing over refusing wrongly — and say so.
func TestReconcileDevDatabasePort_UnreadableEnvFileStandsDown(t *testing.T) {
	dir := t.TempDir()
	composeAt(t, dir)
	t.Setenv(devpg.PortEnv, "")

	entities := &KCLEntities{Workloads: []WorkloadEntity{{
		Name: "postgres", Kind: "service",
		Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{
			Service: "postgres", EnvFile: "does-not-exist.env",
		}},
	}}}
	// A DSN that WOULD look like a mismatch against the 5432 default.
	if err := reconcileDevDatabasePort("postgres://postgres:postgres@localhost:5460/pt", entities); err != nil {
		t.Errorf("refused on an env file forge cannot read; it should stand down instead:\n%v", err)
	}
}

// TestReconcileDevDatabasePort_EnvFileReplacesDotEnv pins the semantic that
// makes the --env-file path correct rather than merely permissive: compose's
// --env-file REPLACES the default .env, so the port comes from that file.
func TestReconcileDevDatabasePort_EnvFileReplacesDotEnv(t *testing.T) {
	dir := t.TempDir()
	composeAt(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("POSTGRES_PORT=5460\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pg.env"), []byte("POSTGRES_PORT=5470\n"), 0o644); err != nil {
		t.Fatalf("write pg.env: %v", err)
	}
	t.Setenv(devpg.PortEnv, "")

	entities := &KCLEntities{Workloads: []WorkloadEntity{{
		Name: "postgres", Kind: "service",
		Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{
			Service: "postgres", EnvFile: "pg.env",
		}},
	}}}
	if err := reconcileDevDatabasePort("postgres://postgres:postgres@localhost:5470/pt", entities); err != nil {
		t.Errorf("--env-file names 5470 and the DSN agrees; should pass:\n%v", err)
	}
	if err := reconcileDevDatabasePort("postgres://postgres:postgres@localhost:5460/pt", entities); err == nil {
		t.Error("accepted the .env port 5460 although --env-file pg.env REPLACES .env with 5470")
	}
}

// TestReconcileDevDatabasePort_NoComposeIsNotAnError: a project running its
// database out of band has nothing to reconcile against and must not be
// blocked (the 20% are never disempowered).
func TestReconcileDevDatabasePort_NoComposeIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: pt\n"), 0o644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	if err := reconcileDevDatabasePort("postgres://postgres:postgres@localhost:5434/pt", nil); err != nil {
		t.Errorf("no compose file should mean nothing to reconcile; got: %v", err)
	}
}
