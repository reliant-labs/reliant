package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
)

// `forge env build <env>` is where an environment's artifacts are produced:
// built, optionally pushed, and — with --release — recorded as an immutable
// release.
//
// WHY THE SPLIT. Pushing and cutting a release are ENVIRONMENT acts. Both need
// an env to be meaningful at all: a push destination is declared per workload
// in the env's render (resolvePushPlan), and a release's artifact SET is
// discovered from deploy/kcl/<env>/main.k, including the per-env external
// build_cmd images that exist nowhere else. `forge env build --push` and
// `forge env build --release` therefore both already REQUIRED an env argument
// while living on a command whose env was optional — the flag combination
// `forge env build --push` with no env was a usage error that could only be
// reported at runtime, and `forge env build --release` without one silently built
// an incomplete set until a guard was added for it.
//
// So the env moves from an optional modifier to the command's subject, and the
// two flags move with it. Top-level `forge build` keeps what genuinely needs
// no env — compiling — and refuses the two flags with a pointer here (see
// refuseBuildPublishFlag in build.go).
//
// `forge release cut` folded into this command as `--release <v> --no-build`:
// it was the cut WITHOUT the build, for the pipeline shape where build and
// release are separate jobs. That is this command with the build phase off,
// reading the digests the earlier push already recorded in .forge/state — one
// code path for "cut a release", differing only in whether it builds first.

func newEnvBuildCmd() *cobra.Command {
	opts := buildOptions{buildTarget: "all"}
	var noBuild bool
	var basePromotion string
	var snapshotBootstrap bool
	var selectors []string

	cmd := &cobra.Command{
		Use:   "build <environment>",
		Short: "Build an environment's artifacts; --push publishes them, --release records an immutable release",
		Args:  cobra.ExactArgs(1),
		Long: `Build the artifacts an environment declares.

Iterates the workloads deploy/kcl/<env>/ declares and dispatches on each
one's build.type — go, docker, shell, remote — exactly as the compile-only
` + "`forge build`" + ` does. What this command adds is everything that needs
an environment to mean anything:

  --push          publish each image to the reference ITS OWN workload
                  declares (its image field in deploy/kcl/workloads.k) — the
                  same reference ` + "`forge env deploy`" + ` reads, so what is
                  pushed is what is deployed. Two workloads may name two
                  different registries; both are pushed. Takes no value and
                  carries no registry.

  --release vX    record an IMMUTABLE RELEASE: capture every artifact's digest
                  into a release ledger (.forge/releases/vX.json, or the
                  control plane when the env declares one).
                  IMPLIES --push — a release pins registry-addressable
                  digests, and a local tag cannot be promoted anywhere.

A release is build-once → promote: ` + "`forge env deploy <env> vX`" + ` pins the
SAME digests in every environment, with no per-env rebuild. The images are
env-agnostic; the env argument supplies the artifact SET to build and the
registries to push to, so pick any env that declares the full set.

CUTTING WITHOUT REBUILDING. In a pipeline where the build and the release are
separate jobs, pass --no-build with --release: the digests an earlier
` + "`--push`" + ` recorded in .forge/state are harvested and the release is
recorded without rebuilding anything. Rebuilding to cut would be exactly the
rebuild the release model exists to avoid.

  forge env build prod --push                  # job 1: build and push
  forge env build prod --release v1.4.0 --no-build   # job 2: record the release
  forge env deploy prod v1.4.0                 # bind prod to it

Re-cutting the same version over the same artifacts is a no-op; over
DIFFERENT artifacts it is refused. The cut FAILS if anything the env declares
is missing from the ledger — a release with a hole in it promotes like a
complete one and ships an environment that is missing a piece.

--release owns the image tag (it IS the release version), so a conflicting
--tag is refused: a release build pushes under the release version only, so a
cut that fails part-way can never leave a shared, mutable tag pointing at
bytes no release contains.

Examples:
  forge env build dev                          # build the env's artifacts
  forge env build prod --push                  # build + push to declared registries
  forge env build prod --release v1.4.0        # build + push + record the release
  forge env build prod --release v1.4.0 --plan # preflight the cut, build nothing
  forge env build prod --target api --push     # scope to one workload`,
		// A build failure, a registry rejection or an incomplete release are
		// not usage errors; a cobra flag dump after one buries the message
		// that says what to do.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateBuildEnvArg(args[0]); err != nil {
				return err
			}
			opts.env = args[0]
			composing := basePromotion != "" || snapshotBootstrap
			if len(selectors) > 0 {
				opts.buildTarget = selectors[0]
			}
			if composing {
				if noBuild {
					return fmt.Errorf("snapshot composition cannot harvest --no-build state; use captured base and invocation outputs")
				}
				if err := prepareComposition(cmd.Context(), &opts, basePromotion, snapshotBootstrap, selectors); err != nil {
					return err
				}
			} else if len(selectors) > 1 {
				return fmt.Errorf("repeatable --target requires --base-promotion or --snapshot-bootstrap")
			}
			// The build feature gate applies to BUILDING. A --no-build cut
			// records a release over digests an earlier build already
			// produced, so it is not gated on `features.build` — gating it
			// there would make the release step of a pipeline depend on a
			// flag about the build step, in a job that does not build.
			if !noBuild {
				if _, err := requireFeature(config.FeatureBuild); err != nil {
					return err
				}
			}
			// --push and --release both produce images, so both imply a
			// docker build whether or not the caller said so.
			if opts.push || opts.release != "" {
				opts.buildDocker = true
			}
			// --release IMPLIES --push, per the ADR. A release pins digests,
			// and a digest exists only once a registry has the bytes; a
			// release built without pushing would fail its own ledger write
			// with "no image digest was captured".
			if opts.release != "" {
				opts.push = true
			}
			if err := validateReleaseFlags(opts); err != nil {
				return err
			}
			opts.parallelSet = cmd.Flags().Changed("parallel")

			// Record what the env DECLARES before producing anything.
			//
			// This is the whole point of the slice, and the ordering is
			// the contract: `forge env build <env>` with no --release and
			// even with --no-build still tells the control plane what the
			// env is, because every other surface — the Live view, the
			// set-secret form, a deploy plan — needs that and nothing
			// else. A build that fails half-way has still answered "what
			// kind, which secrets, which provider", which is the question
			// that previously required a daemon and a checkout.
			//
			// --plan is excluded: it preflights a build and writes
			// nothing, so recording a declaration from it would make a
			// dry run the thing that changed the world.
			if !opts.plan {
				if err := recordEnvBuildDeclaration(cmd.Context(), opts.env); err != nil {
					return err
				}
			}

			if noBuild {
				return runEnvBuildCutOnly(cmd.Context(), opts)
			}
			if opts.planJSON {
				if !opts.plan || opts.composition == nil {
					return fmt.Errorf("--json requires snapshot composition --plan")
				}
				_, err := captureProcessStdout(func() error { return runBuild(cmd.Context(), opts) })
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(opts.composition.PlanDocument))
				return nil
			}
			return runBuild(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.outputDir, "output", "o", "bin", "Output directory for binaries")
	cmd.Flags().BoolVar(&opts.planJSON, "json", false, "With --plan and snapshot composition: emit a machine-readable pending pre-build plan")
	cmd.Flags().StringArrayVarP(&selectors, "target", "t", nil, "Build target; repeatable for snapshot composition (default all)")
	cmd.Flags().StringVar(&basePromotion, "base-promotion", "", "Compose from this exact desired promotion (or resolve current once before builds)")
	cmd.Flags().BoolVar(&snapshotBootstrap, "snapshot-bootstrap", false, "Build a complete sealed snapshot selecting all consumers; never use legacy state as a partial base")
	cmd.Flags().StringArrayVarP(&opts.renderOptions, "option", "D", nil, "Set a render option the env's KCL declares, as name=value (repeatable). Relayed to KCL verbatim — forge does not interpret the value. List an env's options with 'forge env options <env>'.")
	cmd.Flags().BoolVar(&opts.parallel, "parallel", true, "Build services in parallel")
	cmd.Flags().BoolVar(&opts.buildDocker, "docker", false, "Build Docker images for all services (implied by --push and --release)")
	cmd.Flags().BoolVar(&opts.debug, "debug", false, "Build with debug symbols for Delve")
	cmd.Flags().BoolVar(&opts.push, "push", false, "Push docker images after build (implies --docker), each to the reference its own workload declares (its image field in deploy/kcl/workloads.k). Takes no value and carries no registry")
	cmd.Flags().StringVar(&opts.targetArch, "target-arch", "", "Override target GOARCH for cross-compilation (default: forge.yaml deploy.target_arch, then amd64 for docker builds)")
	cmd.Flags().StringVar(&opts.tag, "tag", "", "Override the image tag of every image this build writes (default: the tag a workload's image pins, else the env's image_tag, else git describe --tags --always --dirty). Refused when it differs from the tag a selected workload's image pins, or when it conflicts with --release. Recorded in .forge/state so 'forge env deploy' uses the same value.")
	cmd.Flags().BoolVar(&opts.skipGenerate, "no-generate", false, "Skip the pre-build code-generation check. By default this runs forge generate when gen/ is missing or proto sources are newer than the generated tree.")
	cmd.Flags().BoolVar(&opts.plan, "plan", false, "Resolve the exact build set this invocation would build (same KCL discovery, same --target narrowing) and PREFLIGHT every step without running it: each go-build package exists and is a main package, each Dockerfile and frontend build script exists, each ShellBuild cwd exists, and with --release the ledger would cover everything the env declares. Builds, pushes, generates and writes nothing; exits non-zero on anything the real build would fail on. Pass it the release's exact arguments to gate a PR on the cut.")
	cmd.Flags().StringVar(&opts.gateJSON, "gate-json", "", "Also write this build's result to `FILE` as a gate document, for `forge gate record` or `forge env deploy --gate`. A FILE, not a stdout mode: the build log and the exit code are unchanged.")
	cmd.Flags().StringVar(&opts.release, "release", "", "Record an immutable release with this version label (e.g. v1.4.0). IMPLIES --push. The release's artifact SET (project images plus per-env external build_cmd images) is discovered from deploy/kcl/<env>/main.k; the built images stay env-agnostic, so promote the release to every env with 'forge env deploy <env> <version>'. Captures each artifact's digest into a release ledger, which every later deploy pins.")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "With --release: record the release over the digests an earlier --push already captured in .forge/state, without rebuilding. This is the cut-only half of a pipeline whose build and release are separate jobs.")
	cmd.Flags().StringVar(&opts.bundleEnvs, "bundle-envs", "", "Write a bundle for each of these envs (comma-separated) from this one build, instead of only for the env argument. A release is env-agnostic and a bundle is not, so this is how one cut produces the manifests for every env it will ship to — each pinned to the same release and the same source. Each env is rendered; each bundle is pushed beside its images for a control-plane env, or written to this machine's ledger otherwise.")
	registerRunFlags(cmd.Flags(), &opts.run)

	return cmd
}

// recordEnvBuildDeclaration is `forge env build`'s declaration step: render
// the env and, if it declares a control plane, ensure it there with its shape
// and that render's provenance.
//
// A render failure here is NOT fatal, and that is a deliberate asymmetry with
// the recording itself. An env with no deploy/kcl/<env>/ at all is a legitimate
// `forge env build` target (a test project, an env named only on the command
// line), and refusing to build it because nothing could be projected would
// break a command that has always worked. A render that SUCCEEDS and then
// fails to record is a different matter: that failure is returned, because
// the build would otherwise report success while every Live surface reads a
// stale declaration.
//
// A BUILD NEVER NEEDS A REACHABLE CONTROL PLANE. `forge env build` builds and
// pushes images; it is the primitive a CI build job runs, and the one a
// developer runs on a plane. So when the declaration cannot be DELIVERED —
// no credential configured, the control plane unreachable, the transport
// failing — the build warns once and carries on: nothing about the env is
// wrong, and the next build or deploy that reaches the control plane records
// it (a deploy cannot proceed without one anyway). What still fails the build
// is the control plane ANSWERING and saying no: a refused shape, a kind
// change, a credential it rejected. Those are facts about the env or its
// configuration, and a green build over them would leave Live wrong.
func recordEnvBuildDeclaration(ctx context.Context, envName string) error {
	entities, err := renderKCLForDeclaration(ctx, projectDirForKCL(), envName)
	if err != nil || entities == nil {
		return nil
	}
	err = recordEnvDeclaration(ctx, envName, entities)
	if err != nil && declarationUndeliverable(err) {
		fmt.Printf("[declare] Warning: env %s's declaration was not recorded: %v\n"+
			"[declare]   The build continues; the next build or deploy that reaches the control plane records it.\n", envName, err)
		return nil
	}
	return err
}

// declarationUndeliverable reports whether a declaration failed to REACH the
// control plane, as opposed to reaching it and being refused. No credential,
// a transport error and an Unavailable/DeadlineExceeded answer are
// undeliverable; any other control-plane answer — InvalidArgument,
// FailedPrecondition, PermissionDenied, Unauthenticated — is a refusal.
func declarationUndeliverable(err error) bool {
	if errors.Is(err, cloud.ErrNoCredential) {
		return true
	}
	var cerr *cloud.Error
	if errors.As(err, &cerr) {
		return cerr.HasCode(cloud.CodeUnavailable) || cerr.HasCode(cloud.CodeDeadlineExceeded)
	}
	// Not a control-plane answer at all: the request never got one (refused
	// connection, DNS, TLS, a timeout in the transport).
	return true
}

// renderKCLForDeclaration is RenderKCL, as a var so a test can state the
// env's declaration without a project on disk.
var renderKCLForDeclaration = RenderKCL

// runEnvBuildCutOnly records a release over the build state an earlier push
// left behind, with no build of its own. This is what `forge release cut`
// was: the artifact set is discovered exactly as a full --release build
// discovers it (every image digest captured in .forge/state for the env,
// every publishable package, every source-pinned frontend), and the cut fails
// if anything the env declares is missing.
func runEnvBuildCutOnly(ctx context.Context, opts buildOptions) error {
	if opts.release == "" {
		return fmt.Errorf("--no-build only applies to a release cut: pass --release <version> with it, " +
			"or drop --no-build to build the env's artifacts")
	}
	if opts.plan {
		return fmt.Errorf("--plan and --no-build are mutually exclusive: --plan preflights a build that " +
			"--no-build would not run. To check a cut-only release, run the cut — it writes nothing until " +
			"every declared artifact is accounted for")
	}
	projectDir := projectDirForKCL()
	entities, err := RenderKCL(ctx, projectDir, opts.env)
	if err != nil {
		return fmt.Errorf("render deploy/kcl/%s: %w", opts.env, err)
	}
	if _, err := cutReleaseFromBuildState(ctx, projectDir, opts.env, opts.release, opts.outputDir, entities, opts); err != nil {
		return err
	}
	// The cut-only job writes the bundle too. It is the half of a split
	// pipeline that RECORDS, and the bundle is a record — leaving it to the
	// deploy would mean `forge env deploy <env> <v>` had to render it on
	// demand, from whatever checkout the deploy job happens to hold. The
	// earlier --push job pushed the images, so this build's pins resolve
	// and the bundle can name the registry it pushed to.
	opts.push = true
	return writeBuildBundle(ctx, opts)
}

// refuseBuildPublishFlag is top-level `forge env build`'s refusal for --push and
// --release, which moved to `forge env build <env>`.
//
// Top-level `forge build` is COMPILE-ONLY: a local check that the tree builds,
// which is why its environment argument is optional. Publishing is not that.
// Both flags always needed an env to resolve — a push destination comes from
// the env's workload declarations, a release's artifact set from its render —
// so on a command with an optional env they were a combination that could only
// fail at runtime. The refusal names the replacement rather than the
// constraint, because the user's next keystroke is the command, not an
// explanation.
func refuseBuildPublishFlag(flag, env string) error {
	target := "<env>"
	if env != "" {
		target = env
	}
	return fmt.Errorf("`forge build` is compile-only and cannot %s — use `forge env build %s --%s`.\n"+
		"  %s needs an environment to resolve: %s\n"+
		"  Top-level `forge build` compiles the tree as a local check, which is why its environment argument is optional",
		publishVerb(flag), target, flag, "--"+flag, publishWhy(flag))
}

// publishVerb and publishWhy keep the refusal specific to the flag the user
// actually passed: a message that says "cannot push or release" makes the
// reader work out which half applies to them.
func publishVerb(flag string) string {
	if flag == "release" {
		return "record a release"
	}
	return "push images"
}

func publishWhy(flag string) string {
	if flag == "release" {
		return "the release's artifact SET (including the per-env external build_cmd images) is discovered from deploy/kcl/<env>/main.k"
	}
	return "each image's destination is declared on its own workload in the env's render"
}
