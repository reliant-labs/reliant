package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The cron scheduler the --kind cron worker template compiles against. The
// version is pinned (not @latest) so every project that scaffolds a cron
// worker lands on the same, tested module version.
const (
	cronModulePath    = "github.com/robfig/cron/v3"
	cronModuleVersion = "v3.0.1"
)

// GenerateWorkerFiles generates all files for a single worker:
//   - internal/workers/<package>/worker.go       (from worker/worker.go.tmpl or worker-cron/worker.go.tmpl)
//   - internal/workers/<package>/worker_test.go   (from worker/worker_test.go.tmpl or worker-cron/worker_test.go.tmpl)
//
// The CLI/display name (which may contain hyphens) is translated to a
// Go-package-safe form for the directory and `package` declaration so
// hyphenated worker names like "email-sender" produce a buildable
// internal/workers/email_sender/ package.
//
// When kind is "cron", the cron-specific templates are used and the schedule
// is embedded as a constant in the generated code.
//
// Both the "new project" and "scaffold worker" flows delegate here so the
// generated output is always identical.
func GenerateWorkerFiles(root, modulePath, workerName, kind, schedule string) error {
	workerPackage := naming.ServicePackage(workerName)
	workerDir := filepath.Join(root, "internal", "workers", workerPackage)

	if err := os.MkdirAll(workerDir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", workerDir, err)
	}

	// Select template prefix based on kind.
	tmplPrefix := "worker"
	if kind == "cron" {
		tmplPrefix = "worker-cron"
	}

	data := struct {
		Name     string // display form, may contain hyphens
		Package  string // Go-package-safe form
		Module   string
		Schedule string
		// HasConfig: Deps takes Config, and the worker imports
		// <module>/pkg/config, only when the project has typed config — the
		// rule service.go follows. A project adopted in place has none.
		HasConfig bool
	}{
		Name:      workerName,
		Package:   workerPackage,
		Module:    modulePath,
		Schedule:  schedule,
		HasConfig: codegen.ProjectHasTypedConfig(root),
	}

	// -- worker.go --
	workerContent, err := renderWorkerTemplate(tmplPrefix+"/worker.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render worker.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(workerDir, "worker.go"), workerContent, 0644); err != nil {
		return err
	}

	// -- worker_test.go --
	testContent, err := renderWorkerTemplate(tmplPrefix+"/worker_test.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render worker_test.go: %w", err)
	}
	if err := atomicfile.WriteFile(filepath.Join(workerDir, "worker_test.go"), testContent, 0644); err != nil {
		return err
	}

	// A cron worker compiles against robfig/cron, which the scaffolded go.mod
	// does not carry (a fresh project has no cron worker, so `go mod tidy`
	// would drop the require anyway). Add it here, alongside the code that
	// needs it — otherwise the very next `go build` (including forge
	// generate's own validate step) fails with "no required module provides
	// package github.com/robfig/cron/v3" and the whole generate run rolls back.
	if kind == "cron" {
		if err := ensureModuleRequirement(root, cronModulePath, cronModuleVersion); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not add %s to go.mod: %v\n", cronModulePath, err)
			fmt.Fprintf(os.Stderr, "         run `go get %s@%s` in %s before building.\n", cronModulePath, cronModuleVersion, root)
		}
	}

	return nil
}

// ensureModuleRequirement makes `modPath` a real dependency of the module at
// root: present in go.mod's require list AND checksummed in go.sum.
//
// It shells out to `go get` rather than editing go.mod directly because a
// require line without the matching go.sum entries leaves the module in a
// state where every subsequent `go build` fails on a missing checksum — the
// build has to be able to VERIFY the module, not merely name it.
//
// A root with no go.mod (a bare temp dir in a unit test, an --in-place tree
// before scaffolding) is a no-op, not an error. An already-required module is
// left at whatever version the project pinned.
func ensureModuleRequirement(root, modPath, version string) error {
	goModPath := filepath.Join(root, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		// No module here — nothing to require against.
		return nil
	}
	if mf, perr := modfile.Parse("go.mod", data, nil); perr == nil {
		for _, r := range mf.Require {
			if r.Mod.Path == modPath {
				return nil
			}
		}
	}

	cmd := exec.Command("go", "get", modPath+"@"+version)
	cmd.Dir = root
	cmd.Env = goexec.Env()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go get %s@%s: %w", modPath, version, err)
	}
	fmt.Printf("  ✅ added %s %s to go.mod\n", modPath, version)
	return nil
}

// renderWorkerTemplate renders a worker template from the embedded FS.
func renderWorkerTemplate(name string, data interface{}) ([]byte, error) {
	engine, err := getTemplateEngine()
	if err != nil {
		return nil, err
	}
	result, err := engine.RenderTemplate(name, data)
	if err != nil {
		return nil, err
	}
	return []byte(result), nil
}
