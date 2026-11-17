package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// These two tests exercise the AUDIT roll-up categories auditConfigDeps /
// auditOptionalDepsGuard, which consume the exported lint collectors
// (lint.CollectConfigDepsFindings / lint.CollectOptionalDepsGuardFindings).
// They moved here with `forge project audit` into internal/cli/audit. The fixtures
// are local copies of the lint-package fixtures (the lint tests keep their
// own).

// writeConfigDepsFixtureAudit lays out a minimal project with one worker
// whose Deps mixes scalars (flagged) and collaborators (not flagged).
func writeConfigDepsFixtureAudit(t *testing.T) string {
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
	return dir
}

func TestAuditConfigDeps_Category(t *testing.T) {
	dir := writeConfigDepsFixtureAudit(t)
	cat := auditConfigDeps(dir)
	if cat.Status != audittype.StatusWarn {
		t.Errorf("status = %q, want warn", cat.Status)
	}
	if !strings.Contains(cat.Summary, "scalar Deps field(s)") {
		t.Errorf("summary = %q", cat.Summary)
	}
	if cat.Details["finding_count"].(int) == 0 {
		t.Error("expected non-zero finding_count")
	}

	clean := auditConfigDeps(t.TempDir())
	if clean.Status != audittype.StatusOK {
		t.Errorf("clean project status = %q, want ok", clean.Status)
	}
}

// writeOptionalGuardPkgAudit writes a handler package with two optional-dep
// fields, then handlers.go with the supplied method bodies.
func writeOptionalGuardPkgAudit(t *testing.T, projectDir, pkg, methods string) {
	t.Helper()
	dir := filepath.Join(projectDir, "internal", "handlers", pkg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	service := `package ` + pkg + `

type Svc interface {
	Do() error
}

type Deps struct {
	Logger *Logger
	// forge:optional-dep
	Svc Svc
}

type Logger struct{}

func (l *Logger) Info(msg string) {}

type Service struct {
	deps Deps
}

func New(deps Deps) *Service { return &Service{deps: deps} }
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(service), 0o644); err != nil {
		t.Fatal(err)
	}
	handlers := "package " + pkg + "\n\n" + methods
	if err := os.WriteFile(filepath.Join(dir, "handlers.go"), []byte(handlers), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOptionalDepsGuard_AuditCategory(t *testing.T) {
	clean := t.TempDir()
	writeOptionalGuardPkgAudit(t, clean, "fixture", `
func (s *Service) Call() error {
	if s.deps.Svc == nil {
		return nil
	}
	return s.deps.Svc.Do()
}
`)
	if cat := auditOptionalDepsGuard(clean); cat.Status != audittype.StatusOK {
		t.Errorf("clean project status = %s, want ok (%s)", cat.Status, cat.Summary)
	}

	dirty := t.TempDir()
	writeOptionalGuardPkgAudit(t, dirty, "fixture", `
func (s *Service) Call() error {
	return s.deps.Svc.Do()
}
`)
	cat := auditOptionalDepsGuard(dirty)
	if cat.Status != audittype.StatusWarn {
		t.Fatalf("dirty project status = %s, want warn (%s)", cat.Status, cat.Summary)
	}
	if cat.Details["finding_count"] != 1 {
		t.Errorf("finding_count = %v, want 1", cat.Details["finding_count"])
	}
	byPkg, ok := cat.Details["by_package"].(map[string][]string)
	if !ok || len(byPkg["internal/handlers/fixture"]) != 1 {
		t.Errorf("by_package = %#v, want one entry under internal/handlers/fixture", cat.Details["by_package"])
	}
	if !strings.Contains(fmt.Sprint(cat.Details["hint"]), "--optional-deps-guard") {
		t.Errorf("hint missing targeted flag: %v", cat.Details["hint"])
	}
}
