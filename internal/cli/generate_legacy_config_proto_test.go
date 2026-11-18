package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project still declaring the package every forge project used to share is
// refused before generate writes anything, with the codemod that moves it and
// where it moves to: reading the old layout would keep generating a pkg/config
// that panics at init beside any other project's.
func TestStepLegacyConfigProto_RefusesTheSharedPackage(t *testing.T) {
	dir := t.TempDir()
	writeLegacyTestFile(t, dir, "go.mod", "module example.com/acme-app\n\ngo 1.25\n")
	writeLegacyTestFile(t, dir, "forge.yaml", "name: acme-app\nmodule_path: example.com/acme-app\n")
	ctx := &pipelineContext{ProjectDir: dir}

	writeLegacyTestFile(t, dir, "proto/acme_app/config/v1/config.proto", "syntax = \"proto3\";\npackage acme_app.config.v1;\n")
	if err := stepLegacyConfigProto(ctx); err != nil {
		t.Fatalf("the project-unique layout was refused: %v", err)
	}

	writeLegacyTestFile(t, dir, "proto/config/v1/config.proto", "syntax = \"proto3\";\n\npackage config.v1;\n")
	err := stepLegacyConfigProto(ctx)
	if err == nil {
		t.Fatal("a config.v1 proto was not refused")
	}
	for _, want := range []string{"config.v1", "proto/config/v1/config.proto", "fix config-proto-package", "proto/acme_app/config/v1", "acme_app.config.v1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q:\n%v", want, err)
		}
	}
}

func writeLegacyTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
