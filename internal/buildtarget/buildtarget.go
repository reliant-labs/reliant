// Package buildtarget owns the per-service build dispatch for services
// whose source lives outside the project's Go module — sibling repos,
// third-party binaries, language runtimes forge doesn't natively build.
// Mirrors internal/deploytarget for the BUILD side: a service declares
// `build = forge.ShellBuild { cmd, cwd, env }` on its KCL Service (the
// SINGLE shell escape hatch), and forge runs that command via `sh -c`
// instead of the built-in Go-build pipeline. The Spec's BuildCmd/BuildCwd/
// BuildEnv fields below are the RESOLVED form of that ShellBuild.
//
// Design notes:
//
//   - Mirrors External (deploy provider) in shape — same `sh -c`
//     execution. The `cmd` is a plain KCL string that forge runs
//     VERBATIM: there is no token substitution and forge exports no
//     ${IMAGE}/${TAG}/${REGISTRY}-style variables, so every `$VAR` in the
//     command is the shell's. Anything forge knows is composed in KCL,
//     where the value already lives. The build side and deploy side
//     are ORTHOGONAL: a service can have a ShellBuild (build externally)
//     AND `deploy = K8sCluster { ... }` (deploy to in-cluster) — the
//     typical cp-forge pattern.
//
//   - A missing cwd is a HARD FAILURE, not a skip. The dispatcher
//     only constructs a Spec for services that are IN the current env, so
//     by the time a Spec reaches Runner.Build its inputs are expected to
//     exist; a missing source tree (e.g. an un-checked-out sibling repo)
//     means the build that was supposed to run can't — and reporting
//     success would let a following deploy reference an unpushed image.
//
//   - The user's command owns BOTH the build AND the push. Forge does
//     NOT run `docker push` afterwards. Matches External's "user owns
//     the command end-to-end" contract.
//
// The runner is split from the dispatcher so unit tests can inject a
// fake commandRunner without spawning a real shell — the External
// provider's testing pattern.
//
// Phase 1 landed the schema + token helper. Phase 2 (this commit)
// wires Runner.Build into internal/cli/build.go and persists per-
// service state. Phase 3 adds audit + doctor surfaces.
//
//forge:exclude-contract: the `sh -c` shell-build escape hatch the CLI dispatches for out-of-module sources; no Service/Deps/New to bind and no I/O client to fake
package buildtarget

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/envutil"
	"github.com/reliant-labs/forge/internal/shellrun"
	"github.com/reliant-labs/forge/internal/statefile"
	"github.com/reliant-labs/forge/pkg/release"
)

// Spec is the per-service build-target shape consumed by the runner.
// Mirrors deploytarget.ExternalSpec for the build side — the fields
// the user declares on KCL Service translate to this shape.
//
// Nothing here is substituted into BuildCmd. The command is whatever the
// KCL render produced, run byte-for-byte; Service/Image/Tag are carried
// only so the dispatcher can label log lines and record build state.
type Spec struct {
	// Service is the KCL Service.name, surfaced in log lines and in the
	// per-service build-state file.
	Service string

	// Image is the rendered Service.image reference, recorded in build
	// state so a later deploy can resolve the same image. The command
	// composes its own reference in KCL.
	Image string

	// Tag is the resolved image tag, recorded in build state and in the
	// build log line. Bound into the render as the `image_tag` KCL input
	// before the command string exists, so the rendered cmd already
	// carries whatever reference KCL composed from it.
	Tag string

	// BuildCmd is the shell command to exec via `sh -c`, verbatim.
	// Required — callers should NOT construct a Spec without one.
	BuildCmd string

	// BuildCwd is the working directory the command runs from.
	// Relative paths are resolved against ProjectDir. Empty means
	// "use ProjectDir directly." Missing-on-disk is a hard failure
	// (see Runner.Build).
	BuildCwd string

	// ProjectDir is the project root containing forge.yaml — the cwd
	// the command runs from when BuildCwd is empty, and the base that
	// a relative BuildCwd resolves against.
	ProjectDir string

	// BuildEnv carries the ShellBuild's declared `env` map, merged onto
	// the process environment for the command (declared keys win).
	BuildEnv map[string]string

	// RequireReceipt opts into the immutable-release builder protocol. Legacy
	// local builds do not claim receipt-backed provenance or automatic reuse.
	RequireReceipt      bool
	ExpectedInputDigest string
}

// commandRunner is the same indirection the deploytarget providers
// use — Run streams stdout/stderr, RunWithEnv layers an env overlay.
// Tests swap in a fake; production uses execRunner.
//
// Kept package-private because the surface is identical to
// deploytarget's: there's no caller outside forge that would benefit
// from a public commandRunner type, and exposing it would invite
// drift from deploytarget's shape.
type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) error
	RunWithEnv(ctx context.Context, env map[string]string, name string, args ...string) error
	// RunInDir runs the command with its working directory set to dir
	// (and an optional env overlay). Unlike a shell `cd <dir> && …`
	// prefix this never quotes the path through a shell, so a dir with
	// spaces or shell metacharacters is handled correctly.
	RunInDir(ctx context.Context, dir string, env map[string]string, name string, args ...string) error
	// RunShell runs a POSIX shell script in dir with an env overlay. The
	// production implementation interprets it in-process (internal/shellrun),
	// so no `sh` binary is needed on PATH — Windows included.
	RunShell(ctx context.Context, dir string, env map[string]string, script string) error
}

// execRunner is the production commandRunner. Run pipes through to
// the parent stdout/stderr so users see build progress in real time.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	return execRunner{}.RunWithEnv(ctx, nil, name, args...)
}

func (execRunner) RunWithEnv(ctx context.Context, env map[string]string, name string, args ...string) error {
	return execRunner{}.RunInDir(ctx, "", env, name, args...)
}

func (execRunner) RunInDir(ctx context.Context, dir string, env map[string]string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Empty dir leaves cmd.Dir unset, so the command inherits the host
	// cwd — which equals ProjectDir for forge build invocations.
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = envutil.MergeExtraWins(os.Environ(), env)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func (execRunner) RunShell(ctx context.Context, dir string, env map[string]string, script string) error {
	err := shellrun.Run(ctx, script, shellrun.Options{Dir: dir, Env: env, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		return fmt.Errorf("shell build %q: %w", script, err)
	}
	return nil
}

// Runner executes a Spec's BuildCmd as POSIX shell (in-process) VERBATIM — the string
// the KCL render produced is the string the shell sees. Mirrors
// deploytarget.ExternalProvider's shape: same `sh -c` invocation, same
// env-overlay precedence, same "user owns the command" contract.
//
// There is no token substitution. A ShellBuild `cmd` is plain KCL, so
// everything forge used to inject is composed in KCL where the value
// already lives (`forge.env()`, `file.workdir()`, the arch accessor, the
// rendered image reference, a literal), and every `$VAR` / `${VAR}` in
// the command belongs to the shell.
//
// Tests inject a fake Runner via the unexported runner field; the
// production zero-value runs commands through execRunner.
type Runner struct {
	// runner is the os/exec indirection used to invoke `sh -c`. Nil
	// falls back to execRunner. Package-private so tests in the same
	// package can swap it.
	runner commandRunner
}

// NewRunner returns a production Runner whose commandRunner is the
// real os/exec wrapper. Exposed so the forge CLI can construct one
// without poking package internals.
func NewRunner() Runner {
	return Runner{runner: execRunner{}}
}

// BuildResult is the outcome of a single Runner.Build call. The Skipped
// field is retained for callers that branch on it but is no longer set
// by Runner.Build: a missing build_cwd now sets Err (a hard failure)
// rather than skipping, because by the time a Spec reaches Build the
// service is known to be in the current env and its inputs are expected
// to exist. Kept on the struct so the dispatcher's skip-branch is inert
// (defensive) rather than removed outright.
//
// Tag carries the resolved tag the build actually produced so the
// caller can persist it to the per-service state file. Duration is
// the wall-clock time the user's command took so the build-summary
// line can show it alongside the docker timings.
type BuildResult struct {
	Service  string
	Tag      string
	Skipped  bool
	SkipMsg  string
	Duration time.Duration
	Err      error
	Receipt  *release.BuildReceipt
}

// Build runs spec.BuildCmd through `sh -c` after substituting tokens
// and merging BuildEnv onto os.Environ(). Failure semantics:
//
//   - spec.BuildCwd resolves against spec.ProjectDir when relative.
//   - If the resolved cwd is non-empty AND doesn't exist on disk,
//     return Err set (naming the missing path). This is a HARD failure,
//     not a skip: the dispatcher only builds services that are in the
//     current env, so a missing source tree means a build that was
//     supposed to run can't, and reporting success would let a deploy
//     reference an unpushed image.
//   - Any other failure (cwd Stat error other than NotExist, exec
//     error) returns Err set.
//
// The user's BuildCmd owns BOTH the build AND the push — forge does
// not run docker push afterwards. Matches External's contract.
func (r Runner) Build(ctx context.Context, spec Spec) BuildResult {
	start := time.Now()
	result := BuildResult{
		Service: spec.Service,
		Tag:     spec.Tag,
	}
	if spec.BuildCmd == "" {
		result.Err = fmt.Errorf("build_cmd is empty (dispatcher bug — Spec without BuildCmd shouldn't reach Runner.Build)")
		result.Duration = time.Since(start)
		return result
	}

	// Resolve BuildCwd: relative paths against ProjectDir, absolute
	// passes through. Empty BuildCwd means "use ProjectDir directly" —
	// resolved EXPLICITLY to spec.ProjectDir (not left empty to inherit
	// the host cwd) so a ShellBuild with no cwd always runs from the
	// project root regardless of where forge was invoked. This is the
	// single documented cwd contract for the shell hatch.
	// A user-DECLARED cwd resolves against ProjectDir and must exist on
	// disk; an UNSET cwd resolves explicitly to ProjectDir itself (always
	// present, so no existence check) so a ShellBuild with no cwd always
	// runs from the project root regardless of where forge was invoked.
	cwd, cerr := ResolveCwd(spec)
	if cerr != nil {
		result.Err = cerr
		result.Duration = time.Since(start)
		return result
	}

	runner := r.runner
	if runner == nil {
		runner = execRunner{}
	}
	// Set the working directory on the runner (cmd.Dir) rather than via
	// a shell `cd <dir> && …` prefix: the latter breaks on a cwd with
	// spaces or shell metacharacters. RunInDir with an empty dir leaves
	// cmd.Dir unset, inheriting the host cwd (== ProjectDir for forge
	// build) — matching the prior no-cwd behavior.
	buildEnv := make(map[string]string, len(spec.BuildEnv)+1)
	for k, v := range spec.BuildEnv {
		buildEnv[k] = v
	}
	// Never leak an outer build invocation's receipt destination into a child.
	buildEnv[ReceiptPathEnv] = ""
	var receiptPath string
	if spec.RequireReceipt {
		dir, err := os.MkdirTemp("", "forge-build-receipt-*")
		if err != nil {
			result.Err = err
			return result
		}
		defer func() { _ = os.RemoveAll(dir) }()
		receiptPath = filepath.Join(dir, "receipt.json")
		buildEnv[ReceiptPathEnv] = receiptPath
	}
	err := runner.RunShell(ctx, cwd, buildEnv, spec.BuildCmd)
	if err == nil && spec.RequireReceipt {
		result.Receipt, err = ReadReceipt(receiptPath, spec.ExpectedInputDigest)
	}
	result.Err = err
	result.Duration = time.Since(start)
	return result
}

// ResolveCwd returns the directory spec.BuildCmd runs in, or the error
// Runner.Build would fail with for it. It is the ONE place the cwd rule
// lives, so `forge build --plan` can report exactly the failure the real
// build would hit — before any other build in the same run has spent time
// or pushed anything — without a second copy of the rule to drift.
func ResolveCwd(spec Spec) (string, error) {
	cwd := spec.ProjectDir
	if spec.BuildCwd != "" {
		cwd = spec.BuildCwd
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(spec.ProjectDir, cwd)
		}
		// FAIL when the declared cwd is missing on disk. By the time a
		// Spec reaches Runner.Build, the dispatcher has already filtered to
		// services that are IN the current env (externalBuildServices over
		// the rendered KCL) — so a service-not-in-this-env is skipped UPSTREAM
		// by never constructing a Spec, and is never seen here. That leaves
		// exactly one meaning for a missing cwd at THIS point: the build
		// was expected to run but its required source tree (e.g. a sibling
		// repo like ../reliant) isn't checked out. Skipping-with-warn here
		// reported the overall build as SUCCESS while silently producing no
		// image — so a following `forge env deploy` referenced an unpushed tag →
		// ImagePullBackOff. Failing loudly, naming the missing path, is the
		// correct contract: the user either checks out the sibling or removes
		// the service from this env's KCL.
		if _, err := os.Stat(cwd); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("ShellBuild for service %q requires working directory %s, which does not exist on disk — check out the required source (e.g. the sibling repo) or remove the service from this env's KCL", spec.Service, cwd)
			}
			return "", fmt.Errorf("stat cwd %s: %w", cwd, err)
		}
	}
	return cwd, nil
}

// State is the per-service build-state record persisted after a
// successful Runner.Build. Mirrors internal/cli's BuildState shape on
// the project-docker side — but per-service so different external-
// build services can carry independent (image, tag) tuples.
//
// PushedAt is RFC3339 wall-clock. The state file is informational
// across forge invocations, so real time is fine.
type State struct {
	Service string `json:"service"`
	// Env is the deploy env this build belongs to. It is recorded because the
	// FILENAME cannot be parsed back into (env, service): both segments may
	// contain "-", so build-dev-k8s-gateway.json is equally readable as
	// (dev, k8s-gateway) and (dev-k8s, gateway). A reader that globs
	// "build-<env>-*.json" therefore also matches every sibling env whose name
	// extends this one, and when both declare the same image the later file
	// silently overwrites the earlier digest. Written by WriteState; verified
	// on read, so a mismatched file is skipped rather than believed.
	Env string `json:"env,omitempty"`
	// Image is the REPOSITORY, registry host included — the reference the
	// workload declared, minus any tag. There is no Registry field: the
	// registry is part of the image.
	Image    string `json:"image"`
	Tag      string `json:"tag"`
	PushedAt string `json:"pushed_at"`
	// Digest is the content-addressed manifest digest of the pushed image
	// (canonical `sha256:...`, no `@` prefix, no repo), captured best-effort
	// from the registry after the user's build_cmd builds+pushes. EMPTY when
	// the digest couldn't be resolved (local-only ref, registry unreachable,
	// or buildx/docker absent) — capture never fails the build. Mirrors
	// internal/cli.BuildState.Digest so `forge env deploy` pins the immutable
	// `<image>@<digest>` reference for external builds too, instead of
	// falling back to the mutable env tag (the stale-arch-cache footgun).
	Digest string `json:"digest,omitempty"`
	// Platforms is the OS/arch set the pushed manifest advertises, captured
	// alongside Digest. Informational; empty when the lookup failed.
	Platforms []string `json:"platforms,omitempty"`
	// Source is the git checkout the ShellBuild's command ran in — its HEAD
	// and dirty flag when the command started. Nil when the cwd is not a git
	// checkout, and on state an older forge wrote. A release cut from this
	// state checks it against the commit the project pins (see Source).
	Source *Source `json:"source,omitempty"`
}

// statePath returns the absolute path to the per-service build-state
// file. Sits under .forge/state/build-<env>-<service>.json alongside
// the project-docker state file (.forge/state/build-<env>.json). One
// file per (env, service) so concurrent external builds of the same
// service across envs don't clobber each other.
//
// projectDir is the project root (the directory holding forge.yaml).
// Empty env collapses to "default" — same convention build_state.go
// uses on the project-docker side.
func statePath(projectDir, env, service string) string {
	if env == "" {
		env = "default"
	}
	// Sanitize the env/service segments before composing the filename.
	// They're KCL-validated identifiers in practice, so for real inputs
	// SafeSegment is a no-op and existing build-<env>-<service>.json
	// files keep loading — but a separator-bearing value used to be able
	// to escape .forge/state, which is the latent path-traversal smell
	// this hoist closes (deploytarget already sanitized; this side did
	// not).
	name := "build-" + statefile.SafeSegment(env) + "-" + statefile.SafeSegment(service) + ".json"
	return statefile.Path(projectDir, name)
}

// WriteState persists a successful Runner.Build to disk. Called from
// the build dispatcher after every successful per-service build so a
// subsequent `forge env deploy <env>` can pin the same tag. Skipped
// builds DO NOT call WriteState — there's no successful tag to record.
//
// The directory is created lazily so projects that never use the
// external-build path never grow .forge/state/build-*-*.json files.
// File mode is 0o644 to match the project-docker state file.
func WriteState(projectDir, env string, state State) error {
	// Stamp the env so a later reader can tell which env this file belongs to
	// without parsing the ambiguous filename. Set here rather than at every
	// call site so no writer can forget it.
	if state.Env == "" {
		state.Env = env
		if state.Env == "" {
			state.Env = "default"
		}
	}
	return statefile.Write(statePath(projectDir, env, state.Service), "build state", state)
}

// StateBelongsTo reports whether a state loaded from build-<env>-<service>.json
// was really written for (env, service), or whether the filename was split at
// the wrong hyphen.
//
// The filename cannot answer this on its own: both segments may contain "-",
// so build-dev-k8s-gateway.json reads equally as (dev, k8s-gateway) and
// (dev-k8s, gateway). But the FILE names its own service, so the split can be
// checked against it — if a caller globbing env "dev" derived the service
// "k8s-daemon-gateway" from a file whose Service is "daemon-gateway", that
// file belongs to env "dev-k8s" and not to this deploy.
//
// This works on state written by older forge versions too, because Service has
// always been recorded. Env is checked first when present, as the direct
// answer; the Service cross-check is what covers everything already on disk.
// A file carrying neither is accepted — the caller's filename filter is all it
// ever had.
func StateBelongsTo(st *State, env, derivedService string) bool {
	if st == nil {
		return false
	}
	if st.Env != "" {
		want := env
		if want == "" {
			want = "default"
		}
		return st.Env == want
	}
	if st.Service != "" && derivedService != "" {
		return st.Service == derivedService
	}
	return true
}

// ReadState loads the per-service build-state file. Returns
// (nil, nil) when the file is missing — that's the deploy-without-
// build path (CI with a separate build job, or a fresh checkout) and
// the caller falls through to whatever default tag-resolution applies.
// Returns (nil, err) for malformed JSON or unreadable files; callers
// should not silently swallow these.
func ReadState(projectDir, env, service string) (*State, error) {
	return statefile.Read[State](statePath(projectDir, env, service), "build state")
}

// StatePath exposes the per-service state path for callers that want
// to print it (forge build's summary, forge project audit) without re-deriving
// the layout. Kept exported so the path lives in one place.
func StatePath(projectDir, env, service string) string {
	return statePath(projectDir, env, service)
}

// ListStates returns the service names that have a recorded build state for
// env, sorted. It lives here because the filename convention does: a caller
// globbing `build-<env>-*.json` itself has to re-derive the SafeSegment
// encoding, and get the ambiguity right.
//
// THE AMBIGUITY. Both segments may contain "-", so build-dev-k8s-gateway.json
// reads equally as (dev, k8s-gateway) and (dev-k8s, gateway) — a glob for one
// env therefore also matches every env whose name EXTENDS it. So each candidate
// is read and kept only when the file's own Env/Service agree
// (StateBelongsTo), which is the check that keeps a sibling env's state out.
func ListStates(projectDir, env string) ([]string, error) {
	if env == "" {
		env = "default"
	}
	prefix := "build-" + statefile.SafeSegment(env) + "-"
	matches, err := filepath.Glob(statefile.Path(projectDir, prefix+"*.json"))
	if err != nil {
		return nil, err
	}
	var services []string
	for _, path := range matches {
		base := filepath.Base(path)
		if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".json") {
			continue
		}
		service := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".json")
		st, err := statefile.Read[State](path, "build state")
		if err != nil || st == nil || !StateBelongsTo(st, env, service) {
			continue
		}
		services = append(services, service)
	}
	sort.Strings(services)
	return services, nil
}
