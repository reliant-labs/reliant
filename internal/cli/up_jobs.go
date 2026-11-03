package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/hostlaunch"
)

// The HOST lowering of the one-shot `job` component kind.
//
// k8s and compose each have a native way to say "run this to completion,
// then start that": an init container, and a `depends_on` with
// `condition: service_completed_successfully`. The host runner has
// neither, because there is no orchestrator underneath it — it is a
// process launcher. So the ordering the other two targets DELEGATE, this
// one has to ENFORCE: run the argv, wait for exit 0, and only then let
// the gated services launch.
//
// That asymmetry is the whole reason `Job` is a primitive rather than a
// k8s feature with a compose shim. The declaration is identical across
// the three; only the enforcement mechanism differs.

// defaultJobTimeout bounds the wait for a host one-shot that declares no
// spec.activeDeadlineSeconds (see jobDeadline).
//
// A one-shot that never exits is indistinguishable, from the outside,
// from one that is merely slow — and the failure mode of guessing wrong
// in the permissive direction is `forge env up` hanging forever with no
// output, which is the worst possible signal. Five minutes is far longer
// than any provisioning step should take and short enough that a wedged
// job is noticed within one coffee.
const defaultJobTimeout = 5 * time.Minute

// BeforeAll is the BROADCAST selector for a one-shot's `before`: "gate
// every workload in this environment", as opposed to an enumerated list
// of dependent names. It mirrors `forge.workloads.BEFORE_ALL` in the KCL
// layer, which is where the value is defined and documented.
//
// A workload can never be NAMED this — a workload name is used verbatim
// as a Kubernetes object name and "*" is not a legal DNS-1123 label — so
// the wildcard cannot collide with a real dependent.
const BeforeAll = "*"

// isBroadcast reports whether a one-shot gates everything rather than an
// enumerated set.
func isBroadcast(j WorkloadEntity) bool {
	for _, dep := range j.Spec.Before {
		if dep == BeforeAll {
			return true
		}
	}
	return false
}

// jobsGating returns the names of the one-shot jobs that must complete
// before the named service launches, in declaration order.
//
// A BROADCAST job gates every service, so it is returned for all of
// them without appearing in any list — which is the entire point: the
// service added next month is gated with nothing to update.
func jobsGating(service string, jobs []WorkloadEntity) []string {
	var out []string
	for _, j := range jobs {
		if j.Name == service {
			continue
		}
		if isBroadcast(j) {
			out = append(out, j.Name)
			continue
		}
		for _, dep := range j.Spec.Before {
			if dep == service {
				out = append(out, j.Name)
				break
			}
		}
	}
	return out
}

// validateJobOrdering rejects a job graph that cannot be executed as
// declared, BEFORE any process starts.
//
// Two failures are possible and both are silent otherwise:
//
//   - `before` naming something that does not exist. The job runs, nothing
//     waits for it, and the ordering the author declared just does not
//     happen. The service then fails somewhere else entirely, at runtime,
//     in a message that names none of this.
//   - a cycle. Two jobs each waiting for the other deadlocks the up.
//
// Both are reported with the names involved, because "invalid job graph"
// without the names is a message that sends the reader back to the file
// to work out what forge already knew.
func validateJobOrdering(jobs []WorkloadEntity, workloads []WorkloadEntity) error {
	known := map[string]bool{}
	for _, w := range workloads {
		known[w.Name] = true
	}
	for _, j := range jobs {
		known[j.Name] = true
	}

	var dangling []string
	for _, j := range jobs {
		for _, dep := range j.Spec.Before {
			// The BROADCAST selector is not a name and cannot dangle —
			// there is no list to go stale, which is why it exists.
			if dep == BeforeAll {
				continue
			}
			if !known[dep] {
				dangling = append(dangling, fmt.Sprintf("%s -> %s", j.Name, dep))
			}
		}
	}
	if len(dangling) > 0 {
		names := make([]string, 0, len(known))
		for n := range known {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf(
			"job ordering names a component that does not exist: %s\n"+
				"  declared components: %s\n"+
				"  fix the name in deploy/kcl/<env>/main.k, or drop `before` to run the job without gating anything",
			strings.Join(dangling, ", "), strings.Join(names, ", "))
	}

	// A job may gate another job; that is a legitimate sequence
	// (provision, then seed). A CYCLE in that graph is not.
	edges := map[string][]string{}
	isJob := map[string]bool{}
	broadcast := map[string]bool{}
	for _, j := range jobs {
		isJob[j.Name] = true
		broadcast[j.Name] = isBroadcast(j)
	}
	for _, j := range jobs {
		if isBroadcast(j) {
			// A broadcast job gates every OTHER job, but never another
			// broadcast job: two of them would each claim to precede the
			// other, which is not an ordering. They are peers — both run
			// before everything else, in declaration order — so no edge
			// is drawn between them and the graph cannot cycle through
			// the wildcard.
			for _, other := range jobs {
				if other.Name != j.Name && !broadcast[other.Name] {
					edges[j.Name] = append(edges[j.Name], other.Name)
				}
			}
			continue
		}
		for _, dep := range j.Spec.Before {
			if isJob[dep] {
				edges[j.Name] = append(edges[j.Name], dep)
			}
		}
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var visit func(string) error
	visit = func(n string) error {
		color[n] = grey
		stack = append(stack, n)
		for _, m := range edges[n] {
			switch color[m] {
			case grey:
				return fmt.Errorf(
					"job ordering has a cycle: %s -> %s\n"+
						"  a job cannot wait for a job that waits for it; break the cycle in deploy/kcl/<env>/main.k",
					strings.Join(stack, " -> "), m)
			case white:
				if err := visit(m); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return nil
	}
	names := make([]string, 0, len(isJob))
	for n := range isJob {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if color[n] == white {
			if err := visit(n); err != nil {
				return err
			}
		}
	}
	return nil
}

// orderJobs returns the jobs in an execution order that respects
// job-gates-job edges (a job listed in another job's `before` runs
// first). Ties keep declaration order, so the common case — a flat list
// of independent one-shots — runs exactly as written.
func orderJobs(jobs []WorkloadEntity) []WorkloadEntity {
	pos := map[string]int{}
	for i, j := range jobs {
		pos[j.Name] = i
	}
	broadcast := map[string]bool{}
	for _, j := range jobs {
		broadcast[j.Name] = isBroadcast(j)
	}
	// depth = how many job-gates-job edges lead OUT of this job. A job
	// that gates another must run before it, so higher depth sorts first.
	depth := map[string]int{}
	var compute func(string, map[string]bool) int
	compute = func(n string, seen map[string]bool) int {
		if d, ok := depth[n]; ok {
			return d
		}
		if seen[n] {
			return 0 // cycle; validateJobOrdering rejects it separately
		}
		seen[n] = true
		best := 0
		for _, j := range jobs {
			if j.Name != n {
				continue
			}
			// A broadcast job gates every other non-broadcast job, so its
			// depth is one deeper than the deepest of them. Computing it
			// from the (absent) name list would score it 0 and run the
			// migration AFTER the seed job it is supposed to precede.
			if isBroadcast(j) {
				for _, other := range jobs {
					if other.Name == n || broadcast[other.Name] {
						continue
					}
					if d := compute(other.Name, seen) + 1; d > best {
						best = d
					}
				}
				continue
			}
			for _, dep := range j.Spec.Before {
				if _, isJob := pos[dep]; !isJob {
					continue
				}
				if d := compute(dep, seen) + 1; d > best {
					best = d
				}
			}
		}
		depth[n] = best
		return best
	}
	for _, j := range jobs {
		compute(j.Name, map[string]bool{})
	}
	out := append([]WorkloadEntity(nil), jobs...)
	sort.SliceStable(out, func(a, b int) bool {
		if depth[out[a].Name] != depth[out[b].Name] {
			return depth[out[a].Name] > depth[out[b].Name]
		}
		return pos[out[a].Name] < pos[out[b].Name]
	})
	return out
}

// runHostJobs runs every one-shot job to completion, in dependency
// order, before the host service phase launches anything.
//
// It is deliberately FAIL-CLOSED: a job that exits non-zero (or times
// out) stops the up. The entire premise of the primitive is that the
// dependents must not run against a world the job was supposed to have
// prepared — proceeding anyway would turn a clear, local failure into an
// obscure one inside a service that has no idea why its dependency is
// missing.
//
// cfg / env feed the same projectConfig env layer host services get, so
// a job and the service it gates see the same configuration.
func runHostJobs(ctx context.Context, cfg *config.ProjectConfig, e *KCLEntities, secretsLayer map[string]string, env string) error {
	jobs := hostJobs(e)
	if len(jobs) == 0 {
		return nil
	}
	if err := validateJobOrdering(jobs, e.Workloads); err != nil {
		return err
	}
	for _, j := range orderJobs(jobs) {
		err := runOneHostJob(ctx, cfg, j, secretsLayer, env)
		if err == nil {
			continue
		}
		// Fail-closed is about the DEPENDENTS. A job that gates nothing has
		// no dependents to protect, so aborting the whole up on its failure
		// takes down a stack it was not standing in front of — which is what
		// happened to every second project on a machine: the dev IdP's port
		// was already held by another stack, the IdP could not
		// start, and `forge env up` refused to start the app at all. The
		// backend and frontend would have come up perfectly well; only
		// sign-in was unavailable.
		//
		// So a gating job still stops the up, and a non-gating one degrades:
		// loudly, naming what is now missing, and continuing.
		if len(j.Spec.Before) > 0 || isBroadcast(j) {
			return err
		}
		fmt.Printf("[up] job %s FAILED — continuing, because it gates nothing:\n%v\n", j.Name, err)
		fmt.Printf("[up] whatever %s provisions is unavailable this run; everything else still starts.\n", j.Name)
	}
	return nil
}

// hostJobs are the env's HOST-bound jobs, in declaration order. A job bound
// to a cluster or the control plane is gated by its own runtime (an
// initContainer, a pre-rollout Job); only the host runner has to enforce the
// ordering itself.
func hostJobs(e *KCLEntities) []WorkloadEntity {
	var out []WorkloadEntity
	for _, w := range e.WorkloadsOn(RuntimeHost) {
		if w.IsJob() {
			out = append(out, w)
		}
	}
	return out
}

// jobDeadline is how long a host job may run: its declared
// spec.activeDeadlineSeconds — the same bound Kubernetes enforces on the
// cluster Job, so the job fails the same way on every runtime — else the
// host runner's safety ceiling, defaultJobTimeout. declared reports which.
func jobDeadline(j WorkloadEntity) (d time.Duration, declared bool) {
	if s := j.Spec.ActiveDeadlineSeconds; s != nil && *s > 0 {
		return time.Duration(*s) * time.Second, true
	}
	return defaultJobTimeout, false
}

// runOneHostJob runs a single one-shot to completion and reports whether
// it exited 0. Its argv is derived exactly as a host service's is
// (hostlaunch.BuildCmd: `go run <build.cmd> db migrate up` for a job whose
// args are the migrate subcommand), so a job needs no host-specific command.
func runOneHostJob(ctx context.Context, cfg *config.ProjectConfig, j WorkloadEntity, secretsLayer map[string]string, env string) error {
	if len(j.Spec.Command) == 0 && len(j.Spec.Args) == 0 {
		return fmt.Errorf("job %s: no command or args declared — a one-shot with nothing to run has nothing to run to completion", j.Name)
	}
	timeout, declared := jobDeadline(j)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, err := hostlaunch.BuildCmd(runCtx, j.Name, hostRunnerSpec(j))
	if err != nil {
		return fmt.Errorf("job %s: %w", j.Name, err)
	}
	if cmd.Dir == "" {
		cmd.Dir = projectDirForKCL()
	}
	// On expiry, kill the job's WHOLE process tree. exec.CommandContext's
	// default kills only the direct child, and under `go run` that is the go
	// tool: the compiled program is its child and would be left running —
	// a deadline in name only, still holding whatever it was holding.
	startInOwnProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = killProcessTree(cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	// Bound the wait for the output pipes once the tree is dead, so a
	// straggler holding stdout cannot turn a fired deadline back into a hang.
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// The job's output ALSO goes to its log under .forge/logs/<env>/, the
	// file the `forge env up` summary lists for it beside every other host
	// process's. Jobs used to print to the terminal only, so the summary
	// pointed at migrate.log and other job log files that never existed.
	if env != "" {
		if logFile, lerr := openJobLog(env, j.Name); lerr == nil {
			defer func() { _ = logFile.Close() }()
			cmd.Stdout = io.MultiWriter(os.Stdout, logFile)
			cmd.Stderr = io.MultiWriter(os.Stderr, logFile)
		} else {
			fmt.Printf("[up] job %s: %v (its output goes to the terminal only)\n", j.Name, lerr)
		}
	}

	// Same env composition host services get: projectConfig → secrets →
	// the job's own env_vars → os.Environ() wins last. A job that
	// provisions the thing a service dials must see the same coordinates
	// that service will.
	var projectConfigEnv map[string]string
	if cfg != nil && env != "" {
		projectConfigEnv = loadProjectConfigEnv(cfg, env)
	}
	// Same dev-run defaults host services get (development runtime,
	// AUTO_MIGRATE on a dev env). A provisioning job that dials the
	// database must see the same DSN the service will.
	dev, _ := seedTargetIsDev(env)
	projectConfigEnv = withDevRunDefaults(projectConfigEnv, dev)
	// A job's OWN KCL env_vars are the last of the three map layers, so a
	// value the environment DECLARES for this job beats project config and
	// secrets alike. That is how an env hands a job coordinates composed from a port it
	// declares once, rather than a literal baked in at generation time.
	// Secrets are scoped to what this job DECLARES, exactly as a host
	// service's are. Handing a job the whole store was the same wholesale
	// injection the dotenv provider was removed for — and it did real
	// damage here: the scaffolded store ships every declared slot present
	// and BLANK, so an untouched project injected `DATABASE_URL=""` into a
	// layer that outranks project config, and the migrate job failed with
	// `required config field database_url is not set` while both the KCL
	// and `forge env config` plainly showed the DSN.
	jobSecrets := scopeSecretsToEnvVars(secretsLayer, j.EnvVars())
	cmd.Env = hostlaunch.LayerHostEnv(os.Environ(), projectConfigEnv, jobSecrets, j.HostEnv())

	// What this job gates, for the human reading the log. The raw
	// `before` would print a bare "*", which says nothing about what is
	// actually waiting; spell the selector out instead.
	gated := strings.Join(j.Spec.Before, ", ")
	switch {
	case isBroadcast(j):
		gated = "every workload in this environment"
	case gated == "":
		gated = "nothing"
	}
	fmt.Printf("[up] job %s: running to completion (gates: %s)\n", j.Name, gated)

	start := time.Now()
	err = cmd.Run()
	elapsed := time.Since(start).Round(time.Millisecond)
	switch {
	case err == nil:
		fmt.Printf("[up] job %s: completed in %s\n", j.Name, elapsed)
		return nil
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && declared:
		return fmt.Errorf(
			"job %s exceeded its activeDeadlineSeconds (%s) and was killed\n"+
				"  it gates: %s — none of them were started\n"+
				"  the job's own spec.activeDeadlineSeconds bounds every runtime (a Kubernetes Job fails the same way); raise it if the job legitimately needs longer",
			j.Name, timeout, gated)
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return fmt.Errorf(
			"job %s did not finish within %s and was killed\n"+
				"  it gates: %s\n"+
				"  a one-shot must RUN TO COMPLETION; a host job that needs longer than this is doing a deploy's work in a dev loop\n"+
				"  (declare spec.activeDeadlineSeconds on the job to set its bound explicitly)",
			j.Name, timeout, gated)
	default:
		return fmt.Errorf(
			"job %s failed after %s: %w\n"+
				"  it gates: %s — none of them were started\n"+
				"  fix the job (or its configuration) and re-run; forge will not start a dependent against a world the job was supposed to prepare",
			j.Name, elapsed, err, gated)
	}
}

// openJobLog truncates and opens a job's log at the path the summary prints
// for it (summaryLogPath / upLogPath): one run's output per file, like a
// one-shot's — a re-run replaces it rather than appending to the last.
func openJobLog(env, name string) (*os.File, error) {
	path, err := upLogPath(env, name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G304 -- path derived from the project's own log dir
}
