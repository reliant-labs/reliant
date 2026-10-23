package config

import (
	"reflect"
	"testing"
)

// TestApplyDerivedDefaults_ReapplyKeepsWrittenSections pins that re-deriving
// an already-LOADED config changes no section.
//
// The generate pipeline does exactly that: stepDeriveFrontendInventory folds
// the KCL-declared frontends into ctx.Cfg and calls ApplyDerivedDefaults to
// re-resolve the features. The no-node path used to overwrite every section
// wholesale with its scaffold default, so in a project with a frontend the
// ORM step read database.orm.package as unset, projected into internal/db,
// and the stale sweep deleted internal/store's generated files. The validate
// build then failed with `undefined: db.<Entity>`. The ci.vuln_scan
// exemptions the frontend scaffold writes were wiped the same way.
func TestApplyDerivedDefaults_ReapplyKeepsWrittenSections(t *testing.T) {
	src := minimalForgeYAML + `
database:
  orm:
    package: internal/store
ci:
  vuln_scan:
    exemptions:
      - id: GHSA-vfj7-8cjw-p6xm
        reason: dev-only toolchain
        expires: "2099-01-01"
lint:
  frontend:
    no_important: error
`
	cfg, err := LoadProject([]byte(src), serviceProjectPath(t, src))
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got := ORMPackage(cfg).PackageDir(); got != "internal/store" {
		t.Fatalf("loaded ORM package = %s, want internal/store", got)
	}
	before := *cfg

	ApplyDerivedDefaults(cfg)

	if got := ORMPackage(cfg).PackageDir(); got != "internal/store" {
		t.Errorf("re-deriving reset database.orm.package: ORM package = %s, want internal/store", got)
	}
	if n := len(cfg.CI.VulnScan.Exemptions); n != 1 {
		t.Errorf("re-deriving dropped ci.vuln_scan.exemptions: %d left, want 1", n)
	}
	for name, pair := range map[string][2]any{
		"database": {before.Database, cfg.Database},
		"ci":       {before.CI, cfg.CI},
		"deploy":   {before.Deploy, cfg.Deploy},
		"k8s":      {before.K8s, cfg.K8s},
		"lint":     {before.Lint, cfg.Lint},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("re-deriving a loaded config changed the %s section:\nbefore %+v\nafter  %+v", name, pair[0], pair[1])
		}
	}
}

// TestApplyDerivedDefaults_FillsAZeroSection keeps the no-node path's other
// half: a config nobody loaded (a hand-built one in a test, say) still gets
// every empty section's default.
func TestApplyDerivedDefaults_FillsAZeroSection(t *testing.T) {
	cfg := &ProjectConfig{Name: "demo", ModulePath: "example.com/demo"}
	ApplyDerivedDefaults(cfg)
	if !cfg.CI.Test.Race {
		t.Errorf("an empty ci section did not take its default (ci.test.race = false)")
	}
	if cfg.Lint.Frontend.NoImportant != "warn" {
		t.Errorf("an empty lint section did not take its default (no_important = %q)", cfg.Lint.Frontend.NoImportant)
	}
}
