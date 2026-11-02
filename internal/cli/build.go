package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/hostlaunch"

	"github.com/reliant-labs/forge/pkg/release"
)

// sortedKeys returns map keys in deterministic order. Used so docker
// build args are stable across runs (relevant for layer caching).
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// resolveBuildContext normalises a forge.yaml `docker.build_contexts`
// value into the exact string `docker buildx --build-context name=…`
// expects.
//
// Scheme-bearing values (anything containing `://`, e.g.
// `docker-image://my-base:latest`, `oci-layout://./layout`,
// `https://example.com/foo.tar`) are passed through verbatim — buildkit
// owns the interpretation and forge has no business rewriting it.
//
// Absolute paths are also passed through unchanged.
//
// Relative paths are resolved against the project root (the directory
// holding forge.yaml — the build commands run with cwd at the project
// root, so an empty projectRoot is treated as "."). Resolving to an
// absolute path means downstream `docker build` invocations work
// regardless of which subdirectory the user actually launched forge
// from and survives the working-directory churn inside test harnesses.
func resolveBuildContext(value, projectRoot string) string {
	if strings.Contains(value, "://") {
		return value
	}
	if filepath.IsAbs(value) {
		return value
	}
	if projectRoot == "" {
		projectRoot = "."
	}
	return filepath.Join(projectRoot, value)
}

// appendBuildContexts extends dockerArgs with one `--build-context
// name=value` pair per entry in cfg.Docker.BuildContexts, in a
// deterministic order. Each value is run through resolveBuildContext so
// relative paths land as absolute paths against projectRoot and
// scheme-bearing values pass through unchanged. A per-context log line
// is emitted so users can confirm what buildx will see — useful when
// debugging a Dockerfile that fails to find a `COPY --from=name`.
//
// No-ops when cfg.Docker.BuildContexts is empty, so existing projects
// see no change in behaviour or output.
func appendBuildContexts(dockerArgs []string, cfg *config.ProjectConfig, projectRoot string) []string {
	return appendNamedBuildContexts(dockerArgs, cfg.Docker.BuildContexts, projectRoot)
}

// appendNamedBuildContexts is the underlying append for an explicit
// name→value build-context map (one `--build-context name=value` per entry, in
// deterministic order, each value resolved against projectRoot). It backs both
// the project-level appendBuildContexts and the per-service DockerBuild
// build_contexts, so the two share identical resolution + logging.
func appendNamedBuildContexts(dockerArgs []string, contexts map[string]config.BuildContext, projectRoot string) []string {
	for _, name := range sortedKeys(contexts) {
		c := contexts[name]
		value := resolveBuildContext(c.Path, projectRoot)
		if c.Source != nil {
			value = c.SourceDir
		}
		dockerArgs = append(dockerArgs, "--build-context", name+"="+value)
		fmt.Printf("[build] docker build-context %s=%s\n", name, value)
	}
	return dockerArgs
}

// buildOptions holds the flag values for the build command.
type buildOptions struct {
	outputDir   string
	buildTarget string
	composition *compositionRun
	planJSON    bool
	parallel    bool
	// parallelSet records whether the user explicitly passed
	// --parallel/--no-parallel. When false, forge is free to auto-select
	// sequential under a constrained memory budget (see
	// resolveBuildConcurrency); an explicit flag always wins.
	parallelSet bool
	buildDocker bool
	debug       bool
	// push is --push: push each built image to the reference its own workload
	// declares. Implies --docker. It carries no value — every destination is
	// resolved from the declarations (resolvePushPlan), never passed.
	push bool
	// pushIfDeclared is `forge env up`'s push mode: push to the workloads'
	// declared references when the env has any pushable image, and build
	// locally (no error) when it has none — a host-only env has no cluster to
	// pull from. Unlike push, having none is not a failure. Resolved by
	// resolvePushPlan like push; never set by a flag.
	pushIfDeclared bool
	// capacityChecked records that the caller (`forge env deploy`) already ran
	// the hosted capacity pre-flight, so the build does not repeat it.
	capacityChecked bool
	// allowPassedMigrations and allowUnmergedMigrations are the
	// migration-order guard's waivers on a release cut
	// (release_migrations.go).
	allowPassedMigrations   bool
	allowUnmergedMigrations bool
	// bundleEnvs is --bundle-envs: write a bundle for each env named,
	// comma-separated, from this one build and this one cut. Empty means
	// the env being built.
	//
	// A release is env-agnostic and a bundle is not, so a pipeline that
	// cuts one release and ships it to three envs would otherwise have to
	// re-render each env later, at deploy time, from a checkout that may
	// have moved. Naming them here binds every env's manifests to the same
	// release and the same source.
	bundleEnvs string
	// gateJSON is a FILE path: write this build's result as a gate
	// document, for `forge gate record` / `forge env deploy --gate`. Not
	// a stdout mode — the build log and the exit code are unchanged.
	gateJSON string
	// pushPlan is the RESOLVED set of destinations, written by
	// resolvePushPlan from the env's workload declarations when push or
	// pushIfDeclared is on. Never set by a caller: any value is overwritten.
	// It is also consulted when the build pushes NOTHING, because a local
	// image is still TAGGED under its declared reference — that is what makes
	// a later `--push` of the same build a retag rather than a rebuild.
	pushPlan pushPlan
	// targetArch overrides the GOARCH used for the Go binary build
	// AND the docker buildx --platform when --docker / --push is set.
	// Empty means "use host arch for plain go build; use forge.yaml
	// deploy.target_arch (default amd64) for docker builds". See
	// resolveBuildArch.
	targetArch string
	// env, when set, scopes the build to a specific deploy environment.
	// Reads `deploy/kcl/<env>/` to determine which services run as host
	// processes (deploy: "host" in the rendered KCL) and excludes them
	// from the docker build/push. The Go binary itself still compiles
	// every service — the host/cluster split is a runtime placement
	// decision, not a code one. Empty (the default) means "build
	// everything", preserving the pre-orchestration behaviour so CI
	// builds for staging/prod aren't affected.
	env string
	// skipFrontends, when true, drops every frontend from the build set
	// regardless of deploy type. Set by `forge env up`'s build phase because
	// up dev-serves frontends via `npm run dev` (in upFrontends) and
	// never consumes the `npm run build` prod artifact. Saves the entire
	// Next.js prod build time on every `forge env up` cycle. Direct
	// `forge build` callers leave this false to preserve prod-build
	// behaviour. Independent of the Frontend.runtime filter
	// (filterFrontendsForBuild), which skips the dev-served and hosted
	// frontends of an env.
	skipFrontends bool
	// targets, when non-empty, scopes the build to the named applications
	// (service / operator / frontend names), exactly as `forge env deploy
	// --target` scopes a deploy. The rendered entity set is narrowed to
	// those names before the build set is derived, so every downstream
	// decision follows from it: the go-build targets, the KCL
	// docker/shell dispatch, the build-only variants, the external
	// build_cmd dispatch, and the skipProjectDocker guard (which reads
	// "does this env still declare a cluster service" off the NARROWED
	// set). Naming only host-mode/frontend apps therefore builds and
	// pushes no image at all. Requires env — without a render there is no
	// entity set to filter. Empty means "build everything", the default.
	targets []string

	// renderOptions are raw `-D name=value` values pushed into the env's KCL
	// render, exactly as `forge env up` does.
	//
	// Without these, an option a project declares is unreachable from the
	// build lane: `forge env up` could set it but builds EVERY workload, and
	// `forge build -t <name>` could scope to one but had no way to say which
	// variant. So a project with a per-build KCL option had to choose between
	// the right scope and the right value. Both flags belong on the command
	// that builds one thing.
	renderOptions []string
	// tag, when set, overrides the git-derived image tag computed by
	// resolveImageTag. CI pipelines that pin the image to a release
	// number (e.g. `--tag v1.2.3`) use this. Empty (the default) means
	// "compute from git" — the same resolution `forge env deploy` falls
	// back to when no build-state file is present.
	tag string
	// skipGenerate disables the pre-build "ensure generated code" step
	// (--no-generate). The default (false) auto-runs `forge generate`
	// when gen/ is missing or proto is newer than the generated tree, so
	// a fresh checkout doesn't fail with the go.work "cannot load module
	// gen" error. Set it when the generated tree is known-good and the
	// caller wants to skip the staleness scan (e.g. a CI lane that runs
	// generate as its own step). See ensureGeneratedCode.
	skipGenerate bool
	// release, when set, is the human-readable version label (semver,
	// "v1.4.0") for a build-once → promote release. After the build's
	// per-image digests are captured (the existing digest-capture flow),
	// runBuild harvests them into a Release ledger at
	// .forge/releases/<release>.json. `forge env deploy <env> <release>`
	// then binds an env to it and `forge env deploy <env>` pins the SAME
	// digests — build once, promote, no per-env rebuild. Implies --push
	// in spirit (a release pins registry digests), but is enforced softly:
	// a --release build with no captured digest fails loudly rather than
	// writing an empty ledger. Empty (the default) is today's behavior.
	release string
	// plan (--plan) resolves the exact build set a real invocation with the
	// same arguments would build — the same render, the same --target
	// narrowing, the same go/docker/external/variant dispatch — then
	// PREFLIGHTS every step instead of running it, and exits non-zero on
	// anything the real build would fail on. Nothing is built, pushed,
	// generated, or written. With --release it also runs the release's
	// completeness gate against the artifacts the build WOULD capture. See
	// runBuildPlan.
	plan bool
	// run is the CI run identity recorded on the release this build cuts
	// (--run-id / --run-url / --no-run). Zero means "default from the CI
	// environment", which is what makes the run id a usable join key
	// without every workflow threading a flag through every step. See
	// run_identity.go.
	run runOptions
}

func newBuildCmd() *cobra.Command {
	var opts buildOptions

	cmd := &cobra.Command{
		Use:   "build [environment]",
		Short: "Compile the project's binaries and frontends locally (never publishes)",
		Args:  cobra.MaximumNArgs(1),
		Long: `Compile the project's services and frontends. A LOCAL CHECK.

This command never publishes anything. Pushing images and recording a
release are environment acts — a push destination is declared per workload
in an env's render, and a release's artifact set is discovered from it — so
they live on ` + "`forge env build <env>`" + `:

  forge env build prod --push             # build + push to declared registries
  forge env build prod --release v1.4.0   # build + push + record the release

which is also why THIS command's environment argument is optional: it scopes
docker builds and tag resolution, and nothing here needs an env to compile.

This command is a PURE EXECUTOR of the per-service, per-env build
declaration in KCL. With an environment argument it iterates the
services the rendered env declares and dispatches on each service's
build.type:
- go     → go build the declared cmd (CGO_ENABLED=0, stripped) — no
           hardcoded ./cmd; the target package comes from KCL
- docker → docker build the service's image (dockerfile/platform/...)
- shell  → run the verbatim build command

It also builds Next.js frontends (npm run build) and, with --docker,
the shared project image. Output binaries land in the output dir.

Examples:
  forge build                                # Compile everything
  forge build staging                        # Scope docker builds/tag resolution to deploy/kcl/staging/
  forge build -t web                         # Build only the "web" frontend
  forge build -o bin                         # Output binaries to bin/
  forge build --docker                       # Also build Docker images (locally; never pushed)
  forge build --debug                        # Build with debug symbols for Delve

When a declared reference is a k3d-local localhost:<port>, the image is
also tagged registry.localhost:<port>/<path> (LOCAL alias only — the host
can't DNS-resolve registry.localhost, so it isn't pushed; the containerd
mirror config inside k3d resolves that reference at pull time).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := validateBuildEnvArg(args[0]); err != nil {
					return err
				}
				opts.env = args[0]
			}
			// COMPILE-ONLY. Publishing moved to `forge env build <env>`,
			// where the environment is the subject rather than an optional
			// modifier. Refused here before any work, naming the
			// replacement command. See refuseBuildPublishFlag in
			// env_build.go.
			if cmd.Flags().Changed("push") {
				return refuseBuildPublishFlag("push", opts.env)
			}
			if cmd.Flags().Changed("release") {
				return refuseBuildPublishFlag("release", opts.env)
			}
			if _, err := requireFeature(config.FeatureBuild); err != nil {
				return err
			}
			// Did the user pin concurrency explicitly? If so it's honoured
			// verbatim; otherwise a constrained memory budget may serialize.
			opts.parallelSet = cmd.Flags().Changed("parallel")
			return runBuild(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.outputDir, "output", "o", "bin", "Output directory for binaries")
	cmd.Flags().StringVarP(&opts.buildTarget, "target", "t", "all", "Build target (all | external | a specific service/frontend name). `external` builds only the KCL services declaring build_cmd; requires the environment argument.")
	cmd.Flags().StringArrayVarP(&opts.renderOptions, "option", "D", nil, "Set a render option the env's KCL declares, as name=value (repeatable). Relayed to KCL verbatim — forge does not interpret the value. Requires the environment argument. List an env's options with 'forge env options <env>'.")
	cmd.Flags().BoolVar(&opts.parallel, "parallel", true, "Build services in parallel")
	cmd.Flags().BoolVar(&opts.buildDocker, "docker", false, "Build Docker images for all services")
	cmd.Flags().BoolVar(&opts.debug, "debug", false, "Build with debug symbols for Delve")
	// --push and --release are REGISTERED but REFUSED here, on purpose.
	// They moved to `forge env build <env>`, and the two surfaces fail very
	// differently: an unregistered flag gets cobra's bare "unknown flag:
	// --push", which tells a user with a working command in their shell
	// history nothing about where it went. Registered, the RunE answers with
	// the exact replacement. Hidden so they do not advertise themselves in
	// --help as things this command can do.
	cmd.Flags().BoolVar(&opts.push, "push", false, "Moved to 'forge env build <env> --push' — a push destination is declared per workload in an env's render, so publishing is an environment act")
	_ = cmd.Flags().MarkHidden("push")
	cmd.Flags().StringVar(&opts.targetArch, "target-arch", "", "Override target GOARCH for cross-compilation (default: forge.yaml deploy.target_arch, then amd64 for docker builds)")
	cmd.Flags().StringVar(&opts.tag, "tag", "", "Override the image tag of every image this build writes (default: the tag a workload's image pins, else the env's image_tag, else git describe --tags --always --dirty). Refused when it differs from the tag a selected workload's image pins — the deploy pulls the pin. Recorded in .forge/state so forge env deploy uses the same value.")
	// No backticks in a usage string: cobra reads the first backticked span
	// as the flag's argument-name placeholder, so a quoted command rendered
	// as `--no-generate forge build` in --help.
	cmd.Flags().BoolVar(&opts.skipGenerate, "no-generate", false, "Skip the pre-build code-generation check. By default forge build runs forge generate when gen/ is missing or proto sources are newer than the generated tree.")
	cmd.Flags().BoolVar(&opts.plan, "plan", false, "Resolve the exact build set this invocation would build (same KCL discovery, same --target narrowing) and PREFLIGHT every step without running it: each go-build package exists and is a main package, each Dockerfile and frontend build script exists, each ShellBuild cwd exists, and with --release the ledger would cover everything the env declares. Builds, pushes, generates and writes nothing; exits non-zero on anything the real build would fail on. Pass it the release cut's exact arguments to gate a PR on the cut.")
	cmd.Flags().StringVar(&opts.gateJSON, "gate-json", "", "Also write this build's result to `FILE` as a gate document, for `forge gate record` or `forge env deploy --gate`. A FILE, not a stdout mode: the build log and the exit code are unchanged.")
	cmd.Flags().StringVar(&opts.release, "release", "", "Moved to 'forge env build <env> --release <version>' — a release's artifact set is discovered from the env's render, so cutting one is an environment act")
	_ = cmd.Flags().MarkHidden("release")
	cmd.AddCommand(newBuildReceiptCmd())

	return cmd
}

// releaseImageTag is the ONE tag a `--release` build writes to a registry: the
// release version itself. Empty for an ordinary build.
//
// A release build must never write a SHARED, MUTABLE tag. Before this, a cut
// tagged each image with the env's manifest tag (prod's `stable`), with every
// service's own `image_tag` pin, and with `:latest` — and it pushed each image
// the moment that image finished, long before the release ledger was written.
// So a cut that failed after its first push (v1.7.0, run 36264878946) left
// prod GAR's `workspace-base:stable` and `reliant:stable` pointing at bytes no
// release contains. Nothing broke only because every prod workload is pinned
// by ledger digest; any render without those digests — a plain `kcl run`, a
// ledger-less deploy — would have pulled unreleased code.
//
// `:<version>` is safe to push early precisely because it is release-scoped:
// nothing references it until the ledger that records its digest exists, and
// the ledger (not the tag) is what every promote and deploy reads.
func releaseImageTag(opts buildOptions) string {
	return releaseVersionTag(opts.release)
}

// validateReleaseFlags enforces that `forge env build <env> --release <ver>` is
// run with an environment argument. A release must pin the FULL image set,
// including the per-env external build_cmd images (e.g. reliant,
// workspace-base) that exist ONLY in deploy/kcl/<env>/main.k. Without an
// env, forge has no rendered KCL to discover them, so it silently builds
// just the project's own images (control-plane + frontends) — an incomplete
// release — and the build can fail outright for want of env context. The
// images themselves stay env-agnostic; the env only supplies the SET to
// build, after which the release is promotable to every env. No-op when
// --release is unset.
func validateReleaseFlags(opts buildOptions) error {
	if opts.release == "" {
		return nil
	}
	// --release owns the tag: it is the release version, by construction
	// (releaseImageTag). A different --tag would either be silently ignored
	// or — worse — push the release's bytes under a second, arbitrary tag.
	if opts.tag != "" && opts.tag != opts.release {
		return fmt.Errorf("--tag %q conflicts with --release %q: a release build pushes its images under the release version only, "+
			"so that a cut that fails part-way never moves a shared tag. Drop --tag", opts.tag, opts.release)
	}
	if opts.env != "" {
		return nil
	}
	return fmt.Errorf("--release requires an environment argument (`forge env build <env> --release <ver>`) so forge can build the full image set " +
		"(including per-env external build_cmd images like reliant/workspace-base, which are declared " +
		"in deploy/kcl/<env>/main.k); the images are still env-agnostic — pick any env that declares " +
		"them, then deploy the release to all envs with `forge env deploy <env> <version>`")
}

// resolveBuildArch chooses the GOARCH for `go build`. The arg-shaped
// signature decouples the three knobs that compose the answer:
//
//   - cfgArch: forge.yaml deploy.target_arch (project-level pin)
//   - flagArch: --target-arch (per-invocation override)
//   - dockerCtx: whether the caller is building a docker image (in
//     which case we always cross-compile to the deploy-target arch
//     since the image is destined for a k8s node, not the dev host)
//
// Returns the empty string when no cross-compile is needed (i.e.
// the resolved target equals runtime.GOARCH). The empty return is
// the signal buildGoBinary uses to skip the GOOS/GOARCH/CGO_ENABLED
// env override.
//
// Rule of thumb: a plain `forge build` (no docker) defaults to host
// arch — the user wants a runnable local binary. `forge build
// --docker` (or --push) defaults to forge.yaml deploy.target_arch
// (or "amd64" when unset) since the resulting image will be pulled
// by kubelet on a node whose arch is fixed at cluster-build time.
func resolveBuildArch(cfgArch, flagArch string, dockerCtx bool) string {
	target := flagArch
	if target == "" && dockerCtx {
		target = cfgArch
		if target == "" {
			target = "amd64"
		}
	}
	if target == "" || target == runtime.GOARCH {
		return ""
	}
	return target
}

// resolveBuildArchForImage resolves the GOARCH for a host go build whose
// binary will be COPYed into the PROJECT image (the COPY-pattern Dockerfile,
// no in-image `RUN go build`). Unlike resolveBuildArch it NEVER returns "" —
// the caller always pairs the returned arch with GOOS=linux, so the produced
// binary is a Linux ELF for the image's platform even when that arch equals
// the host's (e.g. an arm64 image built on an arm64 Mac: GOOS=linux GOARCH=arm64,
// NOT a native darwin/arm64 build). Precedence: --target-arch flag, then the
// per-env deploy platform (cfgArch), then the HOST arch — matching the
// ClusterTarget.platform schema's documented "unset => host arch" contract
// (local k3d nodes share the host arch, so an undeclared platform should track
// the host, not silently cross-build amd64).
func resolveBuildArchForImage(cfgArch, flagArch string) string {
	if flagArch != "" {
		return flagArch
	}
	if cfgArch != "" {
		return cfgArch
	}
	return hostArch
}

// hostArch is the build machine's GOARCH; a variable so tests can state which
// host they model instead of depending on the machine they run on.
var hostArch = runtime.GOARCH

type buildResult struct {
	name     string
	kind     string // "service", "frontend", or "docker"
	duration time.Duration
	err      error
	// digest is the content-addressed manifest digest (`sha256:...`) of a
	// pushed docker image, captured after `docker push` so runBuild can
	// record it in the build state. Empty for
	// non-docker results, non-pushed builds, and any build where the digest
	// lookup failed (capture is best-effort). platforms is the arch set the
	// pushed manifest advertises, captured alongside.
	digest    string
	platforms []string
	// image is the artifact NAME this result built (`internal-console`), as
	// opposed to name, which carries the " (docker)" display suffix. It keys
	// the per-image build-state FILE, so a per-image build state can only be
	// written for results that carry it — and it must stay a plain name, since
	// a repository's slashes would escape the state directory.
	// Set by the frontend docker path; the project image derives its own name
	// from cfg.Name.
	image string
	// repository is the full destination this result was tagged and pushed to
	// (`ghcr.io/acme/internal-console`) — the workload's declared reference
	// minus its tag. It is what the build state and the release ledger RECORD,
	// and the key the KCL `_image_ref` seam looks up in image_digests. Empty
	// when the artifact has no declared repository, in which case the writer
	// falls back to resolving `image` through the push plan.
	repository string
	// tag is the tag this result's image was BUILT and pushed as — the one
	// its build state records. Carried on the result rather than recomputed
	// by the state writer, so the two cannot disagree. Empty for non-image
	// results.
	tag      string
	receipt  *release.BuildReceipt
	workload string // runtime consumer that requested this result; never inferred from repository
	// goTargets are the exact recipes compiled into this invocation's project image.
	goTargets []goBuildTarget
}

// prepareBuild runs the three setup steps every build needs before it can
// reason about targets: load the project config, bind the render options, and
// make sure generated code is fresh.
//
// Extracted from runBuild purely to keep that function under the funlen limit.
// It is a prologue, not an abstraction — the steps are ordered and each one's
// failure aborts the build.
func prepareBuild(opts buildOptions) (*config.ProjectConfig, error) {
	store, err := loadProjectStore()
	if err != nil {
		return nil, err
	}

	if err := bindBuildRenderOptions(opts); err != nil {
		return nil, err
	}

	// Ensure generated code exists / is fresh before any `go build`.
	// Missing gen/ (gitignored, or freshly cleaned) otherwise fails with
	// the cryptic "cannot load module gen listed in go.work" error. Gated
	// on staleness so the steady-state loop pays nothing; --no-generate
	// opts out. See ensureGeneratedCode.
	//
	// --plan never generates: it writes nothing by contract. A plan against a
	// tree whose gen/ is missing reports the go-build packages that cannot
	// load instead, which is the failure the real build would then hit.
	if err := ensureGeneratedCode(projectDirForKCL(), opts.skipGenerate || opts.plan); err != nil {
		return nil, err
	}

	return store.Config(), nil
}

// resolveProjectImageTag is the project image's tag (buildTagFor over its
// pin), after refusing an explicit --tag that contradicts the pin of any
// workload this build — narrowed by --target — actually produces.
func resolveProjectImageTag(cfg *config.ProjectConfig, entities *KCLEntities, targets buildTargetSet, opts buildOptions, resolvedTag string) (string, error) {
	projectImageBuilt := opts.buildDocker && len(targets.goTargets) > 0 && !targets.skipProjectDocker
	if err := checkExplicitTagAgainstPins(entities, opts, cfg.Name, projectImageBuilt); err != nil {
		return "", err
	}
	projectTag := buildTagFor(opts, imagePinFor(entities, cfg.Name), resolvedTag)
	if projectImageBuilt && projectTag != resolvedTag {
		fmt.Printf("[build]   %s image tag: %s (pinned by its declared image)\n", cfg.Name, projectTag)
	}
	return projectTag, nil
}

// resolveBuildImageTag picks the BUILD-WIDE image tag — the tag every image
// this build writes carries unless its own workload pins one (buildTagFor) —
// and says where it came from. The priority is documented at the call site
// in runBuild.
//
// It is deliberately not any one image's tag: the project image's pin used to
// be folded in here, so every other lane inherited a tag that was never
// theirs.
func resolveBuildImageTag(ctx context.Context, entities *KCLEntities, opts buildOptions) (tag, source string, err error) {
	if rt := releaseImageTag(opts); rt != "" {
		// A release pushes ONLY its own version tag — never the env's
		// shared tag (see releaseImageTag). validateReleaseFlags already
		// refused a conflicting --tag.
		return rt, "release version (release-scoped; no shared tag is moved)", nil
	}
	if opts.tag != "" {
		return opts.tag, "explicit --tag flag", nil
	}
	if entities != nil && entities.ImageTag != "" {
		return entities.ImageTag, fmt.Sprintf("env %q image_tag (deploy ref)", opts.env), nil
	}
	if !opts.buildDocker {
		return "", "", nil
	}
	// Only resolve from git when we'll actually use a tag — avoids
	// surfacing "not a git repo" errors on a plain `forge build`
	// (no docker), and is the no-env / no-manifest-tag fallback.
	t, terr := resolveImageTag(ctx, opts.env)
	if terr != nil {
		return "", "", fmt.Errorf("resolve image tag: %w (pass --tag to override)", terr)
	}
	return t, "git describe --tags --always --dirty", nil
}

// refreshBuildRuntimeConfigs renders the frontend runtime config documents a
// build ships (see the comment at its call site in runBuild): this env's when
// one is named, otherwise the dev document if it is missing. Best-effort: a
// failure warns and the build proceeds on the existing document.
func refreshBuildRuntimeConfigs(cfg *config.ProjectConfig, entities *KCLEntities, env string) {
	if env == "" {
		if n, err := ensureDevRuntimeConfigs(cfg, projectDirForKCL()); err != nil {
			fmt.Printf("[build]   Warning: frontend runtime config: %v\n", err)
		} else if n > 0 {
			fmt.Printf("[build]   Rendered %d missing dev frontend runtime config(s)\n", n)
		}
		return
	}
	if changed, err := refreshFrontendRuntimeConfigs(cfg, projectDirForKCL(), env, entities); err != nil {
		fmt.Printf("[build]   Warning: frontend runtime config: %v (building with the previously generated config.js)\n", err)
	} else if changed > 0 {
		fmt.Printf("[build]   Refreshed %d frontend runtime config(s) from env %q\n", changed, env)
	}
}

// preflightBuildCapacity refuses an unaffordable hosted build before any work.
func preflightBuildCapacity(ctx context.Context, opts buildOptions, entities *KCLEntities) error {
	if opts.env == "" || (!opts.push && opts.release == "") || opts.plan || opts.capacityChecked {
		return nil
	}
	capacity, err := buildCapacityPreflight(ctx, opts.env, entities, hostedBuildCount(entitiesOrEmpty(entities)), os.Stdout)
	if err != nil {
		return err
	}
	renderCapacitySection(os.Stdout, capacity)
	return nil
}

func runBuild(ctx context.Context, opts buildOptions) error {
	cfg, err := prepareBuild(opts)
	if err != nil {
		return err
	}

	// When --env is set, read the rendered KCL to drive the docker-skip
	// set, the per-service platform override, the build-only variant
	// builds, AND the default image tag (below). Missing KCL render is
	// logged and treated as "no env filter" so projects that haven't
	// migrated to the deploy module yet keep working unchanged. Rendered
	// BEFORE tag resolution so the env's resolved image_tag can seed the
	// default build tag.
	//
	// The same step decides where this build pushes (resolvePushPlan: each
	// workload's declared reference, or a runbook) and writes it back into
	// opts.pushPlan, so everything below reads one resolution.
	entities, push, err := renderBuildInputs(ctx, cfg, &opts)
	if err != nil {
		return err
	}

	// A hosted env's capacity pre-flight, before any build: a no-plan org must
	// not burn a build to learn it. --plan writes nothing and stays offline.
	if err := preflightBuildCapacity(ctx, opts, entities); err != nil {
		return err
	}

	// Resolve the docker image tag once, up front. Both the docker
	// build/push path below and the post-push build-state write consume
	// this; resolving once guarantees the tag the user sees printed
	// equals the tag that lands in .forge/state/build-<env>.json and the
	// tag that subsequent `forge env deploy` reads back. Override priority:
	//
	//  1. --release: the release version, and nothing else.
	//  2. --tag flag (explicit).
	//  3. With --env: the env's RESOLVED image_tag, read off the rendered
	//     manifests. This is the exact tag `forge env deploy <env>`
	//     references — so `forge env build --env <env> --push` then `forge env
	//     deploy <env>` push and deploy the SAME tag by construction, instead
	//     of build tagging from git-describe while the manifests bake the env
	//     literal (e.g. "staging") → ImagePullBackOff.
	//  4. git-describe (resolveImageTag) — the standalone fallback when
	//     no --env, or the env render carries no image_tag.
	//
	// That is the BUILD-WIDE tag. A workload whose image pins a tag builds
	// that pin in place of (3)/(4) — buildTagFor is the one precedence every
	// lane applies — and a --tag that contradicts a pin is refused below.
	resolvedTag, tagSource, err := resolveBuildImageTag(ctx, entities, opts)
	if err != nil {
		return err
	}

	// Resolve the EMBEDDED build version once, up front, so every binary
	// and the docker image stamp the identical version for this build.
	// Override priority: --tag > git-derived. The forge.yaml `build:`
	// block is GONE — a project that wants to pin the version or stamp an
	// extra -X target adds a `-X main.version=<v>` / `-X pkg.Var=<v>`
	// entry to its KCL GoBuild.ldflags (per-service, per-env), which the
	// go-build dispatch appends AFTER forge's default version stamp so the
	// project's -X wins on the same key.
	resolvedVersion := resolveBuildVersion(ctx, opts.tag)

	printBuildHeader(cfg.Name, opts, resolvedTag, tagSource, push, entities)

	// Filter targets. The Go-build set is KCL-driven: every service the
	// rendered env declares contributes its EffectiveBuild() GoBuild
	// (the synthesized ./cmd/<name> default when it omits `build`),
	// deduped to the unique (cmd, output) set so the shared project
	// binary builds once. Without --env (no KCL service set) we fall back
	// to the single project binary at ./cmd/<project> — NOT the legacy
	// ./cmd hardcode. The --target flag still filters frontends and can
	// drop the binary build entirely (frontend-only / external targets).
	targets, err := resolveBuildTargetSet(cfg, entities, opts)
	if err != nil {
		return err
	}

	// An explicit --tag over a pinned workload this build produces would
	// push a ref nothing deploys. Refused here — after --target narrowing,
	// before anything is built — so a plan refuses it too.
	projectTag, err := resolveProjectImageTag(cfg, entities, targets, opts, resolvedTag)
	if err != nil {
		return err
	}

	// --plan stops HERE: the build set is fully resolved by the same
	// discovery every real build runs, and nothing has been written, built
	// or pushed. See runBuildPlan.
	if opts.plan {
		if opts.composition != nil {
			return runCompositionBuildPlan(ctx, cfg, entities, opts, resolvedTag, projectTag)
		}
		return runBuildPlan(ctx, cfg, entities, targets, opts, resolvedTag, projectTag)
	}
	if err := preflightRealBuild(ctx, entities, opts); err != nil {
		return err
	}
	// A release that can only come out empty is refused now, not after the
	// build (a frontend build alone is minutes).
	if why, empty := releaseCapturesNothing(projectDirForKCL(), entities, targets, opts); empty {
		return errEmptyRelease(why, opts)
	}
	// Refuse conflicting writers, and built_images edges that cannot be
	// scheduled, before ANY build or push, not after earlier lanes have
	// published images. The dispatcher and --plan use this too.
	if _, _, err := externalBuildSchedule(externalBuildServices(entities), opts, resolvedTag, projectDirForKCL()); err != nil {
		return err
	}
	registerBuildStorage(ctx, projectDirForKCL(), entities, push)

	// Create output directory
	if err := os.MkdirAll(opts.outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	frontends := targets.frontends
	dockerFrontends := targets.dockerFrontends
	goTargets := targets.goTargets
	skipProjectDocker := targets.skipProjectDocker
	cfgArchForDocker := targets.cfgArchForDocker

	// Images are built with docker: without a daemon, say so now rather than
	// after minutes of go and frontend builds whose images cannot be made.
	if opts.buildDocker && ((len(goTargets) > 0 && !skipProjectDocker) || len(dockerFrontends) > 0) {
		if err := requireImageDaemon(ctx, "this build's images"); err != nil {
			return err
		}
	}

	// Re-render every frontend's runtime config document (public/config.js)
	// from THIS env's KCL before the frontend build reads it.
	//
	// Without this the built image ships whatever config.js happened to be on
	// disk, and `forge generate` renders that from the DEV environment — so a
	// `forge build prod` baked DEV's runtime config into the production image.
	// It hid for a long time in projects whose envs happened to share a
	// literal (a console whose dev and prod api_url were both
	// http://localhost:8090); the day they diverge, the image silently keeps
	// the dev value and the deployed frontend calls the wrong origin, with
	// nothing in the build output naming the cause.
	//
	// `forge env up` already did exactly this for the dev loop. Doing it here
	// too is what makes "the same bundle can be promoted between environments
	// by shipping it beside a different copy of this file" true for images as
	// well as for the dev server.
	//
	// Best-effort, matching the env-up path: a render failure warns and the
	// build proceeds on the existing document rather than failing a build that
	// would otherwise succeed.
	//
	// With no env, the build ships the DEV document — whatever is on disk.
	// On a fresh checkout there is none (it is machine-local and
	// gitignored), so render just that file rather than the whole generate
	// pipeline a missing config.js used to trigger (see
	// generatedCodeNeedsRefresh).
	if len(frontends) > 0 {
		refreshBuildRuntimeConfigs(cfg, entities, opts.env)
	}

	start := time.Now()
	var results []buildResult

	// Memory-aware concurrency: a constrained memory budget (e.g. a 4Gi
	// cloud-daemon pod) serializes the Go + frontend builds and caps the
	// child compilers, so their combined peak can't trip the OOM killer.
	// An explicit --parallel/--no-parallel wins over the auto-decision.
	budget := detectBuildMemoryBudget()
	parallel, memCaps := resolveBuildConcurrency(opts.parallelSet, opts.parallel, budget)
	opts.parallel = parallel
	fmt.Printf("[build]   Memory:   %s\n", describeBuildBudget(budget, memCaps))

	plan := buildPlan{
		cfg:               cfg,
		frontends:         frontends,
		dockerFrontends:   dockerFrontends,
		goTargets:         goTargets,
		skipProjectDocker: skipProjectDocker,
		cfgArchForDocker:  cfgArchForDocker,
		resolvedTag:       resolvedTag,
		projectTag:        projectTag,
		resolvedVersion:   resolvedVersion,
		opts:              opts,
		memCaps:           memCaps,
	}
	if opts.parallel {
		results = buildParallel(ctx, plan)
	} else {
		results = buildSequential(ctx, plan)
	}

	// KCL DockerBuild / ShellBuild dispatch: services whose build.type is
	// docker or shell are built by their declared mechanism rather than
	// the go-build path. These run after the go-builds (a failing go-build
	// shouldn't waste time on an orthogonal docker/shell build) and are
	// the new home for per-service image builds. See buildKCLDockerShell.
	if entities != nil {
		results = append(results, buildKCLDockerShell(ctx, cfg, entities, opts, cfgArchForDocker, resolvedTag)...)
	}

	// Build-only variants from KCL: each declared variant produces one
	// `bin/<service>-<variant>` binary with the variant's ldflags + build
	// tags. No docker build; this is the lane for sidecar binaries
	// shipped in a release artifact, not container images.
	if entities != nil {
		results = append(results, buildKCLBuildOnlyVariants(ctx, entities, opts.outputDir)...)
	}

	// External-build dispatcher: services whose KCL declares
	// `build_cmd` get their image constructed by a user-supplied shell
	// command rather than forge's built-in Go-build + docker-build
	// pipeline. Mirrors the deploytarget/External provider on the build
	// side. Runs after Go/docker/variant builds so a failing project
	// build doesn't waste time on the (likely orthogonal) external
	// services — but does NOT short-circuit on docker failures because
	// the external builds may target a different registry / pipeline.
	//
	// Skip-with-warn semantics live in the runner: a missing build_cwd
	// produces a "skipped: …" log line and an external-skip result that
	// the summary surfaces but doesn't count as a failure.
	externalResults, err := buildExternalServiceResults(ctx, entities, cfg, opts, cfgArchForDocker, resolvedTag)
	if err != nil {
		return err
	}
	results = append(results, externalResults...)

	succeeded, failed := partitionBuildResults(results)

	// The metrics schema of the binary just built, for the release bundle's
	// observability/metrics-schema.json. Best effort: see emitMetricsSchema.
	if len(failed) == 0 && !opts.debug {
		// The arch the go builds above used: the image platform when the
		// project image consumes them (see buildParallel), else the plain one.
		builtArch := resolveBuildArch(cfg.Deploy.TargetArch, opts.targetArch, false)
		if opts.buildDocker && len(goTargets) > 0 && !skipProjectDocker {
			builtArch = resolveBuildArchForImage(cfgArchForDocker, opts.targetArch)
		}
		emitMetricsSchema(ctx, os.Stdout, opts.outputDir, builtArch, builtTargets(goTargets, succeeded))
	}

	// Persist build state on ANY successful project docker build — not
	// just --push. The state file is the build→deploy tag handoff, which
	// every transport needs (scp/compose deploy a local image just as much
	// as a registry deploy). Pushing only adds the registry coordinates;
	// it is not what makes the handoff worth recording. Skipped only when
	// no docker image was built (host-only env / no --docker / no
	// Dockerfile) or the docker build failed.
	//
	// Each state records the tag its result was BUILT as (buildResult.tag),
	// never a tag recomputed here — so the recorded tag cannot drift from
	// the pushed ref or the digest captured for it.
	if opts.buildDocker && !skipProjectDocker {
		persistProjectBuildState(ctx, cfg, opts, succeeded)
	}

	// The same handoff for every non-project image (frontends and
	// DockerBuild workloads). Deliberately NOT gated on skipProjectDocker: a
	// `--target <frontend>` build skips the project image by design, and
	// that is exactly the case where the frontend's own state was missing.
	persistImageBuildStates(opts, succeeded)

	printBuildSummary(succeeded, failed, start)

	// The gate document, written for EVERY outcome and before the failure
	// return below: a failed build is the one most worth recording, and
	// returning first would write evidence only for the green case.
	if gerr := emitBuildGate(opts, len(succeeded), len(failed), start); gerr != nil {
		return gerr
	}

	if len(failed) > 0 {
		return fmt.Errorf("%d of %d builds failed", len(failed), len(results))
	}

	// Cut the Release ledger when --release is set. This is the build-once →
	// promote spine: harvest the per-image digests the build just captured
	// (the SAME build-state sources resolveDeployImageDigests reads at deploy
	// time) into a durable .forge/releases/<version>.json, plus the resolved
	// commit of every source-pinned frontend the env declares — a Firebase
	// SPA has no image, and omitting it made the ledger cover only the
	// containerized half of an environment. A release with no captured
	// artifact fails loudly rather than writing an empty ledger — a release
	// that can't pin anything is useless and almost always means the user
	// forgot --push (or built against a registry that didn't return a
	// digest). See writeReleaseLedger.
	if opts.composition != nil {
		if err := finishComposition(ctx, cfg, entities, opts, succeeded); err != nil {
			return err
		}
	} else if err := finishReleaseArtifacts(ctx, opts, entities); err != nil {
		return err
	}

	fmt.Printf("\n[build] All %d builds succeeded.\n", len(results))
	fmt.Printf("[build] Binaries available in %s/\n", opts.outputDir)
	return nil
}

// preflightRealBuild is everything a real (non-plan) build refuses on before
// it writes or builds anything: the storage it would fill, and — for a
// release — a ShellBuild checkout that is not at the commit the project pins
// (preflightReleaseSources). Refusing the release HERE rather than at the cut
// is what keeps a refused release from pushing images under its version.
func preflightRealBuild(ctx context.Context, entities *KCLEntities, opts buildOptions) error {
	if err := checkBuildStorageFn(projectDirForKCL()); err != nil {
		return err
	}
	if opts.composition != nil {
		return nil
	} // actual exported inputs are receipt-attested, not inferred from cwd HEAD
	// A version another source already cut is refused before anything is
	// built under its tag (checkReleaseVersionFree).
	if err := preflightReleaseVersion(ctx, projectDirForKCL(), entities, opts); err != nil {
		return err
	}
	if err := preflightReleaseSources(ctx, projectDirForKCL(), entities, opts); err != nil {
		return err
	}
	// A release the target env's schema has already passed is refused here,
	// before 20 minutes of build, and not only at the cut.
	return preflightReleaseMigrations(ctx, projectDirForKCL(), entities, opts)
}

// printBuildHeader prints the block runBuild opens with: what is being built,
// where it lands, the resolved tag and where that tag came from, where images
// are pushed, and the env's KCL build plan.
//
// Extracted for the same reason as printBuildSummary: it is pure rendering,
// and inline it held runBuild over its statement budget.
func printBuildHeader(projectName string, opts buildOptions, resolvedTag, tagSource string, push pushPlan, entities *KCLEntities) {
	fmt.Printf("[build] Building project: %s\n", projectName)
	fmt.Printf("[build]   Output:   %s\n", opts.outputDir)
	fmt.Printf("[build]   Target:   %s\n", opts.buildTarget)
	fmt.Printf("[build]   Docker:   %v\n", opts.buildDocker)
	if opts.env != "" {
		fmt.Printf("[build]   Env:      %s\n", opts.env)
	}
	// The tag is only meaningful when an image is built: a binary-only build
	// resolves no tag, and printing an empty one would read as a bug.
	if opts.buildDocker && resolvedTag != "" {
		fmt.Printf("[build]   Tag:      %s (%s)\n", resolvedTag, tagSource)
	}
	push.printHeader()
	summarizeKCLBuildPlan(entities)
	fmt.Println()
}

// partitionBuildResults splits results into the builds that succeeded and the
// ones that failed, each in its original order — the order the summary prints
// and the build-state writers consume.
func partitionBuildResults(results []buildResult) (succeeded, failed []buildResult) {
	for _, r := range results {
		if r.err != nil {
			failed = append(failed, r)
		} else {
			succeeded = append(succeeded, r)
		}
	}
	return succeeded, failed
}

// printBuildSummary prints the per-target table runBuild ends with.
//
// Extracted because it is pure rendering over the two result slices, and
// leaving it inline kept runBuild at its statement budget — so the next
// person to add a step there had to choose between their change and a lint
// failure that is not about their change.
func printBuildSummary(succeeded, failed []buildResult, start time.Time) {
	fmt.Println()
	fmt.Println(strings.Repeat("-", 50))
	fmt.Printf("[build] Summary (%s)\n", time.Since(start).Truncate(time.Millisecond))
	fmt.Println(strings.Repeat("-", 50))

	for _, r := range succeeded {
		fmt.Printf("  OK   %-20s %-8s (%s)\n", r.name, r.kind, r.duration.Truncate(time.Millisecond))
	}
	for _, r := range failed {
		fmt.Printf("  FAIL %-20s %-8s %v\n", r.name, r.kind, r.err)
	}
}

// buildTargetSet is the resolved set of build inputs runBuild derives from the
// project config, the rendered KCL (when --env is set), the --target flag, and
// the internal skipFrontends switch: which frontends to prod-build, which of
// those need a docker image, the deduped Go-build targets, whether to skip the
// project docker build, and the docker target arch.
//
// skipFrontends is deliberately NOT a CLI flag — `forge build` builds every
// declared frontend by default and users narrow the set with `--target`
// (`-t <project-name>` for the binary alone). The field exists for
// `forge env up`, which dev-serves frontends via `npm run dev` and so must
// suppress the prod build from inside the process.
type buildTargetSet struct {
	frontends         []config.FrontendConfig
	dockerFrontends   []config.FrontendConfig
	goTargets         []goBuildTarget
	skipProjectDocker bool
	cfgArchForDocker  string
}

// resolveBuildTargetSet applies the framework / --target / skipFrontends
// filters and the KCL-driven docker/platform overrides to produce the concrete
// build set. Extracted from runBuild so the filtering logic (and its
// early-return validation for `--target external`) is a single cohesive unit.
// opts is taken by value; its skipFrontends field is only consumed within this
// function.
// renderBuildEntities produces the rendered-KCL entity set `forge build`
// makes its decisions from: render (when --env is set), narrow by
// --target, then materialize any cross-repo frontend source.
//
// The three steps are one helper because they must happen in exactly this
// order and nothing between them may read `entities`. Narrowing before
// source resolution means a `--target` that excludes a cross-repo
// frontend does not fetch it; resolving before anything else reads a
// frontend path means every downstream consumer sees a real directory.
//
// It returns the env's render twice: declared is the FULL render, for the
// facts a narrowed set can lose (resolvePushPlan's destinations, which a
// --target narrowing can drop);
// entities is the set this build acts on, narrowed by --target. Both are nil
// without an env, or when the env has no KCL directory.
func renderBuildEntities(ctx context.Context, cfg *config.ProjectConfig, opts buildOptions) (declared, entities *KCLEntities, err error) {
	if opts.env != "" {
		ents, rerr := renderBuildKCL(ctx, projectDirForKCL(), opts.env)
		if rerr != nil {
			return nil, nil, rerr
		}
		// Re-render with the build's own tag and arch bound, when they
		// differ from what the first pass resolved on its own.
		//
		// This is not a nicety: a ShellBuild's `cmd` is a plain KCL string
		// that forge runs VERBATIM, so whatever tag and arch the KCL read
		// while composing that string are the ones the command will use.
		// There is no substitution pass afterwards to correct them. So the
		// values must be bound BEFORE the render whose `cmd` we run — a
		// `--tag v9` that arrived after the render would print v9, build
		// `:latest`, and record v9.
		//
		// Two passes rather than one because both inputs are partly derived
		// FROM the render: the tag falls back to the env's own `image_tag`,
		// and the arch to the env's declared cluster platform. The first
		// pass discovers those; the second binds forge's resolution of them.
		// Skipped entirely when the first pass already agrees, so the common
		// `forge build dev` renders once.
		ents, rerr = rerenderWithBuildFacts(ctx, cfg, opts, ents)
		if rerr != nil {
			return nil, nil, rerr
		}
		declared, entities = ents, ents
	}

	entities = narrowBuildEntities(cfg.Name, entities, opts)

	// Materialize any cross-repo frontend source, so every downstream
	// consumer (npm install, `npm run build`, the docker context) sees a
	// real directory. No-op — no resolver, no cache access — for a project
	// that declares none.
	if !(opts.plan && opts.composition != nil) {
		var err error
		if opts.composition != nil {
			err = resolveFrontendEntitySources(ctx, projectDirForKCL(), entities)
		} else {
			err = resolveBuildFrontendSources(ctx, cfg, entities)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if err := resolveBuildSources(ctx, projectDirForKCL(), cfg, entities); err != nil {
		return nil, nil, err
	}
	return declared, entities, nil
}

// rerenderWithBuildFacts re-renders env with the tag and arch THIS build
// resolved bound as `-D image_tag=` / `-D target_arch=`, so a ShellBuild's
// rendered `cmd` — which forge runs byte-for-byte — already names the right
// ones.
//
// Both values outrank what the KCL resolves alone, and both are computed from
// the first pass: buildTagFor's precedence (release > --tag > env image_tag >
// git describe) needs the env's image_tag, and the arch needs the env's
// declared cluster platform. Hence discover-then-bind.
//
// Returns the FIRST pass unchanged when the second would bind nothing new —
// no --tag, no --release, and an arch the KCL would derive identically. A
// second render is a second kcl evaluation, so the common case pays nothing.
//
// A re-render failure is returned: the first pass proved the KCL renders, so a
// failure here is forge's own binding being rejected (a project that declared
// a conflicting `option("target_arch")`), and rendering on with the wrong tag
// is what this function exists to prevent.
func rerenderWithBuildFacts(ctx context.Context, cfg *config.ProjectConfig, opts buildOptions, first *KCLEntities) (*KCLEntities, error) {
	if first == nil {
		return nil, nil
	}
	var extra []string

	// The tag: buildTagFor's precedence over the env's own image_tag. Bound
	// only when it differs, so an env that already resolves its own tag is
	// not re-rendered to be told the same thing.
	if tag := buildTagFor(opts, "", first.ImageTag); tag != "" && tag != first.ImageTag {
		extra = append(extra, "image_tag="+strconv.Quote(tag))
	}

	// The arch: the same resolution every other build lane applies
	// (resolveBuildArchForImage over the env's platform), which is also what
	// lib/build.k's own default would produce — so bind it only when forge's
	// answer is the more specific one.
	cfgArch := cfg.Deploy.TargetArch
	if p := kclEnvImagePlatform(first); p != "" {
		cfgArch = p
	}
	if arch := resolveBuildArchForImage(cfgArch, opts.targetArch); arch != "" && arch != "amd64" {
		extra = append(extra, "target_arch="+strconv.Quote(arch))
	}

	if len(extra) == 0 {
		return first, nil
	}
	ents, err := RenderKCLWith(ctx, projectDirForKCL(), opts.env, extra)
	if err != nil {
		return nil, fmt.Errorf("re-render env %q with the build's resolved tag/arch (%s): %w",
			opts.env, strings.Join(extra, " "), err)
	}
	return ents, nil
}

// narrowBuildEntities is the entity set a build acts on: the env's render
// narrowed by --target BEFORE any build decision reads it. Doing it here
// (rather than at each derivation site) is what makes the scoping total:
// resolveBuildTargetSet, buildKCLDockerShell, buildKCLBuildOnlyVariants and
// buildExternalServiceResults all read the result, so one filter covers every
// build lane. nil in, nil out.
func narrowBuildEntities(projectName string, entities *KCLEntities, opts buildOptions) *KCLEntities {
	if entities == nil {
		return nil
	}
	// Whatever the narrowing below keeps, it keeps the images those builds
	// are built FROM (ShellBuild.built_images): naming one workload cannot
	// mean building it on a parent nobody built. A composition is the one
	// exception — its selection is explicit, and an unselected parent is
	// carried from the base, which the build resolves instead.
	full := entities
	narrowed := narrowBuildEntitiesBySelection(projectName, entities, opts)
	if opts.composition != nil {
		return narrowed
	}
	return withBuiltImageDependencies(full, narrowed)
}

// narrowBuildEntitiesBySelection is the narrowing --target and the
// orchestrator's targets select, before built_images dependencies are added.
func narrowBuildEntitiesBySelection(projectName string, entities *KCLEntities, opts buildOptions) *KCLEntities {
	// opts.targets is the orchestrator's channel (set by `forge env up`).
	// Validated by the caller (`forge env up` / `forge env deploy`), which
	// owns the "unknown target" message and its available-names list.
	if len(opts.targets) > 0 {
		entities = filterEntitiesByTarget(entities, opts.targets)
	}

	// opts.buildTarget is this command's own `-t <name>`, and it must narrow
	// the entities too — otherwise naming one service still runs every other
	// service's build_cmd, which is exactly the "one command rebuilt my whole
	// stack" failure. `all` and `external` keep their existing meanings and
	// are not names.
	//
	// A FRONTEND name narrows the same way, and for the same reason. It used
	// not to matter: a frontend was resolvable only from the codegen
	// inventory, which no external-build service is ever in, so the branch
	// was unreachable for one. Now that a KCL-declared frontend resolves,
	// `forge build prod --target reliant-web` reached it — and observed: the
	// frontend built in 22s, then every external service's build_cmd ran
	// anyway and four docker pushes failed, on a command that named one
	// frontend.
	//
	// The PROJECT name narrows too, to the workloads the project image is
	// built for. It is the command a CI job runs to publish that one image,
	// and without this it ran every ShellBuild and DockerBuild the env
	// declares — `forge env build prod --target control-plane --push` needed a
	// sibling-repo checkout for images it was never asked to build.
	// Precedence is resolveNamedBuildTarget's: frontend, project, service.
	switch t := opts.buildTarget; {
	case t == "" || t == "all" || t == "external":
		return entities
	case kclFrontendAsBuildTarget(entities, t) != nil:
		return filterEntitiesByTarget(entities, []string{t})
	case t == projectName:
		return filterEntitiesToProjectImage(entities)
	case kclHasServiceNamed(entities, t):
		return filterEntitiesByTarget(entities, []string{t})
	}
	return entities
}

// filterEntitiesToProjectImage returns a shallow copy of e narrowed to the
// workloads the project image is built for — every workload with a GoBuild —
// and no frontend or host infra. The project Dockerfile copies every built
// binary into the one image, so those workloads are one artifact: building a
// subset would ship an image missing a binary some workload runs.
func filterEntitiesToProjectImage(e *KCLEntities) *KCLEntities {
	out := *e // shallow copy; slices below are rebuilt, the rest shared
	var ws []WorkloadEntity
	for _, w := range e.Workloads {
		if w.GoBuild() != nil {
			ws = append(ws, w)
		}
	}
	out.Workloads = ws
	out.Infra = nil
	out.Frontends = nil
	out.ManifestClusters = nil
	return &out
}

// bindBuildRenderOptions publishes this invocation's `-D name=value` render
// options for the render paths that follow.
//
// Same three steps, same order, same helpers as `forge env up` (up.go): parse
// first so a malformed value or a forge-reserved name gets its own message,
// then check the name against what the env's KCL declares, then publish. An
// option only means something to a specific env's KCL, so requiring the env
// argument is what makes the declared-name check possible at all.
//
// It binds ONLY when this invocation actually carried -D. `forge env up` sets
// the process-global itself and then delegates here; parsing an empty slice
// yields nil, and publishing that nil would CLOBBER the options the
// orchestrator already bound — the build phase would then re-render without
// them and silently build the wrong variant. Observed exactly that:
// `forge env up prod -D desktop_channel=local` ran the RELEASE packaging
// target and failed on missing Apple notarization credentials.
func bindBuildRenderOptions(opts buildOptions) error {
	if len(opts.renderOptions) == 0 {
		return nil
	}
	if opts.env == "" {
		return fmt.Errorf("-D requires the environment argument (e.g. `forge build prod -D name=value`): options are declared per-env in deploy/kcl/<env>/")
	}
	renderDArgs, err := parseRenderOptions(opts.renderOptions)
	if err != nil {
		return err
	}
	if err := validateRenderOptions(projectDirForKCL(), opts.env, opts.renderOptions); err != nil {
		return err
	}
	setRenderOptions(renderDArgs)
	return nil
}

// renderBuildKCL renders env's KCL for a build. An env with NO KCL
// directory is the one tolerated miss — a project that never adopted the
// deploy module builds unfiltered, with a note — and it returns nil
// entities.
//
// Any other render error is returned verbatim. It used to be downgraded
// to the same "skipping KCL filter" note, and the build carried on with
// no entity set: `forge build <env> --target external` then reported "no
// service declares build_cmd" for an env whose services DID declare one,
// because the real cause (a failed `cluster_target.platform is required`
// check) had been printed as a note and discarded. A declared env whose
// KCL does not render cannot be built correctly — the skip sets, platform
// and build_cmd services all come from that render.
func renderBuildKCL(ctx context.Context, projectDir, env string) (*KCLEntities, error) {
	if os.Getenv("FORGE_KCL_RENDER_FIXTURE") == "" {
		kclDir := filepath.Join(projectDir, "deploy", "kcl", env)
		if _, err := os.Stat(kclDir); errors.Is(err, fs.ErrNotExist) {
			fmt.Printf("[build]   Note: skipping KCL filter (no %s)\n", projectRelPath(projectDir, kclDir))
			return nil, nil
		}
	}
	ents, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, fmt.Errorf("render env %q KCL: %w", env, err)
	}
	return ents, nil
}

// validateExternalBuildTarget checks the preconditions for `--target
// external`: an --env to resolve KCL against, and at least one service in it
// that actually declares build_cmd.
//
// No experimental gate here: `build_cmd` is the build-side mirror of
// External's `deploy_cmd`, and `forge env deploy` of an External target needs
// no opt-in. Gating build behind features.experimental.external_builds while
// deploy ran free left the build/deploy pair of the SAME target with
// mismatched maturity gates (fr-da9a6614fb) — you could deploy an external
// target but not build it. The gates are unified by retiring the build-side
// one. The config key is still accepted (back-compat) but no longer governs
// whether build_cmd runs.
func validateExternalBuildTarget(entities *KCLEntities, opts buildOptions) error {
	if opts.env == "" {
		return fmt.Errorf("--target external requires --env to know which KCL services to build")
	}
	if !kclHasExternalBuildService(entities) {
		return fmt.Errorf("--target external: no workload declares a ShellBuild in env %q.\n"+
			"  Declare `build = forge.ShellBuild { ... }` on the workload so `forge build -t external`\n"+
			"  constructs the image. The cmd is plain KCL, run verbatim, e.g.:\n"+
			"      _arch = forge.target_arch()\n"+
			"      _ref  = \"ghcr.io/acme/api:\" + forge.image_tag(%q)\n"+
			"      build = forge.ShellBuild {\n"+
			"          cwd = \"../api\"\n"+
			"          cmd = \"docker build --platform=linux/${_arch} -t ${_ref} . && docker push ${_ref}\"\n"+
			"      }", opts.env, opts.env)
	}
	return nil
}

// resolveNamedBuildTarget narrows the build to a single named target: a
// frontend, the project binary, or a service declared only in the env's KCL.
// It returns the frontends left to build and whether the project binary
// should still be built, and may set opts.skipFrontends.
//
// The KCL lookup is what the flag's own help has always promised ("a specific
// service/frontend name") and what `forge env up --target` already does.
// Without it, -t resolved ONLY against frontends and the project name, so a
// service declared purely in KCL — every sibling-repo build, e.g.
// reliant-desktop — was unreachable: "target not found in project config",
// despite the service being right there in the rendered env.
func resolveNamedBuildTarget(cfg *config.ProjectConfig, entities *KCLEntities, opts *buildOptions, frontends []config.FrontendConfig) ([]config.FrontendConfig, bool, error) {
	if hit := filterFrontends(frontends, opts.buildTarget); len(hit) > 0 {
		// Target is a frontend, skip binary build.
		return hit, false, nil
	}
	// Not in the codegen inventory — which does not mean forge cannot
	// build it. cfg.Frontends (and everything derived from it) answers
	// "whose TypeScript does this repo generate", and that set
	// deliberately excludes a frontend pinned to another repository: see
	// config.KCLFrontend.OwnsFrontendCode. The BUILD set is a different
	// question, and the rendered env answers it — renderBuildEntities has
	// already materialized the pin into a real directory precisely so
	// `npm run build` has somewhere to run.
	//
	// Conflating them made control-plane's `forge build prod --target
	// reliant-web` print "Frontends (skip docker): reliant-web" in its own
	// plan and then fail with "target not found", one screen apart.
	if hit := kclFrontendAsBuildTarget(entities, opts.buildTarget); hit != nil {
		return []config.FrontendConfig{*hit}, false, nil
	}
	if opts.buildTarget == cfg.Name {
		// The project binary and image, and no frontend: a frontend is its
		// own target. Returning the inventory here ran every frontend's
		// `npm run build` on a command that named the project.
		opts.skipFrontends = true
		return nil, true, nil
	}
	if !kclHasServiceNamed(entities, opts.buildTarget) {
		return nil, false, fmt.Errorf("target %q not found in project config or in env %q's KCL services", opts.buildTarget, opts.env)
	}
	// A KCL service target builds that service only: no frontends.
	//
	// It DOES still build the binary. Returning false here reported success
	// having compiled nothing, which is worse than the "not found" error this
	// branch was added to replace — a silent no-op that finishes in 0s and
	// looks like a cache hit.
	//
	// Concretely: control-plane's `admin-server` is a go-build service whose
	// EffectiveBuild maps onto the shared ./cmd/control-plane binary (its
	// container command is ["./control-plane","public-api"]). `forge build prod
	// --target admin-server` printed a 0s summary with no build lines, and the
	// stale image was then deployed and served old code against a migrated
	// database. resolveGoTargets(false, ...) returns nil unconditionally, so
	// no go target survived to be built.
	//
	// goBuildTargetsFromKCL already dedupes by (cmd, output), so a named
	// service resolves to exactly the one binary it needs — the same binary
	// the full build would produce for it, not the whole project's set.
	opts.skipFrontends = true
	return nil, true, nil
}

func resolveBuildTargetSet(cfg *config.ProjectConfig, entities *KCLEntities, opts buildOptions) (buildTargetSet, error) {
	// The starting set is the project-level "frontends forge owns a Node
	// toolchain for", with each path resolved through the frontends/<name>
	// fallback. It is the SAME set `forge lint`'s frontend lane walks —
	// both commands shell into these directories, so neither re-derives it.
	//
	frontends := cfg.ToolchainFrontends()
	if opts.composition != nil && entities != nil {
		// Composition selects runtime consumers, not the codegen inventory.
		// Never build an excluded local frontend because filterFrontendsForBuild
		// historically keeps names missing from the narrowed KCL set.
		frontends = nil
		for _, f := range entities.Frontends {
			if hit := kclFrontendAsBuildTarget(entities, f.Name); hit != nil {
				frontends = append(frontends, *hit)
			}
		}
	}
	buildBinary := true

	// `--target external` is the explicit "build ONLY the KCL services
	// with build_cmd" filter. Useful for the cp-forge pattern where the
	// sibling-repo binary changes faster than the project binary, so the
	// user wants to iterate the external-build leg without rebuilding
	// the whole project image / frontends. Requires --env so we have a
	// rendered KCL set to filter against.
	if opts.buildTarget == "external" {
		if err := validateExternalBuildTarget(entities, opts); err != nil {
			return buildTargetSet{}, err
		}
		// Skip everything else — only the external dispatcher runs.
		frontends = nil
		buildBinary = false
		opts.skipFrontends = true
	} else if opts.buildTarget != "all" {
		var err error
		frontends, buildBinary, err = resolveNamedBuildTarget(cfg, entities, &opts, frontends)
		if err != nil {
			return buildTargetSet{}, err
		}
	}

	// `forge env up` skips frontend prod builds entirely. Its frontend phase
	// (upFrontends) dev-serves via `npm run dev` and never consumes the
	// prod artifact. Set explicitly by upBuildCluster.
	if opts.skipFrontends {
		if len(frontends) > 0 {
			fmt.Printf("[build]   Skipping %d frontend(s): forge env up dev-serves frontends\n", len(frontends))
		}
		frontends = nil
	}

	// KCL-driven prod-build skip for host-mode frontends. Host-mode
	// frontends only ever run via `npm run dev` (the dev loop in
	// `forge env up`); they never consume the `npm run build` artifact, so
	// running the full Next.js prod build is wasted minutes. Skip them
	// from `frontends` (the input to buildFrontend → `npm run build`)
	// while keeping their entry in cfg.Frontends so other commands
	// (forge generate, forge env up's frontend phase) see them unchanged.
	if entities != nil {
		frontends = filterFrontendsForBuild(frontends, entities)
	}

	// KCL-driven docker skip: with --env set, no frontend builds an image
	// (a frontend served from a container is a Workload with a
	// DockerBuild, built by buildKCLDockerShell), and the project image is
	// built only when some workload that runs it needs it
	// (envNeedsProjectImage).
	dockerFrontends := frontends
	skipProjectDocker := false
	if entities != nil {
		dockerFrontends = nil
		if !envNeedsProjectImage(entities) {
			skipProjectDocker = true
		}
	}

	// Per-env platform override from KCL: the env's declared
	// cluster_target.platform, else the first Cluster-bound workload's
	// runtime platform (kclEnvImagePlatform), else the control plane's hosted
	// platform. Falls back to
	// forge.yaml's deploy.target_arch otherwise.
	cfgArchForDocker := cfg.Deploy.TargetArch
	if entities != nil {
		if p := kclEnvImagePlatform(entities); p != "" {
			cfgArchForDocker = p
		}
		if err := requireDeclaredImageArch(entities, cfgArchForDocker, opts.targetArch,
			opts.pushPlan.push || opts.push || opts.release != "", opts.env); err != nil {
			return buildTargetSet{}, err
		}
	}

	goTargets := resolveGoTargets(buildBinary, entities, cfg)

	return buildTargetSet{
		frontends:         frontends,
		dockerFrontends:   dockerFrontends,
		goTargets:         goTargets,
		skipProjectDocker: skipProjectDocker,
		cfgArchForDocker:  cfgArchForDocker,
	}, nil
}

// buildExternalServiceResults runs the external-build dispatcher for services
// whose KCL declares `build_cmd`, returning their build results. Mirrors the
// deploytarget/External provider on the build side. Returns (nil, nil) when
// there is no rendered KCL or no service opts into external builds.
//
// No experimental gate: build_cmd is the build-side mirror of External's
// deploy_cmd (which needs no opt-in), so a service that declares build_cmd just
// builds. See the --target external branch in runBuild for the rationale
// (fr-da9a6614fb).
func buildExternalServiceResults(ctx context.Context, entities *KCLEntities, cfg *config.ProjectConfig, opts buildOptions, cfgArchForDocker, resolvedTag string) ([]buildResult, error) {
	if entities == nil {
		return nil, nil
	}
	externalSvcs := externalBuildServices(entities)
	if len(externalSvcs) == 0 {
		return nil, nil
	}
	externalTag := resolvedTag
	if externalTag == "" {
		// The tag is recorded in build state and reported in the build
		// log line, and it is what was bound as the `image_tag` KCL input
		// for the render whose `cmd` we are about to run — so it must be
		// the same value even when the caller passed no --tag. Resolve the
		// same git-describe tag the docker path would have used.
		t, terr := resolveImageTag(ctx, opts.env)
		if terr != nil {
			return nil, fmt.Errorf("external build: resolve image tag: %w (pass --tag to override)", terr)
		}
		externalTag = t
	}
	projDir := projectDirForKCL()
	return buildExternalServices(ctx, externalSvcs, opts, externalTag, projDir), nil
}

// persistProjectBuildState records the build→deploy tag handoff for a
// successful project docker build. It scans the succeeded results for the
// project image, and on a hit writes .forge/state/build-<env>.json. Failure to
// write is non-fatal (warned): the build already succeeded and deploy can fall
// back to git provenance.
func persistProjectBuildState(ctx context.Context, cfg *config.ProjectConfig, opts buildOptions, succeeded []buildResult) {
	projectDockerSucceeded := false
	var projectDigest, projectTag string
	var projectPlatforms []string
	for _, r := range succeeded {
		if r.kind == "docker" && r.name == cfg.Name+" (docker)" {
			projectDockerSucceeded = true
			projectDigest = r.digest
			projectPlatforms = r.platforms
			projectTag = r.tag
			break
		}
	}
	if !projectDockerSucceeded || projectTag == "" {
		return
	}
	prov := captureBuildProvenance(ctx, projectDirForKCL())
	state := BuildState{
		Image:     opts.pushPlan.repositoryFor(cfg.Name),
		Tag:       projectTag,
		Pushed:    opts.pushPlan.push,
		Commit:    prov.Commit,
		GitTag:    prov.Tag,
		Dirty:     prov.Dirty,
		PushedAt:  nowRFC3339(),
		Digest:    projectDigest,
		Platforms: projectPlatforms,
	}
	if werr := WriteBuildState(projectDirForKCL(), opts.env, state); werr != nil {
		// Non-fatal: the build succeeded; recording the state is
		// a convenience for the downstream deploy. Print a
		// warning so the user knows deploy may fall back to git.
		fmt.Printf("[build]   Warning: failed to write build-state file: %v\n", werr)
	} else {
		fmt.Printf("[build]   Wrote build state: %s\n", buildStatePath(projectDirForKCL(), opts.env))
	}
}

// persistImageBuildStates records the build→deploy handoff for every NON-project
// image built in this run — today, the cluster-deployed frontends.
//
// It writes the same per-image `.forge/state/build-<env>-<image>.json` files the
// external-build dispatcher writes, because `resolveDeployImageDigests` already
// globs that pattern to assemble the per-image name→digest map. Reusing the
// existing file shape means deploy needs no new read path: the frontend's digest
// lands in the map beside reliant's and workspace-base's, and `_image_ref` pins
// `<image>@sha256:...` for it like any other.
//
// Why this exists: the project image was the ONLY thing that recorded state, so
// a successful `forge env build <env> --target <frontend> --push` left no trace. The
// following `forge env deploy <env> --target <frontend>` then found nothing for
// that image, fell through to the release ledger's stale digest, and redeployed
// the OLD image while reporting a clean rollout — a silent no-op deploy that is
// indistinguishable from success until someone opens the app.
//
// Failure to write is non-fatal (warned): the build already succeeded, and the
// worst case is the tag-fallback deploy path that existed before.
func persistImageBuildStates(opts buildOptions, succeeded []buildResult) {
	projDir := projectDirForKCL()
	for _, r := range succeeded {
		// Only docker results carry an image name, and only a pushed one has a
		// registry-addressable digest worth recording. A build with no digest
		// still records the tag handoff, which non-registry transports need.
		if r.kind != "docker" || r.image == "" || r.tag == "" {
			continue
		}
		repository := r.repository
		if repository == "" {
			repository = opts.pushPlan.repositoryFor(r.image)
		}
		state := buildtarget.State{
			Service:   r.image,
			Image:     repository,
			Tag:       r.tag,
			PushedAt:  nowRFC3339(),
			Digest:    r.digest,
			Platforms: r.platforms,
		}
		if werr := buildtarget.WriteState(projDir, opts.env, state); werr != nil {
			fmt.Printf("[build]   Warning: failed to write build-state file for %s: %v\n", r.image, werr)
			continue
		}
		fmt.Printf("[build]   Wrote build state: %s\n", buildtarget.StatePath(projDir, opts.env, r.image))
	}
}

// finishReleaseArtifacts is the build's last artifact step: it builds and
// pushes an env's forge.OnHosted frontends (they ship as OCI release
// artifacts), cuts the release ledger when --release is set so the cut records
// those digests too, and then writes the env's BUNDLE.
//
// THE BUNDLE COMES LAST, AFTER THE CUT, and the order is the contract. A
// bundle names the release it pins, so cutting first is what lets it carry a
// version rather than "" — and the render it seals resolves the digests that
// release just recorded. Writing the bundle first would pin a release that
// did not exist yet.
func finishReleaseArtifacts(ctx context.Context, opts buildOptions, entities *KCLEntities) error {
	if err := buildHostedStaticSites(ctx, projectDirForKCL(), entities, opts); err != nil {
		return err
	}
	if opts.release != "" {
		// Before the cut: a dashboard that does not validate must not leave a
		// release behind that has no bundle to carry it.
		if opts.env != "" && !opts.plan {
			if _, err := collectObservabilityFiles(projectDirForKCL(), opts.outputDir); err != nil {
				return fmt.Errorf("--release %s: %w", opts.release, err)
			}
		}
		if err := writeReleaseLedger(ctx, opts, entities); err != nil {
			return err
		}
	}
	return writeBuildBundle(ctx, opts)
}

// writeBuildBundle is `forge env build`'s bundle step (doc §4.4, §7.2).
//
// A build with no env writes nothing: a bundle is ONE env's render, and there
// is no env to render. Top-level `forge build` is compile-only and reaches
// here with opts.env empty.
//
// --plan writes nothing either, for the reason the declaration step skips it:
// it preflights a build and must not be the thing that changed the world.
func writeBuildBundle(ctx context.Context, opts buildOptions) error {
	if opts.env == "" || opts.plan {
		return nil
	}
	projectDir := projectDirForKCL()
	run, err := opts.run.resolveRun()
	if err != nil {
		return err
	}
	// The pins the bundle seals are the digests this build captured — the
	// same build state the cut harvested and the same source
	// resolveDeployImageDigests reads at deploy time, so the bundle pins
	// what the release pins.
	written, err := writeBundlesFn(ctx, projectDir, parseBundleEnvs(opts.bundleEnvs, opts.env), bundleBuildInputs{
		Release: opts.release,
		Pins:    buildBundlePins(projectDir, opts.env),
		Run:     run,
		Pushed:  opts.push || opts.pushIfDeclared,
		Now:     time.Now().UTC().Truncate(time.Second),

		OutputDir:           opts.outputDir,
		StrictObservability: true,
	})
	printBundleWrites(os.Stdout, written)
	noteRecordedBundles(opts.release, written)
	return err
}

// buildBundlePins projects the build's captured artifacts into the pin set a
// bundle records: images by artifact key, and the resolved source of every
// source-built component.
//
// It reads the same harvest the cut reads rather than re-resolving, so a
// bundle and the release it names cannot disagree about which bytes an
// artifact key points at.
func buildBundlePins(projectDir, env string) release.BundlePins {
	harvested := release.Release{Artifacts: harvestReleaseArtifacts(projectDir, env)}
	pins := release.BundlePins{Images: harvested.SharedDigests()}
	if sources := harvested.Sources(); len(sources) > 0 {
		pins.Sources = sources
	}
	return pins
}

// writeReleaseLedger harvests the artifacts of the just-completed build into a
// Release ledger keyed by opts.release. It is the durable projection of the
// ephemeral build state, across every kind of thing a release ships:
//
//   - OCI images, from the build-state digests (harvestReleaseArtifacts);
//   - npm packages, from what `npm pack` would publish;
//   - Go modules, from the repo's own go.mod/go.sum pair;
//   - files, from the build-only binaries this build emitted.
//
// Images are merged FIRST and never overwritten (see mergeReleaseArtifacts), so
// adding the other kinds cannot perturb the digests an env deploys.
//
// Fails (does not silently no-op) when NOTHING was captured: a release is a
// promise that "these exact bytes ship everywhere", and an empty promise is a
// latent footgun (a later `forge env deploy` would resolve nothing and
// fall back to tags — exactly the mutable-tag failure the release model exists
// to kill). The actionable remedy is in the error: pass --push.
//
// A release with SOME kinds and not others is normal and never an error — a
// project with no npm package simply cuts a release with no npm artifacts.
func writeReleaseLedger(ctx context.Context, opts buildOptions, entities *KCLEntities) error {
	cut, err := cutReleaseFromBuildState(ctx, projectDirForKCL(), opts.env, opts.release, opts.outputDir, entities, opts)
	if err != nil {
		return err
	}
	printReleaseCut(os.Stdout, cut)
	return nil
}

// releaseCutOutcome is what one cut did — the release, whether THIS call
// wrote it, and where. cutReleaseFromBuildState returns it instead of
// printing, so `forge env build <env> --release` can render text and
// `--release-json` a document from the same facts.
type releaseCutOutcome struct {
	Release release.Release
	// Created is false when the same version over the same artifacts was
	// already recorded: an idempotent re-cut, exit 0 all the same.
	Created bool
	// Ledger is where the release was recorded (a control plane URL or the
	// project's .forge/releases).
	Ledger string
	// Images, Packages and Files count the artifacts by kind.
	Images, Packages, Files int
}

// printReleaseCut is the text rendering of a cut.
func printReleaseCut(out io.Writer, cut releaseCutOutcome) {
	verb := "Cut"
	if !cut.Created {
		verb = "Release already recorded (identical artifacts — nothing written):"
	}
	fmt.Fprintf(out, "\n[build] %s release %s (%d image(s), %d package(s), %d file(s)): %s\n",
		verb, cut.Release.Version, cut.Images, cut.Packages, cut.Files, strings.Join(releaseImageNames(cut.Release), ", "))
	fmt.Fprintf(out, "[build]   Ledger: %s\n", cut.Ledger)
	fmt.Fprintf(out, "[build]   Deploy:  forge env deploy <env> %s\n", cut.Release.Version)
}

// cutReleaseFromBuildState is the one CUT path: harvest what the last build
// captured for env, check it covers everything env declares, and record it in
// env's release ledger — the project's files, or the control plane env's KCL
// declares. `forge env build <env> --release` calls it after building;
// `--release --no-build` calls it on its own, for the CI shape where images
// were built and pushed by an earlier step.
func cutReleaseFromBuildState(ctx context.Context, projectDir, env, version, outputDir string, entities *KCLEntities, opts buildOptions) (releaseCutOutcome, error) {
	artifacts := harvestReleaseArtifacts(projectDir, env)
	packages := mergeReleaseArtifacts(artifacts, harvestNPMArtifacts(ctx, projectDir))
	packages += mergeReleaseArtifacts(artifacts, harvestGoModuleArtifacts(projectDir))
	files := mergeReleaseArtifacts(artifacts, harvestFileArtifacts(projectDir, outputDir, entities))
	// A hosted env's backends name images the build state may not hold (CI
	// pushed them). Record each declared image so the release covers what
	// the hosted deploy ships — keyed by the address under the platform's
	// push base, which needs the org the credential acts for.
	if err := requireOrg(ctx, entities); err != nil {
		return releaseCutOutcome{}, fmt.Errorf("--release %s: %w", version, err)
	}
	if err := harvestHostedBackendArtifacts(ctx, env, entities, artifacts); err != nil {
		return releaseCutOutcome{}, fmt.Errorf("--release %s: %w", version, err)
	}
	if err := checkHostedArtifactPlatforms(ctx, os.Stdout, entities, artifacts, env, version); err != nil {
		return releaseCutOutcome{}, fmt.Errorf("--release %s: %w", version, err)
	}
	images := countOCIArtifacts(release.Release{Artifacts: artifacts})
	if len(artifacts) == 0 {
		return releaseCutOutcome{}, fmt.Errorf("--release %s: no image digest was captured to record in the release ledger.\n"+
			"  A release pins immutable digests, which require a registry push — re-run with --push\n"+
			"  (forge env build %s --release %s --push pushes to each image's own registry).\n"+
			"  A release built without --push has only a local tag, which can't be promoted across envs", version, env, version)
	}

	// The source maps each hosted site's last build filed, beside the site.
	harvestSourcemapsArtifacts(projectDir, env, entities, artifacts)

	// Source-pinned frontends (Firebase SPAs fetched via forge.GitSource)
	// carry no image and so contribute no build state. Record each one's
	// resolved commit so the release covers the whole environment, not just
	// the half that ships as containers.
	if err := addFrontendSourceArtifacts(ctx, projectDir, entities, artifacts); err != nil {
		return releaseCutOutcome{}, fmt.Errorf("--release %s: %w", version, err)
	}

	// Completeness gate. Every image the env DECLARES must be in the ledger.
	// A release that silently omits a declared artifact is the failure this
	// whole model exists to prevent: it looks like a full release, promotes
	// like one, and ships an environment with a hole in it.
	opts.env, opts.release = env, version
	if err := checkReleaseCoversEnv(entities, artifacts, opts); err != nil {
		return releaseCutOutcome{}, err
	}

	// captureReleaseProvenance, the SAME capture a no-version deploy matches
	// on (chooseDeployRelease): a release cut here by `forge env build
	// --release` and one cut by `forge env deploy` record provenance through
	// one function over one directory, so the deploy that follows either
	// finds it.
	prov := captureReleaseProvenance(ctx, projectDir)
	if err := checkCutMatchesBuild(version, prov.Commit, harvestedBuildCommits(projectDir, env)); err != nil {
		return releaseCutOutcome{}, err
	}
	// Every sibling checkout a ShellBuild ran in must have been AT the
	// commit this checkout pins, and clean. Checked against what the build
	// state RECORDS, so a --no-build cut is held to it too.
	if err := checkRecordedReleaseSources(ctx, projectDir, env, version, entities); err != nil {
		return releaseCutOutcome{}, err
	}
	// The CI run that cut this release: the join key that ties it to the
	// promotions and gates from the same run (§3.A). Defaulted from the
	// CI environment, so every step of one pipeline carries the same
	// value without each workflow having to thread a flag.
	run, err := opts.run.resolveRun()
	if err != nil {
		return releaseCutOutcome{}, fmt.Errorf("--release %s: %w", version, err)
	}
	rel := release.Release{
		Version:   version,
		Project:   hostedProjectName(),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Artifacts: artifacts,
		Run:       run,
	}
	rel.SetProvenance(prov)
	ledger, err := ledgerFor(ctx, projectDir, env)
	if err != nil {
		return releaseCutOutcome{}, err
	}
	// The migrations the release carries, recorded and judged against env's
	// applied history over the checkouts the build state RECORDS — the same
	// sources checkRecordedReleaseSources held to the pins (release_migrations.go).
	recordedUses, _ := recordedShellSources(projectDir, env, entities)
	migrations, err := releaseMigrationGuard(ctx, os.Stdout, releaseRef{projectDir, env, version}, prov, recordedUses, ledger, opts)
	if err != nil {
		return releaseCutOutcome{}, err
	}
	rel.Migrations = migrations
	if err := pinStorageRelease(rel, os.Stderr); err != nil {
		return releaseCutOutcome{}, err
	}
	// The source history this release adds, while the checkout is at its
	// commit — see release_commits.go.
	rel.Commits = captureReleaseCommits(ctx, projectDir, rel, ledger.Releases, os.Stderr)
	created, err := ledger.Releases.Cut(ctx, rel)
	if err != nil {
		return releaseCutOutcome{}, fmt.Errorf("--release %s: record the release in %s: %w", version, ledger.Releases.Location(), err)
	}
	return releaseCutOutcome{
		Release: rel, Created: created, Ledger: ledger.Releases.Location(),
		Images: images, Packages: packages, Files: files,
	}, nil
}

// buildPlan carries the resolved inputs shared by buildParallel and
// buildSequential: the config, the frontend/go target sets, the
// docker-arch inputs, and the run's buildOptions. Grouping these keeps
// both dispatch functions to a single declarative parameter alongside ctx.
type buildPlan struct {
	cfg               *config.ProjectConfig
	frontends         []config.FrontendConfig
	dockerFrontends   []config.FrontendConfig
	goTargets         []goBuildTarget
	skipProjectDocker bool
	cfgArchForDocker  string
	resolvedTag       string
	// projectTag is the project image's tag: resolvedTag unless the project
	// image is pinned (buildTagFor).
	projectTag      string
	resolvedVersion versionInfo
	opts            buildOptions
	// memCaps are the child-compiler memory caps to apply under a
	// constrained memory budget (empty => apply nothing). Threaded into
	// buildGoTarget (GOMEMLIMIT/GOMAXPROCS) and buildFrontend (NODE_OPTIONS).
	memCaps buildMemoryCaps
}

func buildParallel(ctx context.Context, plan buildPlan) []buildResult {
	cfg := plan.cfg
	frontends, dockerFrontends := plan.frontends, plan.dockerFrontends
	goTargets := plan.goTargets
	skipProjectDocker := plan.skipProjectDocker
	cfgArchForDocker, resolvedTag := plan.cfgArchForDocker, plan.resolvedTag
	resolvedVersion := plan.resolvedVersion
	opts := plan.opts

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []buildResult
	)

	// Build the KCL-driven go targets + frontends in parallel.
	//
	// When the project image will CONSUME these host-built binaries (the
	// COPY-pattern Dockerfile: `COPY bin/<svc>` rather than an in-image
	// `RUN go build`), they must be built for the IMAGE platform — GOOS=linux
	// AND GOARCH=<deploy-target arch> — not the host. resolveBuildArch's plain
	// (dockerCtx=false) path collapses "target == host GOARCH" to "" (a native
	// build), which on a macOS host yields a Darwin/arm64 binary that the
	// Dockerfile then COPYs into a linux image → "exec format error" CrashLoop.
	// Resolving with the docker arch forces GOOS=linux/GOARCH=<arch> even when
	// the arch equals the host's, so the binary matches the image it ships in.
	goArch := resolveBuildArch(cfg.Deploy.TargetArch, opts.targetArch, false)
	if opts.buildDocker && len(goTargets) > 0 && !skipProjectDocker {
		goArch = resolveBuildArchForImage(cfgArchForDocker, opts.targetArch)
		goTargets = withTrimpath(goTargets)
	}
	for _, t := range goTargets {
		wg.Add(1)
		go func(t goBuildTarget) {
			defer wg.Done()
			r := buildGoTarget(ctx, t, opts.outputDir, opts.debug, goArch, resolvedVersion, plan.memCaps)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(t)
	}

	for _, fe := range frontends {
		wg.Add(1)
		go func(f config.FrontendConfig) {
			defer wg.Done()
			r := buildFrontend(ctx, f, plan.memCaps)
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		}(fe)
	}

	wg.Wait()

	// Check if any builds failed before attempting Docker
	hasBuildFailure := false
	for _, r := range results {
		if r.err != nil {
			hasBuildFailure = true
			break
		}
	}

	// Docker builds after binary builds succeed (only if --docker flag is set).
	// dockerArch is resolved with dockerCtx=true so cross-compile kicks in
	// whenever the deploy-target arch differs from the host — even if the
	// preceding go build above happened to use the host arch.
	if opts.buildDocker && !hasBuildFailure {
		// The image platform must match the arch the host go build above
		// produced (resolveBuildArchForImage when the project image consumes
		// those binaries), so a non-empty result here drives
		// `docker build --platform=linux/<arch>` and the COPYed binary's ELF
		// arch agrees with the image. Frontend docker builds (no host go
		// binary) keep the plain resolution.
		dockerArch := resolveBuildArch(cfgArchForDocker, opts.targetArch, true)
		if len(goTargets) > 0 && !skipProjectDocker {
			projectImageArch := resolveBuildArchForImage(cfgArchForDocker, opts.targetArch)
			guard := projectImageGuard{outputDir: opts.outputDir, binaryNames: projectImageBinaryNames(goTargets), envLabel: opts.env}
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := dockerBuildProject(ctx, cfg, opts.imageTags(cfg.Name, plan.projectTag), projectImageArch, resolvedVersion, guard)
				r.goTargets = goTargets
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}()
		}
		for _, fe := range dockerFrontends {
			wg.Add(1)
			go func(f config.FrontendConfig) {
				defer wg.Done()
				r := dockerBuild(ctx, cfg, f.Name, f.DeclaredDir(), opts.imageTags(f.Name, resolvedTag), dockerArch)
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}(fe)
		}
		wg.Wait()
	}

	return results
}

func buildSequential(ctx context.Context, plan buildPlan) []buildResult {
	cfg := plan.cfg
	frontends, dockerFrontends := plan.frontends, plan.dockerFrontends
	goTargets := plan.goTargets
	skipProjectDocker := plan.skipProjectDocker
	cfgArchForDocker, resolvedTag := plan.cfgArchForDocker, plan.resolvedTag
	resolvedVersion := plan.resolvedVersion
	opts := plan.opts

	var results []buildResult

	// See buildParallel: when the project image COPYs these host-built
	// binaries, they must be built for the image platform (GOOS=linux +
	// deploy-target GOARCH), never a native (possibly darwin) host build.
	goArch := resolveBuildArch(cfg.Deploy.TargetArch, opts.targetArch, false)
	if opts.buildDocker && len(goTargets) > 0 && !skipProjectDocker {
		goArch = resolveBuildArchForImage(cfgArchForDocker, opts.targetArch)
		goTargets = withTrimpath(goTargets)
	}
	for _, t := range goTargets {
		r := buildGoTarget(ctx, t, opts.outputDir, opts.debug, goArch, resolvedVersion, plan.memCaps)
		results = append(results, r)
		if r.err != nil {
			return results // Stop on first failure in sequential mode
		}
	}
	for _, fe := range frontends {
		r := buildFrontend(ctx, fe, plan.memCaps)
		results = append(results, r)
		if r.err != nil {
			return results
		}
	}

	// Docker builds only if --docker flag is set
	if opts.buildDocker {
		dockerArch := resolveBuildArch(cfgArchForDocker, opts.targetArch, true)
		if len(goTargets) > 0 && !skipProjectDocker {
			// Image platform == the arch the project binaries were built for.
			projectImageArch := resolveBuildArchForImage(cfgArchForDocker, opts.targetArch)
			guard := projectImageGuard{outputDir: opts.outputDir, binaryNames: projectImageBinaryNames(goTargets), envLabel: opts.env}
			r := dockerBuildProject(ctx, cfg, opts.imageTags(cfg.Name, plan.projectTag), projectImageArch, resolvedVersion, guard)
			r.goTargets = goTargets
			results = append(results, r)
			if r.err != nil {
				return results
			}
		}
		for _, fe := range dockerFrontends {
			r := dockerBuild(ctx, cfg, fe.Name, fe.DeclaredDir(), opts.imageTags(fe.Name, resolvedTag), dockerArch)
			results = append(results, r)
			if r.err != nil {
				return results
			}
		}
	}

	return results
}

// withTrimpath returns targets with trimpath set, without touching the
// caller's slice (the targets are also recorded on the build result).
func withTrimpath(targets []goBuildTarget) []goBuildTarget {
	out := make([]goBuildTarget, len(targets))
	for i, t := range targets {
		t.trimpath = true
		out[i] = t
	}
	return out
}

// goBuildTarget is one resolved `go build` invocation: a unique
// (cmd, output) pair plus the cross-compile/flag knobs from the
// service's KCL GoBuild. Multiple services that map to the same shared
// binary (server/worker/cron all → ./cmd/<project>) collapse to ONE
// target so the shared binary compiles once; build.go dedups by
// (Cmd, OutputName).
type goBuildTarget struct {
	cmd        string // go build target package, e.g. "./cmd/trader"
	outputName string // produced binary basename
	goos       string
	goarch     string
	ldflags    []string
	tags       []string
	flags      []string
	env        map[string]string
	// trimpath strips the build machine's source paths from the binary. Set
	// for targets that feed a container image, so the image's digest is a
	// function of the source and not of where it was compiled; the scaffolded
	// Dockerfile builds with -trimpath for the same reason.
	trimpath bool
}

// resolveGoTargets returns the KCL-driven go-build target set for this
// run. With --env (entities present) it iterates the declared services;
// otherwise it builds only the shared project binary at ./cmd/<project>.
// buildBinary==false (a frontend-only / external --target) drops the
// go-builds entirely.
func resolveGoTargets(buildBinary bool, entities *KCLEntities, cfg *config.ProjectConfig) []goBuildTarget {
	if !buildBinary {
		return nil
	}
	if entities != nil {
		return goBuildTargetsFromKCL(entities)
	}
	return []goBuildTarget{projectGoBuildTarget(cfg)}
}

// goBuildTargetsFromKCL resolves the unique set of go-build targets from
// the env's workloads — of every runtime, since a host go-run still needs
// its module to build. Only a declared GoBuild contributes (docker / shell
// dispatch elsewhere); a workload with no build is not built by forge. Dedup is by (cmd, outputName) so the shared
// project binary — which many server/worker/cron services map onto —
// builds exactly once. The first service to claim a (cmd, output) wins
// its flags; a divergent second declaration for the same target is a
// project misconfiguration, not forge's to reconcile.
func goBuildTargetsFromKCL(entities *KCLEntities) []goBuildTarget {
	if entities == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []goBuildTarget
	for _, w := range entities.Workloads {
		g := w.GoBuild()
		if g == nil {
			continue
		}
		outName := g.OutputName
		if outName == "" {
			outName = w.Name
		}
		key := g.Cmd + "\x00" + outName
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, goBuildTarget{
			cmd:        g.Cmd,
			outputName: outName,
			goos:       g.GOOS,
			goarch:     g.GOARCH,
			ldflags:    g.Ldflags,
			tags:       g.Tags,
			flags:      g.Flags,
			env:        g.Env,
		})
	}
	return out
}

// buildGoTarget runs ONE `go build` for a KCL-resolved goBuildTarget. It
// is a PURE EXECUTOR of the declaration: the target package (cmd), the
// cross-compile arch, ldflags/tags/extra-flags, and build-time env all
// come from KCL — there is no hardcoded ./cmd and no single-binary
// assumption. The version-stamping ldflags forge always injected are
// PREpended (KCL ldflags win on the same -X key since they come later),
// preserving the main.version/commit/date stamp without a forge.yaml
// build block.
//
// crossArch is the build-context arch resolution (host vs deploy-target);
// an explicit GoBuild.goarch overrides it. debug swaps the stripped
// ldflags for delve gcflags.
func buildGoTarget(ctx context.Context, t goBuildTarget, outputDir string, debug bool, crossArch string, versionInfo versionInfo, memCaps buildMemoryCaps) buildResult {
	start := time.Now()
	// An explicit GoBuild.goarch wins over the build-context arch.
	arch := crossArch
	if t.goarch != "" {
		arch = t.goarch
	}
	// When cross-compiling (arch set) GOOS defaults to linux — the deploy
	// target — unless the GoBuild pins an explicit goos.
	targetOS := t.goos
	if arch != "" && targetOS == "" {
		targetOS = "linux"
	}
	// Name the output for the OS it is built for: a Windows target is a
	// .exe, which hostlaunch's binary/delve runners expect under bin/.
	binaryOS := targetOS
	if binaryOS == "" {
		binaryOS = runtime.GOOS
	}
	binaryPath := filepath.Join(outputDir, hostlaunch.ExeName(binaryOS, t.outputName))

	if debug {
		fmt.Printf("[build] %s: go build (debug) %s -> %s\n", t.outputName, t.cmd, binaryPath)
	} else {
		fmt.Printf("[build] %s: go build %s -> %s\n", t.outputName, t.cmd, binaryPath)
	}
	if arch != "" {
		fmt.Printf("[build] cross-compiling for %s/%s (host: %s/%s)\n",
			targetOS, arch, runtime.GOOS, runtime.GOARCH)
	}

	args := []string{"build", "-o", binaryPath}
	if debug {
		// -trimpath=false overrides a -trimpath in GOFLAGS (the Reliant
		// workspace image makes it the toolchain default): a debugger sets
		// breakpoints by absolute source path, and a trimmed binary records
		// only module-relative ones, so every breakpoint would miss.
		args = append(args, "-gcflags=all=-N -l", "-trimpath=false")
	} else {
		// Version stamp first; the KCL ldflags follow so a project that
		// wants to override main.version (e.g. -X main.version=<tag>) wins
		// on the same -X key. The stamp covers main AND the scaffold's
		// cobra-tree package (versionStampPackages), so a server binary's
		// `version` and its error-report release are the build's, not "dev".
		ldflags := "-s -w " + versionStampLdflags(buildModulePath(), t.cmd, versionInfo)
		if len(t.ldflags) > 0 {
			ldflags += " " + strings.Join(t.ldflags, " ")
		}
		args = append(args, "-ldflags", ldflags)
	}
	if t.trimpath && !debug {
		args = append(args, "-trimpath")
	}
	if len(t.tags) > 0 {
		args = append(args, "-tags", strings.Join(t.tags, ","))
	}
	// Arbitrary extra go-build flags (e.g. ["-cover"] for an e2e env).
	args = append(args, t.flags...)
	args = append(args, t.cmd)

	if err := checkBuildStorageFn(outputDir); err != nil {
		return buildResult{name: t.outputName, kind: "service", duration: time.Since(start), err: err}
	}
	cmd := goexec.Graceful(exec.CommandContext(ctx, "go", args...))
	// CGO_ENABLED=0 is forge's pure-Go contract; a GoBuild.env entry can
	// override it (and any other build-time var) since it's appended last.
	cmd.Env = goexec.Env("CGO_ENABLED=0")
	if arch != "" {
		cmd.Env = append(cmd.Env, "GOOS="+targetOS, "GOARCH="+arch)
	} else if targetOS != "" {
		cmd.Env = append(cmd.Env, "GOOS="+targetOS)
	}
	for k, v := range t.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Constrained-budget caps last so they bound the compiler even over an
	// inherited GOMEMLIMIT/GOMAXPROCS. Skipped when empty (unconstrained).
	cmd.Env = applyGoMemoryCaps(cmd.Env, memCaps)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	return buildResult{
		name:     t.outputName,
		kind:     "service",
		duration: time.Since(start),
		err:      err,
	}
}

// projectGoBuildTarget is the fallback go-build target for a plain
// `forge build` with NO --env (no KCL service set to iterate). It builds
// the shared project binary at ./cmd/<project>. This is NOT the legacy
// `./cmd` hardcode — it points at the real cmd/<project> package the
// scaffold writes. With --env set the KCL service set drives the builds
// and this is unused.
//
// The cmd/<bin>/ leaf is the RAW forge.yaml project name — the same
// convention every other resolver uses (bootstrapBinaryName, add's
// binaryName, the generator's binaryName, and the KCL EffectiveBuild
// default of ./cmd/<name>). It must NOT go through
// naming.ServicePackage: that mangles hyphens to underscores
// ("control-plane" → "./cmd/control_plane"), pointing the env-less
// build at a directory the scaffold never wrote.
func projectGoBuildTarget(cfg *config.ProjectConfig) goBuildTarget {
	name := cfg.Name
	return goBuildTarget{
		cmd:        "./cmd/" + name,
		outputName: name,
	}
}

// versionInfo captures the source-of-truth version/commit/date injected into
// built binaries via -ldflags. Fields fall back to a time-based dev version /
// "none" / the current timestamp when the project is not a git repo.
type versionInfo struct {
	version string
	commit  string
	date    string
}

// resolveBuildVersion is the ONE version resolver shared by the host
// binary build and the docker image build, so both embed the identical
// version for a given build. The previous split — gitVersionInfo on the
// host side, an in-container `git describe` on the docker side — was the
// root cause of the "every image is main.version=dev" bug: .dockerignore
// excludes .git, so the in-container describe always failed.
//
// version policy, in order:
//
//	a. override (non-empty) — `--tag`, else forge.yaml build.version.
//	b. `git describe --tags --always --dirty` — semver when tagged,
//	   commit-ish otherwise.
//	c. `git rev-parse --short HEAD` — commit fallback for shallow / no-
//	   describe repos.
//	d. fmt.Sprintf("0.0.0-dev.%d", <unix seconds>) — time-based dev
//	   fallback when there is no git at all.
//
// commit: `git rev-parse HEAD`, else "none". date: now in RFC3339 UTC.
func resolveBuildVersion(ctx context.Context, override string) versionInfo {
	info := versionInfo{
		commit: "none",
		date:   time.Now().UTC().Format(time.RFC3339),
	}

	switch {
	case override != "":
		info.version = override
	default:
		if out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				info.version = v
			}
		}
		if info.version == "" {
			if out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output(); err == nil {
				if v := strings.TrimSpace(string(out)); v != "" {
					info.version = v
				}
			}
		}
		if info.version == "" {
			info.version = fmt.Sprintf("0.0.0-dev.%d", time.Now().Unix())
		}
	}

	if out, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output(); err == nil {
		if c := strings.TrimSpace(string(out)); c != "" {
			info.commit = c
		}
	}
	return info
}

// (gitVersionTag was removed when build/deploy converged on the single
// `resolveImageTag` helper — see internal/cli/image_tag.go. The same
// `git describe --tags --always --dirty` shape now lives there as the
// shared source of truth both `forge build` and `forge env deploy` consume.)

func buildFrontend(ctx context.Context, fe config.FrontendConfig, memCaps buildMemoryCaps) buildResult {
	start := time.Now()
	// DeclaredDir, not Dir: by this point resolveBuildFrontendSources has
	// REWRITTEN the path of every source-pinned frontend to the
	// materialized cache directory, which is outside the project tree on
	// purpose. Re-deriving through Dir would apply a containment check to
	// a path that is legitimately external and undo the resolution.
	feDir := fe.DeclaredDir()
	if err := checkBuildStorageFn(feDir); err != nil {
		return buildResult{name: fe.Name, kind: "frontend", duration: time.Since(start), err: err}
	}
	// forge.yaml's dev_runner picks the package manager; `<runner> run build`
	// is the same invocation for npm, pnpm and yarn.
	runner := fe.EffectiveDevRunner()
	// A fresh checkout (every release worktree) has no node_modules; without
	// this `next build` dies with "command not found". Same step as `env up`.
	if err := frontenddeps.Ensure(ctx, "[build]", fe.Name, feDir, runner, true); err != nil {
		return buildResult{name: fe.Name, kind: "frontend", duration: time.Since(start), err: err}
	}
	fmt.Printf("[build] %s: NODE_ENV=production %s run build in %s\n", fe.Name, runner, feDir)

	cmd := exec.CommandContext(ctx, runner, "run", "build")
	cmd.Dir = feDir
	cmd.Env = withForcedEnv(os.Environ(), "NODE_ENV", "production")
	// Cap V8's heap under a constrained budget so `next build` can't
	// balloon past the envelope. Merged with any inherited NODE_OPTIONS so
	// a project's own flags survive; forge's cap is appended (wins on the
	// repeated --max-old-space-size key). No-op when unconstrained.
	if memCaps.nodeOptions != "" {
		cmd.Env = withMergedNodeOptions(cmd.Env, memCaps.nodeOptions)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	return buildResult{
		name:     fe.Name,
		kind:     "frontend",
		duration: time.Since(start),
		err:      err,
	}
}

// withMergedNodeOptions appends forge's NODE_OPTIONS flags to any inherited
// NODE_OPTIONS (rather than replacing it), so a project's own Node flags are
// preserved. When both set --max-old-space-size, Node honours the LAST
// occurrence, and forge's is appended last — so forge's cap wins by design.
func withMergedNodeOptions(env []string, add string) []string {
	const key = "NODE_OPTIONS="
	rewritten := make([]string, 0, len(env)+1)
	merged := false
	for _, entry := range env {
		if strings.HasPrefix(entry, key) {
			existing := strings.TrimPrefix(entry, key)
			rewritten = append(rewritten, key+strings.TrimSpace(existing+" "+add))
			merged = true
			continue
		}
		rewritten = append(rewritten, entry)
	}
	if !merged {
		rewritten = append(rewritten, key+add)
	}
	return rewritten
}

func withForcedEnv(env []string, key, value string) []string {
	prefix := key + "="
	rewritten := make([]string, 0, len(env)+1)
	replaced := false

	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if !replaced {
				rewritten = append(rewritten, prefix+value)
				replaced = true
			}
			continue
		}
		rewritten = append(rewritten, entry)
	}

	if !replaced {
		rewritten = append(rewritten, prefix+value)
	}

	return rewritten
}

// projectImageGuard carries the inputs the build-time ELF/arch backstop
// (assertProjectImageBinaries, called inside dockerBuildProject) needs: where
// the host go-builds wrote their binaries, the basenames the project image
// will COPY, and the deploy env label for the error text. Bundled into one
// struct so the dockerBuildProject signature doesn't grow three more params.
type projectImageGuard struct {
	outputDir   string
	binaryNames []string
	envLabel    string
}

// projectImageBinaryNames returns the output basenames of the go-build
// targets — the binaries the project image's COPY-pattern Dockerfile consumes.
// The guard stats each in outputDir, so a target whose binary the Dockerfile
// doesn't actually COPY simply isn't found and is skipped (see
// assertProjectImageBinaries).
func projectImageBinaryNames(goTargets []goBuildTarget) []string {
	names := make([]string, 0, len(goTargets))
	for _, t := range goTargets {
		names = append(names, t.outputName)
	}
	return names
}

// dockerBuildProject builds the single project Docker image from the
// root Dockerfile, tagged with every tag in tags.local, and pushes
// tags.push after a successful build (buildOptions.imageTags computes both).
//
// crossArch, when non-empty, drives `docker buildx build --platform=linux/<arch>`
// so the resulting image runs on a node whose arch matches the deploy
// target rather than the build host. Empty means "let docker use the
// host arch" — appropriate when host == target.
//
// A `--release` build's tags carry the release version ONLY — no `:latest` —
// so a cut never moves a shared tag. See imageTagSet.
func dockerBuildProject(ctx context.Context, cfg *config.ProjectConfig, tags dockerImageTags, crossArch string, resolvedVersion versionInfo, guard projectImageGuard) buildResult {
	start := time.Now()
	dockerfile := "Dockerfile"

	if _, err := os.Stat(dockerfile); os.IsNotExist(err) {
		fmt.Printf("[build] %s: skipping docker (no Dockerfile)\n", cfg.Name)
		return buildResult{
			name:     cfg.Name + " (docker)",
			kind:     "docker",
			duration: time.Since(start),
			err:      nil,
		}
	}

	// Backstop guard: the production Dockerfile stage COPYs the host-built
	// binaries straight into a distroless/static Linux image (no in-image
	// `RUN go build`). Assert each is a Linux ELF for the image's arch BEFORE
	// docker build, so a native macOS Mach-O / wrong-arch binary (the class of
	// regression where the GOOS=linux GOARCH=<arch> resolution was skipped)
	// fails the build with an actionable error rather than shipping an image
	// that CrashLoopBackOffs with `exec format error` on every Linux node.
	// crossArch is the resolved image GOARCH (resolveBuildArchForImage), the
	// exact arch the binaries above were built for. Empty crossArch (no cross
	// resolution requested) skips the arch pin but still requires ELF.
	if err := assertProjectImageBinaries(guard.outputDir, crossArch, guard.envLabel, guard.binaryNames); err != nil {
		return buildResult{
			name:     cfg.Name + " (docker)",
			kind:     "docker",
			duration: time.Since(start),
			err:      err,
		}
	}

	if err := requireImageDaemon(ctx, "the project image ("+dockerfile+")"); err != nil {
		return buildResult{name: cfg.Name + " (docker)", kind: "docker", duration: time.Since(start), err: err}
	}

	// The project image's context is the whole repository, so its epoch is
	// the last commit to it.
	dockerArgs := appendSourceDateEpoch([]string{"build"}, resolveSourceDateEpoch(ctx, "."))
	// Pass the resolved build version into the image build as build-args.
	// The Dockerfile bakes these into -ldflags (FORGE_VERSION/COMMIT/DATE),
	// replacing the old in-container `git describe` that always failed
	// because .dockerignore excludes .git. The VersionVar PATH is baked
	// into the Dockerfile at generate time, so only the VALUE flows here.
	dockerArgs = append(dockerArgs,
		"--build-arg", "FORGE_VERSION="+resolvedVersion.version,
		"--build-arg", "FORGE_COMMIT="+resolvedVersion.commit,
		"--build-arg", "FORGE_DATE="+resolvedVersion.date,
	)
	if crossArch != "" {
		dockerArgs = append(dockerArgs, "--platform=linux/"+crossArch)
		fmt.Printf("[build] cross-compiling for linux/%s (host: %s/%s)\n",
			crossArch, runtime.GOOS, runtime.GOARCH)
	}
	for _, t := range tags.local {
		dockerArgs = append(dockerArgs, "-t", t)
	}
	pushTags := tags.push
	// Additional build contexts from forge.yaml's docker.build_contexts.
	// Each becomes a `--build-context name=value` arg, letting the
	// Dockerfile pull files from outside the normal context via
	// `FROM name` / `COPY --from=name`. See [config.DockerConfig.BuildContexts]
	// for the supported value shapes (relative path, absolute path,
	// `docker-image://`, …). cwd is the project root by construction
	// (forge build runs alongside forge.yaml).
	dockerArgs = appendBuildContexts(dockerArgs, cfg, "")
	fmt.Printf("[build] %s: docker build (%d tags)\n", cfg.Name, countTags(dockerArgs))
	dockerArgs = append(dockerArgs, "-f", dockerfile, ".")

	if err := prepareDockerBuildStorage(ctx, projectDirForKCL()); err != nil {
		return buildResult{name: cfg.Name + " (docker)", kind: "docker", duration: time.Since(start), err: err}
	}
	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return buildResult{
			name:     cfg.Name + " (docker)",
			kind:     "docker",
			duration: time.Since(start),
			err:      err,
		}
	}

	// Push every push-registry tag if requested.
	for _, t := range pushTags {
		fmt.Printf("[build] %s: docker push %s\n", cfg.Name, t)
		if err := dockerPush(ctx, t); err != nil {
			return buildResult{
				name:     cfg.Name + " (docker)",
				kind:     "docker",
				duration: time.Since(start),
				err:      err,
			}
		}
	}

	// Capture the pushed image's content-addressed digest so deploy can pin
	// the manifest to `<image>@sha256:...` (immutable, cache-proof) instead
	// of the mutable `:tag`. Inspect the last pushed ref — the version tag
	// when present (most specific), else `:latest`; both resolve to the same
	// manifest digest. Best-effort: a lookup failure logs and records no
	// digest, leaving deploy on the unchanged tag-fallback path.
	digest, platforms := "", []string(nil)
	if len(pushTags) > 0 {
		ref := pushTags[len(pushTags)-1]
		if d, p, derr := imageRepoDigest(ctx, ref); derr == nil {
			digest, platforms = d, p
			fmt.Printf("[build] %s: pushed digest %s\n", cfg.Name, digest)
		} else {
			fmt.Printf("[build]   Note: could not capture image digest for %s (%v); deploy will use the tag\n", ref, derr)
		}
	}

	return buildResult{
		name:       cfg.Name + " (docker)",
		kind:       "docker",
		duration:   time.Since(start),
		err:        nil,
		image:      cfg.Name,
		repository: tags.repository,
		tag:        tags.tag,
		digest:     digest,
		platforms:  platforms,
	}
}

// dockerImageTags is the tag set for one image build: every `-t` the
// `docker build` applies (local), and the subset `docker push` sends to the
// registry (push).
type dockerImageTags struct {
	// repository is the declared push destination, not a display name or tag.
	repository string
	local      []string
	push       []string
	// tag is the version tag the set carries (the non-`latest` one): the tag
	// the image's build state records.
	tag string
}

// imageTagSet computes the tags one forge-built image gets, for all three
// docker paths (project image, frontend image, per-service DockerBuild) — so
// "which tags does a build write" has ONE answer rather than three loops kept
// in step by hand.
//
// `repository` is the FULL declared repository, registry host included — the
// workload's own `image` minus any tag. There is no separate registry
// parameter and no prefixing step: the destination was decided when the author
// wrote the reference, and this only appends tags to it.
//
// An image is tagged `:latest` plus resolvedTag, and pushed to those same refs
// when `push` is set. The version tag is pushed LAST because the digest
// capture inspects the last pushed ref. A local k3d repository is ALSO tagged
// under its `registry.localhost` mirror (tagged, never pushed — the host
// cannot resolve that name), which is how the kubelet inside the node
// container pulls what the host pushed to localhost.
//
// releaseScoped (a `--release` build) drops `:latest` entirely. A release
// writes its images under the release version and NOTHING else, so a cut that
// fails after its first push leaves every shared tag exactly where it was.
// See releaseImageTag for the incident this closes.
func imageTagSet(repository, resolvedTag string, push, releaseScoped bool) dockerImageTags {
	out := dockerImageTags{repository: repository, tag: resolvedTag}
	seen := map[string]bool{}
	add := func(repo string, pushed bool) {
		refs := make([]string, 0, 2)
		if !releaseScoped {
			refs = append(refs, repo+":latest")
		}
		if resolvedTag != "" {
			refs = append(refs, repo+":"+resolvedTag)
		}
		for _, r := range refs {
			if !seen[r] {
				seen[r] = true
				out.local = append(out.local, r)
			}
		}
		if pushed {
			out.push = append(out.push, refs...)
		}
	}
	add(repository, push)
	for _, mirror := range k3dMirrorRepositories(repository) {
		add(mirror, false)
	}
	return out
}

// imageTags is the tag set this build writes for the artifact named `image`:
// tagged (and pushed, when this build pushes) under the repository its workload
// declared, and release-scoped when --release is set. The one place a build's
// options turn into an image's tags.
func (opts buildOptions) imageTags(image, resolvedTag string) dockerImageTags {
	return imageTagSet(opts.pushPlan.repositoryFor(image), resolvedTag,
		opts.pushPlan.push, releaseImageTag(opts) != "")
}

// k3dMirrorRepositories is the additional LOCAL tag a `localhost:<port>`
// repository gets: the same path under `registry.localhost:<port>`. The
// canonical k3d pattern — the host pushes to localhost and the kubelet inside
// the node container pulls from registry.localhost, with deploy/k3d.yaml's
// mirrors block mapping both to one backend. Nil for any other host.
func k3dMirrorRepositories(repository string) []string {
	host, path, ok := strings.Cut(repository, "/")
	if !ok || !strings.HasPrefix(host, "localhost:") {
		return nil
	}
	port := strings.TrimPrefix(host, "localhost:")
	return []string{"registry.localhost:" + port + "/" + path}
}

// countTags counts the `-t` flags in a docker build arg list for the
// progress line. Cheap; only used for human-readable output.
func countTags(args []string) int {
	n := 0
	for _, a := range args {
		if a == "-t" {
			n++
		}
	}
	return n
}

// dockerBuild builds a Docker image for a frontend from its own
// Dockerfile, tagged with tags.local, and pushes tags.push after a
// successful build.
//
// crossArch, when non-empty, drives `docker buildx build --platform=linux/<arch>`
// so frontends destined for the deploy-target node arch are built
// correctly even on a different host arch. Same semantics as
// dockerBuildProject, including release-scoped tags (see imageTagSet).
func dockerBuild(ctx context.Context, cfg *config.ProjectConfig, name, path string, tags dockerImageTags, crossArch string) buildResult {
	start := time.Now()
	dockerfile := filepath.Join(path, "Dockerfile")

	if _, err := os.Stat(dockerfile); os.IsNotExist(err) {
		fmt.Printf("[build] %s: skipping docker (no Dockerfile)\n", name)
		return buildResult{
			name:     name + " (docker)",
			kind:     "docker",
			image:    name,
			duration: time.Since(start),
			err:      nil,
		}
	}

	dockerArgs := appendSourceDateEpoch([]string{"build"}, resolveSourceDateEpoch(ctx, ".", path))
	if crossArch != "" {
		dockerArgs = append(dockerArgs, "--platform=linux/"+crossArch)
		fmt.Printf("[build] cross-compiling for linux/%s (host: %s/%s)\n",
			crossArch, runtime.GOOS, runtime.GOARCH)
	}
	for _, t := range tags.local {
		dockerArgs = append(dockerArgs, "-t", t)
	}
	pushTags := tags.push
	// Additional build contexts from forge.yaml. Same semantics as
	// dockerBuildProject — useful when the frontend Dockerfile needs
	// to reference paths outside its own subtree.
	dockerArgs = appendBuildContexts(dockerArgs, cfg, "")
	fmt.Printf("[build] %s: docker build (%d tags)\n", name, countTags(dockerArgs))
	dockerArgs = append(dockerArgs, "-f", dockerfile, path)

	if err := requireImageDaemon(ctx, "frontend image "+name+" ("+dockerfile+")"); err != nil {
		return buildResult{name: name + " (docker)", kind: "docker", image: name, duration: time.Since(start), err: err}
	}
	if err := prepareDockerBuildStorage(ctx, projectDirForKCL()); err != nil {
		return buildResult{name: name + " (docker)", kind: "docker", duration: time.Since(start), err: err}
	}
	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return buildResult{
			name:     name + " (docker)",
			kind:     "docker",
			image:    name,
			duration: time.Since(start),
			err:      err,
		}
	}

	for _, t := range pushTags {
		fmt.Printf("[build] %s: docker push %s\n", name, t)
		if err := dockerPush(ctx, t); err != nil {
			return buildResult{
				name:     name + " (docker)",
				kind:     "docker",
				image:    name,
				duration: time.Since(start),
				err:      err,
			}
		}
	}

	// Capture the pushed digest, exactly as dockerBuildProject does for the
	// project image. A frontend deployed as forge.K8sCluster is an image like
	// any other, and without this its build recorded nothing — so
	// `forge env deploy --target <frontend>` silently fell back to whatever
	// digest the release ledger still pinned and RE-DEPLOYED THE OLD IMAGE,
	// reporting success. That shipped a months-stale operator console against
	// a current backend.
	digest, platforms := "", []string(nil)
	if len(pushTags) > 0 {
		ref := pushTags[len(pushTags)-1]
		if d, p, derr := imageRepoDigest(ctx, ref); derr == nil {
			digest, platforms = d, p
			fmt.Printf("[build] %s: pushed digest %s\n", name, digest)
		} else {
			fmt.Printf("[build]   Note: could not capture image digest for %s (%v); deploy will use the tag\n", ref, derr)
		}
	}

	return buildResult{
		name:       name + " (docker)",
		kind:       "docker",
		duration:   time.Since(start),
		err:        nil,
		image:      name,
		repository: tags.repository,
		tag:        tags.tag,
		digest:     digest,
		platforms:  platforms,
	}
}

func filterFrontends(frontends []config.FrontendConfig, target string) []config.FrontendConfig {
	for _, f := range frontends {
		if f.Name == target {
			return []config.FrontendConfig{f}
		}
	}
	return nil
}

// kclFrontendAsBuildTarget finds the named frontend in the rendered env
// and converts it to the build-set shape, or nil when the env declares
// no such frontend.
//
// It is deliberately NOT a fallback for every frontend: filterFrontends
// runs first, so a name the codegen inventory already carries keeps that
// entry. The inventory is the project's own declaration and is not
// per-env, whereas a KCL entry is only as true as the environment being
// built. This is the widening for the case the inventory cannot express
// at all.
//
// Path is taken verbatim from the entity because by this point
// resolveFrontendEntitySources has REWRITTEN it to the materialized
// source directory. Falling back to the frontends/<name> convention here
// would hand `npm run build` a path that does not exist.
func kclFrontendAsBuildTarget(e *KCLEntities, target string) *config.FrontendConfig {
	if e == nil || target == "" {
		return nil
	}
	for _, fe := range e.Frontends {
		if fe.Name != target {
			continue
		}
		// WithDir carries the entity path VERBATIM — it has already been
		// rewritten to the materialized source directory by
		// resolveFrontendEntitySources, so this is a resolved location,
		// not a declaration. (WithDir clears Source for exactly that
		// reason: the code is on disk now.)
		fe2 := config.FrontendConfig{Name: fe.Name, Type: fe.Type}.WithDir(fe.Path)
		return &fe2
	}
	return nil
}

// filterFrontendsForBuild drops the frontends this env does not build with
// a plain `npm run build`, by runtime:
//
//   - forge.OnHost — the dev server (`npm run dev` in forge env up) never
//     consumes the production artifact, so building it is pure waste;
//   - forge.OnHosted — built by buildHostedStaticSites, environment-agnostic
//     and pushed as a release artifact; a plain build here would repeat it.
//
// Every other runtime (bucket, firebase, build-only) is kept. A frontend in
// cfg.Frontends that the env does not declare is kept too: with no runtime
// to read there is nothing to skip on.
//
// Prints a one-line note per skipped dev server so users can see at a
// glance why their build finished early.
func filterFrontendsForBuild(frontends []config.FrontendConfig, entities *KCLEntities) []config.FrontendConfig {
	if entities == nil {
		return frontends
	}
	kept := make([]config.FrontendConfig, 0, len(frontends))
	for _, fe := range frontends {
		entity, ok := findFrontendEntity(entities, fe.Name)
		switch {
		case ok && entity.Runtime.Type == FrontendRuntimeHost:
			fmt.Printf("[build] skipping prod build for %s (forge.OnHost: the dev server)\n", fe.Name)
			continue
		case ok && frontendIsHosted(entity):
			continue
		}
		kept = append(kept, fe)
	}
	return kept
}

// projectDirForKCL resolves the project root directory used as the
// argument to RenderKCL. Falls back to "." when forge.yaml isn't found
// (the kcl shell-out will still surface the error with a useful path).
func projectDirForKCL() string {
	if cfgPath, perr := findProjectConfigFile(); perr == nil {
		return filepath.Dir(cfgPath)
	}
	return "."
}

// summarizeKCLBuildPlan prints the per-runtime split so users see, in one
// glance, where each workload of this `forge build <env>` runs and whether
// forge builds its image.
func summarizeKCLBuildPlan(e *KCLEntities) {
	if e == nil {
		return
	}
	line := func(label string, names []string) {
		if len(names) > 0 {
			fmt.Printf("[build]   %-26s %s\n", label+":", strings.Join(names, ", "))
		}
	}
	line("Host (skip docker)", e.WorkloadNames(RuntimeHost))
	line("Compose (compose builds)", e.WorkloadNames(RuntimeCompose))
	for _, rt := range []string{RuntimeCluster, RuntimeHosted} {
		var built, named []string
		for _, w := range e.WorkloadsOn(rt) {
			if w.Build.Type != "" {
				built = append(built, w.Name)
			} else {
				named = append(named, w.Name)
			}
		}
		line(rt+" (built here)", built)
		line(rt+" (declared image)", named)
	}
	line("Build-only (binary)", e.WorkloadNames(RuntimeBuildOnly))
	if len(e.Frontends) > 0 {
		names := make([]string, 0, len(e.Frontends))
		for _, f := range e.Frontends {
			names = append(names, f.Name)
		}
		line("Frontends (skip docker)", names)
	}
}

// findFrontendEntity returns the named frontend from the rendered env.
func findFrontendEntity(e *KCLEntities, name string) (FrontendEntity, bool) {
	if e == nil {
		return FrontendEntity{}, false
	}
	for _, f := range e.Frontends {
		if f.Name == name {
			return f, true
		}
	}
	return FrontendEntity{}, false
}

// envNeedsProjectImage reports whether this env needs the PROJECT image —
// the image `forge build` produces from the project's Dockerfile, which
// every forge-built Go workload runs (its args select the subcommand). True
// when some workload with a GoBuild runs from an image: bound to a cluster,
// to the control plane, or shipped build-only.
//
// It asks "does forge need to BUILD an image for this env", not "does this
// env touch a cluster": a workload that only names a third-party image
// (`image` set, no build) is not counted, and a host workload runs its
// binary directly.
func envNeedsProjectImage(e *KCLEntities) bool {
	for _, w := range e.Workloads {
		if w.GoBuild() == nil {
			continue
		}
		switch w.Runtime.Type {
		case RuntimeCluster, RuntimeHosted, RuntimeBuildOnly:
			return true
		}
	}
	return false
}

// kclFirstClusterPlatform returns the platform (GOARCH) of the first
// cluster service whose deploy.Cluster.Platform is non-empty. KCL renders
// all cluster services in one env onto the same node arch in practice,
// so the first hit is the env-wide default. Returns "" when no cluster
// service declares a platform — callers fall back to forge.yaml's
// deploy.target_arch.
func kclFirstClusterPlatform(e *KCLEntities) string {
	// The env's declared platform, when stated, is the project image's
	// arch — not whichever cross-cluster service renders first.
	if p := e.ClusterTarget.field("platform"); p != "" {
		return p
	}
	for _, w := range e.WorkloadsOn(RuntimeCluster) {
		if w.Runtime.Cluster.Platform != "" {
			return w.Runtime.Cluster.Platform
		}
	}
	return ""
}

// kclEnvImagePlatform is the GOARCH the env's built images must run on, as the
// env DECLARES it: the cluster platform (kclFirstClusterPlatform), else the
// platform of the control plane a built workload is bound to. The hosted arch
// is a property of the platform's nodes, never of the machine running
// the build — an arm64 Mac building a hosted image must still produce amd64.
// "" when the env declares neither.
func kclEnvImagePlatform(e *KCLEntities) string {
	if e == nil {
		return ""
	}
	if p := kclFirstClusterPlatform(e); p != "" {
		return p
	}
	for _, w := range e.WorkloadsOn(RuntimeHosted) {
		if w.Build.Type != "" {
			return w.Runtime.HostedPlatform()
		}
	}
	return ""
}

// requireDeclaredImageArch refuses to let a pushed image's arch fall through to
// the BUILD HOST's arch when the image will run on a remote runtime. declared
// is the arch the env/forge.yaml/--target-arch resolved ("" = none). A purely
// local runtime (host, compose, local k3d) is exempt: there the host IS the
// target, and resolveBuildArchForImage's GOARCH fallback is correct.
func requireDeclaredImageArch(e *KCLEntities, declared, flagArch string, pushes bool, env string) error {
	if declared != "" || flagArch != "" || !pushes || e == nil {
		return nil
	}
	for _, w := range e.Workloads {
		if w.Build.Type == "" || w.Runtime.Type != RuntimeCluster {
			continue
		}
		if ctx := clusterContextOf(w.Runtime.Cluster); ctx != "" && !isLocalCluster(ctx) {
			return fmt.Errorf("env %q pushes image(s) for workload %q on remote cluster %q, but no target architecture is declared.\n"+
				"  expected: a node arch (amd64/arm64) for the image; found: none — forge will not guess the build host's arch (%s) for an image another machine runs\n"+
				"  fix: set `platform` on the env's forge.ClusterTarget, or `deploy.target_arch` in forge.yaml, or pass --target-arch",
				env, w.Name, ctx, runtime.GOARCH)
		}
	}
	return nil
}

// buildKCLBuildOnlyVariants compiles each declared build-only variant
// into bin/<service>-<variant> with the variant's ldflags and build
// tags. Each variant is a separate `go build` invocation; failures are
// captured in the returned buildResult slice rather than short-circuited
// so users see the full list of failures from one run.
func buildKCLBuildOnlyVariants(ctx context.Context, e *KCLEntities, outputDir string) []buildResult {
	var out []buildResult
	for _, w := range e.WorkloadsOn(RuntimeBuildOnly) {
		// The variant's go-build TARGET is the workload's GoBuild cmd: each
		// variant layers its own ldflags/tags/arch on that one package. A
		// build-only workload with variants and no GoBuild has nothing to
		// build them from.
		g := w.GoBuild()
		if len(w.Runtime.BuildOnly.BuildVariants) > 0 && (g == nil || g.Cmd == "") {
			out = append(out, buildResult{name: w.Name, kind: "go", err: fmt.Errorf(
				"build-only workload %s declares build_variants but no GoBuild to build them from", w.Name)})
			continue
		}
		for _, v := range w.Runtime.BuildOnly.BuildVariants {
			out = append(out, buildVariant(ctx, w.Name, g.Cmd, v, outputDir))
		}
	}
	return out
}

// buildVariant builds one binary for a build-only service variant.
// buildCmd is the service's resolved go-build target package (from the
// Build union — NOT a ./cmd hardcode). The output name is
// <service>-<variant> unless v.OutputName overrides it. ldflags and -tags
// are appended to the go-build args; env_at_build pairs join CGO_ENABLED=0
// on the subprocess env.
func buildVariant(ctx context.Context, svcName, buildCmd string, v BuildVariant, outputDir string) buildResult {
	start := time.Now()
	outName := v.OutputName
	if outName == "" {
		outName = svcName + "-" + v.Name
	}
	variantOS := v.GOOS
	if variantOS == "" {
		variantOS = runtime.GOOS
	}
	binPath := filepath.Join(outputDir, hostlaunch.ExeName(variantOS, outName))
	fmt.Printf("[build] %s (variant %s): go build %s -> %s\n", svcName, v.Name, buildCmd, binPath)

	args := []string{"build", "-o", binPath}
	if len(v.Ldflags) > 0 {
		args = append(args, "-ldflags", strings.Join(v.Ldflags, " "))
	}
	if len(v.BuildTags) > 0 {
		args = append(args, "-tags", strings.Join(v.BuildTags, ","))
	}
	args = append(args, buildCmd)

	if err := checkBuildStorageFn(outputDir); err != nil {
		return buildResult{name: svcName + ":" + v.Name, kind: "variant", duration: time.Since(start), err: err}
	}
	cmd := goexec.Graceful(exec.CommandContext(ctx, "go", args...))
	env := goexec.Env("CGO_ENABLED=0")
	if v.GOOS != "" {
		env = append(env, "GOOS="+v.GOOS)
	}
	if v.GOARCH != "" {
		env = append(env, "GOARCH="+v.GOARCH)
	}
	for k, val := range v.EnvAtBuild {
		env = append(env, k+"="+val)
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	return buildResult{
		name:     svcName + ":" + v.Name,
		kind:     "variant",
		duration: time.Since(start),
		err:      err,
	}
}

// buildKCLDockerShell dispatches the per-service DockerBuild type — the
// only per-service build run here. ShellBuild services are dispatched by
// the single external-build dispatcher (buildExternalServices) so they
// share ONE execution + state/digest path with the rest of the shell
// escape hatch; there is no separate shell branch here anymore.
//
//   - docker → `docker build` reusing forge's existing image-build
//     primitives (tags via the env-declared registry + resolvedTag, push when
//     --push, build-contexts) with the service's dockerfile / platform /
//     target / build_args. A DockerBuild is the ONLY per-service image
//     build — there is no unconditional auto-docker step for these.
//
// A workload whose build is a GoBuild (handled by the go-build path) or a
// ShellBuild (handled by buildExternalServices) is skipped here. Failures are captured, not short-circuited, so the summary shows
// the full set.
var compositionDockerBuilder = buildServiceDocker

func buildKCLDockerShell(ctx context.Context, cfg *config.ProjectConfig, e *KCLEntities, opts buildOptions, cfgArchForDocker, resolvedTag string) []buildResult {
	var out []buildResult
	workloads := e.Workloads
	aliases := map[string][]WorkloadEntity{}
	if opts.composition != nil {
		units, err := compositionBuildUnits(e)
		if err != nil {
			return []buildResult{{err: err}}
		}
		workloads = nil
		for _, unit := range units {
			workloads = append(workloads, unit[0])
			aliases[unit[0].Name] = unit[1:]
		}
	}
	for _, w := range workloads {
		switch w.Build.Type {
		case "docker":
			repository, imageTag := serviceDockerImage(w, resolvedTag, opts)
			builder := buildServiceDocker
			if opts.composition != nil {
				builder = compositionDockerBuilder
			}
			result := builder(ctx, cfg, w.Name, repository, w.Build.Docker, opts, cfgArchForDocker, imageTag)
			result.workload = w.Name
			out = append(out, result)
			for _, alias := range aliases[w.Name] {
				clone := result
				clone.workload = alias.Name
				clone.name = alias.Name + " (docker)"
				out = append(out, clone)
			}
		case "remote":
			out = append(out, buildServiceRemote(w.Name))
		}
	}
	return out
}

// buildServiceRemote handles a RemoteBuild, which forge cannot yet submit.
//
// IT FAILS LOUDLY RATHER THAN SKIPPING, and that choice is the point. A
// skipped build produces no artifact, so a deploy that followed it would
// reference an image nobody pushed — and it would surface much later as an
// ImagePullBackOff, which reads as a registry or credentials problem rather
// than as "this build never ran". A hard failure names the cause at the moment
// it applies.
//
// This is the one place in the build dispatcher that is a stub, and it is
// scoped deliberately: the RemoteBuild SCHEMA is what a hosted build service
// needs in order to be declarable at all, and landing it separately from the
// submitting client is what lets the service's own contract settle first. The
// client belongs with the API it calls.
func buildServiceRemote(svcName string) buildResult {
	return buildResult{
		name: svcName,
		err: fmt.Errorf(
			"service %q declares build = forge.RemoteBuild, which this forge cannot submit yet: "+
				"the hosted build service client is not wired into `forge build`. "+
				"Refusing rather than skipping, because a skipped build leaves a following deploy "+
				"pointing at an image nothing pushed — which surfaces later as an ImagePullBackOff "+
				"and reads like a registry problem. "+
				"Use a DockerBuild or a ShellBuild to build this service locally in the meantime",
			svcName),
	}
}

// serviceDockerBuildArgs assembles the full `docker build …` argument vector
// (and the subset of `-t` tags that get pushed) for a per-service DockerBuild,
// WITHOUT executing docker — so the registry + build-context resolution is
// unit-testable. It is the per-service analogue of dockerBuildProject's arg
// assembly.
//
// The two per-service `docker` facts NOT expressible in a Dockerfile resolve
// here:
//
//   - the destination — the workload's own `image`, registry included. There is
//     no registry knob on the build: where the image goes is where the runtime
//     pulls it from, stated once.
//   - build_contexts — DockerBuild.build_contexts win when set, else the
//     project-level forge.yaml docker.build_contexts.
//
// forge is base-image-AGNOSTIC: NO base-image discovery, mirror, pin, or
// `--build-arg BASE_*` injection happens — the Dockerfile's `FROM` lines are
// the whole story. The only `--build-arg`s are the service's explicit
// DockerBuild.build_args.
func serviceDockerBuildArgs(cfg *config.ProjectConfig, repository, dockerfile string, d *DockerBuild, opts buildOptions, cfgArchForDocker, resolvedTag string) (dockerArgs, pushTags []string) {
	dockerArgs = []string{"build"}
	// platform: the DockerBuild's explicit platform wins; otherwise the
	// env-wide cluster arch (cfgArchForDocker), cross-compiled to linux.
	platform := ""
	if d != nil && d.Platform != "" {
		platform = d.Platform
	} else if a := resolveBuildArch(cfgArchForDocker, opts.targetArch, true); a != "" {
		platform = "linux/" + a
	}
	if platform != "" {
		dockerArgs = append(dockerArgs, "--platform="+platform)
	}
	if d != nil && d.Target != "" {
		dockerArgs = append(dockerArgs, "--target", d.Target)
	}
	if d != nil {
		for _, k := range sortedKeys(d.BuildArgs) {
			dockerArgs = append(dockerArgs, "--build-arg", k+"="+d.BuildArgs[k])
		}
	}
	// `repository` is already the declared destination (serviceDockerImage), so
	// it is used as-is rather than resolved again through the push plan — a
	// second lookup by name would not find a repository and would silently
	// fall back to a bare tag.
	tags := imageTagSet(repository, resolvedTag, opts.pushPlan.push, releaseImageTag(opts) != "")
	for _, t := range tags.local {
		dockerArgs = append(dockerArgs, "-t", t)
	}
	pushTags = tags.push
	// Build contexts: the per-service DockerBuild.build_contexts win when set,
	// else the project-level forge.yaml docker.build_contexts. A service whose
	// Dockerfile COPY --from=s a sibling checkout declares only the contexts it
	// actually needs.
	if d != nil && len(d.BuildContexts) > 0 {
		dockerArgs = appendNamedBuildContexts(dockerArgs, d.BuildContexts, "")
	} else {
		dockerArgs = appendBuildContexts(dockerArgs, cfg, "")
	}
	dockerArgs = append(dockerArgs, "-f", dockerfile, serviceDockerContext(d))
	return dockerArgs, pushTags
}

// serviceDockerContext is the MAIN `docker build` context directory for a
// DockerBuild: its declared context, else the project root. The build runs
// with the project root as its cwd, so a relative context is used verbatim.
//
// A Dockerfile written to be run from its own directory (`COPY package.json
// ./`) needs this; one that COPYs repo-root paths wants the default. Keeping
// the default "." is what makes every pre-existing DockerBuild byte-identical.
func serviceDockerContext(d *DockerBuild) string {
	if d != nil && strings.TrimSpace(d.Context) != "" {
		return strings.TrimSpace(d.Context)
	}
	return "."
}

// checkServiceDockerContext refuses a DockerBuild.context that does not name a
// directory inside the project. It runs at plan time AND before the real build,
// because the docker failure it replaces is inscrutable: a context pointing at
// the wrong place surfaces as `failed to compute cache key: "/package.json":
// not found`, which reads as a Dockerfile bug rather than a context bug.
//
// The DOCKERFILE is deliberately not checked against the context — docker
// allows `-f` outside the context, and forge does not narrow that.
func checkServiceDockerContext(projectDir, svcName string, d *DockerBuild) error {
	if d == nil || strings.TrimSpace(d.Context) == "" {
		return nil
	}
	ctxPath := strings.TrimSpace(d.Context)
	if filepath.IsAbs(ctxPath) {
		return fmt.Errorf(
			"service %q declares build context %q, which is absolute: "+
				"DockerBuild.context is a directory RELATIVE to the project root, so it stays "+
				"valid on another machine and in CI", svcName, ctxPath)
	}
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("service %q: resolving the project root %q: %w", svcName, projectDir, err)
	}
	full := filepath.Join(root, ctxPath)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf(
			"service %q declares build context %q, which escapes the project root: "+
				"DockerBuild.context must name a directory inside the project. "+
				"To pull in a sibling checkout, declare a NAMED build_contexts entry "+
				"the Dockerfile COPY --from=s instead", svcName, ctxPath)
	}
	info, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf(
			"service %q declares build context %q, which does not exist (looked in %s): "+
				"DockerBuild.context is a directory relative to the project root", svcName, ctxPath, full)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"service %q declares build context %q, which is a file, not a directory (%s): "+
				"DockerBuild.context is the directory docker sends as the build context",
			svcName, ctxPath, full)
	}
	return nil
}

// serviceDockerImage is the repository and tag a DockerBuild workload's image
// is built as — the SAME ref the render resolved into its spec.image, so the
// deploy pulls what the build pushed.
//
// The repository is the workload's artifact (`image` with any tag stripped;
// unset, the output_name, else the workload name — kcl/render.k `_artifact`).
// The tag is the shared precedence (buildTagFor): the release version, else
// an explicit --tag, else the workload's own pin (`image = "gw:v7"` builds
// gw:v7, read off the runtime-independent build identity,
// WorkloadEntity.BuildImage), else the build-wide resolvedTag. The pin wins
// over the build-wide tag because it IS the deploy ref; a --tag that
// contradicts it is refused up front (checkExplicitTagAgainstPins).
// serviceDockerImage is the REPOSITORY a DockerBuild workload's image goes to
// and the tag it carries. The repository is the workload's own declared image
// minus any pin — registry host included, because that is where the build
// pushes and where the deploy pulls. A workload that declares no image falls
// back to its build's output_name / its own name, which names no host and so
// is only ever tagged locally.
func serviceDockerImage(w WorkloadEntity, resolvedTag string, opts buildOptions) (repository, tag string) {
	repository = imageRepository(w.Image)
	if repository == "" {
		repository = w.Name
		if d := w.Build.Docker; d != nil && d.OutputName != "" {
			repository = d.OutputName
		}
	}
	pin, _ := w.PinnedBuildTag()
	return repository, buildTagFor(opts, pin, resolvedTag)
}

// buildServiceDocker runs `docker build` for a DockerBuild service. It
// reuses the same tag/registry/push/build-context primitives the project
// image build uses (resolveBuildContext / appendBuildContexts /
// k3dMirrorRepositories) so a per-service image is tagged and pushed the
// same way. repository/resolvedTag come from serviceDockerImage. platform
// overrides the env-wide arch.
func buildServiceDocker(ctx context.Context, cfg *config.ProjectConfig, svcName, repository string, d *DockerBuild, opts buildOptions, cfgArchForDocker, resolvedTag string) buildResult {
	start := time.Now()
	dockerfile := "Dockerfile"
	if d != nil && d.Dockerfile != "" {
		dockerfile = d.Dockerfile
	}

	if _, err := os.Stat(dockerfile); os.IsNotExist(err) {
		// No image was built, so the result carries no image: nothing is
		// recorded for a build that did not happen.
		fmt.Printf("[build] %s: skipping docker (no %s)\n", svcName, dockerfile)
		return buildResult{name: svcName + " (docker)", kind: "docker", duration: time.Since(start)}
	}

	if err := checkServiceDockerContext(".", svcName, d); err != nil {
		return buildResult{name: svcName + " (docker)", kind: "docker", duration: time.Since(start), err: err}
	}

	dockerArgs, pushTags := serviceDockerBuildArgs(cfg, repository, dockerfile, d, opts, cfgArchForDocker, resolvedTag)
	// The image's timestamps follow the last change to ITS inputs (context +
	// Dockerfile), so an unrelated commit does not move its digest.
	dockerArgs = appendSourceDateEpoch(dockerArgs, resolveSourceDateEpoch(ctx, ".", serviceDockerContext(d), dockerfile))
	fmt.Printf("[build] %s: docker build -f %s %s (%d tags)\n", svcName, dockerfile, serviceDockerContext(d), countTags(dockerArgs))

	if err := requireImageDaemon(ctx, "service "+svcName+" ("+dockerfile+")"); err != nil {
		return buildResult{name: svcName + " (docker)", kind: "docker", duration: time.Since(start), err: err}
	}
	if err := prepareDockerBuildStorage(ctx, projectDirForKCL()); err != nil {
		return buildResult{name: svcName + " (docker)", kind: "docker", duration: time.Since(start), err: err}
	}
	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return buildResult{name: svcName + " (docker)", kind: "docker", duration: time.Since(start), err: err}
	}
	for _, t := range pushTags {
		fmt.Printf("[build] %s: docker push %s\n", svcName, t)
		if err := dockerPush(ctx, t); err != nil {
			return buildResult{name: svcName + " (docker)", kind: "docker", duration: time.Since(start), err: err}
		}
	}

	// Capture the pushed digest, exactly as the project and frontend images
	// do, and carry the image + tag so persistImageBuildStates records this
	// build. Without it a DockerBuild recorded nothing: deploy could not pin
	// it and a release cut refused the env as incomplete.
	digest, platforms := "", []string(nil)
	if len(pushTags) > 0 {
		ref := pushTags[len(pushTags)-1]
		if dg, p, derr := imageRepoDigest(ctx, ref); derr == nil {
			digest, platforms = dg, p
			fmt.Printf("[build] %s: pushed digest %s\n", svcName, digest)
		} else {
			fmt.Printf("[build]   Note: could not capture image digest for %s (%v); deploy will use the tag\n", ref, derr)
		}
	}
	return buildResult{
		name: svcName + " (docker)", kind: "docker", duration: time.Since(start),
		// The state file is keyed by the SERVICE name (a path segment), while
		// what it records is the repository. Keying it by the repository would
		// put slashes in the filename.
		image: svcName, repository: repository, tag: resolvedTag, digest: digest, platforms: platforms,
	}
}
