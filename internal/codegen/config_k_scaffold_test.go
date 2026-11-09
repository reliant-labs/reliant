package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devpg"
	"github.com/reliant-labs/forge/internal/secrets"
)

func defaultConfigFields() []ConfigField {
	var fields []ConfigField
	for _, m := range DefaultConfigMessages() {
		fields = append(fields, m.Fields...)
	}
	return fields
}

// TestGenerateConfigKScaffold_DevSeedsMode: the dev env's config.k is
// scaffolded turnkey — the MODE marker (environment = "development") plus the
// boots-alive auto_migrate seed — so `forge env up` boots the dev backend in a
// positively-development mode against a migrated database.
func TestGenerateConfigKScaffold_DevSeedsMode(t *testing.T) {
	dir := t.TempDir()
	fields := defaultConfigFields()

	if _, err := GenerateConfigKScaffold(fields, "myapp", filepath.Join(dir, "deploy", "kcl"), "dev"); err != nil {
		t.Fatalf("scaffold dev config.k: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "dev", "config.k"))
	if err != nil {
		t.Fatalf("read dev config.k: %v", err)
	}
	got := string(body)

	if !strings.Contains(got, `environment = "development"`) {
		t.Errorf("dev config.k missing MODE marker environment = \"development\":\n%s", got)
	}
	// Dev boots alive: auto_migrate is seeded True so the app applies its
	// migrations on the first `forge env up` boot (the projection lowercases it to
	// AUTO_MIGRATE=true). Without this the projected schema default (false)
	// would override the dev-run default and the app would boot tableless.
	if !strings.Contains(got, `auto_migrate = True`) {
		t.Errorf("dev config.k missing auto_migrate = True (dev boots alive):\n%s", got)
	}
	if !strings.Contains(got, "app_config: "+ConfigSchemaModule+".AppConfig = {") {
		t.Errorf("dev config.k missing typed AppConfig instance:\n%s", got)
	}
	// Sparse: a defaulted field (log_level has a schema default) is NOT pinned.
	if strings.Contains(got, "log_level") {
		t.Errorf("dev config.k pinned a defaulted field (log_level) — should inherit:\n%s", got)
	}
}

// TestGenerateConfigKScaffold_NeverPinsSensitive: config.k is GIT-TRACKED, so a
// `sensitive` field never appears in it — in ANY environment. Its AppConfig type
// is a ConfigSecretRef (a Secret name+key reference carrying its own schema
// default), and its VALUE comes from the env's secret provider.
//
// The failure this pins closed is the one that made every rendered Deployment
// carry DATABASE_URL as a literal `value:`: a credential-shaped field pinned as
// a string in a committed .k file, projected inline into the manifest.
func TestGenerateConfigKScaffold_NeverPinsSensitive(t *testing.T) {
	fields := defaultConfigFields()

	sawSensitive := false
	for _, f := range fields {
		if f.Sensitive {
			sawSensitive = true
		}
	}
	if !sawSensitive {
		t.Fatal("the default config set declares no sensitive field — this test would be vacuous " +
			"(database_url must carry sensitive: true)")
	}

	for _, env := range []string{"dev", "staging", "prod"} {
		dir := t.TempDir()
		if _, err := GenerateConfigKScaffold(fields, "myapp", filepath.Join(dir, "deploy", "kcl"), env); err != nil {
			t.Fatalf("scaffold %s config.k: %v", env, err)
		}
		body, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", env, "config.k"))
		if err != nil {
			t.Fatalf("read %s config.k: %v", env, err)
		}
		got := string(body)
		for _, f := range fields {
			if !f.Sensitive {
				continue
			}
			if strings.Contains(got, f.Name) {
				t.Errorf("%s config.k pins the sensitive field %q — a credential must never "+
					"land in a git-tracked file:\n%s", env, f.Name, got)
			}
		}
		if strings.Contains(got, "postgres://") {
			t.Errorf("%s config.k carries a concrete DSN — the value belongs in the gitignored "+
				"secrets dotenv, not here:\n%s", env, got)
		}
	}
}

// TestGenerateConfigKScaffold_NonDevIsSparse: a non-dev env's config.k carries
// no dev-only seeds. With database_url sensitive (and therefore absent), the
// scaffolded prod instance is an empty AppConfig — every field inherits its
// schema default.
func TestGenerateConfigKScaffold_NonDevIsSparse(t *testing.T) {
	dir := t.TempDir()

	if _, err := GenerateConfigKScaffold(defaultConfigFields(), "myapp", filepath.Join(dir, "deploy", "kcl"), "prod"); err != nil {
		t.Fatalf("scaffold prod config.k: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "prod", "config.k"))
	if err != nil {
		t.Fatalf("read prod config.k: %v", err)
	}
	got := string(body)

	if strings.Contains(got, `environment = "development"`) {
		t.Errorf("prod config.k must NOT carry the dev MODE marker:\n%s", got)
	}
	// auto_migrate is a DEV-ONLY boots-alive seed; a cloud deploy owns its
	// migration story (the rendered migration initContainer) and must inherit
	// the proto default (false), never an implicit on-boot migrate.
	if strings.Contains(got, "auto_migrate") {
		t.Errorf("prod config.k must NOT seed auto_migrate (dev-only boots-alive seed):\n%s", got)
	}
}

// TestGenerateConfigKScaffold_UnmarkedDBURLStillSeedsDev: a project that
// deliberately un-marks the database field still gets the turnkey local DSN in
// its dev config.k. The sensitive skip must not silently drop the dev seed for
// projects that opted out.
func TestGenerateConfigKScaffold_UnmarkedDBURLStillSeedsDev(t *testing.T) {
	dir := t.TempDir()
	fields := defaultConfigFields()
	for i := range fields {
		if fields[i].EnvVar == "DATABASE_URL" {
			fields[i].Sensitive = false
		}
	}

	if _, err := GenerateConfigKScaffold(fields, "myapp", filepath.Join(dir, "deploy", "kcl"), "dev"); err != nil {
		t.Fatalf("scaffold dev config.k: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "dev", "config.k"))
	if err != nil {
		t.Fatalf("read dev config.k: %v", err)
	}
	// The seeded DSN names the port the project's docker-compose publishes
	// postgres on (${POSTGRES_PORT:-5432}), not a hardcoded one — see
	// devDatabaseDSN. Pinning a literal port here is what let the scaffold
	// and compose disagree silently.
	wantDSN := fmt.Sprintf(`database_url = %q`, devpg.DSN("myapp"))
	if !strings.Contains(string(body), wantDSN) {
		t.Errorf("dev config.k for an UNMARKED database_url must still seed the local DSN %q:\n%s",
			wantDSN, body)
	}
}

// TestGenerateEnvSecretsScaffold_DevOnly: the VALUES half of every sensitive
// field gets a slot in the gitignored `secrets/dev.yaml`, scaffolded EMPTY —
// including DATABASE_URL, whose value the env's KCL declares per render (see
// devSecretValue). A CLOUD env gets no store at all — its provider is
// external and forge must never hold its credentials.
//
// The store is YAML at secrets/<env>.yaml, NOT a dotenv: the dotenv provider
// was removed, and `forge lint`'s no-dotenv rule rejects `.env*` outright, so
// a scaffold that wrote one failed forge's own linter on a brand-new project.
func TestGenerateEnvSecretsScaffold_DevOnly(t *testing.T) {
	fields := defaultConfigFields()

	dir := t.TempDir()
	wrote, err := GenerateEnvSecretsScaffold(fields, "myapp", dir, "dev")
	if err != nil {
		t.Fatalf("scaffold secrets/dev.yaml: %v", err)
	}
	if !wrote {
		t.Fatal("expected a fresh secrets/dev.yaml to be written")
	}
	body, err := os.ReadFile(filepath.Join(dir, "secrets", "dev.yaml"))
	if err != nil {
		t.Fatalf("read secrets/dev.yaml: %v", err)
	}
	got := string(body)
	// A labelled slot, with no value: the dev connection string is composed
	// in deploy/kcl/dev/main.k from the port it resolves there, so a copy
	// here would be a second declaration of the port that goes stale the
	// first time 5432 is busy. See devSecretValue.
	if !strings.Contains(got, `DATABASE_URL: ""`) {
		t.Errorf("secrets/dev.yaml missing the empty DATABASE_URL slot:\n%s", got)
	}
	if strings.Contains(got, "postgres://") {
		t.Errorf("secrets/dev.yaml carries a DSN — the dev port is KCL's fact, not this file's:\n%s", got)
	}
	// The store must parse as the YAML the file provider reads back.
	if _, err := secrets.ReadSecretFile(filepath.Join(dir, "secrets", "dev.yaml")); err != nil {
		t.Errorf("scaffolded store is not readable by the file provider: %v\n%s", err, got)
	}
	// It must never be a dotenv again — forge lint rejects those.
	if _, statErr := os.Stat(filepath.Join(dir, ".env.dev")); !os.IsNotExist(statErr) {
		t.Error(".env.dev was written; forge lint's no-dotenv rule rejects it")
	}

	for _, env := range []string{"staging", "prod"} {
		cloudDir := t.TempDir()
		wrote, err := GenerateEnvSecretsScaffold(fields, "myapp", cloudDir, env)
		if err != nil {
			t.Fatalf("scaffold secrets/%s.yaml: %v", env, err)
		}
		if wrote {
			t.Errorf("%s must NOT get a secret store — its provider is external, and a second "+
				"unread place to put a production credential is a footgun", env)
		}
		if _, statErr := os.Stat(filepath.Join(cloudDir, "secrets", env+".yaml")); !os.IsNotExist(statErr) {
			t.Errorf("secrets/%s.yaml exists but must not", env)
		}
	}
}

// TestGenerateEnvSecretsScaffold_NeverClobbers: the store holds a developer's
// REAL local credentials. Regenerating must leave an existing file alone.
func TestGenerateEnvSecretsScaffold_NeverClobbers(t *testing.T) {
	dir := t.TempDir()
	existing := "DATABASE_URL: \"postgres://me:hunter2@localhost:5432/mine\"\n"
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets", "dev.yaml"), []byte(existing), 0o600); err != nil {
		t.Fatalf("seed existing store: %v", err)
	}

	wrote, err := GenerateEnvSecretsScaffold(defaultConfigFields(), "myapp", dir, "dev")
	if err != nil {
		t.Fatalf("scaffold secrets/dev.yaml: %v", err)
	}
	if wrote {
		t.Error("an existing secrets/dev.yaml must be left untouched")
	}
	body, _ := os.ReadFile(filepath.Join(dir, "secrets", "dev.yaml"))
	if string(body) != existing {
		t.Errorf("secrets/dev.yaml was rewritten:\n%s", body)
	}
}
