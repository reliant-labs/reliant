package deploytarget

// The external runtime: forge.OnExternal (kcl/workload.k).
//
// A workload bound to it is run by the AUTHOR'S tooling — Helm, Fly.io, Cloud
// Run, ECS, Argo CD, a script. forge still owns everything around the deploy:
// it builds and pushes the image (when the workload declares a build), records
// the release and the promotion in the env's ledger, and records the deploy
// attempt, all in `forge env deploy`'s own flow, exactly as for every other
// runtime. What it hands over is the last step: running the frozen digest.
//
// FOUR OPTIONAL ARGV HOOKS, NO SHELL. Each hook is an argv list exec'd
// directly, so nothing is word-split or quoted on the author's behalf and a
// value forge substitutes can never become a second argument or a second
// command. Only forge's own `$FORGE_*` names are substituted.
//
//   - status: read-only; prints one JSON object (ExternalStatus) naming the
//     digest RUNNING. The only way forge can read an external target back,
//     so it is what Observe and `forge env verify` stand on.
//   - plan:   read-only dry run; exit 0 = no change, 2 = changes, anything
//     else = error. Its output is the author's own diff, shown verbatim.
//   - deploy: ships the frozen image; exit 0 = shipped.
//   - health: run after deploy; exit 0 = healthy.
//
// NO HOOKS IS RECORD-ONLY. forge records the release and the promotion and
// runs nothing: the author's CI (or a person) deploys. That is the passive end
// of the same binding, which is what makes adoption incremental — record
// first, add `status` to prove parity, then `deploy` to let forge drive.
//
// WHY THIS IS NOT THE OLD forge.External. That target (removed with the one
// workload model, ADR 0002) ran a `sh -c` string and kept its own per-checkout
// state file: invisible to the release ledger, unobservable by construction,
// and a second workload declaration beside fw.Workload. This is a runtime
// BINDING of the one workload, it records through the ledger like every other
// runtime, and `status` makes it observable. There is no rollback hook:
// recovery is roll forward, as everywhere else.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ExternalProviderID is the external provider's registry id.
const ExternalProviderID = "external"

// The hook names. Also FORGE_HOOK's values.
const (
	HookStatus = "status"
	HookPlan   = "plan"
	HookDeploy = "deploy"
	HookHealth = "health"
)

// DefaultExternalHookTimeout is OnExternal.timeout's KCL default, repeated for
// a runtime built without a render.
const DefaultExternalHookTimeout = 900 * time.Second

// externalHookEnvNames is every variable forge sets for a hook. Held equal to
// kcl/workload.k's EXTERNAL_HOOK_ENV by TestExternalHookEnvMatchesKCL.
var externalHookEnvNames = []string{
	"FORGE_ENV", "FORGE_WORKLOAD", "FORGE_PROJECT", "FORGE_PROJECT_DIR",
	"FORGE_IMAGE", "FORGE_DIGEST", "FORGE_VERSION", "FORGE_RELEASE",
	"FORGE_PREVIOUS_DIGEST", "FORGE_HOOK",
}

// ExternalHookEnvNames returns the variables forge sets for every hook, in
// declaration order — the documented contract.
func ExternalHookEnvNames() []string { return append([]string(nil), externalHookEnvNames...) }

// ErrNoHook reports that the binding declares no hook of the asked kind. A
// caller that needs the read (verify, observe) reports the workload
// UNVERIFIED on it — never as "no diff".
var ErrNoHook = errors.New("no hook declared")

// ExternalRuntime is forge.OnExternal as rendered (kcl/render.k
// _render_runtime). An unset hook is nil.
type ExternalRuntime struct {
	Status []string `json:"status,omitempty"`
	Plan   []string `json:"plan,omitempty"`
	Deploy []string `json:"deploy,omitempty"`
	Health []string `json:"health,omitempty"`
	// Env is extra environment for every hook; FORGE_* is forge's.
	Env map[string]string `json:"env,omitempty"`
	// PassEnv hands the hooks the workload's resolved env as well.
	PassEnv bool `json:"pass_env,omitempty"`
	// WorkingDir is where hooks run, relative to the project root.
	WorkingDir string `json:"working_dir,omitempty"`
	// Timeout is the per-hook ceiling in seconds; 0 = the default.
	Timeout int `json:"timeout,omitempty"`
	// Platform is the GOARCH the image runs on (OnExternal.platform), which
	// forge builds for. "" = amd64, the KCL default.
	Platform string `json:"platform,omitempty"`
}

// ImagePlatform is the GOARCH forge builds this workload's image for.
func (r ExternalRuntime) ImagePlatform() string {
	if r.Platform != "" {
		return r.Platform
	}
	return "amd64"
}

// Hook returns the argv declared for name, nil when none is.
func (r ExternalRuntime) Hook(name string) []string {
	switch name {
	case HookStatus:
		return r.Status
	case HookPlan:
		return r.Plan
	case HookDeploy:
		return r.Deploy
	case HookHealth:
		return r.Health
	}
	return nil
}

// RecordOnly reports a binding with no hooks at all: forge records, and
// something else deploys.
func (r ExternalRuntime) RecordOnly() bool {
	return r.Status == nil && r.Plan == nil && r.Deploy == nil && r.Health == nil
}

// Validate is the Go half of the schema's checks (kcl/workload.k OnExternal),
// for a runtime that reached forge without the KCL (a hand-built entity, a
// future non-KCL declaration). The messages match the KCL ones.
func (r ExternalRuntime) Validate() error {
	var problems []string
	for _, name := range []string{HookStatus, HookPlan, HookDeploy, HookHealth} {
		argv := r.Hook(name)
		switch {
		case argv == nil:
		case len(argv) == 0:
			problems = append(problems, fmt.Sprintf("OnExternal.%s is an empty argv: drop the field (no hook), or name the program to run", name))
		case argv[0] == "":
			problems = append(problems, fmt.Sprintf("OnExternal.%s[0] is empty: the first element is the program forge execs", name))
		}
	}
	var reserved []string
	for k := range r.Env {
		if isExternalHookEnvName(k) {
			reserved = append(reserved, k)
		}
	}
	if len(reserved) > 0 {
		sort.Strings(reserved)
		problems = append(problems, fmt.Sprintf("OnExternal.env may not set %v: forge sets FORGE_* itself and its value wins", reserved))
	}
	if r.Platform != "" && r.Platform != "amd64" && r.Platform != "arm64" {
		problems = append(problems, fmt.Sprintf("OnExternal.platform must be 'amd64' or 'arm64' (the GOARCH the image runs on) — got '%s'", r.Platform))
	}
	if r.Timeout < 0 {
		problems = append(problems, fmt.Sprintf("OnExternal.timeout must be > 0 seconds — got %d", r.Timeout))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func isExternalHookEnvName(k string) bool {
	for _, n := range externalHookEnvNames {
		if n == k {
			return true
		}
	}
	return false
}

func (r ExternalRuntime) timeout() time.Duration {
	if r.Timeout > 0 {
		return time.Duration(r.Timeout) * time.Second
	}
	return DefaultExternalHookTimeout
}

// ExternalSpec is one external workload, with everything its hooks are told.
type ExternalSpec struct {
	Runtime ExternalRuntime
	// EnvName and Workload are FORGE_ENV and FORGE_WORKLOAD.
	EnvName  string
	Workload string
	// Project and ProjectDir are FORGE_PROJECT and FORGE_PROJECT_DIR; hooks
	// run in ProjectDir (joined with Runtime.WorkingDir).
	Project    string
	ProjectDir string
	// Image is the reference to run: `repo@sha256:…` when the deploy pinned
	// it (FORGE_IMAGE; FORGE_DIGEST is its digest).
	Image string
	// Version is the image's version tag (FORGE_VERSION).
	Version string
	// Release is the release the env is promoted to (FORGE_RELEASE).
	Release string
	// WorkloadEnv is the workload's resolved env, passed when
	// Runtime.PassEnv. Built by the caller, which owns secret resolution.
	WorkloadEnv map[string]string
}

// Digest is the sha256 digest Image pins, "" when it pins none.
func (s ExternalSpec) Digest() string {
	if i := strings.LastIndex(s.Image, "@"); i >= 0 {
		return s.Image[i+1:]
	}
	return ""
}

// hookEnv is the overlay a hook runs with: Runtime.Env, then (PassEnv) the
// workload's env, then forge's FORGE_* set, which wins.
func (s ExternalSpec) hookEnv(hook, previousDigest string) map[string]string {
	env := map[string]string{}
	for k, v := range s.Runtime.Env {
		env[k] = v
	}
	if s.Runtime.PassEnv {
		for k, v := range s.WorkloadEnv {
			env[k] = v
		}
	}
	for k, v := range s.forgeEnv(hook, previousDigest) {
		env[k] = v
	}
	return env
}

func (s ExternalSpec) forgeEnv(hook, previousDigest string) map[string]string {
	return map[string]string{
		"FORGE_ENV":             s.EnvName,
		"FORGE_WORKLOAD":        s.Workload,
		"FORGE_PROJECT":         s.Project,
		"FORGE_PROJECT_DIR":     s.ProjectDir,
		"FORGE_IMAGE":           s.Image,
		"FORGE_DIGEST":          s.Digest(),
		"FORGE_VERSION":         s.Version,
		"FORGE_RELEASE":         s.Release,
		"FORGE_PREVIOUS_DIGEST": previousDigest,
		"FORGE_HOOK":            hook,
	}
}

// hookToken matches `$FORGE_X` and `${FORGE_X}` in an argv element.
var hookToken = regexp.MustCompile(`\$\{(FORGE_[A-Z_]+)\}|\$(FORGE_[A-Z_]+)`)

// expandHookArgv substitutes forge's own variables into argv. A name forge
// does not set is left as written, so a literal `$FORGE_SOMETHING_ELSE` the
// author meant for their own tool survives. Each element stays one argument.
func expandHookArgv(argv []string, vars map[string]string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = hookToken.ReplaceAllStringFunc(a, func(m string) string {
			sub := hookToken.FindStringSubmatch(m)
			name := sub[1]
			if name == "" {
				name = sub[2]
			}
			if !isExternalHookEnvName(name) {
				return m
			}
			return vars[name]
		})
	}
	return out
}

// HookInvocation is one hook run.
type HookInvocation struct {
	Argv    []string
	Dir     string
	Env     map[string]string // overlaid on the process environment
	Stdout  io.Writer
	Stderr  io.Writer
	Timeout time.Duration
}

// HookRunner runs a hook. exitCode is the process's exit status; err is set
// only when it could not be run to an exit (not found, timed out, killed),
// in which case exitCode is -1. A non-zero exit alone is NOT an error here:
// plan's 2 is an answer.
type HookRunner interface {
	RunHook(ctx context.Context, inv HookInvocation) (exitCode int, err error)
}

// ExecHookRunner is the production HookRunner: os/exec, no shell.
type ExecHookRunner struct{}

// RunHook implements HookRunner.
func (ExecHookRunner) RunHook(ctx context.Context, inv HookInvocation) (int, error) {
	if inv.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, inv.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, inv.Argv[0], inv.Argv[1:]...)
	cmd.Dir = inv.Dir
	cmd.Stdout = inv.Stdout
	cmd.Stderr = inv.Stderr
	cmd.Env = hookEnviron(os.Environ(), inv.Env)
	err := cmd.Run()
	if ctx.Err() != nil {
		return -1, fmt.Errorf("%s: %w", inv.Argv[0], ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// hookEnviron is base with every key the overlay sets removed, then the
// overlay appended in sorted order. Removing rather than only appending is
// what makes the overlay authoritative: with a duplicate key a child's libc
// getenv returns the FIRST occurrence, so an inherited FORGE_IMAGE would
// otherwise win over the release's pinned one.
func hookEnviron(base []string, overlay map[string]string) []string {
	norm := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	set := make(map[string]bool, len(overlay))
	keys := make([]string, 0, len(overlay))
	for k := range overlay {
		set[norm(k)] = true
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(base)+len(keys))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if set[norm(k)] {
			continue
		}
		env = append(env, kv)
	}
	for _, k := range keys {
		env = append(env, k+"="+overlay[k])
	}
	return env
}

// ExternalHooks runs an external workload's hooks. The zero value execs them
// for real and sends their stderr to os.Stderr.
type ExternalHooks struct {
	Runner HookRunner
	// Stderr receives every hook's stderr (and deploy/health's stdout).
	Stderr io.Writer
}

func (x ExternalHooks) runner() HookRunner {
	if x.Runner != nil {
		return x.Runner
	}
	return ExecHookRunner{}
}

func (x ExternalHooks) stderr() io.Writer {
	if x.Stderr != nil {
		return x.Stderr
	}
	return os.Stderr
}

// run execs one hook and returns its exit code. ErrNoHook when undeclared.
func (x ExternalHooks) run(ctx context.Context, s ExternalSpec, hook, previous string, stdout io.Writer) (int, error) {
	argv := s.Runtime.Hook(hook)
	if argv == nil {
		return -1, fmt.Errorf("%s: %s: %w", s.Workload, hook, ErrNoHook)
	}
	vars := s.hookEnv(hook, previous)
	dir := s.ProjectDir
	if wd := s.Runtime.WorkingDir; wd != "" {
		if filepath.IsAbs(wd) {
			dir = wd
		} else {
			dir = filepath.Join(s.ProjectDir, wd)
		}
	}
	if stdout == nil {
		stdout = x.stderr()
	}
	code, err := x.runner().RunHook(ctx, HookInvocation{
		Argv:    expandHookArgv(argv, vars),
		Dir:     dir,
		Env:     vars,
		Stdout:  stdout,
		Stderr:  x.stderr(),
		Timeout: s.Runtime.timeout(),
	})
	if err != nil {
		return -1, fmt.Errorf("%s: %s hook %q: %w", s.Workload, hook, argv[0], err)
	}
	return code, nil
}

// ExternalStatus is the `status` hook's stdout document — the same four
// facts an ObservedItem carries.
type ExternalStatus struct {
	// Digest is the digest RUNNING (sha256:…). Empty = the hook could not
	// say, which a comparison treats as unknown, never as a match.
	Digest  string `json:"digest"`
	Version string `json:"version,omitempty"`
	Image   string `json:"image,omitempty"`
	// Health is healthy | degraded | absent | unknown ("" = unknown).
	Health string `json:"health,omitempty"`
}

// ObservedHealth maps the document's health onto the observe vocabulary.
// Anything unrecognised is unknown: a typo must not read as healthy.
func (st ExternalStatus) ObservedHealth() Health {
	switch strings.ToLower(strings.TrimSpace(st.Health)) {
	case "healthy":
		return HealthHealthy
	case "degraded":
		return HealthDegraded
	case "absent":
		return HealthAbsent
	}
	return HealthUnknown
}

// ParseExternalStatus decodes a status hook's stdout. It must be ONE JSON
// object; a digest, when present, must be a sha256 digest (a bare
// `repo@sha256:…` is accepted and reduced to its digest).
func ParseExternalStatus(out []byte) (ExternalStatus, error) {
	var st ExternalStatus
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(out)))
	if err := dec.Decode(&st); err != nil {
		return ExternalStatus{}, fmt.Errorf("status hook printed no JSON object on stdout (%v); want {\"digest\":\"sha256:…\",\"version\":…,\"image\":…,\"health\":…}", err)
	}
	// Decoder.More reports only on the current array or object, so a second
	// top-level value is detected by decoding once more and requiring EOF.
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return ExternalStatus{}, errors.New("status hook printed more than one JSON value on stdout; print exactly one object (send logs to stderr)")
	}
	if i := strings.LastIndex(st.Digest, "@"); i >= 0 {
		st.Digest = st.Digest[i+1:]
	}
	if st.Digest != "" && !strings.HasPrefix(st.Digest, "sha256:") {
		return ExternalStatus{}, fmt.Errorf("status hook digest %q is not a sha256 digest (want sha256:<64 hex>)", st.Digest)
	}
	return st, nil
}

// Status runs the status hook and decodes its document.
func (x ExternalHooks) Status(ctx context.Context, s ExternalSpec) (ExternalStatus, error) {
	var out bytes.Buffer
	code, err := x.run(ctx, s, HookStatus, "", &out)
	if err != nil {
		return ExternalStatus{}, err
	}
	if code != 0 {
		return ExternalStatus{}, fmt.Errorf("%s: status hook exited %d", s.Workload, code)
	}
	st, err := ParseExternalStatus(out.Bytes())
	if err != nil {
		return ExternalStatus{}, fmt.Errorf("%s: %w", s.Workload, err)
	}
	return st, nil
}

// PlanOutcome is the plan hook's verdict.
type PlanOutcome int

const (
	// PlanNoChange is the hook exiting 0.
	PlanNoChange PlanOutcome = iota
	// PlanChanges is the hook exiting 2.
	PlanChanges
)

// Plan runs the plan hook, writing its output (stdout and stderr) to out.
// Exit 0 is PlanNoChange, 2 is PlanChanges, anything else an error.
func (x ExternalHooks) Plan(ctx context.Context, s ExternalSpec, out io.Writer) (PlanOutcome, error) {
	hooks := x
	if out != nil {
		hooks.Stderr = out
	}
	code, err := hooks.run(ctx, s, HookPlan, "", out)
	if err != nil {
		return PlanNoChange, err
	}
	switch code {
	case 0:
		return PlanNoChange, nil
	case 2:
		return PlanChanges, nil
	}
	return PlanNoChange, fmt.Errorf("%s: plan hook exited %d (0 = no change, 2 = changes, anything else is an error)", s.Workload, code)
}

// Deploy runs the deploy hook. previous is FORGE_PREVIOUS_DIGEST.
func (x ExternalHooks) Deploy(ctx context.Context, s ExternalSpec, previous string) error {
	return x.mustSucceed(ctx, s, HookDeploy, previous)
}

// Health runs the health hook.
func (x ExternalHooks) Health(ctx context.Context, s ExternalSpec, previous string) error {
	return x.mustSucceed(ctx, s, HookHealth, previous)
}

func (x ExternalHooks) mustSucceed(ctx context.Context, s ExternalSpec, hook, previous string) error {
	code, err := x.run(ctx, s, hook, previous, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s: %s hook exited %d", s.Workload, hook, code)
	}
	return nil
}

// ExternalProvider deploys and observes forge.OnExternal workloads through
// their hooks.
type ExternalProvider struct {
	Hooks ExternalHooks
	// Out receives the provider's own progress lines; nil = os.Stdout.
	Out io.Writer
}

// Name implements Provider.
func (ExternalProvider) Name() string { return ExternalProviderID }

func (p ExternalProvider) out() io.Writer {
	if p.Out != nil {
		return p.Out
	}
	return os.Stdout
}

// Deploy runs each workload's deploy hook, then its health hook. A workload
// with no deploy hook is record-only: forge has recorded the release (the
// caller's ledger write) and reports that something else deploys it. When a
// status hook exists it is read first (FORGE_PREVIOUS_DIGEST) and again
// after, and a digest that did not move to the pin is reported — as a
// warning, because an asynchronous applier (a GitOps commit) converges after
// its deploy hook returns.
func (p ExternalProvider) Deploy(ctx context.Context, group ServiceGroup) error {
	out := p.out()
	for _, svc := range group.Services {
		s := svc.External
		if s == nil {
			return fmt.Errorf("external provider: service %q has no external spec", svc.Name)
		}
		if s.Runtime.Deploy == nil {
			fmt.Fprintf(out, "  %s: record-only (forge.OnExternal declares no deploy hook) — the release is recorded; your own tooling deploys %s\n",
				svc.Name, emptyAsUnpinned(s.Image))
			continue
		}
		if group.DryRun {
			fmt.Fprintf(out, "  %s: would run deploy hook %q with FORGE_IMAGE=%s\n", svc.Name, s.Runtime.Deploy, emptyAsUnpinned(s.Image))
			continue
		}
		previous := ""
		if s.Runtime.Status != nil {
			if st, err := p.Hooks.Status(ctx, *s); err == nil {
				previous = st.Digest
			} else {
				fmt.Fprintf(out, "  %s: warning: could not read what is running before the deploy: %v\n", svc.Name, err)
			}
		}
		fmt.Fprintf(out, "  %s: running deploy hook (%s)\n", svc.Name, s.Runtime.Deploy[0])
		if err := p.Hooks.Deploy(ctx, *s, previous); err != nil {
			return err
		}
		if s.Runtime.Health != nil {
			if err := p.Hooks.Health(ctx, *s, previous); err != nil {
				return err
			}
		}
		if want := s.Digest(); want != "" && s.Runtime.Status != nil {
			st, err := p.Hooks.Status(ctx, *s)
			switch {
			case err != nil:
				fmt.Fprintf(out, "  %s: warning: deployed, but the status hook could not confirm it: %v\n", svc.Name, err)
			case st.Digest != want:
				fmt.Fprintf(out, "  %s: warning: deployed, but status reports %s running, not %s (an asynchronous applier may still be converging; check with `forge env verify %s`)\n",
					svc.Name, emptyAsUnknownDigest(st.Digest), want, group.Env)
			default:
				fmt.Fprintf(out, "  %s: running %s\n", svc.Name, want)
			}
		}
	}
	return nil
}

// Observe reads each workload through its status hook. A workload without one
// is reported unknown with the reason; a group where NO workload has one
// returns ErrObservationUnsupported, because forge then has nothing to read.
func (p ExternalProvider) Observe(ctx context.Context, group ServiceGroup) (Observed, error) {
	readable := false
	for _, svc := range group.Services {
		if svc.External != nil && svc.External.Runtime.Status != nil {
			readable = true
		}
	}
	if !readable {
		return unsupported(ExternalProviderID,
			"forge.OnExternal declares no status hook, so forge cannot read what is running; add `status = [...]` printing {\"digest\": …}",
			serviceNames(group))
	}
	obs := Observed{ProviderID: ExternalProviderID, ObservedAt: time.Now().UTC()}
	var errs []error
	for _, svc := range group.Services {
		item := ObservedItem{Name: svc.Name, Health: HealthUnknown}
		switch {
		case svc.External == nil || svc.External.Runtime.Status == nil:
			item.Detail = "no status hook (forge.OnExternal.status): forge cannot read it back"
		default:
			st, err := p.Hooks.Status(ctx, *svc.External)
			if err != nil {
				item.Detail = err.Error()
				errs = append(errs, err)
				break
			}
			item.Digest = st.Digest
			item.Health = st.ObservedHealth()
			if item.Health != HealthHealthy {
				item.Detail = "status hook reported health " + strings.TrimSpace(emptyAs(st.Health, "unknown"))
			}
		}
		obs.Items = append(obs.Items, item)
	}
	return obs, errors.Join(errs...)
}

func emptyAs(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func emptyAsUnpinned(image string) string { return emptyAs(image, "(no image)") }

func emptyAsUnknownDigest(d string) string { return emptyAs(d, "an unknown digest") }
