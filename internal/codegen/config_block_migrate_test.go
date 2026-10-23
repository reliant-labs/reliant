package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migrateFixtureFields() []ConfigField {
	return FlattenBlockLeaves([]ConfigMessage{
		{Name: "AppConfig", Fields: []ConfigField{
			{Name: "log_level", GoName: "LogLevel", GoType: "string", ProtoType: "string", EnvVar: "LOG_LEVEL", DefaultValue: "info"},
			{Name: "github", GoName: "Github", ProtoType: "message", MessageType: "GithubConfig"},
			{Name: "static_site", GoName: "StaticSite", ProtoType: "message", MessageType: "StaticSiteConfig"},
			{Name: "simple_backend", GoName: "SimpleBackend", ProtoType: "message", MessageType: "SimpleBackendConfig"},
		}},
		{Name: "GithubConfig", Fields: []ConfigField{
			{Name: "github_client_id", GoName: "GithubClientId", GoType: "string", ProtoType: "string", EnvVar: "GITHUB_CLIENT_ID"},
			{Name: "github_redirect_uri", GoName: "GithubRedirectUri", GoType: "string", ProtoType: "string", EnvVar: "GITHUB_REDIRECT_URI"},
		}},
		{Name: "StaticSiteConfig", Fields: []ConfigField{
			{Name: "base_domain", GoName: "BaseDomain", GoType: "string", ProtoType: "string", EnvVar: "STATIC_SITE_BASE_DOMAIN"},
		}},
		{Name: "SimpleBackendConfig", Fields: []ConfigField{
			{Name: "base_domain", GoName: "BaseDomain", GoType: "string", ProtoType: "string", EnvVar: "SIMPLE_BACKEND_BASE_DOMAIN"},
		}},
	}, "AppConfig")
}

// Existing projects author block leaves flat (the old projection), and main.k
// reads them flat. Nesting the blocks must not strand those user-owned files:
// forge moves the keys it caused to dangle, and ONLY those.
func TestMigrateConfigBlockReferences(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "dev")
	configK := `import config_gen

app_config: config_gen.AppConfig = {
    log_level = "debug"
    # the OAuth app for local dev
    github_client_id = "Iv23li"
    github_redirect_uri = "http://x/cb"
    static_site_base_domain = "sites.local"
    nested = {
        github_client_id = "not-an-app-config-key"
    }
}
`
	mainK := `import .config as appcfg

_redir = appcfg.app_config.github_redirect_uri
_lvl = appcfg.app_config.log_level
`
	mustWrite(t, filepath.Join(env, "config.k"), configK)
	mustWrite(t, filepath.Join(env, "main.k"), mainK)

	moved, err := MigrateConfigBlockReferences(dir, []ConfigInstanceBlocks{{Var: "app_config", Fields: migrateFixtureFields()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 {
		t.Fatalf("moved = %v, want config.k and main.k", moved)
	}
	gotConfig, _ := os.ReadFile(filepath.Join(env, "config.k"))
	for _, want := range []string{
		`    log_level = "debug"`,                // root field untouched
		`    # the OAuth app for local dev`,      // comments survive
		`    github.github_client_id = "Iv23li"`, // bare leaf → path
		`    github.github_redirect_uri = "http://x/cb"`,
		`    static_site.base_domain = "sites.local"`,        // legacy qualified → path
		`        github_client_id = "not-an-app-config-key"`, // nested literal untouched
	} {
		if !strings.Contains(string(gotConfig), want) {
			t.Errorf("config.k missing %q:\n%s", want, gotConfig)
		}
	}
	gotMain, _ := os.ReadFile(filepath.Join(env, "main.k"))
	for _, want := range []string{"appcfg.app_config.github.github_redirect_uri", "appcfg.app_config.log_level"} {
		if !strings.Contains(string(gotMain), want) {
			t.Errorf("main.k missing %q:\n%s", want, gotMain)
		}
	}

	// Idempotent: a second pass changes nothing.
	again, err := MigrateConfigBlockReferences(dir, []ConfigInstanceBlocks{{Var: "app_config", Fields: migrateFixtureFields()}})
	if err != nil || len(again) != 0 {
		t.Fatalf("second migration = %v, %v; want no-op", again, err)
	}
}

// A bare leaf two blocks declare (base_domain) is ambiguous — it is left for
// the author rather than guessed.
func TestMigrateConfigBlockReferences_AmbiguousLeafIsLeftAlone(t *testing.T) {
	r := blockRenames(migrateFixtureFields())
	if _, ok := r["base_domain"]; ok {
		t.Fatalf("an ambiguous bare leaf must not be rewritten: %v", r)
	}
	if r["simple_backend_base_domain"] != "simple_backend.base_domain" {
		t.Fatalf("legacy qualified spelling must map to its path: %v", r)
	}
}
