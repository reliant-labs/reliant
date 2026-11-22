//go:build e2e

package cli

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2EEveryScaffoldedWorkloadRunsACommandOfTheBinary pins bug 42f7f65b:
// `forge project new --service orders --service payments` declared a
// workload per service in deploy/kcl/workloads.k with `args = ["orders"]` /
// `["payments"]`, but a project on the composition graph has one command per
// root (`server`) and none per service, so any env that bound one deployed a
// pod that crash-looped on "unknown command".
//
// So this reads every workload the fresh project declares — workloads.k and
// each env's own (`_api`) — that runs the project binary, and runs each one's
// args against the built binary: a job must exit 0, a service must start and
// answer /healthz. A declaration naming a command the binary lacks fails here
// rather than in a cluster.
func TestE2EEveryScaffoldedWorkloadRunsACommandOfTheBinary(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "demo", "--mod", "github.com/test/demo",
		"--service", "orders", "--service", "payments", "--frontend", "web")
	projectDir := filepath.Join(dir, "demo")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")

	bin := filepath.Join(projectDir, "demo-bin")
	runCmd(t, projectDir, "go", "build", "-o", bin, "./cmd/demo")

	// Every workload of the project binary, by its args, from every file
	// that declares one.
	sources := []string{codegen.WorkloadsKCLRelPath}
	for _, env := range []string{"dev", "staging", "prod"} {
		sources = append(sources, filepath.Join("deploy", "kcl", env, "main.k"))
	}
	type run struct {
		kind  string
		args  []string
		where []string
	}
	runs := map[string]*run{}
	for _, rel := range sources {
		for name, w := range codegen.DeclaredWorkloads(readFileE2E(t, filepath.Join(projectDir, rel))) {
			if !codegen.ProjectBinaryRuns("demo", w.BuildCmd) {
				continue
			}
			key := strings.Join(w.Args, " ")
			if runs[key] == nil {
				runs[key] = &run{kind: w.Kind, args: w.Args}
			}
			runs[key].where = append(runs[key].where, rel+": "+name)
		}
	}
	for _, svc := range []string{"orders", "payments"} {
		if r := runs[svc]; r != nil {
			t.Errorf("a workload runs `demo %s`, a command the binary does not have (declared in %v): the server root runs every service", svc, r.where)
			delete(runs, svc)
		}
	}
	if runs["server"] == nil {
		t.Fatalf("no workload runs `demo server`, which serves orders and payments; declared: %v", runs)
	}
	keys := make([]string, 0, len(runs))
	for k := range runs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision postgres: %v", err)
	}
	defer cleanup()

	// Jobs first: the migrate job gates everything else on every runtime.
	sort.SliceStable(keys, func(i, j int) bool {
		return runs[keys[i]].kind == codegen.WorkloadKindJob && runs[keys[j]].kind != codegen.WorkloadKindJob
	})
	for _, key := range keys {
		r := runs[key]
		if r.kind == codegen.WorkloadKindJob {
			cmd := exec.Command(bin, r.args...)
			cmd.Dir = projectDir
			cmd.Env = append(os.Environ(), "DATABASE_URL="+dsn, "PPROF_ADDR=")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("job `demo %s` (declared in %v) failed: %v\n%s", key, r.where, err, out)
			}
			continue
		}
		bootRootArgs(t, bin, projectDir, []string{"DATABASE_URL=" + dsn}, r.args, map[string]int{"/healthz": http.StatusOK})
	}
}
