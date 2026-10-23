package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"
)

func writeAdapterConfigFixture(t *testing.T, root string) {
	t.Helper()
	layout := configproto.ForProject(root)
	content, err := templates.ProjectTemplates().Render("config.proto.tmpl", layout.TemplateData("example.com/testproject"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, filepath.FromSlash(layout.Dir()))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.proto"), content, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterConfigMissingRefusesBeforeWritingPackage(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile("forge.yaml", []byte("name: testproject\nmodule_path: example.com/testproject\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := newTestPackageNewCmd("")
	cmd.Flags().String("type", "adapter", "")
	err := cmd.RunE(cmd, []string{"alpha"})
	if err == nil || !strings.Contains(err.Error(), "declare the config root") {
		t.Fatalf("expected actionable missing config refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "alpha")); !os.IsNotExist(err) {
		t.Fatalf("refused scaffold created package: %v", err)
	}
}

func TestAdapterScaffoldRerunIsIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile("forge.yaml", []byte("name: testproject\nmodule_path: example.com/testproject\n"), 0644); err != nil {
		t.Fatal(err)
	}
	writeAdapterConfigFixture(t, root)
	cmd := newTestPackageNewCmd("")
	cmd.Flags().String("type", "adapter", "")
	if err := cmd.RunE(cmd, []string{"alpha"}); err != nil {
		t.Fatal(err)
	}
	protoPath := filepath.Join(root, filepath.FromSlash(configproto.ForProject(root).RootPath()))
	before, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	adapterPath := filepath.Join(root, "internal", "alpha", "adapter.go")
	owned, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	owned = append(owned, []byte("\n// hand-owned sentinel\n")...)
	if err := os.WriteFile(adapterPath, owned, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, []string{"alpha"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rerun changed proto")
	}
	after, err = os.ReadFile(adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(owned) != string(after) {
		t.Fatal("rerun changed owned adapter")
	}
}
