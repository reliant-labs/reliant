package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeSurfaceTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The surface is read off the tree with the loader's own derivation, and
// describes a project whose forge.yaml the strict loader would refuse — a key
// a newer forge wrote, a mistyped value — because what only describes a
// project (the agent memory) must not go blank over it.
func TestReadSurfaceIsLenientAndDerived(t *testing.T) {
	t.Parallel()
	root := writeSurfaceTree(t, map[string]string{
		"forge.yaml": "name: shop\nenvs: [prod, staging]\nmonitoring:\n  endpoint: https://otel.example.com\n" +
			"database:\n  migrations:\n    tool: Goose\n    dir: sql\nsomething_newer: {x: 1}\ndev_stack: nope\n",
		"deploy/kcl/kcl.mod":           "",
		"deploy/kcl/prod/main.k":       "",
		"deploy/kcl/dev/main.k":        "",
		"deploy/kcl/lib/helpers.k":     "",
		"deploy/kcl/workloads.k":       "",
		"frontends/web/package.json":   `{"dependencies":{"vite":"^7"}}`,
		"frontends/web/vite.config.ts": "export default {}\n",
	})
	if _, err := LoadProjectDir(root); err == nil {
		t.Fatal("fixture should be one the strict loader refuses")
	}
	s, err := ReadSurface(root)
	if err != nil {
		t.Fatalf("ReadSurface: %v", err)
	}
	if s.Config.Name != "shop" {
		t.Errorf("name = %q", s.Config.Name)
	}
	if !s.DeployTree || !s.Workloads || s.GoModule || s.ServerSources || s.RootFrontend {
		t.Errorf("shape = %+v", s)
	}
	if want := []string{"dev", "prod"}; !reflect.DeepEqual(s.DeployEnvs, want) {
		t.Errorf("DeployEnvs = %v, want %v (lib/ holds no main.k)", s.DeployEnvs, want)
	}
	if !s.Monitoring || !reflect.DeepEqual(s.Envs, []string{"prod", "staging"}) {
		t.Errorf("monitoring = %v, envs = %v", s.Monitoring, s.Envs)
	}
	if s.MigrationTool != "goose" || s.MigrationsDir != "sql" {
		t.Errorf("migrations = %q in %q", s.MigrationTool, s.MigrationsDir)
	}
	if len(s.Config.Frontends) != 1 || s.Config.Frontends[0].Name != "web" {
		t.Errorf("frontends = %+v", s.Config.Frontends)
	}
	if !s.Config.Features.DeployEnabled() {
		t.Error("the derived features did not apply: deploy is off with a deploy tree")
	}
}

func TestReadSurfaceMonitoringSpellings(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]bool{
		"monitoring: true\n":                      true,
		"monitoring: false\n":                     false,
		"monitoring: {}\n":                        true,
		"monitoring:\n  enabled: false\n":         false,
		"monitoring:\n  console_url: https://x\n": true,
		"": false,
	} {
		root := writeSurfaceTree(t, map[string]string{"forge.yaml": "name: shop\n" + body})
		s, err := ReadSurface(root)
		if err != nil {
			t.Fatal(err)
		}
		if s.Monitoring != want {
			t.Errorf("%q: Monitoring = %v, want %v", body, s.Monitoring, want)
		}
	}
}

func TestReadSurfaceRefusesOnlyWhatIsNotYAML(t *testing.T) {
	t.Parallel()
	if _, err := ReadSurface(t.TempDir()); err == nil {
		t.Error("no forge.yaml must be an error")
	}
	root := writeSurfaceTree(t, map[string]string{"forge.yaml": "name: [unclosed\n"})
	if _, err := ReadSurface(root); err == nil {
		t.Error("forge.yaml that is not YAML must be an error")
	}
}
