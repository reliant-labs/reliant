package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// write drops a file (creating parents) under root.
func write(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadTree writes a bare forge.yaml into root and loads it, so the loader's
// derivation reads the tree the test built.
func loadTree(t *testing.T, root string) *ProjectConfig {
	t.Helper()
	write(t, root, "forge.yaml", "name: demo\nmodule_path: example.com/demo\n")
	// Every tree these tests model is a Go project (it declares a
	// module_path), and features such as contracts and ci derive from the
	// go.mod (ProjectConfig.HasGoModule). A site's tree is site_test.go's.
	if _, err := os.Stat(filepath.Join(root, "go.mod")); os.IsNotExist(err) {
		write(t, root, "go.mod", "module example.com/demo\n\ngo 1.24\n")
	}
	cfg, err := LoadProjectDir(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// serviceTree is a service-shaped project with a database, which is what
// control-plane and every scaffolded service is. The service proto is the
// evidence codegen derives from.
func serviceTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "internal/handlers/.keep", "")
	write(t, root, "proto/services/demo/v1/demo.proto", "syntax = \"proto3\";\n")
	write(t, root, "deploy/kcl/kcl.mod", "")
	write(t, root, "db/migrations/00001_init.up.sql", "CREATE TABLE t (id bigint primary key);\n")
	return root
}

// A forge.yaml with no `features:` block derives every feature from the tree.
func TestDeriveFeatures_ServiceWithMigrations(t *testing.T) {
	cfg := loadTree(t, serviceTree(t))
	for _, name := range []FeatureName{
		FeatureCodegen, FeatureORM, FeatureMigrations, FeatureCI, FeatureBuild,
		FeatureContracts, FeatureObservability, FeatureHotReload, FeatureDeploy,
	} {
		if !cfg.Features.resolve(name) {
			t.Errorf("feature %q should derive ON for a service with db/migrations", name)
		}
	}
	if cfg.Database.Driver != "postgres" || cfg.Database.MigrationsDir != "db/migrations" {
		t.Errorf("database should derive to postgres + db/migrations, got %+v", cfg.Database)
	}
	for _, name := range []FeatureName{FeatureIngress, FeatureOperators, FeatureFrontend} {
		if cfg.Features.resolve(name) {
			t.Errorf("feature %q has nothing in the repo to turn it on and must derive OFF", name)
		}
	}
}

// No db/migrations means no database, and the features that read it follow.
func TestDeriveFeatures_NoMigrationsMeansNoDatabase(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/handlers/.keep", "")
	write(t, root, "proto/services/demo/v1/demo.proto", "syntax = \"proto3\";\n")
	cfg := loadTree(t, root)
	if cfg.Database.Driver != "none" || cfg.Database.MigrationsDir != "" {
		t.Errorf("database = %+v, want driver none", cfg.Database)
	}
	if cfg.Features.ORMEnabled() || cfg.Features.MigrationsEnabled() {
		t.Error("orm and migrations must derive OFF without db/migrations")
	}
	if !cfg.Features.CodegenEnabled() {
		t.Error("codegen is a function of the protos, not of the database")
	}
}

// THE NO-ORM MARKER. control-plane hand-writes its DB layer, so it used to set
// `features.orm: false`. The opt-out now sits in the package it affects.
func TestDeriveFeatures_NoORMMarker(t *testing.T) {
	root := serviceTree(t)
	write(t, root, "internal/db/doc.go", "// Package db is the hand-owned repository layer.\n//\n//forge:no-orm: the repository is hand-written; entities are not projected\npackage db\n")
	cfg := loadTree(t, root)
	if cfg.Features.ORMEnabled() {
		t.Fatal("//forge:no-orm in internal/db must turn the ORM off")
	}
	if !cfg.Features.MigrationsEnabled() || !cfg.Features.CodegenEnabled() {
		t.Error("the marker opts out of the ORM only: migrations and codegen stay on")
	}
	if cfg.Database.Driver != "postgres" {
		t.Error("the marker must not hide the database itself")
	}
}

func TestDeriveFeatures_NoORMMarkerIsScoped(t *testing.T) {
	cases := map[string]struct {
		file, body string
	}{
		"bare marker":        {"internal/db/doc.go", "//forge:no-orm\npackage db\n"},
		"marker with reason": {"internal/db/db.go", "// x\n//forge:no-orm: why\npackage db\n"},
		// Spaced, like every other Go marker. It used to be read as prose:
		// the project kept its ORM and nothing said the marker was ignored.
		"spaced marker":          {"internal/db/doc.go", "// forge:no-orm: hand-written repository\npackage db\n"},
		"generated file ignored": {"internal/db/user_gen.go", "//forge:no-orm\npackage db\n"},
		"test file ignored":      {"internal/db/db_test.go", "//forge:no-orm\npackage db\n"},
		"other package ignored":  {"internal/other/doc.go", "//forge:no-orm\npackage other\n"},
		"prefix lookalike":       {"internal/db/doc.go", "//forge:no-orm-ever\npackage db\n"},
		"mentioned in prose":     {"internal/db/doc.go", "// say //forge:no-orm to opt out\npackage db\n"},
	}
	wantOff := map[string]bool{"bare marker": true, "marker with reason": true, "spaced marker": true}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := serviceTree(t)
			write(t, root, tc.file, tc.body)
			got := loadTree(t, root).Features.ORMEnabled()
			if want := !wantOff[name]; got != want {
				t.Errorf("ORMEnabled() = %v, want %v", got, want)
			}
		})
	}
}

// ingress derives from a forge.Gateway declared in deploy/kcl.
func TestDeriveFeatures_IngressFromKCLGateway(t *testing.T) {
	root := serviceTree(t)
	if loadTree(t, root).Features.IngressEnabled() {
		t.Fatal("no Gateway declared: ingress must be OFF")
	}
	write(t, root, "deploy/kcl/ingress.k", "PUBLIC = forge.Gateway {\n    name = \"public\"\n}\n")
	if !loadTree(t, root).Features.IngressEnabled() {
		t.Error("a forge.Gateway in deploy/kcl must turn ingress ON")
	}
}

func TestDeriveFeatures_IngressIgnoresProseAndVendoredKCL(t *testing.T) {
	root := serviceTree(t)
	write(t, root, "deploy/kcl/dev/main.k", "# declare a forge.Gateway for ingress\nx = 1\n")
	write(t, root, ".forge-kcl/schema.k", "schema Gateway:\n    x: int\n_g = forge.Gateway {\n}\n")
	if loadTree(t, root).Features.IngressEnabled() {
		t.Error("a comment, and the vendored KCL outside deploy/kcl, must not count as a declared gateway")
	}
}

// operators derives from internal/operators/<name>/.
func TestDeriveFeatures_OperatorsFromPackages(t *testing.T) {
	root := serviceTree(t)
	if loadTree(t, root).Features.OperatorsEnabled() {
		t.Fatal("no operator package: operators must be OFF")
	}
	write(t, root, "internal/operators/workspace/controller.go", "package workspace\n")
	if !loadTree(t, root).Features.OperatorsEnabled() {
		t.Error("internal/operators/<name>/ must turn operators ON")
	}
}

// The frontend feature derives from a frontend existing on disk.
func TestDeriveFeatures_FrontendFromDisk(t *testing.T) {
	root := serviceTree(t)
	if loadTree(t, root).Features.FrontendEnabled() {
		t.Fatal("no frontend: feature must be OFF")
	}
	write(t, root, "frontends/web/next.config.ts", "export default {}\n")
	cfg := loadTree(t, root)
	if !cfg.Features.FrontendEnabled() || len(cfg.Frontends) != 1 {
		t.Errorf("frontends/web with a next.config.ts must derive the frontend: %+v", cfg.Frontends)
	}
}

// The pnpm-workspaces layout is read from pnpm-workspace.yaml.
func TestDeriveFrontendWorkspacesFromTree(t *testing.T) {
	root := serviceTree(t)
	if loadTree(t, root).IsFrontendWorkspacesEnabled() {
		t.Fatal("no pnpm-workspace.yaml: workspaces must be off")
	}
	write(t, root, "pnpm-workspace.yaml", "packages:\n  - packages/*\n")
	if !loadTree(t, root).IsFrontendWorkspacesEnabled() {
		t.Error("pnpm-workspace.yaml at the root must turn the workspaces layout on")
	}
}

func TestDeriveFeatures_KindMatrix(t *testing.T) {
	cli := t.TempDir()
	write(t, cli, "cmd/tool/main.go", "package main\n\nfunc main() {}\n")
	c := loadTree(t, cli)
	if c.EffectiveKind() != ProjectKindCLI || !c.Features.CIEnabled() || !c.Features.BuildEnabled() ||
		c.Features.CodegenEnabled() || c.Features.DeployEnabled() {
		t.Errorf("cli: want ci+build on, codegen+deploy off, got %v", c.Features.EffectiveFeatures())
	}
	lib := t.TempDir()
	l := loadTree(t, lib)
	if l.EffectiveKind() != ProjectKindLibrary || l.Features.CIEnabled() || l.Features.BuildEnabled() {
		t.Errorf("library: want ci+build off, got %v", l.Features.EffectiveFeatures())
	}
}

// With applies a scaffold-time override, and leaves the receiver alone.
func TestFeaturesWith(t *testing.T) {
	var zero FeaturesConfig
	off := zero.With(FeatureCI, false)
	if off.CIEnabled() || !zero.CIEnabled() {
		t.Error("With must return a changed copy and not mutate the receiver")
	}
	if !off.CodegenEnabled() || off.IngressEnabled() {
		t.Error("With must keep the zero-value defaults for every other feature")
	}
}

// NormalizeForWrite never reintroduces a removed key.
func TestNormalizeForWriteEmitsNoRemovedKeys(t *testing.T) {
	cfg := loadTree(t, serviceTree(t))
	out, err := marshalNormalized(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"features:", "frontends:", "frontend:", "stack:", "smoke:", "driver:", "migrations_dir:", "seed:", "allowed_destructive"} {
		if strings.Contains(out, key) {
			t.Errorf("normalized forge.yaml must not carry %q:\n%s", key, out)
		}
	}
}

func marshalNormalized(cfg *ProjectConfig) (string, error) {
	b, err := yaml.Marshal(NormalizeForWrite(cfg))
	return string(b), err
}

// deployOnlyTree is deploy/kcl and nothing else: no protos, no server, no
// database. It is a service-KIND project (deploy/kcl is service evidence), and
// the case that exposed codegen being derived from the kind: forge generate
// ran buf over a module with no files and failed.
func deployOnlyTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "deploy/kcl/kcl.mod", "")
	write(t, root, "deploy/kcl/prod/main.k", "")
	write(t, root, "cmd/echo/main.go", "package main\n")
	return root
}

func TestDeriveFeatures_DeployOnlyProjectGetsDeployButNoCodegen(t *testing.T) {
	cfg := loadTree(t, deployOnlyTree(t))
	if cfg.EffectiveKind() != ProjectKindService {
		t.Fatalf("deploy/kcl should still read as a service-kind project, got %s", cfg.EffectiveKind())
	}
	want := map[FeatureName]bool{
		FeatureDeploy:        true,
		FeatureBuild:         true,
		FeatureCI:            true,
		FeatureContracts:     true,
		FeatureCodegen:       false,
		FeatureORM:           false,
		FeatureMigrations:    false,
		FeatureFrontend:      false,
		FeatureObservability: false,
		FeatureHotReload:     false,
		FeatureIngress:       false,
		FeatureOperators:     false,
	}
	for name, on := range want {
		if got := cfg.Features.resolve(name); got != on {
			t.Errorf("feature %q = %v, want %v for a deploy-only project", name, got, on)
		}
	}
	if cfg.CI.Lint.Buf || cfg.CI.Lint.BufBreaking {
		t.Errorf("buf CI lints must be off with no protos, got %+v", cfg.CI.Lint)
	}
}

// codegen follows .proto files, wherever the project keeps them: proto/, or
// any module buf.yaml names. An empty module is not evidence.
func TestDeriveFeatures_CodegenFollowsProtoFiles(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{"proto dir with a .proto", map[string]string{"proto/a/v1/a.proto": "syntax = \"proto3\";"}, true},
		{"proto dir with no .proto", map[string]string{"proto/.keep": ""}, false},
		{"buf module elsewhere with a .proto", map[string]string{
			"buf.yaml":             "version: v2\nmodules:\n  - path: api\n",
			"api/svc/v1/svc.proto": "syntax = \"proto3\";",
		}, true},
		{"buf module that is empty", map[string]string{"buf.yaml": "version: v2\nmodules:\n  - path: api\n", "api/.keep": ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := deployOnlyTree(t)
			for rel, body := range tc.files {
				write(t, root, rel, body)
			}
			if got := loadTree(t, root).Features.resolve(FeatureCodegen); got != tc.want {
				t.Errorf("codegen = %v, want %v", got, tc.want)
			}
		})
	}
}

// A db/migrations directory is a database whatever the kind; the ORM and
// migrations features additionally need codegen, so a deploy-only project with
// migrations has a database but neither.
func TestDeriveFeatures_DatabaseWithoutProtosHasNoORMOrMigrations(t *testing.T) {
	root := deployOnlyTree(t)
	write(t, root, "db/migrations/00001_init.up.sql", "CREATE TABLE t (id bigint primary key);\n")
	cfg := loadTree(t, root)
	if cfg.Database.Driver != "postgres" {
		t.Errorf("db/migrations should still derive a postgres database, got %+v", cfg.Database)
	}
	for _, name := range []FeatureName{FeatureORM, FeatureMigrations, FeatureCodegen} {
		if cfg.Features.resolve(name) {
			t.Errorf("feature %q needs codegen, which has no protos to run on", name)
		}
	}
}
