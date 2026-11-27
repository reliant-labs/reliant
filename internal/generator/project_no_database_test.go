package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// An API with no database (orm and migrations off, no frontend) must neither
// require DATABASE_URL to start nor declare it as a secret its hosted envs
// must provide: those envs declare no managed database, so the first deploy
// waited forever on a secret nothing could set (obs a931e0c4).
func TestServiceWithoutDatabaseNeedsNoDatabaseURL(t *testing.T) {
	testkit.Slow(t, "execs go")
	root := t.TempDir()
	g := NewProjectGenerator("hello", root, "example.com/hello")
	g.ServiceName = "greeter"
	g.Features = g.Features.With(config.FeatureMigrations, false).With(config.FeatureORM, false)
	if err := g.createConfigProto(); err != nil {
		t.Fatal(err)
	}
	if err := g.generateKCLDeploy(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(configproto.For("hello").RootPath())))
	if err != nil {
		t.Fatal(err)
	}
	field := string(raw)[strings.Index(string(raw), "string database_url"):]
	field = field[:strings.Index(field, "}];")]
	if strings.Contains(field, "required: true") {
		t.Errorf("database_url is still required in a project with no database:\n%s", field)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		raw, err := os.ReadFile(filepath.Join(root, "deploy/kcl", env, "main.k"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `config_secrets = ["DATABASE_URL"`) || strings.Contains(string(raw), "forge.ManagedDatabase {") {
			t.Errorf("%s/main.k declares a database the project does not have:\n%s", env, raw)
		}
	}
}

// With a database the scaffold is unchanged: database_url required, the API
// reading it as a secret.
func TestServiceWithDatabaseStillRequiresDatabaseURL(t *testing.T) {
	testkit.Slow(t, "execs go")
	root := t.TempDir()
	g := NewProjectGenerator("hello", root, "example.com/hello")
	g.ServiceName = "greeter"
	if err := g.createConfigProto(); err != nil {
		t.Fatal(err)
	}
	if err := g.generateKCLDeploy(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(configproto.For("hello").RootPath())))
	if err != nil {
		t.Fatal(err)
	}
	field := string(raw)[strings.Index(string(raw), "string database_url"):]
	if !strings.Contains(field[:strings.Index(field, "}];")], "required: true") {
		t.Errorf("database_url lost required: true:\n%s", field)
	}
	prod, err := os.ReadFile(filepath.Join(root, "deploy/kcl/prod/main.k"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prod), `"DATABASE_URL"`) {
		t.Errorf("prod/main.k lost the API's DATABASE_URL:\n%s", prod)
	}
}
