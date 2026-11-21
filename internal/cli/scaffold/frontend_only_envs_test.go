package scaffold

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// writeFrontendOnlyEnvs writes dev, staging and prod exactly as `forge
// project new` renders them for a project born with no service, no database
// and no frontend it knows of: kcl/frontend-only/main.k.tmpl.
func writeFrontendOnlyEnvs(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, env := range []string{"dev", "staging", "prod"} {
		body, err := templates.DeployTemplates().Render("kcl/frontend-only/main.k.tmpl", templates.EnvTemplateData{
			ProjectName: "acme", EnvName: env, DatabaseDisabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "deploy", "kcl", env)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.k"), body, 0o644); err != nil {
			t.Fatal(err)
		}
		out[env] = string(body)
	}
	return out
}

// The first service of a frontend-only project (forge bugs aee44198,
// e208f389): the envs have no `_workloads` list, so nothing is bound and no
// "add to its `_workloads` list" hint is printed for a list that does not
// exist. Instead the scaffold names the envs and the rescaffold commands that
// give them the backend shape (verified to render), and leaves the files —
// the user's — untouched.
func TestBindWorkloadInEnvs_FrontendOnlyEnvsGetTheGrowGuidance(t *testing.T) {
	root := t.TempDir()
	before := writeFrontendOnlyEnvs(t, root)
	comp := config.ComponentConfig{Name: "contact", Kind: config.ComponentKindServer}
	cfg := &config.ProjectConfig{Name: "acme", ModulePath: "github.com/acme/acme"}

	var envs []string
	out, _ := captureStdout(t, func() error {
		envs = bindWorkloadInEnvs(root, cfg.Name, comp)
		reportFrontendOnlyEnvs(root, cfg, comp, envs)
		return nil
	})
	if want := []string{"dev", "prod", "staging"}; !reflect.DeepEqual(envs, want) {
		t.Fatalf("frontend-only envs = %v, want %v", envs, want)
	}
	for env, body := range before {
		if got := readEnvMain(t, root, env); got != body {
			t.Errorf("%s/main.k is the user's and must not be rewritten", env)
		}
	}
	if strings.Contains(out, "add to its `_workloads` list") {
		t.Errorf("printed a binding for a `_workloads` list the env does not have:\n%s", out)
	}
	for _, want := range []string{
		"deploy/kcl/dev/main.k, deploy/kcl/prod/main.k, deploy/kcl/staging/main.k were scaffolded for a frontend with no backend",
		"service 'contact' runs in none of them",
		"mv deploy/kcl/dev/main.k deploy/kcl/dev/main.k.frontend-only",
		"mv deploy/kcl/prod/main.k deploy/kcl/prod/main.k.frontend-only",
		"project rescaffold deploy/kcl/dev/main.k deploy/kcl/prod/main.k deploy/kcl/staging/main.k deploy/kcl/base/env.k deploy/kcl/ingress.k deploy/kcl/dev/ingress.k deploy/kcl/prod/ingress.k deploy/kcl/staging/ingress.k\n",
		"your frontend's declaration",
		"generate` (it writes the deploy/kcl/config_gen.k",
		"env render <env>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guidance lacks %q:\n%s", want, out)
		}
	}
	// No migrations: nothing about the database.
	if strings.Contains(out, "serve.go") || strings.Contains(out, "migrate") {
		t.Errorf("a project with no migrations was told about its database:\n%s", out)
	}
}

// With migrations, the two other files born before the database are named:
// a serve.go that hands serverkit no DatabaseURL (re-scaffolded with the
// envs), and a workloads.k with no migrate job (its stanza printed). Modules
// that already exist are not offered to rescaffold, which refuses them.
func TestFrontendOnlyEnvsNotice_NamesTheDatabaseGaps(t *testing.T) {
	root := t.TempDir()
	writeFrontendOnlyEnvs(t, root)
	for rel, body := range map[string]string{
		"db/migrations/20260101000000_init.up.sql": "CREATE TABLE leads (id TEXT PRIMARY KEY);\n",
		"cmd/acme/cmd/serve.go":                    "package cmd\n\nvar skCfg = serverkitConfig{Port: 8080}\n",
		"deploy/kcl/workloads.k":                   "import forge.workload as fw\n\nleads = fw.Workload {name = \"leads\"}\n",
		"deploy/kcl/ingress.k":                     "# yours\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.ProjectConfig{Name: "acme", ModulePath: "github.com/acme/acme"}
	out := frontendOnlyEnvsNotice(root, cfg, config.ComponentConfig{Name: "leads", Kind: config.ComponentKindServer}, []string{"prod"})
	for _, want := range []string{
		"mv cmd/acme/cmd/serve.go cmd/acme/cmd/serve.go.frontend-only",
		"project rescaffold deploy/kcl/prod/main.k deploy/kcl/base/env.k deploy/kcl/prod/ingress.k cmd/acme/cmd/serve.go\n",
		"passes no\n   DatabaseURL or AutoMigrate to serverkit",
		"deploy/kcl/workloads.k declares no `migrate` job",
		"     migrate = fw.Workload",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guidance lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "rescaffold deploy/kcl/prod/main.k deploy/kcl/base/env.k deploy/kcl/ingress.k") {
		t.Errorf("an existing deploy/kcl/ingress.k was offered to rescaffold, which refuses it:\n%s", out)
	}

	// A serve.go that already passes the database, and a declared migrate
	// job, are not mentioned.
	if err := os.WriteFile(filepath.Join(root, "cmd", "acme", "cmd", "serve.go"), []byte("package cmd\n\nvar c = serverkitConfig{DatabaseURL: cfg.DatabaseUrl}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy", "kcl", "workloads.k"), []byte("migrate = fw.Workload {name = \"migrate\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = frontendOnlyEnvsNotice(root, cfg, config.ComponentConfig{Name: "leads", Kind: config.ComponentKindServer}, []string{"prod"})
	if strings.Contains(out, "serve.go") || strings.Contains(out, "declares no `migrate` job") {
		t.Errorf("wired files were reported as gaps:\n%s", out)
	}
}
