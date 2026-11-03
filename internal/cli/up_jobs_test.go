package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/hostlaunch"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// job is a HOST-bound job workload whose argv is spec.command (verbatim).
func job(name string, command []string, before ...string) WorkloadEntity {
	return WorkloadEntity{
		Name: name, Kind: "job",
		Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{Runner: "go-run"}},
		Spec:    deployv1alpha1.WorkloadSpec{Kind: deployv1alpha1.KindJob, Command: command, Before: before},
	}
}

func svc(name string) WorkloadEntity { return hostWL(name) }

// shPath is p as an `sh -c` script must spell it: single-quoted and
// slash-separated. sh reads a backslash as an escape, so a native Windows
// temp path (C:\Users\...) named a different, relative file and the job's
// marker never appeared where the test looked.
func shPath(p string) string { return "'" + filepath.ToSlash(p) + "'" }

// The rendered contract's host jobs are what the host runner reads: a
// workload of kind job bound to forge.OnHost, its gating in spec.before.
func TestHostJobs_FromTheContract(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		svc("item"),
		job("provision-idp", []string{"sh", "-c", "echo PROVISIONED"}, "item"),
		func() WorkloadEntity {
			w := job("cluster-job", []string{"true"})
			w.Runtime = RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}}
			return w
		}(),
	}}
	jobs := hostJobs(e)
	if len(jobs) != 1 || jobs[0].Name != "provision-idp" {
		t.Fatalf("hostJobs = %v, want only the host-bound job", jobs)
	}
	if err := validateJobOrdering(jobs, e.Workloads); err != nil {
		t.Errorf("valid contract rejected: %v", err)
	}
}

func TestJobsGating(t *testing.T) {
	jobs := []WorkloadEntity{
		job("provision", nil, "api", "sync"),
		job("seed", nil),
		job("warm", nil, "api"),
	}
	if got := jobsGating("api", jobs); len(got) != 2 || got[0] != "provision" || got[1] != "warm" {
		t.Errorf("jobsGating(api) = %v, want [provision warm]", got)
	}
	if got := jobsGating("sync", jobs); len(got) != 1 || got[0] != "provision" {
		t.Errorf("jobsGating(sync) = %v, want [provision]", got)
	}
	if got := jobsGating("nobody", jobs); len(got) != 0 {
		t.Errorf("jobsGating(nobody) = %v, want empty", got)
	}
}

// A `before` naming a component that does not exist must be rejected
// with the name involved. Silently ignoring it is the failure this guard
// exists to prevent: the job runs, nothing waits, and the dependent
// fails later somewhere that names none of this.
func TestValidateJobOrdering_DanglingBefore(t *testing.T) {
	jobs := []WorkloadEntity{job("provision", nil, "apiserver")}
	services := []WorkloadEntity{svc("api")}

	err := validateJobOrdering(jobs, services)
	if err == nil {
		t.Fatal("expected an error for a before naming a nonexistent component")
	}
	for _, want := range []string{"provision -> apiserver", "api", "does not exist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}
}

func TestValidateJobOrdering_Cycle(t *testing.T) {
	jobs := []WorkloadEntity{job("a", nil, "b"), job("b", nil, "a")}
	err := validateJobOrdering(jobs, nil)
	if err == nil {
		t.Fatal("expected an error for a job-gates-job cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should name the cycle:\n%s", err)
	}
}

// A job gating a SERVICE (not another job) is the ordinary case and must
// not be mistaken for a cycle.
func TestValidateJobOrdering_Valid(t *testing.T) {
	jobs := []WorkloadEntity{job("provision", nil, "api"), job("seed", nil, "api", "provision")}
	services := []WorkloadEntity{svc("api")}
	if err := validateJobOrdering(jobs, services); err != nil {
		t.Fatalf("valid ordering rejected: %v", err)
	}
}

// A job that gates another job must run first, regardless of the order
// the two were declared in.
func TestOrderJobs_JobGatesJob(t *testing.T) {
	jobs := []WorkloadEntity{job("seed", nil, "api"), job("provision", nil, "seed")}
	got := orderJobs(jobs)
	if got[0].Name != "provision" || got[1].Name != "seed" {
		t.Errorf("orderJobs = [%s %s], want [provision seed]", got[0].Name, got[1].Name)
	}
}

// Independent jobs keep declaration order — the common case must not be
// reshuffled by the sort.
func TestOrderJobs_StableForIndependentJobs(t *testing.T) {
	jobs := []WorkloadEntity{job("one", nil, "api"), job("two", nil, "api"), job("three", nil, "api")}
	got := orderJobs(jobs)
	for i, want := range []string{"one", "two", "three"} {
		if got[i].Name != want {
			t.Errorf("orderJobs[%d] = %s, want %s", i, got[i].Name, want)
		}
	}
}

// The whole point of the primitive: a job that exits non-zero must STOP
// the up, naming what it gated, rather than letting the dependents run.
func TestRunOneHostJob_FailureIsFailClosed(t *testing.T) {
	j := job("provision", []string{"sh", "-c", "echo provisioning failed >&2; exit 3"}, "api")
	err := runOneHostJob(context.Background(), nil, j, nil, "")
	if err == nil {
		t.Fatal("expected a failing job to return an error")
	}
	for _, want := range []string{"provision", "api", "none of them were started"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}
}

func TestRunOneHostJob_SuccessRunsToCompletion(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	j := job("provision", []string{"sh", "-c", "echo done > " + shPath(marker)})
	if err := runOneHostJob(context.Background(), nil, j, nil, ""); err != nil {
		t.Fatalf("job failed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("job did not actually run: %v", err)
	}
}

// A one-shot that never exits must fail loudly instead of hanging the up
// forever. The bound is the host default (defaultJobTimeout); a caller's
// shorter deadline bounds it too, which is what this exercises.
func TestRunOneHostJob_TimeoutIsReported(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a subprocess that sleeps; covered in full mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	err := runOneHostJob(ctx, nil, job("hangs", []string{"sh", "-c", "sleep 30"}, "api"), nil, "")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("timeout took %s; the ceiling did not fire", elapsed)
	}
	if !strings.Contains(err.Error(), "did not finish within") {
		t.Errorf("error should explain the timeout:\n%s", err)
	}
}

// The job's own spec.env reaches the process.
func TestRunOneHostJob_EnvVarsReachTheProcess(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "env")
	j := job("provision", []string{"sh", "-c", "echo $IDP_URL > " + shPath(marker)})
	j.Spec.Env = []deployv1alpha1.EnvVar{{Name: "IDP_URL", Value: "http://idp:8080"}}
	if err := runOneHostJob(context.Background(), nil, j, nil, ""); err != nil {
		t.Fatalf("job failed: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.TrimSpace(string(got)) != "http://idp:8080" {
		t.Errorf("IDP_URL = %q, want http://idp:8080", strings.TrimSpace(string(got)))
	}
}

// An env with no jobs must behave exactly as it did before the bucket
// existed.
func TestRunHostJobs_NoJobsIsANoop(t *testing.T) {
	if err := runHostJobs(context.Background(), nil, &KCLEntities{}, nil, "dev"); err != nil {
		t.Fatalf("empty job set errored: %v", err)
	}
}

// Ordering across a multi-job sequence, observed by the jobs themselves.
func TestRunHostJobs_RunsInDependencyOrder(t *testing.T) {
	log := filepath.Join(t.TempDir(), "order")
	e := &KCLEntities{Workloads: []WorkloadEntity{
		svc("api"),
		// Declared second-first on purpose: `seed` is gated by
		// `provision`, so provision must still run first.
		job("seed", []string{"sh", "-c", "echo seed >> " + shPath(log)}, "api"),
		job("provision", []string{"sh", "-c", "echo provision >> " + shPath(log)}, "seed"),
	}}
	if err := runHostJobs(context.Background(), nil, e, nil, ""); err != nil {
		t.Fatalf("jobs failed: %v", err)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read order log: %v", err)
	}
	want := "provision\nseed\n"
	if string(got) != want {
		t.Errorf("execution order = %q, want %q", got, want)
	}
}

// A failing job stops the sequence: the jobs after it never run.
func TestRunHostJobs_StopsAtFirstFailure(t *testing.T) {
	log := filepath.Join(t.TempDir(), "order")
	e := &KCLEntities{Workloads: []WorkloadEntity{
		svc("api"),
		job("broken", []string{"sh", "-c", "exit 1"}, "api"),
		job("later", []string{"sh", "-c", "echo later >> " + shPath(log)}, "api"),
	}}
	if err := runHostJobs(context.Background(), nil, e, nil, ""); err == nil {
		t.Fatal("expected the failing job to stop the sequence")
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("a job after the failing one ran; the sequence did not stop")
	}
}

// ---------------------------------------------------------------------------
// The BROADCAST selector on the host lowering.
// ---------------------------------------------------------------------------
//
// `before = ["*"]` is how the deploy-time migration step is declared, so
// these are the host-side no-regression tests for it. The k8s and compose
// lowerings expand the wildcard in KCL; the host runner has to do it in Go,
// because there is no orchestrator underneath it to enforce the ordering.

// The wildcard is a SELECTOR, not a name. Treating it as a name is the
// bug this pins: validateJobOrdering rejected `*` as a dangling
// reference, which failed every `forge env up` of a project that scaffolds
// a migration — with a message telling the reader to fix a name that was
// never wrong.
func TestValidateJobOrdering_BroadcastIsNotADanglingName(t *testing.T) {
	jobs := []WorkloadEntity{job("migrate", []string{"true"}, BeforeAll)}
	svcs := []WorkloadEntity{svc("api"), svc("sync")}
	if err := validateJobOrdering(jobs, svcs); err != nil {
		t.Fatalf("broadcast before rejected: %v", err)
	}
}

// A broadcast job gates every service without naming one — including a
// service added after it was written, which is the entire reason the
// selector exists.
func TestJobsGating_BroadcastGatesEveryServiceAndNeverItself(t *testing.T) {
	jobs := []WorkloadEntity{job("migrate", []string{"true"}, BeforeAll), job("seed", []string{"true"})}
	for _, svc := range []string{"api", "sync", "a-service-added-later"} {
		got := jobsGating(svc, jobs)
		if len(got) != 1 || got[0] != "migrate" {
			t.Errorf("jobsGating(%q) = %v, want [migrate]", svc, got)
		}
	}
	// A broadcast job gates other JOBS too — a seed must not beat the
	// migration that creates the table it seeds.
	if got := jobsGating("seed", jobs); len(got) != 1 || got[0] != "migrate" {
		t.Errorf("jobsGating(seed) = %v, want [migrate]", got)
	}
	// ...but never itself: a job cannot wait for its own completion.
	if got := jobsGating("migrate", jobs); len(got) != 0 {
		t.Errorf("jobsGating(migrate) = %v, want []", got)
	}
}

// Two broadcast jobs are PEERS, not a cycle. Each gates everything else;
// neither gates the other, so the graph stays acyclic by construction
// rather than by a cycle check catching it.
func TestValidateJobOrdering_TwoBroadcastJobsAreNotACycle(t *testing.T) {
	jobs := []WorkloadEntity{job("migrate", []string{"true"}, BeforeAll), job("provision", []string{"true"}, BeforeAll)}
	if err := validateJobOrdering(jobs, []WorkloadEntity{svc("api")}); err != nil {
		t.Fatalf("two broadcast jobs reported as invalid: %v", err)
	}
}

// A broadcast job RUNS FIRST, even when it is declared last. Ordering by
// the (empty) name list would score it zero and run the migration after
// the seed job it is supposed to precede.
func TestRunHostJobs_BroadcastRunsBeforeOtherJobs(t *testing.T) {
	log := filepath.Join(t.TempDir(), "order")
	e := &KCLEntities{Workloads: []WorkloadEntity{
		svc("api"),
		job("seed", []string{"sh", "-c", "echo seed >> " + shPath(log)}, "api"),
		job("migrate", []string{"sh", "-c", "echo migrate >> " + shPath(log)}, BeforeAll),
	}}
	if err := runHostJobs(context.Background(), nil, e, nil, ""); err != nil {
		t.Fatalf("jobs failed: %v", err)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read order log: %v", err)
	}
	if want := "migrate\nseed\n"; string(got) != want {
		t.Errorf("execution order = %q, want %q", got, want)
	}
}

// A host job's argv is DERIVED like any host workload's: its GoBuild plus
// its args. The same declaration that runs `/app/<p> db migrate up` in its
// image runs `go run ./cmd/<p> db migrate up` here — no in-image path to
// rewrite, because none was written.
func TestHostJobArgvIsDerivedFromBuildAndArgs(t *testing.T) {
	w := hostWL("migrate", asJob([]string{"db", "migrate", "up"}, BeforeAll))
	w.Build = BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/demo", OutputName: "demo"}}
	cmd, err := hostlaunch.BuildCmd(context.Background(), w.Name, hostRunnerSpec(w))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go", "run", "./cmd/demo", "db", "migrate", "up"}
	if !slicesEqual(cmd.Args, want) {
		t.Errorf("derived argv = %v, want %v", cmd.Args, want)
	}
}

// A host job with neither command nor args has nothing to run and says so.
func TestRunOneHostJob_NothingToRun(t *testing.T) {
	err := runOneHostJob(context.Background(), nil, job("empty", nil), nil, "")
	if err == nil || !strings.Contains(err.Error(), "nothing to run") {
		t.Fatalf("err = %v, want a nothing-to-run refusal", err)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// spec.activeDeadlineSeconds bounds a HOST job exactly as it bounds a
// Kubernetes Job: once the job has run that long its process is killed and
// the run fails, naming the deadline, so the jobs it gates never start.
//
// The job forks a grandchild (the `go run` shape: the real program is a
// child of the launcher) and records its pid. Killing only the direct child
// would orphan that grandchild still running, which is a deadline in name
// only — so the test also proves the whole tree is gone.
func TestRunOneHostJob_ActiveDeadlineKillsTheProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The grandchild's pid comes from sh's $!, which under the Windows
		// runner's sh (MSYS) is an MSYS pid, not a Windows one — processAlive
		// cannot observe it, so the tree-kill assertion has nothing to check.
		t.Skip("the grandchild pid is sh's $!, which on Windows (MSYS sh) is not a Windows pid processAlive can observe")
	}
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	deadline := int64(1)
	j := job("seed", []string{"sh", "-c", "sleep 60 & echo $! > " + shPath(pidFile) + "; wait"}, "api")
	j.Spec.ActiveDeadlineSeconds = &deadline

	start := time.Now()
	err := runOneHostJob(context.Background(), nil, j, nil, "")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a job past its activeDeadlineSeconds must fail")
	}
	if elapsed > 15*time.Second {
		t.Errorf("the job ran %s; activeDeadlineSeconds=1 did not bound it", elapsed)
	}
	for _, want := range []string{"seed", "activeDeadlineSeconds", "1s", "killed", "api"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err)
		}
	}

	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("the job never started its grandchild: %v", rerr)
	}
	var pid int
	if _, serr := fmt.Sscan(strings.TrimSpace(string(raw)), &pid); serr != nil || pid <= 0 {
		t.Fatalf("bad grandchild pid %q", raw)
	}
	waitUntil := time.Now().Add(5 * time.Second)
	for processAlive(pid) && time.Now().Before(waitUntil) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		_ = signalProcessGroup(pid, 9)
		t.Fatalf("grandchild %d outlived the deadline: only the direct child was killed", pid)
	}
}

// The deadline a host job runs under: its declared activeDeadlineSeconds,
// else the host runner's safety ceiling.
func TestJobDeadline(t *testing.T) {
	j := job("seed", []string{"true"})
	if d, declared := jobDeadline(j); d != defaultJobTimeout || declared {
		t.Errorf("undeclared: got (%s, %v), want (%s, false)", d, declared, defaultJobTimeout)
	}
	secs := int64(90)
	j.Spec.ActiveDeadlineSeconds = &secs
	if d, declared := jobDeadline(j); d != 90*time.Second || !declared {
		t.Errorf("declared 90: got (%s, %v), want (1m30s, true)", d, declared)
	}
}

// A failed deadline stops the up when the job gates something, exactly like
// any other job failure: the gated workload's job ordering never proceeds.
func TestRunHostJobs_DeadlineFailureStopsTheGatedJobs(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "second-ran")
	deadline := int64(1)
	slow := job("slow", []string{"sh", "-c", "sleep 30"}, "after")
	slow.Spec.ActiveDeadlineSeconds = &deadline
	after := job("after", []string{"sh", "-c", "touch " + shPath(marker)}, "api")
	e := &KCLEntities{Workloads: []WorkloadEntity{slow, after, svc("api")}}
	err := runHostJobs(context.Background(), nil, e, nil, "")
	if err == nil || !strings.Contains(err.Error(), "activeDeadlineSeconds") {
		t.Fatalf("err = %v, want the deadline failure", err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("a job gated by the timed-out job ran anyway")
	}
}

// The `forge env up` summary lists every host process's log under
// .forge/logs/<env>/, jobs included. Jobs printed to the terminal only, so
// the summary pointed at migrate.log / idp-provision.log files that did not
// exist. The output must reach both.
func TestRunOneHostJob_OutputLandsInTheLogTheSummaryNames(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "forge.yaml"), []byte("name: logs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectDir) // projectDirForKCL resolves from the working directory

	if err := runOneHostJob(context.Background(), nil, job("migrate", []string{"sh", "-c", "echo migrations applied"}), nil, "dev"); err != nil {
		t.Fatalf("job failed: %v", err)
	}
	logPath := filepath.Join(projectDir, summaryLogPath("dev", "migrate"))
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the summary names %s, which the job never wrote: %v", summaryLogPath("dev", "migrate"), err)
	}
	if !strings.Contains(string(got), "migrations applied") {
		t.Fatalf("job log does not hold the job's output:\n%s", got)
	}
}
