package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
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
	if !strings.Contains(content, "forge/pkg/cronkit") || strings.Contains(content, "robfig/cron") {
		t.Error("cron worker should run its schedule on forge/pkg/cronkit")
	}
	if !strings.Contains(content, "func (w *Worker) Run(ctx context.Context) error") {
		t.Error("cron worker should have Run method")
	}
	// No db/migrations: no database, so no ORM field to claim ticks on.
	if strings.Contains(content, "orm.Context") || strings.Contains(content, "Claims:") {
		t.Errorf("a project with no database got tick claims:\n%s", content)
	}

	// worker_test.go should contain cron-specific test
	testContent := readFile(t, filepath.Join(workerDir, "worker_test.go"))
	if !strings.Contains(testContent, "TestCronWorkerStartStop") {
		t.Error("cron worker_test.go should contain TestCronWorkerStartStop")
	}
}

// A project with a database (db/migrations, as config.DeriveDatabase reads
// it) gets a cron worker whose ticks are claimed on its ORM handle, so each
// fires on one replica.
func TestGenerateWorkerFilesCronClaimsTicksOnTheDatabase(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "db", "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := GenerateWorkerFiles(root, "example.com/myapp", "poll_reddit", "cron", "0 * * * *"); err != nil {
		t.Fatalf("GenerateWorkerFiles(cron) error = %v", err)
	}
	content := readFile(t, filepath.Join(root, "internal", "workers", "poll_reddit", "worker.go"))
	for _, want := range []string{
		"\t// forge:optional-dep\n\tDB orm.Context\n",
		"Claims:     cronkit.Postgres(w.deps.DB),",
		"const PerReplica = false",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("worker.go lacks %q:\n%s", want, content)
		}
	}
}

// TestGenerateWorkerFilesCron_CompilesAgainstProjectModule is the guard the
// string-matching tests above could never be: it BUILDS the scaffolded cron
// worker inside a real module.
//
// The shipped bug this pins: the cron worker's schedule compiles against
// github.com/robfig/cron/v3 (now through forge/pkg/cronkit), nothing put that
// module in the project's go.mod, and the very next `go build` — including
// forge generate's own "validate generated code" step — died with "no
// required module provides package github.com/robfig/cron/v3", rolling the
// whole generate run back. Asserting that the rendered template CONTAINS the
// import string is exactly what let that ship.
//
// The module stands in for a real project: it requires this forge checkout
// (a replace) and already uses forge/pkg/serverkit, so its go.mod carries
// everything forge's runtime needs except the cron module; no typed config.
// What is under test is the go.mod requirement, not the template body.
func TestGenerateWorkerFilesCron_CompilesAgainstProjectModule(t *testing.T) {
	testkit.Slow(t, "builds a module (go mod tidy + go get + go build); -short runs the string-level assertions only")
	forgeRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	gomod := "module example.com/cronmod\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge v0.0.0\n\nreplace github.com/reliant-labs/forge => " + forgeRoot + "\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "main.go"),
		[]byte("package main\n\nimport _ \"github.com/reliant-labs/forge/pkg/serverkit\"\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "-C", root, "mod", "tidy").CombinedOutput(); err != nil {
		t.Skipf("module fetch unavailable in this environment: %v\n%s", err, out)
	}
	if strings.Contains(readFile(t, filepath.Join(root, "go.mod")), cronModulePath) {
		t.Fatalf("the stand-in project already requires %s; the test would measure nothing", cronModulePath)
	}

	if err := GenerateWorkerFiles(root, "example.com/cronmod", "cleanup", "cron", "*/5 * * * *"); err != nil {
		t.Fatalf("GenerateWorkerFiles(cron) error = %v", err)
	}

	if got := readFile(t, filepath.Join(root, "go.mod")); !strings.Contains(got, cronModulePath) {
		t.Fatalf("go.mod does not require %s after scaffolding a cron worker:\n%s", cronModulePath, got)
	}

	out, err := exec.Command("go", "-C", root, "build", "./internal/workers/cleanup").CombinedOutput()
	if err != nil {
		t.Fatalf("scaffolded cron worker does not build: %v\n%s", err, out)
	}
}
