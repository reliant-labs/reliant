package lint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// scaffoldedWorkloadsProject writes the workloads.k a fresh project is born
// with — through the scaffolder itself, so the test tracks what forge really
// emits — and returns the project dir. hasFrontend adds the idp-provision job.
func scaffoldedWorkloadsProject(t *testing.T, name string, hasFrontend bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := generator.ScaffoldWorkloadsKCL(dir, "github.com/acme/"+name, name, nil, hasFrontend); err != nil {
		t.Fatalf("ScaffoldWorkloadsKCL: %v", err)
	}
	return dir
}

func orphanNames(findings []componentDriftFinding) []string {
	var out []string
	for _, f := range findings {
		if !f.Undeclared {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// TestComponentDrift_ScaffoldedJobsAreNotOrphans: a freshly scaffolded
// project declares `migrate` (`db migrate up`) and, with a frontend,
// `idp-provision` (`auth idp-provision`). Both run built-in subcommands of
// the project binary, not components, so neither is drift. Before this, every
// `forge project new` linted with a warning about its own migrate job.
func TestComponentDrift_ScaffoldedJobsAreNotOrphans(t *testing.T) {
	for _, hasFrontend := range []bool{false, true} {
		dir := scaffoldedWorkloadsProject(t, "demo", hasFrontend)
		findings, err := collectComponentDrift(dir, &config.ProjectConfig{Name: "demo"})
		if err != nil {
			t.Fatal(err)
		}
		if len(findings) != 0 {
			t.Errorf("hasFrontend=%v: fresh scaffold drift findings = %+v, want none", hasFrontend, findings)
		}
	}
}

// TestComponentDrift_GenuineOrphansStillWarn: the exemption is judged by what
// a workload RUNS, not its name. A project-binary workload selecting a
// component subcommand that no longer exists is stale; a workload named
// `migrate` that runs something else is not exempted by its name; and a
// built-in subcommand of a DIFFERENT binary is not the project's own command.
func TestComponentDrift_GenuineOrphansStillWarn(t *testing.T) {
	dir := scaffoldedWorkloadsProject(t, "demo", false)
	path := filepath.Join(dir, codegen.WorkloadsKCLRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	extra := `
billing = fw.Workload {
    name = "billing"
    kind = "service"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["billing"]
}

legacy_migrate = fw.Workload {
    name = "legacy-migrate"
    kind = "job"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["migrate-legacy"]
}

other_db = fw.Workload {
    name = "other-db"
    kind = "job"
    build = forge.GoBuild {cmd = "./cmd/other", output_name = "other"}
    args = ["db", "migrate", "up"]
}

all_in_one = fw.Workload {
    name = "all-in-one"
    kind = "service"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["server"]
}
`
	if err := os.WriteFile(path, append(raw, []byte(extra)...), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := collectComponentDrift(dir, &config.ProjectConfig{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(orphanNames(findings), ",")
	if want := "billing,legacy-migrate,other-db"; got != want {
		t.Errorf("orphan entries = %q, want %q (migrate and the all-in-one server run built-ins; the rest are drift)", got, want)
	}
}

// withWorkers adds one discoverable worker package per name — discovery
// counts any internal/workers/<name> holding a non-test Go file.
func withWorkers(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		pkg := filepath.Join(dir, "internal", "workers", n)
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "worker.go"), []byte("package "+n+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func appendWorkloads(t *testing.T, dir, extra string) {
	t.Helper()
	path := filepath.Join(dir, codegen.WorkloadsKCLRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte(extra)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func undeclaredNames(findings []componentDriftFinding) []string {
	var out []string
	for _, f := range findings {
		if f.Undeclared {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// TestComponentDrift_ServedComponentsAreDeclared: a component run by a
// workload that names it in `serves` — control-plane's in-binary admin
// services, served by `admin-api` — is deployed, so it is not reported
// undeclared, and the grouping workload is not an orphan just because its
// name is the process's rather than a component's. A component nothing
// serves still warns. Before `serves`, every one of these warned, with no way
// to say why that was fine.
func TestComponentDrift_ServedComponentsAreDeclared(t *testing.T) {
	dir := scaffoldedWorkloadsProject(t, "demo", false)
	withWorkers(t, dir, "reaper", "billing_sweep", "forgotten")
	appendWorkloads(t, dir, `
_demo_build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}

background = fw.Workload {
    name = "background"
    kind = "worker"
    build = _demo_build
    args = ["workers"]
    serves = [
        "reaper",
        "billing-sweep",  # kebab spelling of billing_sweep
    ]
}
`)
	findings, err := collectComponentDrift(dir, &config.ProjectConfig{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(undeclaredNames(findings), ","); got != "forgotten" {
		t.Errorf("undeclared = %q, want only %q", got, "forgotten")
	}
	if got := orphanNames(findings); len(got) != 0 {
		t.Errorf("orphans = %v, want none (background serves live components)", got)
	}
}

// TestComponentDrift_AMultiWordComponentMatchesItsWorkload: a worker named
// like a Go package (`billing_sweep`) is declared the way scaffold declares
// it, as the DNS-1123 label `billing-sweep` (codegen.WorkloadName). The two
// spellings are one workload: neither undeclared nor an orphan.
func TestComponentDrift_AMultiWordComponentMatchesItsWorkload(t *testing.T) {
	dir := scaffoldedWorkloadsProject(t, "demo", false)
	withWorkers(t, dir, "billing_sweep")
	appendWorkloads(t, dir, codegen.WorkloadStanza("github.com/acme/demo", "demo",
		config.ComponentConfig{Name: "billing_sweep", Kind: config.ComponentKindWorker}))
	findings, err := collectComponentDrift(dir, &config.ProjectConfig{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none", findings)
	}
}

// TestComponentDrift_AllInOneServerDeclaresEverything: a project deploying
// its binary's `server` command runs every service, worker and operator in
// that one process, so no component of it is undeclared.
func TestComponentDrift_AllInOneServerDeclaresEverything(t *testing.T) {
	dir := scaffoldedWorkloadsProject(t, "demo", false)
	withWorkers(t, dir, "reaper", "billing_sweep")
	appendWorkloads(t, dir, `
app = fw.Workload {
    name = "app"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["server"]
}
`)
	findings, err := collectComponentDrift(dir, &config.ProjectConfig{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none", findings)
	}
}

// TestComponentDrift_UndeclaredSaysHowToMarkServed: the warning names the
// `serves` alternative, because "add a workload" is the wrong fix for a
// component another process already runs.
func TestComponentDrift_UndeclaredSaysHowToMarkServed(t *testing.T) {
	dir := scaffoldedWorkloadsProject(t, "demo", false)
	withWorkers(t, dir, "reaper")
	rc := &lintRunCtx{cwd: dir, cfg: &config.ProjectConfig{Name: "demo"}}
	out, _, err := collectComponentDriftJSON(rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !strings.Contains(out[0].Message, "`serves`") || !strings.Contains(out[0].Message, `"reaper"`) {
		t.Errorf("findings = %+v, want one naming the serves list for reaper", out)
	}
}

// TestComponentDrift_ComposedProcessesDeclareTheirKind: control-plane's
// shape. Its workers all run in `workers`, a process it composes itself, and
// its operator runs through its own subcommand under a process name. Neither
// is drift — `forge scaffold` reads the file the same way and declares no
// workload for them — and neither process is an orphan. A worker with a
// workload of its own, and a job on a subcommand of its own making, are
// judged as before.
func TestComponentDrift_ComposedProcessesDeclareTheirKind(t *testing.T) {
	dir := scaffoldedWorkloadsProject(t, "demo", false)
	withWorkers(t, dir, "reaper", "billing_sweep", "sweeper", "spend_snapshot")
	appendWorkloads(t, dir, `
_demo_build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}

background = fw.Workload {
    name = "demo-workers"
    kind = "worker"
    build = _demo_build
    args = ["workers"]
}

spend = fw.Workload {
    name = "spend-snapshotter"
    kind = "worker"
    build = _demo_build
    args = ["spend_snapshot"]
}

prune = fw.Workload {
    name = "prune"
    kind = "job"
    build = _demo_build
    args = ["cloudsql", "prune-backups"]
}
`)
	findings, err := collectComponentDrift(dir, &config.ProjectConfig{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if got := undeclaredNames(findings); len(got) != 0 {
		t.Errorf("undeclared = %v, want none (reaper, billing_sweep and sweeper run in demo-workers, spend_snapshot in its own subcommand)", got)
	}
	if got := strings.Join(orphanNames(findings), ","); got != "prune" {
		t.Errorf("orphans = %q, want only prune (demo-workers is the project's process, spend-snapshotter runs spend_snapshot)", got)
	}
}
