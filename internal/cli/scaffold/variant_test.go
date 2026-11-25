// Tests for `forge scaffold variant <pkg> <Name> [--root <root>]`.

package scaffold

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func variantProject(t *testing.T) string {
	t.Helper()
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "internal", "controlplane"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunVariant_WritesWrapperAndRootOnlyProvider(t *testing.T) {
	dir := variantProject(t)
	if err := runVariant("controlplane", "SigningControlPlane", "worker"); err != nil {
		t.Fatalf("runVariant: %v", err)
	}
	path := filepath.Join(dir, "internal", "app", "variant_signing_control_plane.go")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scaffolded file: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"package app\n",
		`import "example.com/testproj/internal/controlplane"`,
		"type SigningControlPlane struct{ controlplane.Service }",
		"type SigningControlPlaneDeps struct {",
		"//forge:root-only worker\nfunc NewSigningControlPlane(d SigningControlPlaneDeps) (*SigningControlPlane, error) {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("variant file lacks %q:\n%s", want, got)
		}
	}
	if _, err := parser.ParseFile(token.NewFileSet(), path, body, parser.ParseComments); err != nil {
		t.Errorf("variant file is not valid Go: %v", err)
	}
}

func TestRunVariant_NoRootIsNotRootOnly(t *testing.T) {
	dir := variantProject(t)
	if err := runVariant("controlplane", "RecordOnly", ""); err != nil {
		t.Fatalf("runVariant: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "internal", "app", "variant_record_only.go"))
	if strings.Contains(string(body), "forge:root-only") {
		t.Errorf("a variant without --root must not be root-only:\n%s", body)
	}
}

func TestRunVariant_Refusals(t *testing.T) {
	dir := variantProject(t)
	if err := runVariant("missing", "X", ""); err == nil || !strings.Contains(err.Error(), "no package internal/missing") {
		t.Errorf("missing package: got %v", err)
	}
	if err := runVariant("controlplane", "lower", ""); err == nil {
		t.Error("an unexported name must be refused")
	}
	app := filepath.Join(dir, "internal", "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(app, "providers.go")
	if err := os.WriteFile(existing, []byte("package app\n\nfunc NewTaken(d TakenDeps) (*Taken, error) { return nil, nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runVariant("controlplane", "Taken", ""); err == nil || !strings.Contains(err.Error(), "already declares") {
		t.Errorf("a name internal/app already declares: got %v", err)
	}
	if _, err := os.Stat(filepath.Join(app, "variant_taken.go")); err == nil {
		t.Error("a refused variant still wrote its file")
	}
}

func TestVariantSubcommandRegistered(t *testing.T) {
	root := newScaffoldCmd(testFactory())
	for _, sub := range root.Commands() {
		if sub.Name() == "variant" {
			if sub.Flags().Lookup("root") == nil {
				t.Error("variant lacks --root")
			}
			if !strings.Contains(root.Long, "scaffold variant") {
				t.Error("`forge scaffold --help` should advertise variant")
			}
			return
		}
	}
	t.Fatal("variant subcommand not registered on `forge scaffold`")
}
