package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestFrontendOnlyDeployHasNoUnusedInfrastructure(t *testing.T) {
	root := t.TempDir()
	g := NewProjectGenerator("landing", root, "example.com/landing")
	g.FrontendName = "web"
	g.Features = g.Features.With(config.FeatureMigrations, false).With(config.FeatureORM, false)
	if err := g.generateKCLDeploy(); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		raw, err := os.ReadFile(filepath.Join(root, "deploy/kcl", env, "main.k"))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"wl.migrate", "forge.ClusterTarget {", "forge.HostInfra {", "forge.ManagedDatabase {", "database_url", "DATABASE_URL", "network_policy =", "identity ="} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("%s contains unused %s", env, forbidden)
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "deploy/kcl/workloads.k"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "migrate =") || strings.Contains(string(raw), "[migrate") {
		t.Fatalf("disabled migration declared:\n%s", raw)
	}
}
