package codegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"
)

func writeTypedConfigFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestProjectHasTypedConfig(t *testing.T) {
	none := t.TempDir()
	// An empty config dir (a CLI scaffold leaves one) is not config.
	if err := os.MkdirAll(filepath.Join(none, filepath.FromSlash(configproto.ForProject(none).Dir())), 0o755); err != nil {
		t.Fatal(err)
	}
	if HasConfigProtos(none) || ProjectHasTypedConfig(none) {
		t.Error("no config protos and no pkg/config: no typed config")
	}

	protos := t.TempDir()
	writeTypedConfigFile(t, protos, configproto.ForProject(protos).RootPath(), "syntax = \"proto3\";\n")
	if !HasConfigProtos(protos) || !ProjectHasTypedConfig(protos) {
		t.Error("config protos are typed config")
	}

	stub := t.TempDir()
	writeTypedConfigFile(t, stub, "pkg/config/config.go", "package config\n\ntype Config struct{}\n")
	if HasConfigProtos(stub) || !ProjectHasTypedConfig(stub) {
		t.Error("a pkg/config declaring Config is typed config, without being config protos")
	}
}

// The handler scaffold imports <module>/pkg/config and declares
// Deps.Config only when the project has typed config. Adopting a service
// into a project without it (reliant) used to scaffold a service.go that
// did not compile, and generate rolled back blaming go.mod (P3b F13).
func TestServiceTemplate_ConfigOnlyWithTypedConfig(t *testing.T) {
	svc := ServiceDef{
		Name: "GreeterService", ModulePath: "example.com/app",
		GoPackage: "example.com/app/gen/app/v1", PkgName: "appv1", ProtoFile: "proto/app/v1/app.proto",
	}
	render := func(projectDir string) string {
		t.Helper()
		data := mapServiceDefToTemplateData(svc, projectDir)
		out, err := templates.ServiceTemplates().Render("service.go.tmpl", data)
		if err != nil {
			t.Fatal(err)
		}
		if _, perr := parser.ParseFile(token.NewFileSet(), "service.go", out, 0); perr != nil {
			t.Fatalf("rendered service.go does not parse: %v\n%s", perr, out)
		}
		return string(out)
	}

	without := render(t.TempDir())
	for _, banned := range []string{"pkg/config", "Config *config.Config", "Deps.Config"} {
		if strings.Contains(without, banned) {
			t.Errorf("a project without typed config must not get %q:\n%s", banned, without)
		}
	}

	with := t.TempDir()
	writeTypedConfigFile(t, with, configproto.ForProject(with).RootPath(), "syntax = \"proto3\";\n")
	got := render(with)
	for _, want := range []string{`"example.com/app/pkg/config"`, "Config *config.Config", "Deps.Config is required"} {
		if !strings.Contains(got, want) {
			t.Errorf("a project with typed config must get %q:\n%s", want, got)
		}
	}
}
