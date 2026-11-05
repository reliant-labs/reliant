// File: internal/cli/config_check.go
//
// The boot config check: before `forge env build <env> --push` pushes the
// project image, run every workload of the env that runs that image the way
// the env runs it — its command, its args, its rendered environment — in the
// binary's config-check mode (forge/pkg/config, check.go), and refuse to push
// an image a workload would refuse to boot with.
//
// ── Why it is here ────────────────────────────────────────────────────────
//
// A forge process's first act is its typed config load, and how that load
// ends is decided by the binary and the env alone. The plan step diffs
// objects, so it cannot see it: control-plane's delivery-signer CrashLooped
// in prod twice on 2026-11-03 on exactly this — a required secret its env
// never wired (cp #1886), then a cross-field rule its env broke (cp #1890).
// Both were deterministic from the image and the render, before the push.
//
// ── What it can judge ─────────────────────────────────────────────────────
//
// Values the render states are passed as they are. A variable wired from a
// Secret, the managed secret store, a database credential, a ConfigMap, the
// Downward API or another workload's URL is passed as a PLACEHOLDER and named
// in FORGE_CONFIG_CHECK_PRESENT: it counts as set, and its value is not
// judged. A variable the render does not wire at all is absent, as in the pod
// — which is how an unmounted secret is caught without reading any secret.
// The container runs with no network, so nothing it does reaches anything.
//
// Out of scope, and NAMED in the output: workloads whose image forge does not
// build from this project, workloads that do not run it from an image (host,
// build-only, tool), and a binary built before the check mode existed.
//
// ── Rollout ───────────────────────────────────────────────────────────────
//
// A failing check gates the push from configCheckGatesFrom. A project pinned
// below it sees the failures as warnings, and the push goes on, so the
// release that ships the check does not turn anyone's release red the day
// they pin it. --config-check=gate opts in early; =warn and =off are the
// project's dial.

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/forgecompat"
	forgeconfig "github.com/reliant-labs/forge/pkg/config"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The release that ships the check, and the release it starts to gate in.
const (
	configCheckIntroducedIn = "v1.0.15"
	configCheckGatesFrom    = "v1.0.16"
)

// configCheckTimeout bounds one workload's check. A process that loads its
// config does so in milliseconds; one that has not printed a result by now
// never reached its load (a command that loads none, or blocks before it),
// and is reported as unverified. Unverified is not OK: it fails the check
// like a problem does, under the same gate, because a push the check never
// judged is exactly the push it exists to stop.
var configCheckTimeout = 30 * time.Second

// configCheckParallel is how many checks run at once.
const configCheckParallel = 4

// The --config-check values.
const (
	configCheckAuto = "auto"
	configCheckGate = "gate"
	configCheckWarn = "warn"
	configCheckOff  = "off"
)

func validateConfigCheckMode(mode string) error {
	switch mode {
	case "", configCheckAuto, configCheckGate, configCheckWarn, configCheckOff:
		return nil
	}
	return fmt.Errorf("--config-check=%q: want one of auto, gate, warn, off", mode)
}

// configCheckTarget is one workload to check: how to run it, and with what.
type configCheckTarget struct {
	Workload string
	// Binary is the host-built binary the image ships for it; its bytes say
	// whether it has the check mode.
	Binary string
	// Entrypoint overrides the image's (the workload's command[0]); Args
	// follow it, or the image's entrypoint when it is empty.
	Entrypoint string
	Args       []string
	// Env is the process environment: the render's values and placeholders,
	// plus the check-mode variables.
	Env map[string]string
}

// configCheckSkip is a workload the check does not run, and why.
type configCheckSkip struct {
	Workload, Why string
}

// configCheckOutcome is one workload's verdict.
type configCheckOutcome struct {
	Workload string
	// Result is set when the process printed one; Unverified says why it
	// did not, and fails the check as a problem does.
	Result     *forgeconfig.CheckResult
	Unverified string
}

// planConfigCheck selects the env's workloads that run the project image
// (every workload with a go build: the project Dockerfile ships every built
// binary in the one image) and resolves each one's process environment.
// Every other workload is a skip with its reason.
func planConfigCheck(e *KCLEntities, outputDir string) ([]configCheckTarget, []configCheckSkip) {
	if e == nil {
		return nil, nil
	}
	var targets []configCheckTarget
	var skips []configCheckSkip
	for _, w := range e.Workloads {
		g := w.GoBuild()
		switch {
		case g == nil:
			skips = append(skips, configCheckSkip{w.Name, "its image is not built by forge from this project"})
			continue
		case w.Kind == string(deployv1alpha1.KindTool):
			skips = append(skips, configCheckSkip{w.Name, "a tool is never scheduled"})
			continue
		}
		switch w.Runtime.Type {
		case RuntimeCluster, RuntimeHosted, RuntimeCompose:
		default:
			skips = append(skips, configCheckSkip{w.Name, fmt.Sprintf("its %s runtime does not run the image", w.Runtime.Type)})
			continue
		}
		out := g.OutputName
		if out == "" {
			out = w.Name
		}
		t := configCheckTarget{
			Workload: w.Name,
			Binary:   filepath.Join(outputDir, out),
			Args:     append([]string(nil), w.Spec.Args...),
			Env:      configCheckEnv(w.Spec),
		}
		if len(w.Spec.Command) > 0 {
			t.Entrypoint = w.Spec.Command[0]
			t.Args = append(append([]string(nil), w.Spec.Command[1:]...), t.Args...)
		}
		targets = append(targets, t)
	}
	return targets, skips
}

// configCheckEnv is the environment a workload's container is started with,
// as the check can know it: each literal as rendered, each reference as a
// placeholder named in FORGE_CONFIG_CHECK_PRESENT, and the mount paths of
// the volumes whose content only the running pod has.
func configCheckEnv(s deployv1alpha1.WorkloadSpec) map[string]string {
	env := map[string]string{forgeconfig.CheckEnv: "1"}
	var present []string
	for _, e := range s.Env {
		if e.SecretRef != nil || e.ManagedSecret != nil || e.DatabaseRef != nil ||
			e.ConfigMapRef != nil || e.FieldRef != nil || e.WorkloadURL != nil {
			env[e.Name] = forgeconfig.CheckPlaceholder
			present = append(present, e.Name)
			continue
		}
		env[e.Name] = e.Value
	}
	// What the renderer adds to the container (pkg/deploy renderLongRunning):
	// an operator is told to elect a leader unless it says otherwise.
	if _, declared := env["LEADER_ELECTION"]; s.EffectiveLeaderElection() && !declared {
		env["LEADER_ELECTION"] = "true"
	}
	var mounts []string
	for _, v := range s.Volumes {
		if v.Source.EmptyDir == nil {
			mounts = append(mounts, v.MountPath)
		}
	}
	if s.StorageGiB > 0 {
		mounts = append(mounts, "/data")
	}
	sort.Strings(present)
	sort.Strings(mounts)
	if len(present) > 0 {
		env[forgeconfig.CheckPresentEnv] = strings.Join(present, ",")
	}
	if len(mounts) > 0 {
		env[forgeconfig.CheckMountsEnv] = strings.Join(mounts, ",")
	}
	return env
}

// configCheckRunner runs one target's process in the built image and returns
// its stdout, a tail of its stderr and how it ended. A variable, so tests run
// the binary on the host instead of in docker.
var configCheckRunner = dockerConfigCheckRunner

func dockerConfigCheckRunner(ctx context.Context, image string, t configCheckTarget) (stdout, stderr string, err error) {
	args := []string{"run", "--rm", "--network", "none", "--pull", "never"}
	if t.Entrypoint != "" {
		args = append(args, "--entrypoint", t.Entrypoint)
	}
	names := make([]string, 0, len(t.Env))
	for k := range t.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	// `-e NAME` takes the value from the docker client's environment, so a
	// value never appears on a command line and may hold any byte.
	childEnv := append([]string(nil), os.Environ()...)
	for _, k := range names {
		args = append(args, "-e", k)
		childEnv = append(childEnv, k+"="+t.Env[k])
	}
	args = append(args, image)
	args = append(args, t.Args...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = childEnv
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// hostConfigCheckRunner runs the target's host-built binary directly. Tests
// use it; it is never the runner of a real build, whose binary is the
// image's.
func hostConfigCheckRunner(ctx context.Context, _ string, t configCheckTarget) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, t.Binary, t.Args...)
	cmd.Env = nil
	for k, v := range t.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// binaryHasConfigCheck reports whether the binary at path links the check
// mode: its result prefix is in every binary that does. One read per path.
func binaryHasConfigCheck(path string, cache map[string]bool) (bool, error) {
	if ok, seen := cache[path]; seen {
		return ok, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	ok := bytes.Contains(b, []byte(forgeconfig.CheckResultPrefix))
	cache[path] = ok
	return ok, nil
}

// runConfigChecks runs every target against image, configCheckParallel at a
// time, in target order. A binary without the mode is a skip.
func runConfigChecks(ctx context.Context, image string, targets []configCheckTarget) ([]configCheckOutcome, []configCheckSkip) {
	cache := map[string]bool{}
	var run []configCheckTarget
	var skips []configCheckSkip
	for _, t := range targets {
		ok, err := binaryHasConfigCheck(t.Binary, cache)
		switch {
		case err != nil:
			skips = append(skips, configCheckSkip{t.Workload, fmt.Sprintf("its binary %s could not be read: %v", t.Binary, err)})
		case !ok:
			skips = append(skips, configCheckSkip{t.Workload, fmt.Sprintf("its binary predates the check mode (it needs forge/pkg/config %s or later in go.mod)", configCheckIntroducedIn)})
		default:
			run = append(run, t)
		}
	}
	outcomes := make([]configCheckOutcome, len(run))
	sem := make(chan struct{}, configCheckParallel)
	var wg sync.WaitGroup
	for i, t := range run {
		wg.Add(1)
		go func(i int, t configCheckTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			outcomes[i] = checkOneWorkload(ctx, image, t)
		}(i, t)
	}
	wg.Wait()
	return outcomes, skips
}

func checkOneWorkload(ctx context.Context, image string, t configCheckTarget) configCheckOutcome {
	cctx, cancel := context.WithTimeout(ctx, configCheckTimeout)
	defer cancel()
	stdout, stderr, err := configCheckRunner(cctx, image, t)
	if r, ok := forgeconfig.ParseCheckResult(stdout); ok {
		return configCheckOutcome{Workload: t.Workload, Result: &r}
	}
	// A clean exit with no result is a command that reads no typed config:
	// say how such a command joins the check (a `db migrate` scaffolded
	// before it did is the common case).
	why := "it exited without loading its config; a command that reads its settings from environment " +
		"variables answers the check by calling config.CheckVariables (forge/pkg/config) with them first — " +
		"the scaffolded `db migrate` does it in checkMigratorConfig (cmd/<binary>/cmd/db.go)"
	switch {
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		why = fmt.Sprintf("it printed no result within %s (a command that loads no config, or blocks before it)", configCheckTimeout)
	case err != nil:
		why = fmt.Sprintf("it ended (%v) before loading its config", err)
	}
	if tail := lastLines(stderr, 3); tail != "" {
		why += ": " + tail
	}
	return configCheckOutcome{Workload: t.Workload, Unverified: why}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}

// configCheckGates decides whether a failing check refuses the push: the
// flag when it says gate or warn, else the rollout against the project's
// pin. A pin that is absent, the sentinel, not a version, or forge's own
// tree gates — nothing says the project is behind.
func configCheckGates(cfg *config.ProjectConfig, mode string) bool {
	switch mode {
	case configCheckGate:
		return true
	case configCheckWarn:
		return false
	}
	if cfg == nil || cfg.ModulePath == forgecompat.ModulePath {
		return true
	}
	pin := strings.TrimSpace(cfg.ForgeVersion)
	if pin != "" && !strings.HasPrefix(pin, "v") {
		pin = "v" + pin
	}
	if !semver.IsValid(pin) || pin == "v0.0.0" {
		return true
	}
	return semver.Compare(pin, configCheckGatesFrom) >= 0
}

// projectConfigCheck is the check a project image build runs before it
// pushes; nil runs nothing.
type projectConfigCheck struct {
	cfg      *config.ProjectConfig
	entities *KCLEntities
	env      string
	mode     string
	out      io.Writer
	// outputDir is where the host-built binaries the image ships are.
	outputDir string
}

// newProjectConfigCheck is the check for a build: one that pushes, of an
// env, with the check not turned off.
func newProjectConfigCheck(cfg *config.ProjectConfig, entities *KCLEntities, opts buildOptions) *projectConfigCheck {
	if entities == nil || opts.env == "" || opts.configCheck == configCheckOff || !(opts.push || opts.release != "") {
		return nil
	}
	return &projectConfigCheck{cfg: cfg, entities: entities, env: opts.env, mode: opts.configCheck, out: os.Stdout, outputDir: opts.outputDir}
}

// run checks every workload of the image just built as image, prints each
// verdict and every skip, and returns an error when a gated check fails.
func (c *projectConfigCheck) run(ctx context.Context, image string) error {
	if c == nil {
		return nil
	}
	targets, skips := planConfigCheck(c.entities, c.outputDir)
	if len(targets) == 0 {
		return nil
	}
	fmt.Fprintf(c.out, "[build] config check: running %d workload(s) of %s with %s's config, no network\n", len(targets), image, c.env)
	outcomes, more := runConfigChecks(ctx, image, targets)
	skips = append(skips, more...)

	var failed []string
	for _, o := range outcomes {
		switch {
		case o.Result == nil:
			failed = append(failed, o.Workload)
			fmt.Fprintf(c.out, "[build]   ? %s: unverified: %s\n", o.Workload, o.Unverified)
		case o.Result.OK:
			fmt.Fprintf(c.out, "[build]   ✓ %s\n", o.Workload)
			for _, n := range o.Result.Notes {
				fmt.Fprintf(c.out, "[build]       note: %s\n", n)
			}
		default:
			failed = append(failed, o.Workload)
			fmt.Fprintf(c.out, "[build]   ✗ %s would refuse to start:\n", o.Workload)
			for _, p := range o.Result.Problems {
				fmt.Fprintf(c.out, "[build]       %s\n", p)
			}
		}
	}
	for _, s := range skips {
		fmt.Fprintf(c.out, "[build]   - %s: not checked: %s\n", s.Workload, s.Why)
	}
	if len(failed) == 0 {
		return nil
	}
	if !configCheckGates(c.cfg, c.mode) {
		pin := "no pin"
		if c.cfg != nil && c.cfg.ForgeVersion != "" {
			pin = c.cfg.ForgeVersion
		}
		fmt.Fprintf(c.out, "[build]   warning: the config check is new in %s and gates from %s (this project pins %s); pushing anyway. --config-check=gate refuses the push now.\n",
			configCheckIntroducedIn, configCheckGatesFrom, pin)
		return nil
	}
	return fmt.Errorf("config check: %s would refuse to start, or could not be verified, with %s's config, so nothing was pushed; fix the env's KCL (or the config it breaks), or pass --config-check=warn to push anyway",
		strings.Join(failed, ", "), c.env)
}
