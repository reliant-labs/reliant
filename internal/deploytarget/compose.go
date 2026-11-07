package deploytarget

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// ComposeProvider deploys each service in a group via docker-compose
// on the local host (or a remote docker context — that's a CLI-side
// concern, not the provider's). Unlike ExternalProvider there's no
// shell-override option: the docker-compose CLI is the contract, and
// users who want to escape it should reach for External.
//
// Deploy is a vanilla `docker compose pull` + `docker compose up -d`
// against the compose file declared in KCL. Compose handles the
// container swap itself.
//
// There is no rollback: recovery is roll forward — deploy a newer tag.
type ComposeProvider struct {
	// ProjectDir is the project root used for state-file paths. Empty
	// means "current working directory".
	ProjectDir string

	// Runner is the os/exec indirection used to invoke docker compose.
	// Nil falls back to the package default.
	Runner commandRunner
}

// Name returns the provider identifier.
func (ComposeProvider) Name() string { return "compose" }

// Deploy ships every service in the group via docker compose.
//
// Services deploy in order and each readiness-gated one BLOCKS until it
// is healthy, so a group can express "postgres, then the thing that
// dials postgres" as an ordered list of ordinary services.
func (p ComposeProvider) Deploy(ctx context.Context, group ServiceGroup) error {
	runner := p.runner()
	// Probe the compose version ONCE per group rather than per service:
	// it is a property of the installed CLI, not of any one service, and
	// re-shelling for every service would add a process per deploy to
	// re-learn the same answer.
	if err := p.checkWaitSupport(ctx, runner, group); err != nil {
		return err
	}
	for _, svc := range group.Services {
		if svc.Compose == nil {
			return fmt.Errorf("compose %s: Compose spec is nil (group misrouted?)", svc.Name)
		}
		if err := p.deployOne(ctx, runner, group, svc); err != nil {
			return err
		}
	}
	return nil
}

// checkWaitSupport verifies the installed docker compose understands
// the readiness flags, but only when some service in the group actually
// asks to wait. A group that has opted out entirely should not need a
// newer compose than the one it was working with before.
func (p ComposeProvider) checkWaitSupport(ctx context.Context, runner commandRunner, group ServiceGroup) error {
	if group.DryRun {
		return nil
	}
	for _, svc := range group.Services {
		if svc.Compose != nil && composeWait(svc.Compose) {
			return ensureComposeSupportsWait(ctx, runner)
		}
	}
	return nil
}

func (p ComposeProvider) runner() commandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return defaultRunner
}

func (p ComposeProvider) projectDir() string {
	if p.ProjectDir != "" {
		return p.ProjectDir
	}
	return "."
}

// composeServiceName returns the compose-side service name. KCL lets
// the user override via Compose.service; default is the forge service
// name (matches how a K8sCluster deploy names its Deployment).
func composeServiceName(spec *ComposeSpec, svcName string) string {
	if spec.Service != "" {
		return spec.Service
	}
	return svcName
}

// composeFile returns the file path the user declared. Empty falls
// back to the docker-compose default — keeps the provider working
// when KCL leaves the field unset (which is the common case).
//
// A relative file is resolved against ProjectDirectory when the spec pins
// one: compose resolves `-f` against the PROCESS working directory, not
// against --project-directory, so passing both relative would read the
// invoking checkout's file while resolving its mounts somewhere else.
func composeFile(spec *ComposeSpec) string {
	file := spec.ComposeFile
	if file == "" {
		file = "docker-compose.yml"
	}
	if spec.ProjectDirectory != "" && !filepath.IsAbs(file) {
		file = filepath.Join(spec.ProjectDirectory, file)
	}
	return file
}

// composeEnvFile resolves env_file the same way composeFile resolves the
// file: against ProjectDirectory when the spec pins one. A home-relative
// path (`~/…`, `$HOME/…`) is left for expandHomePath.
func composeEnvFile(spec *ComposeSpec) string {
	f := spec.EnvFile
	if f == "" || spec.ProjectDirectory == "" || filepath.IsAbs(f) ||
		strings.HasPrefix(f, "~") || strings.HasPrefix(f, "$HOME") {
		return f
	}
	return filepath.Join(spec.ProjectDirectory, f)
}

// composeArgs is the `compose …` prefix that SELECTS the project — every
// subcommand forge runs (pull, up, ps) starts with it, so all of them
// resolve the same file, the same project name and the same bind-mount
// sources. Two subcommands that selected the project differently would
// inspect one config and deploy another.
func composeArgs(spec *ComposeSpec) []string {
	if spec.ProjectDirectory == "" {
		return []string{"compose", "-f", composeFile(spec)}
	}
	return []string{"compose", "--project-directory", spec.ProjectDirectory, "-f", composeFile(spec)}
}

// composeProjectDir is the directory compose runs the project from, and
// therefore the `com.docker.compose.project.working_dir` it stamps on the
// containers: ProjectDirectory when pinned, else the directory of the
// compose file (compose's own default).
func composeProjectDir(spec *ComposeSpec) string {
	if spec.ProjectDirectory != "" {
		return spec.ProjectDirectory
	}
	abs, err := filepath.Abs(composeFile(spec))
	if err != nil {
		return ""
	}
	return filepath.Dir(abs)
}

// composeWait reports whether this deploy should block on readiness.
// Unset means true: a deploy that returns before the thing it deployed
// can serve is not a deploy anyone wanted, and every caller that has
// not thought about it should get the safe behavior.
func composeWait(spec *ComposeSpec) bool {
	if spec.Wait == nil {
		return true
	}
	return *spec.Wait
}

// composeWaitMinVersion is the first docker-compose release carrying
// `--wait-timeout` (PR docker/compose#10276, shipped in v2.17.0).
// `--wait` itself is older (v2.1.1), but forge asks for the pair, and
// gating on the newer of the two keeps one version to explain.
const composeWaitMinVersion = "v2.17.0"

// composeVersionSupportsWait reports whether the reported docker
// compose version string is at least composeWaitMinVersion.
//
// Docker Desktop appends a build suffix (`2.34.0-desktop.1`), and the
// CLI prints a leading `v` in some versions and not others; both are
// normalized before comparison. An UNPARSEABLE version returns true:
// forge should not refuse to deploy because it failed to recognize a
// version string it has never seen. In that case the flag either works
// or docker itself reports the unknown flag, which is a clearer error
// than forge guessing.
func composeVersionSupportsWait(raw string) bool {
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(v, "Docker Compose version ")
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	// Trim the build suffix Docker Desktop adds (`-desktop.1`) so the
	// comparison sees a plain semver core. semver treats a `-suffix` as
	// a PRE-release, which sorts BEFORE the release — that would read
	// 2.34.0-desktop.1 as older than 2.34.0 and, at the boundary,
	// reject a version that is actually fine.
	if canonical := semver.Canonical(strings.SplitN(v, "-", 2)[0]); canonical != "" {
		v = canonical
	}
	if !semver.IsValid(v) {
		return true
	}
	return semver.Compare(v, composeWaitMinVersion) >= 0
}

// ensureComposeSupportsWait fails with an actionable message when the
// installed docker compose predates `--wait-timeout`. Silently not
// waiting would be the worst outcome: the deploy would report success
// while the ordering guarantee callers rely on had quietly evaporated.
func ensureComposeSupportsWait(ctx context.Context, runner commandRunner) error {
	out, err := runner.Output(ctx, "docker", "compose", "version", "--short")
	if err != nil {
		// Can't ask — don't block the deploy on a version probe. If
		// the flag is genuinely missing, the up command below says so.
		return nil
	}
	if composeVersionSupportsWait(string(out)) {
		return nil
	}
	return fmt.Errorf("docker compose %s does not support `up --wait` (need %s or newer); "+
		"upgrade docker compose, or set `wait = False` on the Compose deploy block to opt out of readiness gating",
		strings.TrimSpace(string(out)), composeWaitMinVersion)
}

// appendComposeWaitArgs adds the readiness flags to an `up` invocation.
//
// `--wait` blocks until every selected service reports running|healthy.
// Observed semantics, which the callers depend on:
//
//   - service WITH a passing healthcheck: returns when it goes healthy.
//   - service with NO healthcheck: returns as soon as the container is
//     running, reporting it "Healthy". It does NOT hang — so adding
//     --wait to a service that declares no probe is safe, it simply
//     degrades to today's behavior (created + running) rather than
//     becoming a readiness gate. That is why forge can pass --wait
//     unconditionally instead of first inspecting the compose file for
//     healthcheck blocks.
//   - service whose healthcheck never passes: compose gives up and
//     exits 1 with `container <name> is unhealthy`, bounded by the
//     healthcheck's own retries x interval, or sooner by --wait-timeout.
func appendComposeWaitArgs(args []string, spec *ComposeSpec) []string {
	if !composeWait(spec) {
		return args
	}
	args = append(args, "--wait")
	if spec.WaitTimeoutSeconds > 0 {
		args = append(args, "--wait-timeout", strconv.Itoa(spec.WaitTimeoutSeconds))
	}
	return args
}

// deployOne ships a single compose service via pull + up -d.
func (p ComposeProvider) deployOne(ctx context.Context, runner commandRunner, group ServiceGroup, svc ResolvedService) error {
	spec := svc.Compose
	file := composeFile(spec)
	envFile := composeEnvFile(spec)
	target := composeServiceName(spec, svc.Name)
	if spec.Shared {
		fmt.Printf("  deploying %s via compose (SHARED stack, run from %s; file %s, service %s)...\n",
			svc.Name, spec.ProjectDirectory, file, target)
	} else {
		fmt.Printf("  deploying %s via compose (file %s, service %s)...\n", svc.Name, file, target)
	}

	// 1. Pull — `docker compose pull` is a no-op when the local image
	//    is current, so it's safe to always run. Surfacing pull
	//    failures here (rather than at up time) gives a clearer error.
	pullArgs := append(composeArgs(spec), "pull", target)

	// 2. Up -d — compose decides whether to recreate based on its own
	//    diff against the running container. We don't force --force-
	//    recreate here because the typical case (image tag changed)
	//    triggers a recreate naturally and forcing it on no-op deploys
	//    is unnecessary container churn.
	//
	//    --wait (default on) makes this call BLOCK until the service is
	//    ready by its own declared healthcheck. Readiness is the
	//    mechanism; the timeout is only a ceiling. Without it the
	//    provider returned the instant the container was created, so
	//    nothing downstream — the next service in the group, a host
	//    process about to dial the database — could express "this must
	//    be usable before I start".
	upArgs := composeArgs(spec)
	if envFile != "" {
		upArgs = append(upArgs, "--env-file", envFile)
	}
	upArgs = append(upArgs, "up", "-d")
	upArgs = appendComposeWaitArgs(upArgs, spec)
	upArgs = append(upArgs, target)

	if group.DryRun {
		fmt.Printf("  [DRY-RUN] would run: docker %s\n", strings.Join(pullArgs, " "))
		fmt.Printf("  [DRY-RUN] would run: docker %s\n", strings.Join(upArgs, " "))
		return nil
	}

	// Load env_file (if declared) and merge into the docker-compose
	// process env so the compose file's `${VAR}` references resolve
	// even when the user forgets to pre-export. Passing --env-file
	// only forwards values to *containers*; the compose file itself
	// reads from the docker-compose process env. Layering both keeps
	// the two cases in sync.
	envOverlay, ferr := ComposeProcessEnv(spec, svc.Secrets)
	if ferr != nil {
		return fmt.Errorf("compose %s: env_file: %w", svc.Name, ferr)
	}

	// 0. Ownership — refuse to take over containers another checkout is
	//    running, BEFORE anything can recreate them. See
	//    checkComposeOwnership.
	if err := checkComposeOwnership(ctx, runner, envOverlay, spec, svc.Name); err != nil {
		return err
	}

	if err := runner.RunWithEnv(ctx, envOverlay, "docker", pullArgs...); err != nil {
		return fmt.Errorf("compose %s: pull: %w", svc.Name, err)
	}
	before, err := outputWithEnv(ctx, runner, envOverlay, "docker", append(composeArgs(spec), "ps", "--all", "--quiet")...)
	if err != nil {
		return fmt.Errorf("compose %s: inventory before up: %w", svc.Name, err)
	}
	upErr := runner.RunWithEnv(ctx, envOverlay, "docker", upArgs...)
	// Even an unhealthy up can have created containers. Remember them before
	// returning so env down can clean up a partially started stack.
	ownedErr := p.recordOwned(ctx, runner, envOverlay, spec, group.Env, target, strings.Fields(string(before)))
	if upErr != nil {
		err := errors.Join(upErr, ownedErr)
		if composeWait(spec) {
			// With --wait, an `up` failure most often means the
			// container came up but never became healthy. Say so —
			// "up: exit status 1" sends people looking at the wrong
			// thing (image, ports) when the answer is in the
			// service's own healthcheck and logs.
			return fmt.Errorf("compose %s: up: %w (service did not become healthy; "+
				"check its healthcheck and `docker compose -f %s logs %s`)",
				svc.Name, err, file, target)
		}
		return fmt.Errorf("compose %s: up: %w", svc.Name, err)
	}

	// 3. Health check — `compose ps --status running` returns a non-
	//    empty line for running services and empty for failed ones.
	//
	//    This runs through runOutputWithEnv, not the bare Output, because
	//    `compose ps` re-reads and re-interpolates the compose file like
	//    every other compose subcommand: without the same overlay the up
	//    used, a project whose ports come from `${VAR}` gets a warning and
	//    a different resolved config than the one it just deployed.
	psArgs := append(composeArgs(spec), "ps", "--status", "running", target)
	out, err := outputWithEnv(ctx, runner, envOverlay, "docker", psArgs...)
	if err != nil {
		return fmt.Errorf("compose %s: health check: %w", svc.Name, err)
	}
	// The header line `NAME ... STATUS ... PORTS` shows up even when
	// no services match in some docker-compose versions; we count
	// lines past the header.
	if !composeHasRunningLine(out, target) {
		return fmt.Errorf("compose %s: service not in running state", svc.Name)
	}

	if ownedErr != nil {
		return fmt.Errorf("compose %s: record ownership: %w", svc.Name, ownedErr)
	}

	// 4. Persist the deployed tag so the reconciler can read back what
	//    is deployed. If the dispatcher didn't set ImageTag we still
	//    record the service+env tuple so a deploy is visible.
	st := DeployState{Tag: group.ImageTag}
	if _, err := WriteDeployState(p.projectDir(), "compose", group.Env, svc.Name, st); err != nil {
		return fmt.Errorf("compose %s: record state: %w", svc.Name, err)
	}
	return nil
}

// mergeComposeEnv layers the KCL-declared `env` on top of the overlay
// already assembled from secrets + env_file. Returns the base untouched
// when there is nothing declared, so a project that uses none of this
// keeps exactly the process env it had before the field existed.
func mergeComposeEnv(base, declared map[string]string) map[string]string {
	if len(declared) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(declared))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range declared {
		merged[k] = v // KCL wins — it is the declaration, not a fallback
	}
	return merged
}

// composeHasRunningLine returns true when the `compose ps` output
// contains a non-header line referencing the given service. Docker
// versions differ in whether they print a header — this normalizes
// both shapes.
func composeHasRunningLine(out []byte, service string) bool {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip header rows; the docker compose ps header always starts
		// with `NAME` (or `SERVICE` in older versions). Body rows
		// reference the container name, which embeds the service.
		if strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "SERVICE") {
			continue
		}
		if strings.Contains(line, service) {
			return true
		}
	}
	return false
}
