package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// serviceProject is a service-kind project named testproj, on the legacy
// composition (whose binary has a subcommand per component).
func serviceProject(t *testing.T) string {
	t.Helper()
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir)
	markLegacyComposition(t, dir)
	writeFixtureFile(t, dir, "go.mod", "module example.com/testproj\n\ngo 1.22\n")
	return dir
}

func TestValidateProjectName(t *testing.T) {
	for name, wantErr := range map[string]bool{"my-app": false, "app_2": false, "": true, "1app": true, "my app": true, "func": true} {
		if err := validateProjectName(name); (err != nil) != wantErr {
			t.Errorf("validateProjectName(%q) err=%v, wantErr=%v", name, err, wantErr)
		}
	}
}

func TestRequireNoComponentNamed(t *testing.T) {
	inv := codegen.Inventory{
		{Name: "billing", Kind: config.ComponentKindServer},
		{Name: "admin-server", Kind: config.ComponentKindWorker},
	}
	tests := []struct {
		name    string
		want    []string // substrings of the error; empty means no error
		compare string
	}{
		{name: "fresh", compare: "reports"},
		{name: "billing", want: []string{`"billing" already exists in the project as a server`, "pick a different name"}},
		{name: "admin_server", want: []string{`"admin_server" collides with the existing worker "admin-server"`, `both normalise to "admin_server"`}},
	}
	for _, tc := range tests {
		err := requireNoComponentNamed(inv, tc.name, "forge scaffold worker "+tc.name)
		if len(tc.want) == 0 {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected a conflict", tc.name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error lacks %q:\n%v", tc.name, w, err)
			}
		}
	}
}

func TestRunWorker_Validation(t *testing.T) {
	serviceProject(t)
	tests := []struct {
		name, kind, schedule string
		want                 string
	}{
		{"1bad", "", "", "invalid worker name"},
		{"reaper", "daemon", "", `invalid worker kind "daemon": valid kinds are cron`},
		{"reaper", "cron", "", "--schedule is required when --kind cron"},
		{"reaper", "cron", "every day", `invalid cron schedule "every day"`},
		{"reaper", "", "*/5 * * * *", "--schedule requires --kind cron"},
	}
	for _, tc := range tests {
		err := runWorker(testFactory(), tc.name, tc.kind, tc.schedule, true)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("runWorker(%q,%q,%q) = %v, want %q", tc.name, tc.kind, tc.schedule, err, tc.want)
		}
	}
	if entries, _ := os.ReadDir("internal/workers"); len(entries) != 0 {
		t.Errorf("a refused worker wrote files: %v", entries)
	}
}

func TestRunWorker_WritesTheWorkerAndWiresItsSubcommand(t *testing.T) {
	dir := serviceProject(t)
	seedCmdTree(t, dir, "testproj", wiringMain)

	out, err := captureStdout(t, func() error { return runWorker(testFactory(), "reaper", "", "", true) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "workers", "reaper", "worker.go")); err != nil {
		t.Errorf("worker.go not written: %v", err)
	}
	for _, want := range []string{
		"Adding worker 'reaper'...",
		"--no-generate: skipping post-scaffold `forge generate` run",
		"Worker 'reaper' scaffold-only mode complete",
		// The verbs print these with a literal "/" on every OS (they are
		// display strings, not filesystem paths).
		"scaffolded cmd/testproj/cmd/workers/reaper.go (owned)",
		"wired workers.NewReaperCmd into cmd/testproj/main.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if got := readTestFile(t, filepath.Join(dir, "cmd", "testproj", "main.go")); !strings.Contains(got, "workers.NewReaperCmd") {
		t.Errorf("main.go was not wired:\n%s", got)
	}

	t.Run("the name is now taken", func(t *testing.T) {
		err := runWorker(testFactory(), "reaper", "", "", true)
		if err == nil || !strings.Contains(err.Error(), `"reaper" already exists in the project as a worker`) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunWorker_CronAnnouncesItsSchedule(t *testing.T) {
	serviceProject(t)
	out, err := captureStdout(t, func() error {
		return runWorker(testFactory(), "nightly", "CRON", " 0 3 * * * ", true)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `Adding cron worker 'nightly' (schedule "0 3 * * *")...`) {
		t.Errorf("output = %s", out)
	}
}

func TestRunPostScaffoldGenerate(t *testing.T) {
	t.Run("a failing pipeline is a warning, not an error, and is not reported as success", func(t *testing.T) {
		f := testFactory()
		f.Gen.RunPipelineBootstrapOnly = func(string) error { return os.ErrInvalid }
		out, err := captureStdout(t, func() error { return runPostScaffoldGenerate(f, t.TempDir(), "reaper", false) })
		if err != nil {
			t.Fatalf("err = %v, want nil: the worker files were written", err)
		}
		if !strings.Contains(out, "Worker 'reaper' files written, but `forge generate` failed") || strings.Contains(out, "added successfully") {
			t.Errorf("output = %s", out)
		}
	})
	t.Run("a green pipeline says the worker was added", func(t *testing.T) {
		var ranIn string
		f := testFactory()
		f.Gen.RunPipelineBootstrapOnly = func(root string) error { ranIn = root; return nil }
		root := t.TempDir()
		out, err := captureStdout(t, func() error { return runPostScaffoldGenerate(f, root, "reaper", false) })
		if err != nil || ranIn != root || !strings.Contains(out, "Worker 'reaper' added successfully!") {
			t.Fatalf("err=%v ranIn=%q out=%s", err, ranIn, out)
		}
	})
}

func TestRunOperator_FlagGuardAndScaffold(t *testing.T) {
	dir := serviceProject(t)
	if err := runOperator(testFactory(), "mgr", "", "v1alpha1", "api", "", false); err == nil ||
		!strings.Contains(err.Error(), "--api-package and --crd-type only apply with --with-placeholder-crd") {
		t.Fatalf("flag guard: err = %v", err)
	}
	if err := runOperator(testFactory(), "1bad", "", "v1alpha1", "", "", false); err == nil || !strings.Contains(err.Error(), "invalid operator name") {
		t.Fatalf("name: err = %v", err)
	}
	if entries, _ := os.ReadDir("internal/operators"); len(entries) != 0 {
		t.Errorf("a refused operator wrote files: %v", entries)
	}

	seedCmdTree(t, dir, "testproj", wiringMain)
	out, err := captureStdout(t, func() error { return runOperator(testFactory(), "mgr", "", "v1alpha1", "", "", false) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Adding operator 'mgr' (group=testproj.io, version=v1alpha1)...", // group defaults from the project name
		"Operator 'mgr' added successfully!",
		"Next: 'forge scaffold crd <Name> --operator mgr' to scaffold a CRD.",
		"wired operators.NewMgrCmd into cmd/testproj/main.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "operators", "mgr")); err != nil {
		t.Errorf("operator package not written: %v", err)
	}
}

func TestRunOperator_PipelineFailureIsNonFatal(t *testing.T) {
	serviceProject(t)
	f := testFactory()
	f.Gen.RunPipeline = func(string) error { return os.ErrInvalid }
	out, err := captureStdout(t, func() error {
		return runOperator(f, "mgr", "ops.example", "v1", "api", "Thing", true)
	})
	if err != nil {
		t.Fatalf("err = %v, want nil: the operator files were written", err)
	}
	if !strings.Contains(out, "(group=ops.example, version=v1)") || strings.Contains(out, "Next: 'forge scaffold crd") {
		t.Errorf("output = %s", out)
	}
}

func TestRunCRD(t *testing.T) {
	t.Run("name and shape are validated", func(t *testing.T) {
		serviceProject(t)
		if err := runCRD("1bad", "", "", "state-machine", ""); err == nil || !strings.Contains(err.Error(), "invalid CRD name") {
			t.Errorf("name: %v", err)
		}
	})
	t.Run("no operator yet says to scaffold one", func(t *testing.T) {
		serviceProject(t)
		err := runCRD("Workspace", "", "", "state-machine", "")
		if err == nil || !strings.Contains(err.Error(), "no operators in this project") || !strings.Contains(err.Error(), "forge scaffold operator <name>") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an unknown --operator is named", func(t *testing.T) {
		serviceProject(t)
		err := runCRD("Workspace", "", "", "state-machine", "ghost")
		if err == nil || !strings.Contains(err.Error(), `operator "ghost" not found`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("two operators need --operator", func(t *testing.T) {
		dir := serviceProject(t)
		for _, op := range []string{"alpha", "beta"} {
			writeFixtureFile(t, dir, "internal/operators/"+op+"/operator.go", "package "+op+"\n")
		}
		err := runCRD("Workspace", "", "", "state-machine", "")
		if err == nil || !strings.Contains(err.Error(), "multiple operators in this project") || !strings.Contains(err.Error(), "--operator <name>") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an invalid shape is refused", func(t *testing.T) {
		dir := serviceProject(t)
		writeFixtureFile(t, dir, "internal/operators/mgr/operator.go", "package mgr\n")
		err := runCRD("Workspace", "", "", "bogus", "")
		if err == nil || !strings.Contains(err.Error(), `invalid --shape "bogus"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a CRD already reconciled by the operator is refused", func(t *testing.T) {
		dir := serviceProject(t)
		writeFixtureFile(t, dir, "internal/operators/mgr/operator.go", "package mgr\n")
		writeFixtureFile(t, dir, "internal/operators/mgr/workspace_controller.go", "package mgr\n")
		err := runCRD("Workspace", "", "", "config", "")
		if err == nil || !strings.Contains(err.Error(), `CRD "Workspace" already exists in operator "mgr"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the CRD's files are written before its deepcopy is projected", func(t *testing.T) {
		// The deepcopy step type-checks api/ against client-go, which this
		// fixture module does not require: it fails, and the error says which
		// step. The scaffolded controller and types stay (they are the user's
		// from birth) and the header line names the defaults taken.
		dir := serviceProject(t)
		if _, err := captureStdout(t, func() error { return runOperator(testFactory(), "mgr", "", "v1alpha1", "", "", false) }); err != nil {
			t.Fatal(err)
		}
		out, err := captureStdout(t, func() error { return runCRD("Workspace", "", "", "config", "") })
		if err == nil || !strings.Contains(err.Error(), `generate api deepcopy for CRD "Workspace"`) {
			t.Fatalf("err = %v, want the deepcopy step named", err)
		}
		if !strings.Contains(out, "Adding CRD 'Workspace' to operator 'mgr' (group=testproj.io, version=v1alpha1, shape=config)...") {
			t.Errorf("output = %s", out)
		}
		for _, rel := range []string{"internal/operators/mgr/workspace_controller.go", "api/v1alpha1/workspace_types.go"} {
			if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
				t.Errorf("%s not written: %v", rel, err)
			}
		}
	})
}

func TestRunWebhook(t *testing.T) {
	t.Run("an unknown service is named with the way out", func(t *testing.T) {
		serviceProject(t)
		err := runWebhook(testFactory(), "stripe", "payments")
		if err == nil || !strings.Contains(err.Error(), `service "payments" not found in this project`) || !strings.Contains(err.Error(), "forge scaffold service payments") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a bad name is refused", func(t *testing.T) {
		serviceProject(t)
		if err := runWebhook(testFactory(), "9x", "payments"); err == nil || !strings.Contains(err.Error(), "invalid webhook name") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a service the binary does not register cannot take webhooks", func(t *testing.T) {
		dir := serviceProject(t)
		writeFixtureFile(t, dir, "internal/handlers/payments/service.go", fixtureHandlerServiceGo)
		writeFixtureFile(t, dir, "gen/forge_descriptor/fixture.json",
			`{"Services":[{"Name":"PaymentsService","Package":"services.payments.v1","ProtoFile":"services/payments/v1/payments.proto","Methods":[]}]}`)
		f := testFactory()
		f.Gen.LoadServiceRegistry = func(string) (factory.ServiceRegistry, error) { return unregistered{}, nil }
		err := runWebhook(f, "stripe", "payments")
		if err == nil || !strings.Contains(err.Error(), `service "payments" is not registered in pkg/app/services.go — webhooks require a serving binary`) {
			t.Fatalf("err = %v", err)
		}
	})
}

type unregistered struct{ stubRegistry }

func (unregistered) Registered(string) bool { return false }

func TestRunBinary(t *testing.T) {
	t.Run("name rules", func(t *testing.T) {
		serviceProject(t)
		if err := runBinary("1bad"); err == nil || !strings.Contains(err.Error(), "invalid binary name") {
			t.Errorf("name: %v", err)
		}
		for _, reserved := range []string{"server", "version", "db"} {
			err := runBinary(reserved)
			if err == nil || !strings.Contains(err.Error(), "conflicts with a reserved cobra subcommand") {
				t.Errorf("%s: err = %v", reserved, err)
			}
		}
	})
	t.Run("a binary is written with its glue and then conflicts", func(t *testing.T) {
		dir := serviceProject(t)
		out, err := captureStdout(t, func() error { return runBinary("workspace-proxy") })
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Adding binary 'workspace-proxy'...", "Binary 'workspace-proxy' added successfully!", "internal/workspace_proxy/contract.go"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "internal", "workspace_proxy")); err != nil {
			t.Errorf("binary package missing: %v", err)
		}
		if err := runBinary("workspace_proxy"); err == nil || !strings.Contains(err.Error(), `"workspace_proxy" already exists in the project as a binary`) {
			t.Errorf("a name spelled differently but canonically equal must collide: %v", err)
		}
	})
}

func TestReportComponentWiring(t *testing.T) {
	w := componentCmdWiring{group: "workers"}
	t.Run("a project that lets forge generate compose is told so", func(t *testing.T) {
		out, _ := captureStdout(t, func() error { reportComponentWiring(t.TempDir(), "testproj", "reaper", w); return nil })
		if !strings.Contains(out, "are handled by `forge generate`") {
			t.Errorf("output = %s", out)
		}
	})

	t.Run("a project that disowned its composition is told exactly what to add", func(t *testing.T) {
		root := t.TempDir()
		writeFixtureFile(t, root, ".forge/disowned.json",
			`{"files":{"internal/app/compose.go":{"reason":"hand-composed"},"internal/app/lifecycle.go":{"reason":"hand-composed"}}}`)
		files := disownedCompositionFiles(root)
		if len(files) != 2 || files[0] != "internal/app/compose.go" || files[1] != "internal/app/lifecycle.go" {
			t.Fatalf("disowned = %v, want compose.go then lifecycle.go in a stable order", files)
		}
		// The subcommand's binding is still the placeholder naming what internal/app lacks.
		writeFixtureFile(t, root, "cmd/testproj/cmd/workers/reaper_accessor_gen.go",
			"package workers\n\n// internal/app declares no (*Components).WorkerReaper() yet\n")
		out, _ := captureStdout(t, func() error { reportComponentWiring(root, "testproj", "reaper", w); return nil })
		for _, want := range []string{
			"internal/app/compose.go and internal/app/lifecycle.go disowned",
			`wire the worker "reaper" into internal/app`,
			"`testproj reaper` refuses to start",
			"a Reaper field on Components holding internal/workers/reaper",
			"c.WorkerReaper() in AllWorkers()",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("disowned but already wired says nothing to do by hand", func(t *testing.T) {
		root := t.TempDir()
		writeFixtureFile(t, root, ".forge/disowned.json", `{"files":{"internal/app/compose.go":{"reason":"x"}}}`)
		out, _ := captureStdout(t, func() error { reportComponentWiring(root, "testproj", "reaper", w); return nil })
		if !strings.Contains(out, "are handled by `forge generate`") {
			t.Errorf("no pending accessor means nothing to add by hand:\n%s", out)
		}
	})

	t.Run("an operator's aggregate is AllOperators", func(t *testing.T) {
		root := t.TempDir()
		writeFixtureFile(t, root, ".forge/disowned.json", `{"files":{"internal/app/lifecycle.go":{"reason":"x"}}}`)
		writeFixtureFile(t, root, "cmd/testproj/cmd/operators/mgr_accessor_gen.go",
			"package operators\n\n// internal/app declares no (*Components).OperatorMgr() yet\n")
		out, _ := captureStdout(t, func() error {
			reportComponentWiring(root, "testproj", "mgr", componentCmdWiring{group: "operators"})
			return nil
		})
		if !strings.Contains(out, "c.OperatorMgr() in AllOperators()") {
			t.Errorf("output = %s", out)
		}
	})
}

func TestWireComponentIntoTree_GraphProjectJoinsOnGenerate(t *testing.T) {
	dir := withTempProject(t, minimalServiceForgeYAML)
	markServiceProject(t, dir) // no providers.go: the composition graph
	cfg := readTestConfig(t, dir)
	out, _ := captureStdout(t, func() error { wireWorkerIntoTree(cfg, dir, "reaper"); return nil })
	if !strings.Contains(out, "worker 'reaper' joins the composition graph on `forge generate`: `testproj server` runs it") {
		t.Errorf("output = %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd")); !os.IsNotExist(err) {
		t.Errorf("a graph project has no per-component subcommand; cmd/ was written: %v", err)
	}
}

func TestWireComponentIntoTree_UnrecognizedMainPrintsTheEdit(t *testing.T) {
	dir := serviceProject(t)
	seedCmdTree(t, dir, "testproj", "package main\n\nfunc main() { build().Run() }\n")
	cfg := readTestConfig(t, dir)
	out, _ := captureStdout(t, func() error { wireOperatorIntoTree(cfg, dir, "mgr"); return nil })
	for _, want := range []string{"One owned file left to wire", `import "example.com/testproj/cmd/testproj/cmd/operators"`, "operators.NewMgrCmd,"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if got := readTestFile(t, filepath.Join(dir, "cmd", "testproj", "main.go")); strings.Contains(got, "NewMgrCmd") {
		t.Errorf("an unrecognized main.go was edited:\n%s", got)
	}
}

func TestBinaryNameFallsBackToTheProjectDir(t *testing.T) {
	if got := binaryName(&config.ProjectConfig{Name: "acme"}, "/x/other"); got != "acme" {
		t.Errorf("named project: %q", got)
	}
	if got := binaryName(nil, "/x/other"); got != "other" {
		t.Errorf("nil config: %q", got)
	}
	if got := binaryName(&config.ProjectConfig{}, "/x/other"); got != "other" {
		t.Errorf("unnamed config: %q", got)
	}
}
