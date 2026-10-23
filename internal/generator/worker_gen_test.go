package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateWorkerFilesDefault(t *testing.T) {
	root := t.TempDir()

	if err := GenerateWorkerFiles(root, "example.com/myapp", "processor", "", ""); err != nil {
		t.Fatalf("GenerateWorkerFiles() error = %v", err)
	}

	workerDir := filepath.Join(root, "internal", "workers", "processor")

	// Both files must exist
	for _, f := range []string{"worker.go", "worker_test.go"} {
		if _, err := os.Stat(filepath.Join(workerDir, f)); err != nil {
			t.Errorf("expected %s to exist: %v", f, err)
		}
	}

	// worker.go should use the default (non-cron) template
	content := readFile(t, filepath.Join(workerDir, "worker.go"))
	if !strings.Contains(content, "package processor") {
		t.Errorf("worker.go should have package processor, got:\n%s", content)
	}
	if !strings.Contains(content, "func (w *Worker) Start(ctx context.Context) error") {
		t.Error("worker.go should contain Start method")
	}
	if !strings.Contains(content, "func (w *Worker) Stop(ctx context.Context) error") {
		t.Error("worker.go should contain Stop method")
	}
	// Default worker should NOT have cron imports
	if strings.Contains(content, "robfig/cron") {
		t.Error("default worker should not import cron package")
	}
	if strings.Contains(content, "Schedule") {
		t.Error("default worker should not contain Schedule constant")
	}
}

func TestGenerateWorkerFilesCron(t *testing.T) {
	root := t.TempDir()

	if err := GenerateWorkerFiles(root, "example.com/myapp", "cleanup", "cron", "*/5 * * * *"); err != nil {
		t.Fatalf("GenerateWorkerFiles(cron) error = %v", err)
	}

	workerDir := filepath.Join(root, "internal", "workers", "cleanup")

	// Both files must exist
	for _, f := range []string{"worker.go", "worker_test.go"} {
		if _, err := os.Stat(filepath.Join(workerDir, f)); err != nil {
			t.Errorf("expected %s to exist: %v", f, err)
		}
	}

	// worker.go should use the cron template
	content := readFile(t, filepath.Join(workerDir, "worker.go"))
	if !strings.Contains(content, "package cleanup") {
		t.Errorf("worker.go should have package cleanup, got:\n%s", content)
	}
	if !strings.Contains(content, `Schedule = "*/5 * * * *"`) {
		t.Error("cron worker.go should contain Schedule constant with the cron expression")
	}
	if !strings.Contains(content, "robfig/cron") {
		t.Error("cron worker should import robfig/cron")
	}
	if !strings.Contains(content, "func (w *Worker) Run()") {
		t.Error("cron worker should have Run method")
	}

	// worker_test.go should contain cron-specific test
	testContent := readFile(t, filepath.Join(workerDir, "worker_test.go"))
	if !strings.Contains(testContent, "TestCronWorkerStartStop") {
		t.Error("cron worker_test.go should contain TestCronWorkerStartStop")
	}
}

// TestGenerateWorkerFilesCron_CompilesAgainstProjectModule is the guard the
// string-matching tests above could never be: it BUILDS the scaffolded cron
// worker inside a real module.
//
// The shipped bug this pins: worker-cron/worker.go.tmpl imports
// github.com/robfig/cron/v3, nothing put that module in the project's go.mod,
// and the very next `go build` — including forge generate's own "validate
// generated code" step — died with "no required module provides package
// github.com/robfig/cron/v3", rolling the whole generate run back. Asserting
// that the rendered template CONTAINS the import string is exactly what let
// that ship.
//
// The module is compiled standalone (its own go.mod, no forge/pkg
// dependency, no typed config); what is under test is the go.mod
// requirement, not the template body.
func TestGenerateWorkerFilesCron_CompilesAgainstProjectModule(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a module (go get + go build); -short runs the string-level assertions only")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/cronmod\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// go.opentelemetry.io/otel is a real project dependency too; require it up
	// front so the only thing the assertion below can be measuring is whether
	// GenerateWorkerFiles added the CRON module.
	if out, err := exec.Command("go", "-C", root, "get", "go.opentelemetry.io/otel@v1.34.0").CombinedOutput(); err != nil {
		t.Skipf("module fetch unavailable in this environment: %v\n%s", err, out)
	}

	if err := GenerateWorkerFiles(root, "example.com/cronmod", "cleanup", "cron", "*/5 * * * *"); err != nil {
		t.Fatalf("GenerateWorkerFiles(cron) error = %v", err)
	}

	gomod := readFile(t, filepath.Join(root, "go.mod"))
	if !strings.Contains(gomod, cronModulePath) {
		t.Fatalf("go.mod does not require %s after scaffolding a cron worker:\n%s", cronModulePath, gomod)
	}

	// The module has no typed config, so the worker names none and builds
	// as scaffolded; the cron dependency is what this test is about.

	out, err := exec.Command("go", "-C", root, "build", "./internal/workers/cleanup").CombinedOutput()
	if err != nil {
		t.Fatalf("scaffolded cron worker does not build: %v\n%s", err, out)
	}
}
