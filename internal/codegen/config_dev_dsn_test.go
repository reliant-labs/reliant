package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devpg"
	"github.com/reliant-labs/forge/internal/secrets"
)

// TestDevSecretStoreCarriesNoDSN pins the invariant that replaced the
// seeded-DSN scheme: the dev secret store carries NO connection string.
//
// The dev postgres port is ONE fact, and the env's KCL is where it is
// stated: `deploy/kcl/dev/main.k` resolves it with
// `plugin.resolve_port("<project>-dev-postgres", 5432)` and composes
// DATABASE_URL from that variable, on every render.
//
// The store, by contrast, is written ONCE (write-if-absent, at scaffold
// time) and never revisited. Seeding a full DSN into it therefore created a
// SECOND copy of the port that could not track the first: resolve_port steps
// to 5433 when 5432 is busy, and the stored string stays on 5432 forever.
// Host-launch precedence hid the divergence (hostlaunch.LayerHostEnv layers
// KCL env vars ABOVE the secret store, so the declaration won), which made
// the stale copy inert rather than harmless — it is read by anyone opening
// the file, and by shadowdb's candidate scan.
//
// The seed also protected nothing. The scaffolded dev DSN's credentials are
// `postgres:postgres` on loopback, and the git-tracked main.k already spells
// them out in the same string. An empty slot leaves the port single-source
// and costs nothing: the KCL declaration supplies the value.
func TestDevSecretStoreCarriesNoDSN(t *testing.T) {
	// Exercised through the store body writer rather than a value helper:
	// the per-field seed is no longer a function (a helper that ignored both
	// its arguments to return "" is a claim nothing backs — deadcodeguard
	// rejects it), so the rendered file IS the unit under test.
	fields := []ConfigField{{
		Name: "database_url", EnvVar: configDBURLEnvVar, Sensitive: true,
		Description: "Postgres connection string.",
	}}

	got := generateEnvSecretsBody(fields, "pt", configDevEnvName)

	if !strings.Contains(got, configDBURLEnvVar+`: ""`) {
		t.Errorf("dev secret seed for %s is not an empty slot.\n"+
			"A DSN here is a second copy of the dev postgres port that cannot track the\n"+
			"KCL resolve_port declaration, and goes stale the first time 5432 is busy:\n%s",
			configDBURLEnvVar, got)
	}
}

// TestEnvDevScaffoldWritesEmptyDBSlot covers the whole store writer, not just
// the value helper — this is the file `forge env up` layers onto host processes
// (below the KCL declaration) and that shadowdb scans for a DSN candidate.
//
// The KEY must still be present: the store's job is to list every declared
// sensitive ref as a labelled slot, so `forge secret ensure dev` and a human
// reading the file both see what wants a value. It is the VALUE that must be
// empty.
func TestEnvDevScaffoldWritesEmptyDBSlot(t *testing.T) {
	t.Setenv(devpg.PortEnv, "15433")
	dir := t.TempDir()

	fields := []ConfigField{{
		Name: "database_url", EnvVar: configDBURLEnvVar, Sensitive: true,
		Description: "Postgres connection string.",
	}}
	wrote, err := GenerateEnvSecretsScaffold(fields, "pt", dir, configDevEnvName)
	if err != nil {
		t.Fatalf("GenerateEnvSecretsScaffold: %v", err)
	}
	if !wrote {
		t.Fatal("expected secrets/dev.yaml to be written")
	}
	body, err := os.ReadFile(filepath.Join(dir, EnvSecretsFileName(configDevEnvName)))
	if err != nil {
		t.Fatalf("read secrets/dev.yaml: %v", err)
	}
	got := string(body)

	// No connection string, on ANY port — not the stale 5432 default, not
	// the 15433 override. The port is not this file's fact to carry.
	if strings.Contains(got, "postgres://") {
		t.Errorf("secrets/dev.yaml carries a DSN — the dev port is declared in KCL "+
			"(resolve_port) and must not be copied here:\n%s", got)
	}
	// The slot itself remains, so the ref is still visible/settable.
	if !strings.Contains(got, configDBURLEnvVar+`: ""`) {
		t.Errorf("secrets/dev.yaml missing the empty %s slot — a declared sensitive ref "+
			"must still appear as a labelled slot:\n%s", configDBURLEnvVar, got)
	}
	// The store must remain readable by the provider that consumes it.
	if _, err := secrets.ReadSecretFile(filepath.Join(dir, EnvSecretsFileName(configDevEnvName))); err != nil {
		t.Errorf("scaffolded store is not readable by the file provider: %v\n%s", err, got)
	}
}

// TestDevConfigKDSNFollowsComposePort covers the second emit site: a project
// that un-marked database_url as sensitive gets its DSN pinned in
// deploy/kcl/dev/config.k instead of .env.dev. Both sites must derive the
// port from the same fact, or the bug simply moves.
func TestDevConfigKDSNFollowsComposePort(t *testing.T) {
	t.Setenv(devpg.PortEnv, "15433")

	fields := []ConfigField{{Name: "database_url", EnvVar: configDBURLEnvVar}}
	lines := configKValueLines(fields, "pt", configDevEnvName)

	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, ":5434/") {
		t.Errorf("dev config.k still pins the hardcoded 5434:\n%s", joined)
	}
	if !strings.Contains(joined, "localhost:15433/pt") {
		t.Errorf("dev config.k DSN should name localhost:15433/pt; got:\n%s", joined)
	}
}
