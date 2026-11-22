package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigDepsFixture lays out a minimal project with one worker
// whose Deps mixes scalars (flagged) and collaborators (not flagged) —
// the kalshi-trader naked-scalar shape.
func writeConfigDepsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	workerDir := filepath.Join(dir, "internal", "workers", "trader")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `package trader

import (
	"log/slog"
	"time"

	"example.com/proj/pkg/config"
)

type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	Cfg    config.TraderConfig

	WTIPersistMaxPerTick int           // the kalshi friction shape
	CycleInterval        time.Duration // hand-projected via AppExtras today
	DryRun               bool
	APIBase              *string

	Repo Repository
}

type Repository interface{}
`
	if err := os.WriteFile(filepath.Join(workerDir, "worker.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	// Generated + test files must be skipped even when they declare
	// scalar Deps shapes.
	genSource := `package trader

type Deps2 struct{}

// regenerated content referencing Deps would not be scanned anyway,
// but keep a scalar-bearing Deps here to prove _gen.go files are skipped.
`
	if err := os.WriteFile(filepath.Join(workerDir, "mock_gen.go"), []byte(genSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCollectConfigDepsFindings_FlagsScalarsOnly(t *testing.T) {
	dir := writeConfigDepsFixture(t)

	findings, err := collectConfigDepsFindings(dir)
	if err != nil {
		t.Fatalf("collectConfigDepsFindings: %v", err)
	}

	got := map[string]string{}
	for _, f := range findings {
		got[f.Field] = f.Type
		if f.Role != "internal/workers" || f.Package != "trader" {
			t.Errorf("finding %s: role/package = %s/%s, want internal/workers/trader", f.Field, f.Role, f.Package)
		}
		if f.Line == 0 || f.Col == 0 {
			t.Errorf("finding %s: missing position (%d:%d)", f.Field, f.Line, f.Col)
		}
		if !strings.HasPrefix(f.File, filepath.Join("internal", "workers", "trader")) {
			t.Errorf("finding %s: file %q not project-relative", f.Field, f.File)
		}
	}

	want := map[string]string{
		"WTIPersistMaxPerTick": "int",
		"CycleInterval":        "time.Duration",
		"DryRun":               "bool",
		"APIBase":              "*string",
	}
	for field, typ := range want {
		if got[field] != typ {
			t.Errorf("expected finding %s (%s), got %q", field, typ, got[field])
		}
	}
	for field := range got {
		if _, ok := want[field]; !ok {
			t.Errorf("unexpected finding for non-scalar field %s (%s)", field, got[field])
		}
	}
	if len(findings) != len(want) {
		t.Errorf("finding count = %d, want %d: %+v", len(findings), len(want), findings)
	}
}

// A not-a-component package is built by something other than the server
// composition (the CLI, a daemon, another package), which hands it its values
// directly: there is no loaded config for a block to be filled from, so its
// scalar Deps are constructor arguments and the rule does not apply. A
// component beside it with the same Deps shape is still flagged.
func TestCollectConfigDepsFindings_SkipsNotAComponentPackages(t *testing.T) {
	dir := t.TempDir()
	write := func(pkg, src string) {
		t.Helper()
		pkgDir := filepath.Join(dir, "internal", pkg)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, "contract.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cliadapter", `// Package cliadapter is built by the CLI.
//
//forge:not-a-component: CLI-only — built by the login command; the server never builds it
package cliadapter

type Deps struct {
	CachePath string
}
`)
	write("serveradapter", `package serveradapter

type Deps struct {
	BaseURL string
}
`)

	findings, err := collectConfigDepsFindings(dir)
	if err != nil {
		t.Fatalf("collectConfigDepsFindings: %v", err)
	}
	if len(findings) != 1 || findings[0].Package != "serveradapter" || findings[0].Field != "BaseURL" {
		t.Fatalf("findings = %+v, want exactly serveradapter.Deps.BaseURL", findings)
	}
}

func TestConfigDepsFixHint_CarriesSnippet(t *testing.T) {
	f := configDepsFinding{
		Role: "internal/workers", Package: "trader",
		Field: "WTIPersistMaxPerTick", Type: "int",
	}
	hint := configDepsFixHint(f)
	for _, want := range []string{
		"message TraderConfig",
		"int64 wti_persist_max_per_tick = 1",
		"(forge.v1.config)",
		"Cfg *config.TraderConfig",
		"deploy/kcl/<env>/config.k",
		"wti_persist_max_per_tick = <value>",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q:\n%s", want, hint)
		}
	}
}

func TestCollectConfigDepsJSON_Shape(t *testing.T) {
	dir := writeConfigDepsFixture(t)
	fs, err := collectConfigDepsJSON(dir)
	if err != nil {
		t.Fatalf("collectConfigDepsJSON: %v", err)
	}
	if len(fs) == 0 {
		t.Fatal("expected JSON findings")
	}
	for _, f := range fs {
		if f.Rule != "forge-config-deps" {
			t.Errorf("rule = %q, want forge-config-deps", f.Rule)
		}
		if f.Severity != lintSevWarning {
			t.Errorf("severity = %q, want %q (advisory linter)", f.Severity, lintSevWarning)
		}
		if f.FixHint == "" {
			t.Error("missing fix_hint")
		}
	}
}

// TestAuditConfigDeps_Category moved to internal/cli/audit_deps_category_test.go
// with the audit roll-up it exercises (auditConfigDeps consumes this
// package's CollectConfigDepsFindings).
