package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/internal/testcollect"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// newTestCollectCmd is `forge test collect <env>`: pull the coverage of the
// env's running instrumented Go servers into a test run (internal/testcollect
// says how each runtime is reached).
func newTestCollectCmd() *cobra.Command {
	var (
		workloads []string
		reset     bool
		run, lane string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "collect <env> [--workload <w>] [--reset] [--run <dir>] [--lane e2e]",
		Short: "Pull coverage from an env's running instrumented servers into a test run",
		Long: `Pulls the Go coverage of the env's running instrumented workloads (the -cover
builds forge makes for local, ephemeral and preview envs) into a run, as the
lane's go-covdata input:

  host     GET http://127.0.0.1:<pprof port>/debug/coverage, plus what earlier
           processes flushed to .forge/tests/covdata/<env>/<workload>/
  compose  docker cp of the container's GOCOVERDIR (compose binds pprof on
           0.0.0.0, so the pull endpoint is withheld): what its process
           flushed at exit
  cluster  a port-forward to each running pod's debug port (6060), then the
           same GET

The coverage goes in <run>/<lane>/covdata/<workload>/, grouped by build: the
newest build is the workload's coverage, and an older one (an air rebuild, a
pod of the last rollout) is kept under stale/<hash>/, labelled "stale build"
and never merged. Without --run, FORGE_TEST_RUN is the run, else a new one is
begun; a collect after ` + "`forge test capture --lane e2e`" + ` joins that lane.

--reset starts the counts from zero instead, without changing the running
processes (the endpoint is read-only): each live process's counters are
pulled and recorded as its baseline, which every later collect subtracts, and
the counter files already flushed (by any process, including one that exited
before the reset) are recorded and left out of every later collect. A process
started after the reset, or restarted, is counted whole, and a file flushed
after the reset is always collected. Run it before the lane, collect after it.

A workload that is not a forge Go build reports
"coverage: unavailable (not a forge Go build)"; one the env does not
instrument, "not instrumented". Collection is best effort per workload.

Exit codes: 0 when anything was collected, or there was nothing to collect; 2
when an instrumented workload could not be reached and nothing was collected;
1 for a bad argument. --json prints forge.test.collect/v1.`,
		Example: `  forge test collect dev --reset --run "$RUN"            # baseline: count from here
  forge test capture --run "$RUN" --lane e2e -- go test -tags=e2e -json ./e2e/...
  forge test collect dev --run "$RUN" --lane e2e         # pull, convert, record`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := projectRoot()
			if err != nil {
				return err
			}
			env := args[0]
			command := []string{"forge", "test", "collect", env}
			cmd.Flags().Visit(func(f *pflag.Flag) { command = append(command, "--"+f.Name+"="+f.Value.String()) })
			req := testCollectRequest{
				projectDir: projectDir, env: env, workloads: workloads, reset: reset, run: run, lane: lane, command: command,
			}
			if testcapture.Off(os.Getenv) {
				doc := &testcollect.Document{Env: env, Lane: lane, Workloads: []testcollect.WorkloadResult{}, Warnings: []string{testcapture.EnvTests + "=off: nothing collected"}}
				doc.Stamp(testcollect.Schema, nil)
				return printTestCollect(cmd, doc, nil, asJSON)
			}
			entities, err := RenderKCL(cmd.Context(), projectDir, env)
			if err != nil {
				return fmt.Errorf("render env %s: %w", env, err)
			}
			doc, err := runTestCollect(cmd.Context(), entities, req)
			if doc == nil {
				return err
			}
			return printTestCollect(cmd, doc, err, asJSON)
		},
	}
	cmd.Flags().StringArrayVar(&workloads, "workload", nil, "Collect only this workload (repeatable; default: every workload of the env)")
	cmd.Flags().BoolVar(&reset, "reset", false, "Start the counts from zero instead of collecting: record the live counters as a baseline later collects subtract, and leave the counter files flushed before it out of every later collect")
	cmd.Flags().StringVar(&run, "run", "", "Run directory to record into (default: $FORGE_TEST_RUN, else a new run)")
	cmd.Flags().StringVar(&lane, "lane", testcollect.DefaultLane, "Lane to record the coverage under")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print forge.test.collect/v1")
	return cmd
}

func printTestCollect(cmd *cobra.Command, doc *testcollect.Document, err error, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(doc); encErr != nil {
			return encErr
		}
		return err
	}
	testcollect.WriteText(cmd.OutOrStdout(), doc)
	return err
}

// testCollectRequest is one collect: the env and the flags.
type testCollectRequest struct {
	projectDir, env string
	workloads       []string
	reset           bool
	run, lane       string
	command         []string
}

// Seams: the transports a collect reaches workloads through. Tests replace
// them with fakes; nil is the real one.
var (
	testCollectDocker testcollect.Docker
	testCollectKube   testcollect.Kube
	// testCollectConvert stands in for `go tool covdata textfmt`.
	testCollectConvert func(ctx context.Context, goBin, dir string) ([]testreport.CovdataBuild, error)
)

// runTestCollect collects the env's coverage. The document is nil only for
// a bad request (an unknown --workload, an env that fails to resolve).
func runTestCollect(ctx context.Context, e *KCLEntities, req testCollectRequest) (*testcollect.Document, error) {
	targets, err := collectTargets(e, req.projectDir, req.env, config.ReadTestsCoverage(req.projectDir))
	if err != nil {
		return nil, exitCodeError{code: exitWrong, msg: fmt.Sprintf("env %s: %v", req.env, err)}
	}
	if len(req.workloads) > 0 {
		var picked []testcollect.Target
		for _, name := range req.workloads {
			i := slices.IndexFunc(targets, func(t testcollect.Target) bool { return t.Workload == name })
			if i < 0 {
				var names []string
				for _, t := range targets {
					names = append(names, t.Workload)
				}
				return nil, exitCodeError{code: exitWrong, msg: fmt.Sprintf("env %s has no workload %q to collect (it has: %s)", req.env, name, strings.Join(names, ", "))}
			}
			picked = append(picked, targets[i])
		}
		targets = picked
	}
	docker, kube := testCollectDocker, testCollectKube
	if docker == nil {
		docker = collectDocker{}
	}
	if kube == nil {
		kube = &collectKube{}
	}
	doc := testcollect.Collect(ctx, testcollect.Options{
		ProjectRoot: req.projectDir,
		Env:         req.env,
		Lane:        req.lane,
		Reset:       req.reset,
		Targets:     targets,
		Run:         collectRun(req),
		Command:     req.command,
		Docker:      docker,
		Kube:        kube,
		Convert:     testCollectConvert,
		ModulePath:  goModulePath(req.projectDir),
	})
	if doc.ExitCode == 0 {
		return doc, nil
	}
	// 2 is a verdict (nothing could be reached), printed as is; anything
	// else is a bad request.
	return doc, exitCodeError{code: doc.ExitCode, msg: doc.Error, verdict: doc.ExitCode == exitUndetermined}
}

// collectRun resolves the run a collect records into: --run, else
// FORGE_TEST_RUN, else a new one (testcapture.Begin, as every lane does).
func collectRun(req testCollectRequest) func(context.Context) (string, []string, error) {
	return func(ctx context.Context) (string, []string, error) {
		getenv := os.Getenv
		if req.run != "" {
			getenv = func(k string) string {
				if k == testcapture.EnvRun {
					return req.run
				}
				return os.Getenv(k)
			}
		}
		dir, _ := cmdutil.ResolutionRoot()
		res := testcapture.Begin(ctx, testcapture.BeginOptions{
			Dir: dir, ProjectRoot: req.projectDir, ForgeVersion: buildinfo.Version(), Getenv: getenv,
		})
		if !res.Recording {
			return "", res.Warnings, fmt.Errorf("could not create a test run (%s)", strings.Join(res.Warnings, "; "))
		}
		return res.Dir, res.Warnings, nil
	}
}

// imageCoverDir is GOCOVERDIR in a forge cover image: the Dockerfile's
// cover stage sets `ENV GOCOVERDIR=/tmp`, the one directory a distroless
// non-root image can always write.
const imageCoverDir = "/tmp"

// defaultPprofAddr is serverkit's debug listener when PPROF_ADDR is unset
// (the scaffolded config.proto default).
const defaultPprofAddr = "127.0.0.1:6060"

// collectTargets resolves each workload of the env to what `forge test
// collect` reaches: whether it is a forge Go build the env instruments
// (ResolveCoverBinaries, the same answer the build used), and where its
// coverage is on its runtime. Tools and build-only workloads never run, so
// they are left out.
func collectTargets(e *KCLEntities, projectDir, env string, optIn bool) ([]testcollect.Target, error) {
	if e == nil {
		return nil, nil
	}
	bins, err := ResolveCoverBinaries(e, optIn)
	if err != nil {
		return nil, err
	}
	known, enabled := map[string]bool{}, map[string]bool{}
	for _, b := range bins {
		for _, w := range b.Workloads {
			known[w], enabled[w] = true, b.Enabled
		}
	}
	var out []testcollect.Target
	hostAddrs := map[string][]int{} // pull address → indexes into out
	for _, w := range e.Workloads {
		if deployv1alpha1.WorkloadKind(w.Kind) == deployv1alpha1.KindTool || w.OnRuntime(RuntimeBuildOnly) {
			continue
		}
		t := testcollect.Target{Workload: w.Name, Runtime: w.Runtime.Type}
		switch {
		case !known[w.Name]:
			t.Coverage = testcollect.CoverageUnavailable
		case !enabled[w.Name]:
			t.Coverage = testcollect.CoverageNotInstrumented
		case w.OnRuntime(RuntimeHost):
			addr, withheld := pullAddr(w.HostEnv()["PPROF_ADDR"])
			t.Host = &testcollect.HostTarget{PullAddr: addr, Withheld: withheld, CoverDir: coverDir(projectDir, env, w.Name)}
			if addr != "" {
				hostAddrs[addr] = append(hostAddrs[addr], len(out))
			}
		case w.OnRuntime(RuntimeCompose) && w.Runtime.Compose != nil:
			spec := composeSpecFor(projectDir, env, w.Runtime.Compose)
			service := w.Runtime.Compose.Service
			if service == "" {
				service = w.Name
			}
			dir := w.HostEnv()["GOCOVERDIR"]
			if dir == "" {
				dir = imageCoverDir
			}
			t.Compose = &testcollect.ComposeTarget{Service: service, WorkingDir: spec.ProjectDirectory, Project: spec.Env["COMPOSE_PROJECT_NAME"], CoverDir: dir}
		case w.OnRuntime(RuntimeCluster) && w.Runtime.Cluster != nil:
			t.Cluster = clusterCollectTarget(e, w, env)
		case w.OnRuntime(RuntimeHosted):
			t.Unreachable = "runs on Reliant hosting: its pods are the control plane's, so forge cannot reach their debug listener"
		default:
			t.Unreachable = fmt.Sprintf("runtime %q: forge does not run it, so it has no process to pull from", w.Runtime.Type)
		}
		out = append(out, t)
	}
	// Two host processes cannot both bind one debug listener: the second
	// one's pprof failed, and a pull there reads the first one's coverage.
	for addr, idx := range hostAddrs {
		if len(idx) < 2 {
			continue
		}
		var names []string
		for _, i := range idx {
			names = append(names, out[i].Workload)
		}
		sort.Strings(names)
		for _, i := range idx {
			out[i].Host.PullAddr = ""
			out[i].Host.Withheld = fmt.Sprintf("workloads %s share the debug listener %s, so a pull could read another process (give each its own PPROF_ADDR)", strings.Join(names, ", "), addr)
		}
	}
	return out, nil
}

// pullAddr is where a host process serves /debug/coverage, from its
// PPROF_ADDR: only a loopback listener serves it (serverkit's rule), so any
// other bind is withheld, with the reason.
func pullAddr(pprof string) (addr, withheld string) {
	pprof = strings.TrimSpace(pprof)
	if pprof == "" {
		return defaultPprofAddr, ""
	}
	host, port, err := net.SplitHostPort(pprof)
	if err != nil || port == "" {
		return "", fmt.Sprintf("PPROF_ADDR=%q is not a host:port", pprof)
	}
	if !loopbackHost(host) {
		return "", fmt.Sprintf("PPROF_ADDR=%s is not loopback, so serverkit does not serve %s", pprof, testcollect.CoveragePath)
	}
	return net.JoinHostPort(host, port), ""
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// clusterCollectTarget is where a cluster workload's pods are: its declared
// cluster (the kube context) and namespace, else the env's cluster target,
// else forge's namespace convention, and the debug port of its PPROF_ADDR.
func clusterCollectTarget(e *KCLEntities, w WorkloadEntity, env string) *testcollect.ClusterTarget {
	c := w.Runtime.Cluster
	t := &testcollect.ClusterTarget{
		Context:   c.Cluster,
		Namespace: c.Namespace,
		Selector:  "app.kubernetes.io/name=" + w.Name,
		Port:      6060,
	}
	if t.Context == "" {
		t.Context = e.ClusterTarget.field("cluster")
	}
	if t.Namespace == "" {
		t.Namespace = e.ClusterTarget.field("namespace")
	}
	if t.Namespace == "" {
		t.Namespace = e.ManifestNamespace
	}
	if t.Namespace == "" && e.Project != "" {
		t.Namespace = e.Project + "-" + env
	}
	if p := w.HostEnv()["PPROF_ADDR"]; p != "" {
		if _, withheld := pullAddr(p); withheld != "" {
			t.Withheld = withheld
		}
		if n, err := strconv.Atoi(pprofPortFromAddr(p)); err == nil {
			t.Port = n
		}
	}
	return t
}
