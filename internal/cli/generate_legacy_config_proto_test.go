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

// A project renamed in forge.yaml whose config proto still sits at the old
// name's layout is refused before generate drops pkg/config, naming the old
// and new layouts and everything else the name decides. A project that keeps
// a second project's config proto beside its own is not.
func TestStepLegacyConfigProto_RefusesAHalfFinishedRename(t *testing.T) {
	dir := t.TempDir()
	writeLegacyTestFile(t, dir, "go.mod", "module example.com/shop\n\ngo 1.25\n")
	writeLegacyTestFile(t, dir, "forge.yaml", "name: shop-v2\nmodule_path: example.com/shop\n")
	writeLegacyTestFile(t, dir, "proto/shop/config/v1/config.proto", "syntax = \"proto3\";\npackage shop.config.v1;\n")
	writeLegacyTestFile(t, dir, "proto/services/order/v1/order.proto", "syntax = \"proto3\";\npackage services.order.v1;\n")
	ctx := &pipelineContext{ProjectDir: dir}

	// Another project's config layout, vendored beside a project with no
	// config of its own, is not a rename: nothing of this project imports it.
	if err := stepLegacyConfigProto(ctx); err != nil {
		t.Fatalf("a vendored config layout was taken for a rename: %v", err)
	}

	writeLegacyTestFile(t, dir, "pkg/config/config_gen.go", "package config\n\nimport configv1 \"example.com/shop/gen/shop/config/v1\"\n")
	err := stepLegacyConfigProto(ctx)
	if err == nil {
		t.Fatal("a config proto left at the old name's layout was not refused")
	}
	for _, want := range []string{`"shop_v2"`, "proto/shop/config/v1/config.proto", "shop.config.v1", "git mv proto/shop proto/shop_v2",
		"package shop_v2.config.v1;", "example.com/shop/gen/shop_v2/config/v1", "git mv cmd/<old name> cmd/<new name>", "hosted app", "put `name:` back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q:\n%v", want, err)
		}
	}

	// The rename finished: nothing to refuse, even with another project's
	// config proto vendored beside it.
	writeLegacyTestFile(t, dir, "proto/shop_v2/config/v1/config.proto", "syntax = \"proto3\";\npackage shop_v2.config.v1;\n")
	if err := stepLegacyConfigProto(ctx); err != nil {
		t.Fatalf("a project on its own layout was refused: %v", err)
	}
}
