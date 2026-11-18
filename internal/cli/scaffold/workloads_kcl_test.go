package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// writeScaffoldedEnvs writes dev, staging and prod exactly as `forge project
// new` renders them, binding only migrate.
func writeScaffoldedEnvs(t *testing.T, root string) {
	t.Helper()
	for _, env := range []string{"dev", "staging", "prod"} {
		bindings := "    _hosted(wl.migrate)"
		if env == "dev" {
			bindings = "    _on_host_job(wl.migrate)"
		}
		out, err := templates.DeployTemplates().Render(scaffoldedEnvTemplate(env), templates.EnvTemplateData{
			ProjectName: "acme", EnvName: env, IngressEnabled: true, Bindings: bindings,
			APIWorkload: codegen.APIWorkloadStanza("github.com/acme/acme", "acme"),
		})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "deploy", "kcl", env)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.k"), out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readEnvMain(t *testing.T, root, env string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "deploy", "kcl", env, "main.k"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A kind the hosted platform refuses must never be bound where it cannot run,
// and must never yield an env that renders green and deploys nothing.
// `forge scaffold operator` binds it to a cluster you operate in every
// deployed env, and — because a scaffolded env declares no cluster yet — says
// which envs now refuse to render and why, at the moment it happens.
func TestBindWorkloadInEnvs_OperatorBindsToClusterAndWarns(t *testing.T) {
	root := t.TempDir()
	writeScaffoldedEnvs(t, root)

	out, _ := captureStdout(t, func() error {
		bindWorkloadInEnvs(root, "acme", config.ComponentConfig{Name: "reaper", Kind: config.ComponentKindOperator})
		return nil
	})

	if got := readEnvMain(t, root, "dev"); !strings.Contains(got, "    _on_k3d(wl.reaper)\n]") {
		t.Errorf("dev must run the operator in the local cluster:\n%s", got)
	}
	for _, env := range []string{"staging", "prod"} {
		if got := readEnvMain(t, root, env); !strings.Contains(got, "    _on_cluster(wl.reaper)\n]") {
			t.Errorf("%s must bind the operator to a cluster you operate:\n%s", env, got)
		}
	}
	for _, want := range []string{
		"operator 'reaper' cannot run on Reliant hosting",
		"Kubernetes API",
		"deploy/kcl/prod/main.k",
		"deploy/kcl/staging/main.k",
		"declare `_cluster`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scaffold output does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "deploy/kcl/dev/main.k —") {
		t.Errorf("dev runs the operator on k3d and needs no cluster; it must not be named:\n%s", out)
	}
}

// forge's cron component is a worker running its own scheduler, so it is
// hosted like any worker — inside the env's one API workload, the binary's
// `server`, which supervises every worker: no cluster, no warning, and no
// second hosted workload running it again.
func TestBindWorkloadInEnvs_CronComponentIsHosted(t *testing.T) {
	root := t.TempDir()
	writeScaffoldedEnvs(t, root)

	out, _ := captureStdout(t, func() error {
		bindWorkloadInEnvs(root, "acme", config.ComponentConfig{Name: "cleanup", Kind: config.ComponentKindCron})
		return nil
	})
	for _, env := range []string{"staging", "prod"} {
		got := readEnvMain(t, root, env)
		// Born with no service, the env had not bound `_api` yet.
		if !strings.Contains(got, "    _hosted(_api)\n]") || strings.Contains(got, "wl.cleanup") {
			t.Errorf("%s must run the cron component in the hosted `_api`, not on a line of its own:\n%s", env, got)
		}
	}
	if !strings.Contains(out, "worker 'cleanup' runs in _api, the binary's `server`, now bound: _hosted") {
		t.Errorf("scaffold output does not say where the worker runs:\n%s", out)
	}
	if strings.Contains(out, "cannot run on Reliant hosting") {
		t.Errorf("a hosted-admissible workload drew the refusal warning:\n%s", out)
	}
}

// Every env serves every service at one origin through `_api`, the binary's
// `server`. A service scaffolded into it is already mounted there, so it gets
// no workload of its own — in dev a process on a port the frontend never
// dials, hosted a hostname it never calls.
func TestBindWorkloadInEnvs_ServiceRunsInTheAPI(t *testing.T) {
	root := t.TempDir()
	writeScaffoldedEnvs(t, root)

	first, _ := captureStdout(t, func() error {
		bindWorkloadInEnvs(root, "acme", config.ComponentConfig{Name: "orders", Kind: config.ComponentKindServer})
		return nil
	})
	second, _ := captureStdout(t, func() error {
		bindWorkloadInEnvs(root, "acme", config.ComponentConfig{Name: "billing", Kind: config.ComponentKindServer})
		return nil
	})
	for env, line := range map[string]string{"dev": "    _on_host(_api)\n", "staging": "    _hosted(_api)\n", "prod": "    _hosted(_api)\n"} {
		got := readEnvMain(t, root, env)
		if strings.Count(got, line) != 1 || strings.Contains(got, "wl.orders") || strings.Contains(got, "wl.billing") {
			t.Errorf("%s must bind `_api` once and no service on its own:\n%s", env, got)
		}
	}
	if !strings.Contains(first, "deploy/kcl/dev/main.k (service 'orders' runs in _api, the binary's `server`, now bound: _on_host)") {
		t.Errorf("first service: dev output does not say it bound the API:\n%s", first)
	}
	if !strings.Contains(first, "deploy/kcl/prod/main.k (service 'orders' runs in _api, the binary's `server`, now bound: _hosted)") {
		t.Errorf("first service: output does not say it bound the API:\n%s", first)
	}
	if !strings.Contains(second, "deploy/kcl/prod/main.k (service 'billing' runs in _api, the binary's `server`; nothing to bind)") {
		t.Errorf("second service: output does not say the API already runs it:\n%s", second)
	}
}

// devMainK is a dev env's main.k in the scaffolded `_workloads` shape, which
// is what bindWorkloadInEnvs appends a binding to.
const devMainK = "import workloads as wl\n\n_workloads = [\n    _on_host_job(wl.migrate)\n]\n"

// declareProject writes a project with one worker, `reaper`, the given
// workloads.k, and a dev env, then runs the scaffold's declaration step for
// reaper and returns workloads.k and dev/main.k as they are left.
func declareProject(t *testing.T, workloadsK string) (string, string) {
	t.Helper()
	return declareProjectBeside(t, workloadsK)
}

// declareProjectBeside is declareProject in a project that already has the
// named workers. The project is on the legacy composition (an owned `type
// Infra struct`), whose binary has a subcommand per component: a worker of a
// project on the composition graph is never declared (declareGraphProject).
func declareProjectBeside(t *testing.T, workloadsK string, workers ...string) (string, string) {
	t.Helper()
	return declareProjectFiles(t, map[string]string{
		"internal/app/compose.go":   "package app\n\ntype Infra struct{}\n",
		codegen.WorkloadsKCLRelPath: workloadsK,
		"deploy/kcl/dev/main.k":     devMainK,
	}, workers...)
}

// TestDeclareWorkload_GraphProjectDeclaresNothing pins bug 42f7f65b: on the
// composition graph a worker has no subcommand of its own — the `server`
// root runs it — so `forge scaffold worker` declares no workload (one with
// `args = ["reaper"]` crash-loops on "unknown command") and binds nothing
// beside the env's `_api`.
func TestDeclareWorkload_GraphProjectDeclaresNothing(t *testing.T) {
	dev := codegen.APIWorkloadStanza("github.com/acme/demo", "demo") + "\n\n_workloads = [\n    _on_host_job(wl.migrate)\n    _on_host(_api)\n]\n"
	in := migrateDecl + "\nALL: [fw.Workload] = [migrate]\n"
	workloads, gotDev := declareProjectFiles(t, map[string]string{
		codegen.WorkloadsKCLRelPath: in,
		"deploy/kcl/dev/main.k":     dev,
	})
	if workloads != in {
		t.Errorf("workloads.k changed on the composition graph:\n%s", workloads)
	}
	if gotDev != dev {
		t.Errorf("dev/main.k changed although _api runs the worker:\n%s", gotDev)
	}
}

// declareProjectFiles writes files plus the worker `reaper` and the named
// workers, runs the declaration step for reaper, and returns workloads.k and
// dev/main.k as they are left.
func declareProjectFiles(t *testing.T, files map[string]string, workers ...string) (string, string) {
	t.Helper()
	root := t.TempDir()
	files["internal/workers/reaper/worker.go"] = "package reaper\n"
	for _, w := range workers {
		files["internal/workers/"+w+"/worker.go"] = "package " + w + "\n"
	}
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	declareWorkloadInKCL(root, &config.ProjectConfig{Name: "demo", ModulePath: "github.com/acme/demo"},
		componentSpec{name: "reaper", ctxLabel: "forge scaffold worker reaper"})
	read := func(rel string) string {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	return read(codegen.WorkloadsKCLRelPath), read("deploy/kcl/dev/main.k")
}

const migrateDecl = `import forge
import forge.workloads as fw

migrate = fw.Workload {
    name = "migrate"
    kind = "job"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["db", "migrate", "up"]
}
`

// TestDeclareWorkload_PlainProjectDeclaresAndBinds is the default path: no
// grouping, so the worker gets its own declaration, its ALL entry, and a
// binding in every env.
func TestDeclareWorkload_PlainProjectDeclaresAndBinds(t *testing.T) {
	workloads, dev := declareProject(t, migrateDecl+"\nALL: [fw.Workload] = [\n    migrate,\n]\n")
	if !strings.Contains(workloads, "reaper = fw.Workload {") || !strings.Contains(workloads, "    migrate,\n    reaper,\n]") {
		t.Errorf("workloads.k not declared + listed:\n%s", workloads)
	}
	if !strings.Contains(dev, "wl.reaper") {
		t.Errorf("dev/main.k not bound:\n%s", dev)
	}
}

// TestDeclareWorkload_ServedComponentIsNotDeclared: a worker another
// workload already runs (`serves`) gets no workload of its own — one would
// deploy it twice — and no env binding to a workload that does not exist.
func TestDeclareWorkload_ServedComponentIsNotDeclared(t *testing.T) {
	in := migrateDecl + `
background = fw.Workload {
    name = "background"
    kind = "worker"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["workers"]
    serves = ["reaper"]
}

ALL: [fw.Workload] = [migrate, background]
`
	workloads, dev := declareProject(t, in)
	if workloads != in {
		t.Errorf("workloads.k changed for a served component:\n%s", workloads)
	}
	if dev != devMainK {
		t.Errorf("dev/main.k changed for a served component:\n%s", dev)
	}
}

// TestDeclareWorkload_GroupingProjectShowsTheChoice: in a project that groups
// components with `serves`, a component in no group may still belong to one
// — the mount set lives in Go — so nothing is written; the choice is printed.
func TestDeclareWorkload_GroupingProjectShowsTheChoice(t *testing.T) {
	in := migrateDecl + `
admin_api = fw.Workload {
    name = "admin-api"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["admin-api"]
    serves = ["billing-admin"]
}

ALL: [fw.Workload] = [migrate, admin_api]
`
	workloads, dev := declareProject(t, in)
	if workloads != in || dev != devMainK {
		t.Errorf("files changed in a grouping project:\n%s\n---\n%s", workloads, dev)
	}
}

// TestDeclareWorkload_UneditableListBindsNothing: when ALL cannot be edited
// the stanza is printed, not written — so no env may be bound to it. Binding
// `wl.reaper` there would leave every env's main.k naming a workload that
// does not exist.
func TestDeclareWorkload_UneditableListBindsNothing(t *testing.T) {
	in := migrateDecl + "\nALL: [fw.Workload] = [migrate]\nALL: [fw.Workload] = [migrate]\n"
	workloads, dev := declareProject(t, in)
	if workloads != in {
		t.Errorf("workloads.k changed despite an ambiguous ALL:\n%s", workloads)
	}
	if dev != devMainK {
		t.Errorf("dev/main.k bound a workload that was never declared:\n%s", dev)
	}
}

// composedWorkers is a project whose workers all run in one process it
// composes (`demo workers`), the way control-plane runs thirty of them.
const composedWorkers = migrateDecl + `
background = fw.Workload {
    name = "background"
    kind = "worker"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["workers"]
}

ALL: [fw.Workload] = [migrate, background]
`

// TestDeclareWorkload_ComposedProcessProjectDeclaresNothing: the #1106
// defect. A project that runs its components of a kind in processes it
// composes — no `serves`, no per-component workload — got a standalone
// workload for each new one, and an env binding for it in every env. It
// belongs in one of those processes, which only the project's Go knows, so
// nothing is written.
func TestDeclareWorkload_ComposedProcessProjectDeclaresNothing(t *testing.T) {
	workloads, dev := declareProjectBeside(t, composedWorkers, "sweeper", "billing_sweep")
	if workloads != composedWorkers {
		t.Errorf("workloads.k changed in a composed-process project:\n%s", workloads)
	}
	if dev != devMainK {
		t.Errorf("dev/main.k bound a workload in a composed-process project:\n%s", dev)
	}
}

// TestDeclareWorkload_PerComponentConventionStillDeclares: the same custom
// process beside workers that each have a workload of their own is not the
// project's convention, so the new worker is declared and bound as before.
func TestDeclareWorkload_PerComponentConventionStillDeclares(t *testing.T) {
	in := strings.Replace(composedWorkers, "ALL: [fw.Workload] = [migrate, background]", `sweeper = fw.Workload {
    name = "sweeper"
    kind = "worker"
    build = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
    args = ["sweeper"]
}

ALL: [fw.Workload] = [migrate, background, sweeper]`, 1)
	workloads, dev := declareProjectBeside(t, in, "sweeper")
	if !strings.Contains(workloads, "reaper = fw.Workload {") {
		t.Errorf("workloads.k did not declare reaper:\n%s", workloads)
	}
	if !strings.Contains(dev, "wl.reaper") {
		t.Errorf("dev/main.k not bound:\n%s", dev)
	}
}
