package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// adoptTemplateBlock matches a fenced block of the `adopt` skill whose first
// line names the file it is (`# deploy/kcl/workloads.k`).
var adoptTemplateBlock = regexp.MustCompile("(?s)```(?:yaml|toml|python)\n# ([^\n]+)\n(.*?)```")

// adoptSkillFiles writes the `adopt` skill's templates into a fresh
// directory beside a Dockerfile app, as an agent following the skill would:
// every block that names a project file, and prod as a copy of staging. With
// noDB it applies the skill's "no database" edit (delete the migrate
// workload and every line marked `# db`).
func adoptSkillFiles(t *testing.T, noDB bool) string {
	t.Helper()
	body, err := RenderSkillContentAt("", "adopt/deploy", SkillAudienceAll, "forge")
	if err != nil {
		t.Fatalf("load the adopt/deploy skill: %v", err)
	}
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", "FROM node:20-slim\nWORKDIR /app\nCOPY . .\nEXPOSE 3000\nCMD [\"node\", \"dist/main.js\"]\n")
	write("package.json", `{"name":"myapp","scripts":{"dev":"node --watch src/main.js"}}`)
	seen := map[string]bool{}
	for _, m := range adoptTemplateBlock.FindAllStringSubmatch(string(body), -1) {
		rel, content := strings.TrimSpace(m[1]), m[2]
		if rel != "forge.yaml" && !strings.HasPrefix(rel, "deploy/kcl/") {
			continue
		}
		if noDB {
			content = dropAdoptDB(content)
		}
		seen[rel] = true
		write(rel, content)
		if rel == "deploy/kcl/staging/main.k" {
			write("deploy/kcl/prod/main.k", strings.Replace(content, `"""staging:`, `"""prod:`, 1))
		}
	}
	for _, rel := range []string{"forge.yaml", "deploy/kcl/kcl.mod", "deploy/kcl/workloads.k", "deploy/kcl/dev/main.k", "deploy/kcl/staging/main.k"} {
		if !seen[rel] {
			t.Fatalf("the adopt/deploy skill has no template for %s (a fenced block whose first line is `# %s`)", rel, rel)
		}
	}
	return root
}

// dropAdoptDB is the skill's no-database edit.
func dropAdoptDB(content string) string {
	var out []string
	inMigrate := false
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "migrate = fw.Workload {"):
			inMigrate = true
		case inMigrate:
			inMigrate = line != "}"
		case !strings.Contains(line, "# db"):
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// The `adopt` skill's templates are what an agent copies into an app, so
// they must render as written: dev binds the app and its migration job to
// host processes beside a host-native Postgres, staging and prod bind them
// to Reliant hosting with a managed database and a managed secret. The
// skill's "no database" edit must leave templates that still render.
func TestAdoptSkillTemplatesRender(t *testing.T) {
	testkit.Slow(t, "renders the adopt skill's templates through native KCL, twice per env")

	root := adoptSkillFiles(t, false)
	dev, err := RenderKCL(t.Context(), root, "dev")
	if err != nil {
		t.Fatalf("render dev from the skill: %v", err)
	}
	api, migrate := dev.FindWorkload("api"), dev.FindWorkload("migrate")
	if api == nil || !api.OnRuntime(RuntimeHost) || api.Build.Type != "docker" || strings.Join(api.Spec.Command, " ") != "npm run dev" {
		t.Fatalf("dev api = %+v, want the DockerBuild workload on the host running npm run dev", api)
	}
	if migrate == nil || !migrate.OnRuntime(RuntimeHost) || migrate.Kind != "job" {
		t.Fatalf("dev migrate = %+v, want a host job", migrate)
	}
	env := api.HostEnv()
	if env["PORT"] == "" || !strings.HasPrefix(env["DATABASE_URL"], "postgres://") {
		t.Errorf("dev api env = %v, want PORT and DATABASE_URL", env)
	}
	if !hasManagedSecret(api, "JWT_SECRET") {
		t.Errorf("dev api env %+v does not deliver JWT_SECRET from the dev secret store", api.Spec.Env)
	}
	if len(dev.Infra) != 1 || dev.Infra[0].Name != "postgres" {
		t.Errorf("dev infra = %+v, want the host-native postgres", dev.Infra)
	}

	for _, envName := range []string{"staging", "prod"} {
		e, err := RenderKCL(t.Context(), root, envName)
		if err != nil {
			t.Fatalf("render %s from the skill: %v", envName, err)
		}
		api := e.FindWorkload("api")
		if api == nil || !api.OnRuntime(RuntimeHosted) || api.Image != "api" || api.Spec.Probes == nil || !api.Spec.Probes.TCP {
			t.Fatalf("%s api = %+v, want the bare image api on hosting with a tcp probe", envName, api)
		}
		var dbRef bool
		for _, v := range api.Spec.Env {
			if v.Name == "DATABASE_URL" && v.DatabaseRef != nil && v.DatabaseRef.Name == "myapp" {
				dbRef = true
			}
		}
		if !dbRef || !hasManagedSecret(api, "JWT_SECRET") {
			t.Errorf("%s api env %+v, want DATABASE_URL from the managed database and JWT_SECRET from the store", envName, api.Spec.Env)
		}
		if m := e.FindWorkload("migrate"); m == nil || !m.OnRuntime(RuntimeHosted) || len(m.Spec.Before) == 0 {
			t.Errorf("%s migrate = %+v, want a hosted pre-rollout job", envName, m)
		}
	}

	bare := adoptSkillFiles(t, true)
	for _, envName := range []string{"dev", "staging"} {
		e, err := RenderKCL(t.Context(), bare, envName)
		if err != nil {
			t.Fatalf("render %s after the skill's no-database edit: %v", envName, err)
		}
		if e.FindWorkload("api") == nil || e.FindWorkload("migrate") != nil || len(e.Infra) != 0 {
			t.Errorf("%s without a database: workloads %+v infra %+v", envName, e.Workloads, e.Infra)
		}
	}
}

func hasManagedSecret(w *WorkloadEntity, name string) bool {
	for _, v := range w.Spec.Env {
		if v.Name == name && v.ManagedSecret != nil && v.ManagedSecret.Name == name {
			return true
		}
	}
	return false
}
