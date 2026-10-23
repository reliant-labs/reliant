package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/goexec"

	"github.com/reliant-labs/forge/pkg/release"
)

// `forge build --plan` — the release cut, checked without cutting.
//
// WHY THIS EXISTS. control-plane's v1.7.0 cut (`forge env build prod --release
// v1.7.0 --push`, pushing to prod's GAR) failed eleven minutes in, on a `go build
// ./cmd/prod-daemon-cluster` for a package that never existed: an image-less
// infra service had been given a synthesized GoBuild. The two PRs that
// introduced that shape were green, because nothing on a PR ever asks forge
// what a release WOULD build — the build set is discovered from the env's KCL
// only when the cut runs. By then two images had been pushed.
//
// --plan asks exactly that question, through exactly the code the cut uses:
// the same render (renderBuildEntities), the same target resolution
// (resolveBuildTargetSet), the same go/docker/external/variant dispatch
// predicates, the same ShellBuild cwd rule (buildtarget.ResolveCwd), and with
// --release the same completeness gate (checkReleaseCoversEnv). It then
// preflights each step instead of running it. Nothing is compiled, built,
// pushed, generated or written, so it runs in seconds on any PR.
//
// What it cannot see: whether a step that STARTS would also FINISH — a
// compile error inside a package, a Dockerfile RUN that fails, a registry that
// refuses a push. Those are what the ordinary build and docker-build lanes
// exist for. What it guarantees is the part that no other lane covers: the
// build SET is the one the cut will run, and every member of it has what it
// needs to start.

// buildPlanStep is one step of the resolved build, in the order the real
// build would run it.
type buildPlanStep struct {
	// kind mirrors buildResult.kind where the real build has one ("service",
	// "frontend", "docker", "variant", "external") so the plan and the build
	// summary use one vocabulary.
	kind string
	name string
	// what is the literal action the real build would take.
	what string
	// pushes are the registry refs this step would write. Empty when the
	// step pushes nothing (no --push) or its pushes are the user's own
	// (a ShellBuild owns its push; its resolved tag is shown in what).
	pushes []string
	// problem is non-empty when the real build would fail this step.
	problem string
}

// buildPlanReport is the whole plan plus the release verdict.
type buildPlanReport struct {
	steps []buildPlanStep
	// releaseErr is checkReleaseCoversEnv's verdict on the artifacts this
	// build would capture. Nil when not a release or when it passes.
	releaseErr error
	// releaseArtifacts are the names the ledger would record, for display.
	releaseArtifacts []string
}

func (r buildPlanReport) problems() []buildPlanStep {
	var out []buildPlanStep
	for _, s := range r.steps {
		if s.problem != "" {
			out = append(out, s)
		}
	}
	return out
}

// planInputs are the resolved facts the planner reads. Grouped so the pure
// planner (planBuild) is testable from literals, while runBuildPlan supplies
// the real resolution.
type planInputs struct {
	cfg         *config.ProjectConfig
	entities    *KCLEntities
	targets     buildTargetSet
	opts        buildOptions
	resolvedTag string
	// projectTag is the project image's tag (buildTagFor over its pin).
	projectTag string
	projectDir string
	// goPackageName answers `go list -f {{.Name}} <pkg>` for a go-build
	// target: the package's name, or an error when it does not load. A
	// seam so tests do not shell out to the go toolchain.
	goPackageName func(ctx context.Context, cmd goBuildTarget) (string, error)
}

// runBuildPlan is `forge build --plan`: print the resolved build and fail on
// anything the real build would fail on. Called from runBuild once the target
// set is resolved and before anything is written.
func runBuildPlan(ctx context.Context, cfg *config.ProjectConfig, entities *KCLEntities, targets buildTargetSet, opts buildOptions, resolvedTag, projectTag string) error {
	report := planBuild(ctx, planInputs{
		cfg:           cfg,
		entities:      entities,
		targets:       targets,
		opts:          opts,
		resolvedTag:   resolvedTag,
		projectTag:    projectTag,
		projectDir:    projectDirForKCL(),
		goPackageName: goListPackageName,
	})
	printBuildPlan(report, opts)

	bad := report.problems()
	if len(bad) == 0 && report.releaseErr == nil {
		fmt.Printf("\n[build] Plan OK: %d step(s) resolved; every one has what it needs to run. Nothing was built or pushed.\n", len(report.steps))
		return nil
	}
	var msgs []string
	for _, s := range bad {
		msgs = append(msgs, fmt.Sprintf("%s %s: %s", s.kind, s.name, s.problem))
	}
	if report.releaseErr != nil {
		msgs = append(msgs, report.releaseErr.Error())
	}
	return fmt.Errorf("build plan: %d problem(s) the real build would fail on:\n  - %s",
		len(msgs), strings.Join(msgs, "\n  - "))
}

// planBuild resolves every step the real build would run, in its order, and
// preflights each one. Pure over its inputs apart from filesystem stats and
// the goPackageName seam.
func planBuild(ctx context.Context, in planInputs) buildPlanReport {
	var report buildPlanReport
	opts := in.opts
	releaseScoped := releaseImageTag(opts) != ""

	// 1. Go builds (buildParallel/buildSequential: goTargets first).
	for _, t := range in.targets.goTargets {
		step := buildPlanStep{kind: "service", name: t.outputName, what: "go build " + t.cmd}
		step.problem = planGoTarget(ctx, t, in.goPackageName)
		report.steps = append(report.steps, step)
	}

	// 2. Frontend production builds (`npm run build` in each frontend dir).
	for _, fe := range in.targets.frontends {
		step := buildPlanStep{kind: "frontend", name: fe.Name, what: "npm run build in " + fe.DeclaredDir()}
		if in.opts.composition != nil && in.opts.plan && kclFrontendHasSource(in.entities, fe.Name) {
			step.what = "pending source materialization from immutable Git pin (read-only plan does not fetch)"
		} else {
			step.problem = planFrontendBuild(fe.DeclaredDir())
		}
		report.steps = append(report.steps, step)
	}

	// 3. Project image + image frontends (only with --docker; same gate as
	// buildParallel's `opts.buildDocker`).
	if opts.buildDocker {
		if len(in.targets.goTargets) > 0 && !in.targets.skipProjectDocker {
			tags := imageTagSet(opts.pushPlan.repositoryFor(in.cfg.Name), in.projectTag, opts.pushPlan.push, releaseScoped)
			// A missing root Dockerfile is a SKIP in the real build
			// (dockerBuildProject), not a failure — the completeness gate
			// below is what refuses a release that lacks the image.
			step := buildPlanStep{kind: "docker", name: in.cfg.Name, what: "docker build -f Dockerfile .", pushes: tags.push}
			if !fileExists(filepath.Join(in.projectDir, "Dockerfile")) {
				step.what = "skipped: no Dockerfile at the project root"
				step.pushes = nil
			}
			report.steps = append(report.steps, step)
		}
		for _, fe := range in.targets.dockerFrontends {
			tags := imageTagSet(opts.pushPlan.repositoryFor(fe.Name), in.resolvedTag, opts.pushPlan.push, releaseScoped)
			df := filepath.Join(fe.DeclaredDir(), "Dockerfile")
			step := buildPlanStep{kind: "docker", name: fe.Name, what: "docker build -f " + df, pushes: tags.push}
			if !fileExists(df) {
				step.what = "skipped: no " + df
				step.pushes = nil
			}
			report.steps = append(report.steps, step)
		}
	}

	if in.entities != nil {
		report.steps = append(report.steps, planKCLDockerRemote(in)...)
		report.steps = append(report.steps, planBuildOnlyVariants(ctx, in)...)
		report.steps = append(report.steps, planExternalBuilds(in)...)
		// Last, as in the real build: finishReleaseArtifacts publishes the
		// hosted sites after every other build has succeeded.
		report.steps = append(report.steps, planHostedStaticSites(in)...)
	}

	if opts.release != "" {
		report.releaseArtifacts, report.releaseErr = planReleaseCoverage(in, report)
		// The release build's other preflight: a ShellBuild checkout that is
		// not at the commit the project pins, or is dirty. The same check,
		// so a plan refuses exactly what the cut would.
		if err := preflightReleaseSources(ctx, in.projectDir, in.entities, opts); err != nil {
			report.releaseErr = errors.Join(report.releaseErr, err)
		}
	}
	return report
}

// planGoTarget reports why `go build <cmd>` would fail to produce a binary,
// or "" when the package loads and is a command.
//
// A NON-main package is a failure too, and a silent one: `go build -o bin/x
// ./pkg` exits 0 having written an archive rather than an executable, and the
// image that COPYs it then crash-loops.
func planGoTarget(ctx context.Context, t goBuildTarget, goPackageName func(context.Context, goBuildTarget) (string, error)) string {
	name, err := goPackageName(ctx, t)
	if err != nil {
		return fmt.Sprintf("`go build %s` would fail: the package does not load (%v)", t.cmd, err)
	}
	if name != "main" {
		return fmt.Sprintf("`go build %s` would not produce an executable: package %q is not a main package", t.cmd, name)
	}
	return ""
}

// goListPackageName is the production goPackageName: `go list` with the
// target's own GOOS/GOARCH/tags/env, so build-constrained files resolve
// exactly as they will in the real build. It loads package metadata only —
// no compilation.
func goListPackageName(ctx context.Context, t goBuildTarget) (string, error) {
	args := []string{"list", "-f", "{{.Name}}"}
	if len(t.tags) > 0 {
		args = append(args, "-tags", strings.Join(t.tags, ","))
	}
	args = append(args, t.cmd)
	cmd := goexec.Graceful(exec.CommandContext(ctx, "go", args...))
	cmd.Env = goexec.Env("CGO_ENABLED=0")
	if t.goos != "" {
		cmd.Env = append(cmd.Env, "GOOS="+t.goos)
	}
	if t.goarch != "" {
		cmd.Env = append(cmd.Env, "GOARCH="+t.goarch)
	}
	for k, v := range t.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// planFrontendBuild reports why `npm run build` in dir would fail to start.
func planFrontendBuild(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json")) //nolint:gosec // dir is a declared frontend path
	if err != nil {
		return fmt.Sprintf("`npm run build` would fail: no package.json in %s", dir)
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return fmt.Sprintf("`npm run build` would fail: %s/package.json does not parse (%v)", dir, err)
	}
	if strings.TrimSpace(pkg.Scripts["build"]) == "" {
		return fmt.Sprintf("`npm run build` would fail: %s/package.json declares no \"build\" script", dir)
	}
	return ""
}

// planKCLDockerRemote mirrors buildKCLDockerShell: per-service DockerBuild
// images, and RemoteBuild, which the real build refuses outright.
func planKCLDockerRemote(in planInputs) []buildPlanStep {
	var out []buildPlanStep
	workloads := in.entities.Workloads
	if in.opts.composition != nil {
		units, err := compositionBuildUnits(in.entities)
		if err != nil {
			return []buildPlanStep{{problem: err.Error()}}
		}
		workloads = nil
		for _, unit := range units {
			workloads = append(workloads, unit[0])
		}
	}
	for _, svc := range workloads {
		b := svc.Build
		switch b.Type {
		case "docker":
			dockerfile := "Dockerfile"
			if b.Docker != nil && b.Docker.Dockerfile != "" {
				dockerfile = b.Docker.Dockerfile
			}
			imageName, imageTag := serviceDockerImage(svc, in.resolvedTag, in.opts)
			_, pushes := serviceDockerBuildArgs(in.cfg, imageName, dockerfile, b.Docker, in.opts, in.targets.cfgArchForDocker, imageTag)
			step := buildPlanStep{
				kind:   "docker",
				name:   svc.Name,
				what:   "docker build -f " + dockerfile + " " + serviceDockerContext(b.Docker),
				pushes: pushes,
			}
			if err := checkServiceDockerContext(in.projectDir, svc.Name, b.Docker); err != nil {
				step.problem = err.Error()
			}
			if !fileExists(resolveProjectPath(in.projectDir, dockerfile)) {
				// A skip in the real build (buildServiceDocker), not a
				// failure; the release gate catches a missing image.
				step.what = "skipped: no " + dockerfile
				step.pushes = nil
			}
			out = append(out, step)
		case "remote":
			out = append(out, buildPlanStep{kind: "remote", name: svc.Name, what: "RemoteBuild", problem: buildServiceRemote(svc.Name).err.Error()})
		}
	}
	return out
}

// planBuildOnlyVariants mirrors buildKCLBuildOnlyVariants.
func planBuildOnlyVariants(ctx context.Context, in planInputs) []buildPlanStep {
	var out []buildPlanStep
	for _, svc := range in.entities.WorkloadsOn(RuntimeBuildOnly) {
		g := svc.GoBuild()
		if g == nil || g.Cmd == "" {
			continue
		}
		buildCmd := g.Cmd
		for _, v := range svc.Runtime.BuildOnly.BuildVariants {
			t := goBuildTarget{cmd: buildCmd, outputName: svc.Name + "-" + v.Name, goos: v.GOOS, goarch: v.GOARCH, tags: v.BuildTags, env: v.EnvAtBuild}
			out = append(out, buildPlanStep{
				kind: "variant", name: svc.Name + ":" + v.Name, what: "go build " + buildCmd,
				problem: planGoTarget(ctx, t, in.goPackageName),
			})
		}
	}
	return out
}

// planExternalBuilds mirrors buildExternalServiceResults + buildExternalServices:
// the same service selection, the same tag resolution, and the same cwd rule —
// so a missing sibling checkout fails the plan with the exact error the cut
// would hit after every build before it had already run.
func planExternalBuilds(in planInputs) []buildPlanStep {
	svcs := externalBuildServices(in.entities)
	if len(svcs) == 0 {
		return nil
	}
	// The same schedule the build runs, so a conflict or an edge the build
	// would refuse (a cycle, a parent this build does not produce) fails the
	// plan.
	graph, aliases, err := externalBuildSchedule(svcs, in.opts, in.resolvedTag, in.projectDir)
	if err != nil {
		return []buildPlanStep{{kind: "external", name: svcs[0].Name, what: "ShellBuild", problem: err.Error()}}
	}
	var out []buildPlanStep
	for _, owner := range graph.order {
		spec := externalBuildSpec(owner, in.opts, in.resolvedTag, in.projectDir)
		step := buildPlanStep{kind: "external", name: spec.Service, what: fmt.Sprintf("ShellBuild (tag %s)", spec.Tag)}
		if consumers := aliases[owner.Name]; len(consumers) > 0 {
			names := []string{owner.Name}
			for _, svc := range consumers {
				names = append(names, svc.Name)
			}
			step.what += "; shared artifact for " + strings.Join(names, ", ")
		}
		for _, edge := range graph.edges[owner.Name] {
			from := "after " + edge.Workload
			if edge.Owner == "" {
				from = edge.Workload + " carried from the base"
			}
			step.what += fmt.Sprintf("; built FROM %s (%s)", from, edge.Env)
		}
		out = append(out, step)
	}
	return out
}

// planHostedStaticSites mirrors buildHostedStaticSites: each frontend bound
// to forge.OnHosted is built (`npm run build` → its public_dir), packed, and
// pushed as an OCI artifact to <registry>/static.v1/<name>. The step carries
// the refusals the real publish would hit before it starts: a build with no
// --push (a hosted site is only deployable by digest), a mock build, and a
// frontend dir that cannot `npm run build`.
func planHostedStaticSites(in planInputs) []buildPlanStep {
	var out []buildPlanStep
	for _, f := range hostedStaticSites(in.entities, in.opts) {
		publicDir := f.PublicDir
		if publicDir == "" {
			publicDir = "<public_dir>"
		}
		step := buildPlanStep{kind: "static", name: f.Name}
		if f.Source != nil && f.Source.Repo != "" {
			// A cross-repo frontend has no directory here until the real
			// build resolves its source, so there is nothing to preflight.
			step.what = fmt.Sprintf("npm run build in %s@%s → %s; push OCI static site", f.Source.Repo, f.Source.Ref, publicDir)
		} else {
			step.what = fmt.Sprintf("npm run build in %s → %s; push OCI static site", f.Path, publicDir)
			step.problem = planFrontendBuild(resolveProjectPath(in.projectDir, f.Path))
		}
		if err := checkDeployableFrontendMock(f); err != nil {
			step.problem = err.Error()
		}
		if !in.opts.pushPlan.push {
			step.problem = errHostedSiteMustPush(in.opts.env, []string{f.Name}).Error()
		} else {
			// The resolved address, so `--plan` previews the place the real
			// build would push rather than a reference the author wrote.
			step.pushes = []string{hostedStaticDestination(in.opts.pushPlan.pushBase, f.Image)}
		}
		out = append(out, step)
	}
	return out
}

// planReleaseCoverage runs the release's completeness gate against the
// artifacts this build WOULD capture: every image a planned docker step or
// external build would push, every hosted static site a planned static step
// would push, plus every source-pinned frontend (whose commit the real cut
// resolves). It is the same checkReleaseCoversEnv the cut runs after
// building — so a declared image nothing builds fails here, on the PR.
func planReleaseCoverage(in planInputs, report buildPlanReport) ([]string, error) {
	would := map[string]release.Artifact{}
	placeholder := release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared}
	for _, s := range report.steps {
		if s.problem != "" {
			continue
		}
		switch s.kind {
		case "docker":
			if len(s.pushes) > 0 {
				would[imageRepositoryOfPlanStep(in, s)] = placeholder
			}
		case "external":
			// The RESOLVED repository the ShellBuild's cmd pushed to, which is
			// what its build state records and what the coverage gate looks
			// up — see externalPushedRepository. Keying the preview by the raw
			// declaration made `--plan` disagree with the cut it exists to
			// gate on, for exactly the bare hosted image that needs it most.
			if svc := in.entities.FindWorkload(s.name); svc != nil && svc.Image != "" {
				would[externalPushedRepository(*svc, in.opts.pushPlan.pushBase)] = placeholder
			}
		case "static":
			// Keyed by the REPOSITORY the release lands in, which is what
			// buildHostedStaticSites records and what the ledger is keyed by.
			if len(s.pushes) > 0 {
				would[s.pushes[0]] = placeholder
			}
		}
	}
	for _, fe := range in.entities.Frontends {
		if fe.Source != nil && fe.Source.Repo != "" {
			would[fe.Name] = release.Artifact{Kind: release.KindGit, Mode: release.ModeSource}
		}
	}
	names := make([]string, 0, len(would))
	for n := range would {
		names = append(names, n)
	}
	sort.Strings(names)
	if err := checkReleaseCoversEnv(in.entities, would, in.opts); err != nil {
		return names, err
	}
	// The cut's other refusal (cutReleaseFromBuildState): a release that
	// captured nothing at all.
	if len(would) == 0 {
		return nil, fmt.Errorf("--release %s: this build would capture no image digest to record — a release pins immutable digests, which require "+
			"a push (forge env build %s --release %s --push pushes to each image's own registry)", in.opts.release, in.opts.env, in.opts.release)
	}
	return names, nil
}

// imageRepositoryOfPlanStep returns the REPOSITORY a docker plan step pushes
// to: the project image's, a frontend's, or a DockerBuild workload's
// (serviceDockerImage). It is a repository rather than a name because that is
// how the release ledger is keyed — the declared reference, host included — so
// the coverage gate compares like with like.
func imageRepositoryOfPlanStep(in planInputs, s buildPlanStep) string {
	if svc := in.entities.FindWorkload(s.name); svc != nil && svc.Build.Type == "docker" {
		repository, _ := serviceDockerImage(*svc, in.resolvedTag, in.opts)
		return repository
	}
	return in.opts.pushPlan.repositoryFor(s.name)
}

func printBuildPlan(r buildPlanReport, opts buildOptions) {
	fmt.Println("[build] PLAN — resolved, preflighted, and NOT executed:")
	for i, s := range r.steps {
		mark := "ok  "
		if s.problem != "" {
			mark = "FAIL"
		}
		fmt.Printf("  %2d. %s %-8s %-28s %s\n", i+1, mark, s.kind, s.name, s.what)
		for _, p := range s.pushes {
			fmt.Printf("                push %s\n", p)
		}
		if s.problem != "" {
			fmt.Printf("                %s\n", s.problem)
		}
	}
	if len(r.steps) == 0 {
		fmt.Println("  (no build steps)")
	}
	if opts.release != "" {
		fmt.Printf("[build] Release %s would record: %s\n", opts.release, strings.Join(r.releaseArtifacts, ", "))
		if r.releaseErr == nil {
			fmt.Println("[build]   Coverage: every artifact the env declares would be in the ledger.")
		}
	}
}

// resolveProjectPath resolves p against the project root when relative — the
// real build runs with the project root as its cwd.
func resolveProjectPath(projectDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projectDir, p)
}
