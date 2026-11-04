package audit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// The audit command group computes most categories itself and reaches the
// few it cannot (KCL-entity-typed ingress/external-builds, the
// registry/env/drift helpers) through factory.AuditAPI. In package cli the
// real AuditAPI is registered via groups.go's init(); the audit package
// can't import cli (cycle), so these tests build a factory.AuditAPI from
// configurable stubs sufficient for the assertions. The stubs preserve the
// behaviors the moved tests pinned (served-classification, tombstoned vs
// unlisted findings, drift surfacing) without re-importing cli internals.

// stubRegistry is a configurable factory.ServiceRegistry. exists toggles the
// fail-open path; registered/tombstoned classify per service name.
type stubRegistry struct {
	exists     bool
	registered map[string]bool
	tombstoned map[string]bool
}

func (r stubRegistry) Exists() bool { return r.exists }
func (r stubRegistry) Registered(name string) bool {
	if !r.exists {
		return true // fail-open: pre-registration "declared ⇒ served"
	}
	return r.registered[name]
}
func (r stubRegistry) Tombstoned(name string) bool { return r.tombstoned[name] }

// auditAPIConfig captures the knobs each test needs to vary on the stub
// AuditAPI. Zero value is a healthy, empty project.
type auditAPIConfig struct {
	registry                      factory.ServiceRegistry
	isConnectService              func(config.ComponentConfig) bool
	projectDefinesConnectServices bool
	scanProjectDriftPaths         []string
	listEnvs                      []string
}

// testFactory builds a *factory.Factory whose Audit field is a stub
// AuditAPI driven by cfg. LoadProjectStoreFrom is a REAL loader (reads
// forge.yaml via generator.ReadProjectConfig) so
// buildAuditReport-level tests load config exactly as production does.
// Ingress/ExternalBuilds return ok categories — none of the
// audit-package tests assert their content (those stay in package cli with
// the real KCL render).
func testFactory(cfg auditAPIConfig) *factory.Factory {
	okCat := func(...any) audittype.Category {
		return audittype.Category{Status: audittype.StatusOK}
	}
	return &factory.Factory{
		Audit: factory.AuditAPI{
			Ingress: func(*config.ProjectConfig, string) audittype.Category {
				return okCat()
			},
			ExternalBuilds: func(*config.ProjectConfig, string) audittype.Category {
				return okCat()
			},
			ListEnvs:                      func(string) ([]string, error) { return cfg.listEnvs, nil },
			ProjectDefinesConnectServices: func(string) bool { return cfg.projectDefinesConnectServices },
			ScanProjectDriftPaths: func(string, *generator.FileChecksums) []string {
				return cfg.scanProjectDriftPaths
			},
			LoadProjectStoreFrom: loadProjectStoreFromTest,
		},
	}
}

// loadProjectStoreFromTest mirrors internal/cli's loadProjectStoreFrom for
// the audit-package tests: read forge.yaml from
// an explicit path, mapping a missing file to cmdutil.ErrProjectConfigNotFound
// so buildAuditReport's not-a-project branch fires.
func loadProjectStoreFromTest(path string) (*projectstore.Store, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, cmdutil.ErrProjectConfigNotFound
	}
	cfg, err := generator.ReadProjectConfig(path)
	if err != nil {
		return nil, err
	}
	return projectstore.New(cfg), nil
}

// markServiceProject makes dir derive to the "service" kind by stamping the
// pkg/app composition root. forge derives both kind and inventory from the
// project's real sources (proto descriptor, service registry, KCL tree,
// handler impls), so a fixture reads as a service only when it carries one.
func markServiceProject(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "pkg", "app"), 0o755); err != nil {
		t.Fatalf("mark service project (mkdir pkg/app): %v", err)
	}
}

// writeFileTest writes content at dir/rel, creating parent dirs. A small
// stand-in for the cli mustWriteScopeFile helper.
func writeFileTest(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
