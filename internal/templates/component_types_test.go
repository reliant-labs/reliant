package templates

import (
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// assertGofmtClean fails if src is not already gofmt-formatted. Rendered
// scaffolds land directly in user repos, so template output must be
// byte-identical to its gofmt form — a drifted template fails every
// downstream `gofmt -l` / lint gate on a fresh `forge scaffold`.
func assertGofmtClean(t *testing.T, name, src string) {
	t.Helper()
	formatted, err := format.Source([]byte(src))
	if err != nil {
		t.Fatalf("%s does not format as valid Go: %v\n----\n%s", name, err, src)
	}
	if string(formatted) != src {
		t.Errorf("%s rendered output is not gofmt-clean\n---- rendered ----\n%s\n---- gofmt ----\n%s", name, src, formatted)
	}
}

// --- Worker templates ---

func TestWorkerTemplatesRenderDefault(t *testing.T) {
	data := struct {
		Name           string
		Package        string
		Module         string
		HasDatabase    bool
		DatabaseDriver string
		OrmEnabled     bool
		Schedule       string
		HasConfig      bool
	}{
		HasConfig: true,
		Name:      "processor",
		Package:   "processor",
		Module:    "example.com/myapp",
	}

	content, err := WorkerTemplates().Render("worker.go.tmpl", data)
	if err != nil {
		t.Fatalf("render worker.go.tmpl: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package processor") {
		t.Error("worker.go should have package processor")
	}
	if !strings.Contains(s, "func (w *Worker) Start(ctx context.Context) error") {
		t.Error("worker.go should contain Start method")
	}
	if !strings.Contains(s, "func (w *Worker) Stop(ctx context.Context) error") {
		t.Error("worker.go should contain Stop method")
	}
	// The scaffolded cycle loop must demonstrate the ctx-aware lifecycle:
	// select on ctx.Done() each iteration and pass ctx into the per-cycle
	// work so long cycles observe graceful shutdown mid-flight. Friction
	// surfaced by kalshi-trader — the prior scaffold parked on
	// <-ctx.Done() with the loop pattern relegated to a TODO comment, so
	// hand-written loops routinely dropped the ctx plumbing.
	if !strings.Contains(s, "case <-ctx.Done():") {
		t.Errorf("worker.go cycle loop should select on ctx.Done(); got:\n%s", s)
	}
	if !strings.Contains(s, "func (w *Worker) runOnce(ctx context.Context) error") {
		t.Errorf("worker.go should scaffold a ctx-taking runOnce cycle method; got:\n%s", s)
	}
	if !strings.Contains(s, "w.runOnce(ctx)") {
		t.Errorf("worker.go cycle loop should pass ctx into runOnce; got:\n%s", s)
	}
	if strings.Contains(s, "robfig/cron") {
		t.Error("default worker should not import cron")
	}
	if strings.Contains(s, "//go:build ignore") {
		t.Error("rendered output should not retain //go:build ignore")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "worker.go", s, parser.SkipObjectResolution); err != nil {
		t.Errorf("worker.go template did not parse as valid Go: %v\n----\n%s", err, s)
	}
	assertGofmtClean(t, "worker/worker.go.tmpl", s)
}

func TestWorkerTemplatesRenderTest(t *testing.T) {
	data := struct {
		Name           string
		Package        string
		Module         string
		HasDatabase    bool
		DatabaseDriver string
		OrmEnabled     bool
		Schedule       string
		HasConfig      bool
	}{
		HasConfig: true,
		Name:      "processor",
		Package:   "processor",
		Module:    "example.com/myapp",
	}

	content, err := WorkerTemplates().Render("worker_test.go.tmpl", data)
	if err != nil {
		t.Fatalf("render worker_test.go.tmpl: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package processor") {
		t.Error("worker_test.go should have package processor")
	}
	if !strings.Contains(s, "Test") {
		t.Error("worker_test.go should contain test functions")
	}
	// TestWorkerRunOnceAcceptsContext pins the ctx-aware cycle contract
	// in the scaffolded test so it survives the user replacing the
	// example body.
	if !strings.Contains(s, "TestWorkerRunOnceAcceptsContext") {
		t.Error("worker_test.go should contain TestWorkerRunOnceAcceptsContext")
	}
	if !strings.Contains(s, "w.runOnce(ctx)") {
		t.Error("worker_test.go should exercise the runOnce(ctx) signature")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "worker_test.go", s, parser.SkipObjectResolution); err != nil {
		t.Errorf("worker_test.go template did not parse as valid Go: %v\n----\n%s", err, s)
	}
	assertGofmtClean(t, "worker/worker_test.go.tmpl", s)
}

// --- Worker-cron templates ---

func TestWorkerCronTemplatesRender(t *testing.T) {
	data := struct {
		Name           string
		Package        string
		Module         string
		HasDatabase    bool
		DatabaseDriver string
		OrmEnabled     bool
		Schedule       string
		HasConfig      bool
	}{
		HasConfig: true,
		Name:      "cleanup",
		Package:   "cleanup",
		Module:    "example.com/myapp",
		Schedule:  "0 * * * *",
	}

	content, err := WorkerCronTemplates().Render("worker.go.tmpl", data)
	if err != nil {
		t.Fatalf("render worker-cron/worker.go.tmpl: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "package cleanup") {
		t.Error("cron worker.go should have package cleanup")
	}
	if !strings.Contains(s, `Schedule = "0 * * * *"`) {
		t.Error("cron worker should embed the schedule constant")
	}
	if !strings.Contains(s, "robfig/cron") {
		t.Error("cron worker should import robfig/cron")
	}
	if !strings.Contains(s, "func (w *Worker) Run(ctx context.Context)") {
		t.Error("cron worker should have Run(ctx) method — the context-aware signature lets Run observe Stop/shutdown via ctx.Done()")
	}
	// Start must wire baseCtx so per-tick contexts derive from the
	// caller's lifecycle; Stop must cancel it so in-flight Run sees
	// ctx.Done() immediately.
	if !strings.Contains(s, "w.baseCtx, w.baseCancel = context.WithCancel(ctx)") {
		t.Errorf("cron worker Start should set up baseCtx via context.WithCancel(ctx); got:\n%s", s)
	}
	if !strings.Contains(s, "w.baseCancel()") {
		t.Errorf("cron worker Stop should cancel baseCtx; got:\n%s", s)
	}
	if !strings.Contains(s, "w.Run(w.baseCtx)") {
		t.Errorf("cron closure should pass baseCtx to Run; got:\n%s", s)
	}
	// Rendered output must parse as valid Go syntax — catches template
	// reshuffling regressions. We can't resolve imports in the test, but
	// parser.ParseFile checks the structural shape.
	if _, err := parser.ParseFile(token.NewFileSet(), "worker.go", s, parser.SkipObjectResolution); err != nil {
		t.Errorf("cron worker.go template did not parse as valid Go: %v\n----\n%s", err, s)
	}
	// Run() body should log "schedule", Schedule so a fresh scaffold is
	// useful in logs / OTel traces without the user hand-editing the
	// log line. Friction surfaced by kalshi-trader migration round —
	// the prior body logged only `"worker", w.Name()` so log scrapers
	// had no way to tell two workers apart by cadence.
	if !strings.Contains(s, `"schedule", Schedule`) {
		t.Errorf("cron worker Run() body should log the cron schedule. Got:\n%s", s)
	}
	if strings.Contains(s, "//go:build ignore") {
		t.Error("rendered output should not retain //go:build ignore")
	}
	assertGofmtClean(t, "worker-cron/worker.go.tmpl", s)
}

func TestWorkerCronTemplatesRenderTest(t *testing.T) {
	data := struct {
		Name           string
		Package        string
		Module         string
		HasDatabase    bool
		DatabaseDriver string
		OrmEnabled     bool
		Schedule       string
		HasConfig      bool
	}{
		HasConfig: true,
		Name:      "cleanup",
		Package:   "cleanup",
		Module:    "example.com/myapp",
		Schedule:  "0 * * * *",
	}

	content, err := WorkerCronTemplates().Render("worker_test.go.tmpl", data)
	if err != nil {
		t.Fatalf("render worker-cron/worker_test.go.tmpl: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "TestCronWorkerStartStop") {
		t.Error("cron worker_test.go should contain TestCronWorkerStartStop")
	}
	if !strings.Contains(s, "TestCronWorkerName") {
		t.Error("cron worker_test.go should contain TestCronWorkerName")
	}
	// TestCronWorkerRunAcceptsContext pins the Run(ctx) signature — the
	// scaffolded test should still cover the context contract even after
	// the user replaces the example body.
	if !strings.Contains(s, "TestCronWorkerRunAcceptsContext") {
		t.Error("cron worker_test.go should contain TestCronWorkerRunAcceptsContext")
	}
	if !strings.Contains(s, "w.Run(ctx)") {
		t.Error("cron worker_test.go should exercise the Run(ctx) signature")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "worker_test.go", s, parser.SkipObjectResolution); err != nil {
		t.Errorf("cron worker_test.go template did not parse as valid Go: %v\n----\n%s", err, s)
	}
	assertGofmtClean(t, "worker-cron/worker_test.go.tmpl", s)
}

// --- Operator templates ---

func TestOperatorTemplatesRender(t *testing.T) {
	data := struct {
		Name           string
		Package        string
		TypeName       string
		TypeRef        string
		Group          string
		Version        string
		Module         string
		HasDatabase    bool
		DatabaseDriver string
		OrmEnabled     bool
		APIImportPath  string
		SplitAPI       bool
	}{
		Name:     "scaler",
		Package:  "scaler",
		TypeName: "Scaler",
		TypeRef:  "Scaler",
		Group:    "apps",
		Version:  "v1",
		Module:   "example.com/myapp",
	}

	// types.go
	typesContent, err := OperatorTemplates().Render("types.go.tmpl", data)
	if err != nil {
		t.Fatalf("render operator/types.go.tmpl: %v", err)
	}
	ts := string(typesContent)
	if !strings.Contains(ts, "type Scaler struct") {
		t.Error("types.go should define Scaler struct")
	}
	if !strings.Contains(ts, "type ScalerSpec struct") {
		t.Error("types.go should define ScalerSpec")
	}
	if !strings.Contains(ts, "type ScalerStatus struct") {
		t.Error("types.go should define ScalerStatus")
	}
	if !strings.Contains(ts, `Group: "apps"`) {
		t.Error("types.go should embed the group")
	}
	if !strings.Contains(ts, `Version: "v1"`) {
		t.Error("types.go should embed the version")
	}
	if strings.Contains(ts, "//go:build ignore") {
		t.Error("rendered output should not retain //go:build ignore")
	}

	// controller.go
	ctrlContent, err := OperatorTemplates().Render("controller.go.tmpl", data)
	if err != nil {
		t.Fatalf("render operator/controller.go.tmpl: %v", err)
	}
	cs := string(ctrlContent)
	if !strings.Contains(cs, "func (c *Controller) Reconcile(") {
		t.Error("controller.go should have Reconcile method")
	}
	if !strings.Contains(cs, "func (c *Controller) SetupWithManager(") {
		t.Error("controller.go should have SetupWithManager method")
	}
	if !strings.Contains(cs, "var obj Scaler") {
		t.Error("controller.go should reference the Scaler type")
	}

	// controller_test.go
	testContent, err := OperatorTemplates().Render("controller_test.go.tmpl", data)
	if err != nil {
		t.Fatalf("render operator/controller_test.go.tmpl: %v", err)
	}
	tts := string(testContent)
	if !strings.Contains(tts, "TestReconcile") {
		t.Error("controller_test.go should contain TestReconcile")
	}
	if !strings.Contains(tts, "TestReconcileNotFound") {
		t.Error("controller_test.go should contain TestReconcileNotFound")
	}
}
