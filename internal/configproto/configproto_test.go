package configproto

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/internal/templates"
)

func TestSlug(t *testing.T) {
	for name, want := range map[string]string{
		"reliant":       "reliant",
		"control-plane": "control_plane",
		"MyApp":         "my_app",
		"my--app":       "my_app",
		"9lives":        "app9lives",
		"":              "app",
	} {
		if got := Slug(name); got != want {
			t.Errorf("Slug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestLayout(t *testing.T) {
	l := For("control-plane")
	for got, want := range map[string]string{
		l.Dir():      "proto/control_plane/config/v1",
		l.BufDir():   "control_plane/config/v1",
		l.RootPath(): "proto/control_plane/config/v1/config.proto",
		l.Package():  "control_plane.config.v1",
		l.GoImportPath("github.com/reliant-labs/control-plane"): "github.com/reliant-labs/control-plane/gen/control_plane/config/v1",
		l.GoPackage("github.com/reliant-labs/control-plane"):    "github.com/reliant-labs/control-plane/gen/control_plane/config/v1;configv1",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// The project's name comes from forge.yaml, else from the module path with
// a major-version suffix dropped — the same identity <PROJECT>_CONFIG uses.
func TestForProjectNamesTheLayout(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module github.com/acme/billing-api/v2\n\ngo 1.25\n")
	if got := ForProject(dir).Slug; got != "billing_api" {
		t.Errorf("from go.mod: slug %q, want billing_api", got)
	}
	write(t, dir, "forge.yaml", "name: ledger\nmodule_path: github.com/acme/billing-api/v2\n")
	if got := ForProject(dir).Slug; got != "ledger" {
		t.Errorf("from forge.yaml: slug %q, want ledger", got)
	}
}

func TestLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	if got := LegacyFiles(dir); len(got) != 0 {
		t.Fatalf("no config at all: %v", got)
	}
	write(t, dir, "proto/config/v1/config.proto", "syntax = \"proto3\";\n\npackage config.v1;\n")
	write(t, dir, "proto/config/v1/web_config.proto", "syntax = \"proto3\";\npackage config.v1;\n")
	write(t, dir, "proto/config/v1/other.proto", "syntax = \"proto3\";\npackage acme.config.v1;\n")
	got := LegacyFiles(dir)
	if strings.Join(got, ",") != "proto/config/v1/config.proto,proto/config/v1/web_config.proto" {
		t.Errorf("LegacyFiles = %v", got)
	}
	if For("acme").HasProtos(dir) {
		t.Error("HasProtos reports a project with no proto/acme/config/v1")
	}
	write(t, dir, "proto/acme/config/v1/config.proto", "syntax = \"proto3\";\n")
	if !For("acme").HasProtos(dir) {
		t.Error("HasProtos misses proto/acme/config/v1/config.proto")
	}
}

// The defect this package exists to remove. Go's protobuf runtime registers
// every generated file in one process-wide registry, keyed on the file's
// path and on every full name it declares, and panics at init on a
// conflict. So the config protos of two forge projects — reliant's and
// control-plane's, say — must differ in BOTH, or a binary that imports both
// projects' pkg/config (roots_gen.go's documented use) cannot start.
//
// The scaffolded config.proto is rendered for two projects, its package and
// go_package read back, and a descriptor with exactly that identity
// registered for each into one registry — the same check the generated
// .pb.go files make at init. The legacy identity is registered the same way
// and must conflict, so the test cannot pass by registering nothing.
func TestTwoProjectsConfigProtosRegisterInOneProcess(t *testing.T) {
	projects := []struct{ name, module string }{
		{"reliant", "github.com/reliant-labs/reliant"},
		{"control-plane", "github.com/reliant-labs/control-plane"},
	}
	files := new(protoregistry.Files)
	for _, p := range projects {
		l := For(p.name)
		src, err := templates.ProjectTemplates().Render("config.proto.tmpl", l.TemplateData(p.module))
		if err != nil {
			t.Fatalf("render config.proto.tmpl for %s: %v", p.name, err)
		}
		pkg := match(t, `(?m)^package\s+([\w.]+)\s*;`, string(src))
		goPkg := match(t, `(?m)^option\s+go_package\s*=\s*"([^"]+)"`, string(src))
		if pkg != l.Package() || goPkg != l.GoPackage(p.module) {
			t.Fatalf("%s: rendered package %q / go_package %q, want %q / %q", p.name, pkg, goPkg, l.Package(), l.GoPackage(p.module))
		}
		// buf's PACKAGE_DIRECTORY_MATCH: the package spells the directory.
		if strings.ReplaceAll(pkg, ".", "/") != l.BufDir() {
			t.Errorf("%s: package %s does not match directory %s", p.name, pkg, l.BufDir())
		}
		if err := register(files, l.BufDir()+"/"+RootFile, pkg); err != nil {
			t.Fatalf("registering %s's config beside the others: %v", p.name, err)
		}
	}

	legacy := new(protoregistry.Files)
	if err := register(legacy, "config/v1/config.proto", LegacyPackage); err != nil {
		t.Fatal(err)
	}
	if err := register(legacy, "config/v1/config.proto", LegacyPackage); err == nil {
		t.Fatal("two legacy config.v1 files registered side by side; the registry should have refused the second")
	}
}

func register(files *protoregistry.Files, path, pkg string) error {
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:        proto.String(path),
		Package:     proto.String(pkg),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("AppConfig")}},
	}, files)
	if err != nil {
		return err
	}
	return files.RegisterFile(fd)
}

func match(t *testing.T, re, s string) string {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("no %s in:\n%s", re, s)
	}
	return m[1]
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
