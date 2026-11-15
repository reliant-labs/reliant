package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The project-global AppConfig carries no (forge.v1.binary_config) for the
// runtime to derive a path variable from, so the generated shim names one on
// the --config flag: <PROJECT>_CONFIG, never a name every forge binary shares.
func TestGenerateConfigLoader_ProjectConfigNamesItsOwnPathVariable(t *testing.T) {
	got := genConfigGo(t, basicMessages()) // module example.com/proj, no forge.yaml
	for _, want := range []string{
		`const ConfigPathEnv = "PROJ_CONFIG"`,
		"forgeconfig.RegisterConfigFlag(cmd, ConfigPathEnv)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated config_gen.go missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "FORGE_CONFIG") {
		t.Errorf("generated config_gen.go still names FORGE_CONFIG:\n%s", got)
	}
}

// forge.yaml's project name wins over the module path: it is the name the
// primary binary (cmd/<name>) carries, so binding AppConfig to that binary
// later keeps the variable.
func TestProjectConfigName(t *testing.T) {
	dir := t.TempDir()
	if got := projectConfigName(dir, "github.com/acme/widgets/v2"); got != "widgets" {
		t.Errorf("no forge.yaml: projectConfigName = %q, want widgets (module base, /v2 skipped)", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: my-app\nmodule_path: github.com/acme/widgets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectConfigName(dir, "github.com/acme/widgets"); got != "my-app" {
		t.Errorf("with forge.yaml: projectConfigName = %q, want my-app", got)
	}
}

// Per-binary configs need no generated name: the runtime derives
// <BINARY>_CONFIG from the binding. The generated docs name it all the same,
// so `go doc` and the source tell an operator which variable to set.
func TestConfigTemplate_PerBinaryDocsNameThePathVariable(t *testing.T) {
	got := renderConfigGo(t, commandScopedMessages())
	if !strings.Contains(got, "RELIANT_CONFIG") {
		t.Errorf("per-binary config docs must name RELIANT_CONFIG:\n%s", got)
	}
}

// A field whose env var IS its process's config-file path variable would be
// read twice — as a path and as a value — so generate refuses it.
func TestGenerateConfigLoader_RefusesFieldNamedLikeThePathVariable(t *testing.T) {
	for name, msgs := range map[string][]ConfigMessage{
		"project-global": {{
			Name: "AppConfig",
			Fields: []ConfigField{{Name: "blob", GoName: "Blob", GoType: "string", ProtoType: "string",
				EnvVar: "PROJ_CONFIG"}},
		}},
		"per-binary": {{
			Name: "ReliantConfig", Binary: "reliant",
			Fields: []ConfigField{{Name: "blob", GoName: "Blob", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_CONFIG"}},
		}},
		// An alias is read exactly as env_var is.
		"alias": {{
			Name: "ReliantConfig", Binary: "reliant",
			Fields: []ConfigField{{Name: "blob", GoName: "Blob", GoType: "string", ProtoType: "string",
				EnvVar: "RELIANT_BLOB", EnvVarAliases: []EnvVarAlias{{Name: "RELIANT_CONFIG", Deprecated: true}}}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/proj\n\ngo 1.24\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := GenerateConfigLoader(msgs, dir, nil)
			if err == nil || !strings.Contains(err.Error(), "config-file path variable") {
				t.Fatalf("GenerateConfigLoader error = %v, want the path-variable collision refused", err)
			}
		})
	}
}
