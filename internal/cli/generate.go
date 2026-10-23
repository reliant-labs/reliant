package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/contractcheck"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/shadowdb"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// generateMu protects the generation pipeline from concurrent runs.
// It is legitimately package-level shared state used by generate, add, and new commands.
var generateMu sync.Mutex

// withGenerateLock runs fn holding both locks the generate pipeline needs:
// the project's cross-process generate lock (.forge/forge.lock — blocks, and
// says so, while another forge run holds it; see acquireGenerateLock), then
// the in-process generateMu that guards the pipeline's package-level state.
// Every entry point that runs the pipeline outside a scaffold command goes
// through here; scaffold commands hold the project lock for their whole run
// (GenAPI.HoldProjectLock) and take only generateMu around the pipeline.
//
// The order is fixed — project lock, then generateMu — on every path. Taken
// the other way round anywhere, a host embedding forge could deadlock: one
// goroutine holding generateMu waits for the project lock while the
// scaffold goroutine holding that lock waits for generateMu.
func withGenerateLock(projectDir string, fn func() error) error {
	release, err := acquireGenerateLock(projectDir)
	if err != nil {
		return err
	}
	defer release()
	generateMu.Lock()
	defer generateMu.Unlock()
	return fn()
}

// generateInSlot runs fn holding a host heavy slot (pkg/hostslot): generate is
// heavy (buf, the schema projection's postgres, a validating compile of every
// package). The slot is taken BEFORE the project lock: a run holding the
// project lock must never wait for a slot, or a slot holder waiting for that
// lock (a gate's verify-generated) deadlocks with it. --plan does no work and
// takes none; --watch holds it for the first generate only, not the session.
func generateInSlot(cmd *cobra.Command, fn func() error) error {
	release, err := cmdutil.HoldHeavySlot(cmd.Context(), cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// generateLockedInSlot runs fn under the project's generate lock inside a heavy
// slot — slot first, then lock (see generateInSlot) — and releases both when
// fn returns, so `--watch` holds neither for its session.
func generateLockedInSlot(cmd *cobra.Command, fn func() error) error {
	return generateInSlot(cmd, func() error { return withGenerateLock(".", fn) })
}

// generateLongHelp is `forge generate --help`'s body. A const, so the
// command constructor stays about flags and wiring.
const generateLongHelp = `Re-project the project from its sources of truth: the .proto files under
proto/ (the API), the migrations under db/migrations/ (the schema), and
forge.yaml plus deploy/kcl/ (features, frontends, environments).

What it writes, in order:
  - gen/: Go + Connect stubs (buf generate) and gen/forge_descriptor.json,
    the services, rpcs and config messages extracted from the protos
  - each frontend's TypeScript stubs, hooks, CRUD pages, nav and mocks
  - pkg/config/ from proto/<project>/config/v1/, the typed config loader
  - internal/handlers/<svc>/: a stub for every rpc no handler implements yet,
    and the CRUD wiring of every entity (an entity is a table in the APPLIED
    schema that a service's CRUD rpcs name; the schema comes from running
    db/migrations against a throwaway Postgres)
  - internal/db/: the ORM stores projected from that schema
  - mocks for services and for every internal/<pkg>/contract.go
  - internal/app/: the composition graph (graph_gen.go, routes_gen.go, and
    the components and Deps keys reconciled into compose.go); cmd/<bin>/
    commands and roots
  - pkg/middleware/procedures_gen.go, the per-rpc auth and deadline table
  - deploy/: KCL config, observability dashboards and alert defaults, CI
    workflows, seed data
then go mod tidy, goimports, and a validating go build of the whole tree.

Hand-written code is never overwritten. Files that are yours from birth
(handlers' rpc_<name>.go, internal/app/providers.go, compose.go) are written
once and afterwards only reconciled; a generated file you edited is refused
as drift until you disown it ('forge project disown') or pass --force.
Without forge.yaml, generate still compiles proto/ and runs the steps that
need no config, and says so.

Hooks:
  Generate hooks are the commands forge.yaml declares under generate.hooks
  (sqlc, a project's own codegen). generate runs every one, in order, after
  its own code generation and before validation, and prints each hook it ran.
  Nothing skips them: a skipped hook leaves its output stale while validation
  still passes. A run narrowed with --steps or --templates-only does not run
  them, and says so.
  FORGE_NO_GIT_HOOKS=1 is about GIT hooks, not these: it stops forge setting
  core.hooksPath=.githooks in your clone. FORGE_NO_HOOKS=1 is its older name
  and means the same. Neither stops forge pointing the clone's merge driver
  for generated files at the pinned forge; FORGE_NO_MERGE_DRIVER=1 does.

Skipping:
  A full run that changes nothing records the tree as a verified fixed point
  (.forge/generate-cache.json, per checkout). The next run that finds every
  input and output byte-identical — the work tree, the files git ignores but
  generate reads or writes, the forge binary, every tool it runs, and the
  environment that steers them — prints one line saying so and stops, in
  about a second instead of minutes. Any change runs every step. --no-cache
  (or FORGE_GENERATE_NO_CACHE=1) always runs every step.

Examples:
  forge generate            # Generate all code
  forge generate --watch    # Watch mode for development
  forge generate --force    # Discard hand-edits to Tier-1 files and regenerate
  forge generate --check    # Run generate into a tmpdir; exit 1 if it would change the tree (CI guard)
  forge generate --no-cache # Run every step even if nothing changed since the last verified run
  forge generate --explain  # Print per-file provenance log after generate
  forge generate --verbose  # Print one line per gate-off skipped step

Additional maintainer/debug flags exist (pipeline narrowing, drift
forensics, parallel-lane and migration escape hatches); run
'forge generate --help-dev' to list them.`

func newGenerateCmd() *cobra.Command {
	var (
		watch           bool
		force           bool
		explain         bool
		explainDrift    bool
		skipValidate    bool
		skipPreChecks   bool
		skipConfigCheck bool
		checkOnly       bool
		forceCleanup    bool
		templatesOnly   bool
		strict          bool
		verbose         bool
		planOnly        bool
		heal            bool
		noRevert        bool
		timings         bool
		noCache         bool
		steps           string
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate code from proto files",
		Long:  generateLongHelp,
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkOnly {
				// No slot here: the regenerate is a child `forge generate`,
				// which queues for its own. Holding one across it would
				// deadlock a machine with a single heavy slot.
				return runGenerateCheck(noCache)
			}
			if planOnly {
				return runGeneratePlan(".", pipelineFlags{
					Force:           force,
					SkipValidate:    skipValidate,
					SkipPreChecks:   skipPreChecks,
					SkipConfigCheck: skipConfigCheck,
					ForceCleanup:    forceCleanup,
					TemplatesOnly:   templatesOnly,
					Strict:          strict,
					Verbose:         verbose,
					Steps:           steps,
				})
			}
			flags := pipelineFlags{
				Force:           force,
				ExplainDrift:    explainDrift,
				SkipValidate:    skipValidate,
				SkipPreChecks:   skipPreChecks,
				SkipConfigCheck: skipConfigCheck,
				ForceCleanup:    forceCleanup,
				TemplatesOnly:   templatesOnly,
				Strict:          strict,
				Verbose:         verbose,
				Heal:            heal,
				NoRevert:        noRevert,
				Timings:         timings,
				Cache:           true,
				NoCache:         noCache,
				Steps:           steps,
			}
			// A run with nothing to do must not queue for a heavy slot
			// behind real work first: the cache check is a second of
			// hashing, not heavy work. --explain wants a pipeline to explain.
			skipped := false
			if !explain {
				var err error
				if skipped, err = generateCachedNoop(".", flags); err != nil {
					return err
				}
			}
			// The explain scans run inside the lock too: a concurrent run
			// finishing between the pre-scan and this run's pipeline would
			// otherwise be labeled as this run's rewrites.
			body := func() error {
				// Capture pre-pipeline body hashes (from the embedded
				// markers) so --explain can diff against post-pipeline
				// state to label rewritten vs idempotent.
				var preChecksums map[string]string
				if explain {
					pre := checksums.ScanMarkers(".")
					preChecksums = make(map[string]string, len(pre))
					for k, v := range pre {
						preChecksums[k] = v.Body
					}
				}

				pipeErr := runGeneratePipelineFlags(".", flags)

				// Print the explain log even when the pipeline failed — partial
				// provenance is still useful for diagnosing what got generated
				// before the build break. The original error is returned below.
				//
				// Honor --strict: a failed explain render under strict promotes
				// to a fatal error (consistent with the rest of the pipeline's
				// loud-by-default thesis). Outside strict it stays a soft warn
				// so an explain-log bug doesn't mask a successful generate.
				if explain {
					if explainErr := printExplainLog(".", preChecksums); explainErr != nil {
						if strict {
							return fmt.Errorf("explain log failed: %w (--strict)", explainErr)
						}
						fmt.Fprintf(os.Stderr, "⚠️  Warning: explain log failed: %v — pass --strict to fail on this\n", explainErr)
					}
				}
				return pipeErr
			}
			var err error
			if !skipped {
				err = generateLockedInSlot(cmd, body)
			}
			if generateRunEndsCommand(err, watch) {
				return err
			}

			if watch {
				fmt.Println("\n👀 Watching for changes... (Press Ctrl+C to stop)")
				return watchForChanges()
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch for changes and regenerate")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Discard hand-edits to Tier-1 files and regenerate from current templates")
	cmd.Flags().BoolVar(&explain, "explain", false, "Print a per-file provenance log after generate")
	cmd.Flags().BoolVar(&explainDrift, "explain-drift", false, "On Tier-1 drift, run the pipeline with drifted files redirected to .forge/render/ side renders, print a bounded diff of on-disk vs fresh render per file, then fail with the drift report (explains; never overwrites or approves)")
	cmd.Flags().BoolVar(&skipValidate, "skip-validate", false, "Skip the final validate step — compiling every package and _test.go, as 'go build ./...' would, without linking (useful during multi-lane migrations when the tree is in a partial-build state)")
	cmd.Flags().BoolVar(&skipPreChecks, "skip-pre-checks", false, "Bypass the pre-codegen contract-shape check (useful when a parallel lane's contract violation would otherwise block regen of this lane)")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Run generate into a tmpdir and diff against the current tree; exit 1 on drift (for CI guards)")
	cmd.Flags().BoolVar(&forceCleanup, "force-cleanup", false, "Also delete stale generated files that carry hand-edits. Generate always deletes the stale files nobody edited; without this flag it lists the edited ones and keeps them.")
	cmd.Flags().BoolVar(&templatesOnly, "templates-only", false, "Re-render template-driven files only. Skips cleanup sweep, drift-guard, and validation. Use when a template change needs to propagate to a project that has uncommitted WIP and can't tolerate a full regen.")
	cmd.Flags().StringVar(&steps, "steps", "", "Narrow the pipeline to a named step preset. Valid values: \"bootstrap-only\" (used internally by 'forge scaffold worker'), \"mocks\" (regen both kinds of mock_gen.go — contract-derived and service-derived — after a contract.go or proto change; skips the Tier-1 drift guard since mocks cannot stomp Tier-1 files), \"harness\" (regen only each component's helpers_gen_test.go — NewTest<Svc>, With<Svc>Deps, the doubles it installs — and the contract mock_gen.go files it installs them from, after a Deps edit; a proto or migration change needs the full run), \"env-config\" (write only the config modules every env's KCL imports — config_gen.k, frontend_config_gen.k, each env's config.k — so the envs render; used internally by 'forge project rescaffold').")
	// Loud-by-default architecture flags. See the per-flag fields on
	// pipelineFlags for the rationale; runGeneratePlan + warnOrFail consume them.
	cmd.Flags().BoolVar(&strict, "strict", false, "Stop at the first failed step and roll the tree back, instead of running the steps that do not depend on it. Either way a failed step makes generate exit non-zero.")
	// -v works here. It used to be swallowed by a root PersistentFlag bound
	// to a variable nobody read, so `forge generate -v` parsed cleanly and
	// changed nothing — the long form was declared without a shorthand
	// precisely to dodge that collision. The root flag is gone; the
	// shorthand belongs to the command that actually consumes it.
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Print one line per gate-off skipped step ('⏩ skipped: <step name> (<reason>)'). Default is silent skip.")
	cmd.Flags().BoolVar(&planOnly, "plan", false, "Print the pipeline plan ([RUN]/[SKIP] annotation per step + gate reason) and exit 0 without running any step. Honors --steps and --templates-only.")
	// --dry-run is the spelling every other forge verb uses for "show me,
	// don't do it" (`forge scaffold`, `disown`, `secrets`, `project upgrade`).
	// Without it, the one command people most want to preview answers
	// `unknown flag` and sends them to --help to learn that this particular
	// verb spells it differently.
	//
	// The description deliberately does NOT name the flag it shares a
	// variable with: that one is hidden from --help (see help_dev.go), and
	// naming it here would leak it back into the visible surface.
	cmd.Flags().BoolVar(&planOnly, "dry-run", false, "Print the pipeline plan ([RUN]/[SKIP] per step + gate reason) and exit without running any step.")
	cmd.Flags().BoolVar(&skipConfigCheck, "skip-config-check", false, "Bypass the forge.yaml ↔ filesystem cross-check (declared services/frontends/packages must have on-disk backing). Use for parallel-lane / mid-migration scenarios.")
	cmd.Flags().BoolVar(&heal, "heal", false, "Overwrite on-disk content that matches a PRIOR forge render (an older vintage) with the current template. OFF by default: such content is byte-indistinguishable from a deliberate edit, so forge leaves it untouched and tells you how to proceed rather than silently reverting your work. Pass --heal to advance every such file to the current templates.")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Run every step even when nothing changed since a full run verified this tree (a no-op generate otherwise skips in about a second; see the 'Skipping' section above). FORGE_GENERATE_NO_CACHE=1 does the same.")
	cmd.Flags().BoolVar(&timings, "timings", false, "Print each pipeline step's wall time when the run ends, slowest first (FORGE_GENERATE_TIMINGS=1 does the same for runs you do not type, such as verify-generated's)")
	cmd.Flags().BoolVar(&noRevert, "no-revert", false, "Diagnostic mode: on a post-write step failure (most often the final 'go build' validate), leave the generated files ON DISK instead of rewinding the tree, so you can inspect the codegen output that failed to build. Default (off) reverts to the clean pre-run tree.")

	// User-vs-maintainer surface split: the flags below are fully
	// functional but hidden from --help (visible via --help-dev). The
	// visible set is pinned by TestGenerateHelpSurface — a new flag must
	// consciously pick a side. See help_dev.go for the rule of thumb.
	hideDevFlags(cmd,
		"explain-drift",     // drift forensics (debugging the drift guard)
		"skip-validate",     // multi-lane migration escape hatch
		"skip-pre-checks",   // parallel-lane escape hatch
		"templates-only",    // forge-template-development fast path
		"steps",             // pipeline narrowing (internal/agent fast paths)
		"strict",            // pipeline-hardening mode for forge CI/dev
		"plan",              // pipeline introspection (debugging gates)
		"skip-config-check", // parallel-lane / mid-migration escape hatch
		"no-revert",         // codegen forensics (inspect the output that failed to build)
		"timings",           // pipeline profiling (where a run's wall time went)
	)

	// --check and --plan/--dry-run write nothing in this checkout.
	return cmdutil.ConfiguresClone(cmd, "check", "plan", "dry-run")
}

// generateRunEndsCommand reports whether the first run's error ends
// `forge generate`. Every failure does, except under --watch after a run
// that reached the end with failed steps: its summary named them, the next
// change re-runs everything, and a failed re-run inside the watch keeps
// watching too. A run that aborted still ends the command, watch or not.
func generateRunEndsCommand(err error, watch bool) bool {
	if err == nil {
		return false
	}
	var stepsFailed *factory.StepsFailedError
	return !watch || !errors.As(err, &stepsFailed)
}

// runGeneratePipeline executes the unified generation pipeline.
//
// Pre-2026-05-06, this was a 584-line procedural function with 25
// numbered ordered steps. As of 2026-05-07 it is a flat loop over the
// typed []GenStep plan defined in generate_pipeline.go — every legacy
// step is now its own GenStep entry with a dedicated stepXxx body.
//
// projectDir is the root of the project (contains go.mod, proto/, etc.).
// The caller must hold the project's generate lock and generateMu — via
// withGenerateLock, or inside a scaffold command (see that function).
func runGeneratePipeline(projectDir string, force bool) error {
	return runGeneratePipelineOpts(projectDir, force, false)
}

// runGeneratePipelineOpts is the variant that lets the caller pass
// additional pipeline flags (currently --skip-validate). Wrapping the
// shorter signature keeps test fixtures (and any out-of-tree callers)
// source-compatible.
func runGeneratePipelineOpts(projectDir string, force, skipValidate bool) error {
	return runGeneratePipelineFlags(projectDir, pipelineFlags{
		Force:        force,
		SkipValidate: skipValidate,
	})
}

// pipelineFlags is the typed bag of opt-in toggles for the generate
// pipeline. Grew out of the per-flag positional-arg signatures
// (runGeneratePipeline, runGeneratePipelineOpts) once the flag count
// crossed three — adding a fourth (--skip-pre-checks) without a struct
// would have meant churning every caller of the positional form.
type pipelineFlags struct {
	Force        bool
	SkipValidate bool
	// SkipValidateNote replaces the "--skip-validate" banner for a caller
	// that skips the compile check on the user's behalf (`project new`).
	SkipValidateNote string

	SkipPreChecks bool
	// SkipConfigCheck opts out of the forge.yaml ↔ filesystem cross-
	// check that stepLoadConfig runs after a successful load. The default
	// is loud-by-default: declared services / frontends / packages with
	// no on-disk backing (or vice versa) abort the pipeline at load time
	// with a batched report pointing at both sides of the asymmetry.
	// Opt-out exists for parallel-lane / mid-migration scenarios where a
	// transient mismatch is expected.
	SkipConfigCheck bool

	// ExplainDrift turns a Tier-1 drift abort into a diagnostic run:
	// drifted paths render to .forge/render/ side files, the run prints
	// a bounded on-disk-vs-fresh-render diff per file, and then still
	// fails with the drift report. See generate_explain_drift.go.
	ExplainDrift bool
	// Steps names a step preset that narrows the set of pipeline steps
	// the runner executes. The empty string runs the full pipeline (the
	// historical default). The "bootstrap-only" value runs JUST the
	// load/parse/bootstrap/validate subset — used by `forge scaffold worker`
	// so adding a single worker doesn't trigger a full project regen
	// that stomps unrelated Tier-1 files (.github/workflows/ci.yml,
	// cmd/server.go, frontend mocks, pkg/config/config.go). The step
	// preset allowlist lives in stepPresetAllowlist
	// (generate_pipeline.go).
	//
	// (Field previously named Scope; renamed because "scope" was
	// overloaded with the file-ownership concept in
	// internal/checksums/inspector.go. The CLI flag is --steps.)
	//
	// FRICTION 2026-06-03: cp-forge port-workers ran `forge scaffold worker`
	// 7× and watched regen rewrite 5 unrelated Tier-1 files per call.
	// Composes with the existing tier1OwnerRegistry scoping in
	// generate_tier1_scope.go — both narrow what `forge scaffold worker`
	// touches, just at different layers (drift-guard vs step execution).
	Steps string

	// ForceCleanup makes the stale-artifact sweep delete hand-edited
	// stale files as well as pristine ones. See the matching
	// pipelineContext.ForceCleanup field.
	ForceCleanup bool

	// TemplatesOnly restricts the pipeline to template-driven render
	// steps only. Skips the Tier-1 drift guard, the validation tail
	// (pre-codegen contract check, post-gen warnings, `go build`), the
	// stale-artifact cleanup sweep, and every external generator
	// (buf/protoc/goimports/go mod tidy/KCL).
	//
	// Use case: a forge template change (e.g. `bootstrap.go.tmpl` gets a
	// louder warning) needs to land in a downstream project that has
	// uncommitted WIP, so a full `forge generate` would either trip the
	// drift guard or shell out to tooling the partial tree can't build.
	// `--templates-only` re-renders just the files the changed template
	// emits, leaving the cleanup/drift/validate machinery for the next
	// full regen once the WIP settles.
	//
	// Composes with Steps: when both are set, only steps that pass BOTH
	// allowlists run (intersection). When Steps is empty and
	// TemplatesOnly is set, every template-driven step runs. The
	// allowlist of template-driven step names lives in
	// templatesOnlyStepAllow (generate_pipeline.go).
	TemplatesOnly bool

	// Strict makes a step that fails through the warnOrFail helper stop
	// the run (and roll the tree back) at that step. Without it the run
	// continues with the steps that do not depend on the failed one,
	// keeps their writes, and still fails at the end with a
	// *factory.StepsFailedError — a failed step never exits 0 either way.
	Strict bool

	// Verbose toggles per-step skip messages. The generate pipeline runs
	// dozens of steps, most of which are gated off by project-shape
	// predicates (no frontends → skip all frontend steps). The default
	// (false) is silent skip — the user only sees output from steps that
	// actually ran. When true, every gated-off step prints one line:
	// "⏩ skipped: <step name> (gate: <gate name>)". Diagnostic surface
	// for "why didn't generate touch X?" questions without requiring
	// --plan.
	Verbose bool

	// NoRevert (--no-revert) is a codegen-forensics escape hatch. By default
	// a post-write step failure — most importantly the final `go build`
	// validate — rewinds the tree to its clean pre-run state (the rollback
	// journal armed by BeginRollbackJournal), so the user is never left with a
	// mid-regen tree to `git checkout`. That same rewind, though, DELETES the
	// generated output that failed to build — exactly what a forge/codegen
	// developer needs to read to diagnose the bug. With NoRevert set, the
	// journal is dropped (CommitRollback) instead of restored on failure, so
	// the generated artifacts stay on disk for inspection; the run still exits
	// non-zero with the underlying error. Off by default (the safe,
	// clean-tree behavior).
	NoRevert bool

	// Heal (--heal) opts IN to overwriting on-disk content that matches a
	// PRIOR forge render (an older vintage, but not the latest) with the
	// current template. It is OFF by default, and that default is the
	// correctness fix for FRICTION cp-forge fr-2c1c2328c7: such content is
	// byte-indistinguishable from a deliberate user revert/edit (a
	// hand-edit to pkg/app/bootstrap.go hash-equaled a prior render and was
	// silently reverted). With Heal off, writers SKIP these files and
	// NoHealSkipFn names them plus the remedies; forge never silently
	// destroys the bytes. With Heal on, the whole tree advances to the
	// current templates (loudly — checksums.HealNoticeFn still fires per
	// file). `forge generate --force` is the per-file equivalent.
	Heal bool

	// Timings (--timings, or FORGE_GENERATE_TIMINGS=1) prints each step's
	// wall time when the run ends. See generate_timings.go.
	Timings bool

	// Cache opts the run into the generate cache (generate_cache.go): skip
	// when the tree is a recorded fixed point, record one when the run
	// changes nothing. Only the `forge generate` command sets it — every
	// other pipeline entry point (a scaffold's regenerate, watch mode) runs
	// in full and records nothing.
	Cache bool
	// NoCache (--no-cache, or FORGE_GENERATE_NO_CACHE=1) runs every step
	// even when the tree is a recorded fixed point. The run still records,
	// so it is also how to refresh the record.
	NoCache bool
}

// announceSkippedChecks names, before any step runs, each check this run
// was told to skip.
func announceSkippedChecks(flags pipelineFlags) {
	switch {
	case flags.SkipValidateNote != "":
		fmt.Println(flags.SkipValidateNote)
	case flags.SkipValidate:
		fmt.Println("⏩ --skip-validate: the final compile check (validate generated code) will be skipped")
	}
	if flags.SkipPreChecks {
		fmt.Println("⚠️  pre-codegen contract check skipped via --skip-pre-checks")
	}
}

// resetChecksumRunState starts checksums' per-run state over: side-render
// redirect tracking (--explain-drift), the heal-notice dedupe set, and the
// heal opt-in, which is off unless this run passed --heal (AutoHeal defaults
// OFF — the non-destructive behavior).
func resetChecksumRunState(flags pipelineFlags) {
	checksums.ResetPerRunState()
	if flags.Heal {
		fmt.Println("♻️  --heal: on-disk content matching a PRIOR forge render will be overwritten with the current template")
		checksums.AutoHeal = true
	}
}

// runGeneratePipelineFlags is the canonical entrypoint. Both the
// shorter runGeneratePipeline (force) and runGeneratePipelineOpts
// (+ skipValidate) call through here. New flags land on pipelineFlags.
//
// It takes no lock itself: the caller holds the project's generate lock
// and generateMu (withGenerateLock), because several callers must hold
// them across more than the pipeline — a scaffold command's own writes,
// rescaffold's deletions, --explain's before/after scans.
func runGeneratePipelineFlags(projectDir string, flags pipelineFlags) error {
	ctx, err := newPipelineContextWithFlags(projectDir, flags)
	if err != nil {
		return err
	}

	// The process state one run owns — routine-line verbosity, and the
	// shadow schema every step that reads the applied schema shares — is
	// opened here and closed on the way out. See beginGenerateRun.
	defer beginGenerateRun(flags)()

	// Fingerprint the tree BEFORE any step can write. Three claims are
	// checked against it rather than asserted from the journal alone: a
	// failed run's "your tree is back to its pre-run state", a successful
	// run's "N changed" (taken under --no-revert too: agents read that line
	// as the fixed-point check, so it must count every write, whatever made
	// it), and the generate cache's "nothing changed since a verified run".
	preRun := snapshotWorkTree(ctx.AbsPath)

	// The whole-run skip (generate_cache.go): when this exact tree, binary,
	// toolset and environment were already verified as a fixed point,
	// running the pipeline again can only reproduce them.
	cache := openGenerateCache(ctx.AbsPath, flags, preRun, generateCacheSystem())
	if cache.skip(os.Stdout) {
		return nil
	}

	announceSkippedChecks(flags)
	resetChecksumRunState(flags)

	// Arm the stage-then-validate rollback journal (fr-40f7ec9bd9). From
	// here on, every forge write captures its target's pre-run bytes so a
	// post-write failure — most importantly the final `go build` validate
	// step — can rewind the tree to its clean pre-regen state instead of
	// leaving it mid-regen for the user to `git checkout`. rolledBack is
	// set by the deferred outcome handler below; it gates SaveChecksums so
	// we never persist manifest state describing writes we just undid.
	checksums.BeginRollbackJournal(ctx.AbsPath)
	rolledBack := false

	// Save checksums on exit, even on partial failures: a step that
	// successfully wrote files should have those tracked so the user's
	// next `forge project audit` doesn't false-flag user-edited drift. EXCEPTION:
	// a rolled-back run restored the tree to its pre-run state, so saving
	// the post-write manifest would describe files that no longer exist on
	// disk — skip it and let the pre-run state files stand. The success
	// path saves before its completion line instead (checksumsSaved), so
	// the line counts the state files.
	checksumsSaved := false
	defer func() {
		if ctx.Checksums == nil {
			return
		}
		if rolledBack || checksumsSaved {
			return
		}
		if saveErr := generator.SaveChecksums(ctx.AbsPath, ctx.Checksums); saveErr != nil {
			log.Printf("Warning: failed to save checksums: %v", saveErr)
		}
	}()

	steps, totalSteps, err := selectPipelineSteps(flags)
	if err != nil {
		return err
	}

	// A plan that keeps the stomp guard keeps its fresh-render check.
	steps = withDriftAdjudication(steps)

	// --timings / FORGE_GENERATE_TIMINGS: per-step wall time, printed on
	// the way out whatever the outcome.
	var timer *stepTimer
	if generateTimingsRequested(flags.Timings) {
		timer = newStepTimer()
		defer timer.print(os.Stderr)
	}

	// onlyReadOnlyRan stays true while every step that has RUN is declared
	// ReadOnly. Together with an empty journal it is the evidence behind a
	// failure's "No files were changed" — see rollbackGeneratedTree.
	onlyReadOnlyRan := true
	// ran is the steps whose gate passed: the cache records the outputs
	// their external tools declare.
	var ran []GenStep
	for _, step := range steps {
		if !step.Gate(ctx) {
			// Verbose mode prints one line per gate-off skip so the user
			// can answer "why didn't generate touch X?" without --plan.
			// Default (silent) preserves the historical low-noise output.
			if ctx.Verbose {
				fmt.Fprintf(os.Stderr, "⏩ skipped: %s (%s)\n", step.Name, gateSkipReason(step))
			}
			continue
		}
		ran = append(ran, step)
		if !step.ReadOnly {
			onlyReadOnlyRan = false
		}
		// An external tool's outputs join the rollback set before the
		// tool runs, exactly as a forge write captures its target.
		if step.Writes != nil {
			checksums.CaptureExternalWrites(ctx.AbsPath, step.Writes(ctx)...)
		}
		stepStart := time.Now()
		err := step.Run(ctx)
		if timer != nil {
			timer.record(step.Name, time.Since(stepStart))
		}
		if err != nil {
			// --explain-drift cleanup still runs on a mid-pipeline
			// failure: whatever renders were parked are diffed, and the
			// snapshot restore keeps the deferred SaveChecksums honest.
			// The step error wins over the drift error.
			if expErr := finishExplainDrift(ctx); expErr != nil {
				fmt.Fprintf(os.Stderr, "%v\n", expErr)
			}
			// Same for the legacy-migration quarantine: stamp the
			// unverified sentinels now so the next run's guard still
			// names the unresolved files. Done BEFORE the rollback so the
			// sentinel stamps survive — they describe the user's pre-run
			// drifted files, not this run's reverted output.
			if migErr := finishLegacyMigration(ctx); migErr != nil {
				fmt.Fprintf(os.Stderr, "%v\n", migErr)
			}
			// Stage-then-validate rollback: a step failed AFTER writes
			// landed (the classic case: the final `go build` validate),
			// so the tree is mid-regen. Rewind every forge-written file to
			// its pre-run state and tell the user the tree is clean rather
			// than leaving them a `git checkout` to find (fr-40f7ec9bd9).
			//
			// --no-revert opts OUT: a forge/codegen developer debugging a
			// generator bug needs to READ the output that failed to build,
			// which the rewind would delete. Keep the artifacts on disk (drop
			// the journal instead of restoring it) and say so; the run still
			// fails with the underlying error.
			if flags.NoRevert {
				checksums.CommitRollback()
				fmt.Fprintln(os.Stderr, "\n🔎 --no-revert: leaving this run's generated files on disk for inspection (the tree was NOT rewound). Re-run without --no-revert to restore the clean pre-run tree.")
				// The compiler output scrolls away behind the pipeline's
				// own tail output — repeat it here so the last thing on
				// screen is the thing to fix (same rationale as the
				// rollback branch).
				reprintCompilerOutput(err, "")
			} else {
				rolledBack = rollbackGeneratedTreeVerified(ctx.AbsPath, err, onlyReadOnlyRan, preRun)
			}
			return fmt.Errorf("step %q: %w", step.Name, err)
		}
	}
	reportGenerateHooksNotRun(os.Stdout, ctx, steps, flags)

	// --explain-drift: print the per-file diffs and fail with the drift
	// report — the flag explains the drift, it never approves it. No-op
	// nil when the guard found no drift (or the flag wasn't set).
	if err := finishExplainDrift(ctx); err != nil {
		// Deliberate end-state, not a mid-regen tree: --explain-drift
		// redirected its writes to side renders and never touched the
		// drifted files, so the journal's real-file captures stand.
		checksums.CommitRollback()
		return err
	}

	// Legacy-manifest quarantine adjudication: rescue fresh-render
	// matches, stamp unverified sentinels on the rest, and fail with the
	// standard drift report when any file stays unresolved. The emit pass
	// SUCCEEDED here; the failure is an adjudication the next run needs,
	// so the writes (and the sentinel stamps) stand — do not roll back.
	if err := finishLegacyMigration(ctx); err != nil {
		checksums.CommitRollback()
		return err
	}

	// Success: the writes stand. The ownership state files are the run's
	// last writes; land them first, so the completion line counts them.
	if ctx.Checksums != nil {
		if saveErr := generator.SaveChecksums(ctx.AbsPath, ctx.Checksums); saveErr != nil {
			log.Printf("Warning: failed to save checksums: %v", saveErr)
		}
		checksumsSaved = true
	}
	// Read the write ledger BEFORE dropping the journal — it is the record
	// of what forge's writers did — and check it against the pre-run
	// fingerprint, which sees every write, journaled or not.
	summary, outside := completeWriteSummary(checksums.SummarizeWrites(ctx.AbsPath), ctx.AbsPath, preRun)
	checksums.CommitRollback()
	if err := os.RemoveAll(filepath.Join(ctx.AbsPath, failedGenerateDir)); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Warning: could not clean up %s: %v\n", failedGenerateDir, err)
	}
	fmt.Println()
	printGenerateOutcome(os.Stdout, summary, outside, flags, len(steps), totalSteps)
	printLeftAlone(os.Stdout, checksums.LeftAloneThisRun())
	// Last, after the journal is committed and the outcome printed: a run
	// that changed nothing has proven this tree is generate's own output —
	// unless a step failed soft, in which case its outputs were not
	// produced, let alone checked.
	cache.record(os.Stdout, ctx, ran, summary)
	return softFailureOutcome(os.Stdout, ctx.SoftFailures)
}

// softFailureOutcome ends a run that went on past failed steps: it names
// them as the last thing the run prints, and fails the run with them. The
// run's other writes stand, but exit 0 is read as success by every agent,
// script and CI gate, and this run did not produce everything it should.
func softFailureOutcome(w io.Writer, failed []string) error {
	if len(failed) == 0 {
		return nil
	}
	printSoftFailures(w, failed)
	return &factory.StepsFailedError{Steps: slices.Clone(failed)}
}

// beginGenerateRun opens the per-run process state a pipeline run owns and
// returns the function that closes it, in reverse order:
//
//   - verbosity: routine "forge did nothing here" lines are printed by
//     helpers well below the pipeline frame (generate_verbosity.go);
//   - the shadow-schema run scope: every step that reads the applied schema
//     (entities, ORM, CRUD wiring, pages, nav, test factories, mocks) shares
//     ONE application of each migration set, and the template databases are
//     dropped when the run ends (internal/shadowdb/applied.go). Opening it
//     costs nothing until a step reads the schema, so a run the generate
//     cache skips never touches postgres.
func beginGenerateRun(flags pipelineFlags) (end func()) {
	restoreVerbosity := setGenerateVerbosity(flags.Verbose)
	endShadowRun := shadowdb.BeginRun()
	return func() {
		endShadowRun()
		restoreVerbosity()
	}
}

// printSoftFailures closes a run that continued past failed steps by naming
// them under the completion line. Each was printed where it happened, often
// hundreds of lines up; a completion line alone reads as a clean run.
func printSoftFailures(w io.Writer, failed []string) {
	if len(failed) == 0 {
		return
	}
	fmt.Fprintf(w, "❌ %d step(s) FAILED and the run continued without their output: %s. Their errors are above; "+
		"steps that depend on them said they were skipped. The run exits non-zero; `--strict` stops it at the first failed step instead.\n",
		len(failed), strings.Join(failed, ", "))
}

// selectPipelineSteps returns the steps this run executes after the
// caller-intent filters (--steps, --templates-only), plus the size of the
// full plan for the "running N of M" lines.
func selectPipelineSteps(flags pipelineFlags) (steps []GenStep, total int, err error) {
	// Step-preset filter — when flags.Steps is non-empty, drop steps not
	// in the allowlist BEFORE the Gate check. The gate is a project-shape
	// predicate ("does this project have services?"); the step preset is
	// a caller-intent predicate ("am I doing a bootstrap-only regen?").
	// They compose: a step that's allowlisted by the preset still has to
	// pass its Gate, and a step gated off would skip regardless of the
	// preset.
	steps = pipelineSteps()
	total = len(steps)
	if flags.Steps != "" {
		allow, ok := stepPresetAllowlist[flags.Steps]
		if !ok {
			return nil, 0, fmt.Errorf("unknown pipeline step preset %q (valid: %s)", flags.Steps, knownStepPresetNames())
		}
		filtered := steps[:0:0]
		for _, step := range steps {
			if allow[step.Name] {
				filtered = append(filtered, step)
			}
		}
		fmt.Printf("⏩ steps=%s: running %d of %d pipeline steps\n", flags.Steps, len(filtered), len(steps))
		steps = filtered
	}

	// --templates-only filter — intersects with --steps (if both are
	// set, a step must pass BOTH allowlists). Applied AFTER --steps so
	// the user-visible "running N of M" count reflects whichever filter
	// is narrower. See templatesOnlyStepAllow for the included set and
	// the WIP-tree rationale.
	if flags.TemplatesOnly {
		before := len(steps)
		filtered := steps[:0:0]
		for _, step := range steps {
			if templatesOnlyStepAllow[step.Name] {
				filtered = append(filtered, step)
			}
		}
		fmt.Printf("⏩ --templates-only: running %d of %d pipeline steps (skipping cleanup, drift-guard, validation, external generators)\n", len(filtered), before)
		steps = filtered
	}
	return steps, total, nil
}

// completeWriteSummary adds to sum every file the pre-run fingerprint
// shows changed that forge's write journal does not hold, and returns the
// ones outside forge's own state directory (.forge/) separately, so the
// completion line can name them.
//
// The journal records the writes that go through forge's chokepoints. A
// write that does not — the scaffold birth ledger saving itself, a step's
// raw os.WriteFile, a tool nobody declared — used to vanish from the count:
// generate printed "0 changed" while rewriting .forge/scaffolded.json, and
// the agent reading that line as its fixed point never saw the drift. The
// fingerprint closes the class rather than the instance: any file whose
// bytes differ from the pre-run tree counts, whatever wrote it.
//
// A nil snapshot (the tree could not be listed) leaves sum as the journal
// alone reported it.
func completeWriteSummary(sum checksums.WriteSummary, root string, pre *workTreeSnapshot) (checksums.WriteSummary, []string) {
	if pre == nil {
		return sum, nil
	}
	changed, ok := pre.changedSince(root, nil)
	if !ok {
		return sum, nil
	}
	counted := map[string]bool{}
	for _, set := range [][]string{sum.Created, sum.Updated, sum.Deleted} {
		for _, rel := range set {
			counted[rel] = true
		}
	}
	var outside []string
	for _, rel := range changed {
		if counted[rel] {
			continue
		}
		sum.Touched++
		_, existed := pre.files[rel]
		_, exists := stampOf(root, rel, false)
		switch {
		case !existed:
			sum.Created = append(sum.Created, rel)
		case !exists:
			sum.Deleted = append(sum.Deleted, rel)
		default:
			sum.Updated = append(sum.Updated, rel)
		}
		if !strings.HasPrefix(rel, ".forge/") {
			outside = append(outside, rel)
		}
	}
	sort.Strings(sum.Created)
	sort.Strings(sum.Updated)
	sort.Strings(sum.Deleted)
	return sum, outside
}

// pipelineSteps is the step plan runGeneratePipelineFlags executes —
// generateSteps, except in tests that drive the real pipeline driver
// (journal arming, external-write capture, rollback and its report) over a
// reduced plan.
var pipelineSteps = generateSteps

// printGenerateOutcome renders the completion line from the run's write
// ledger instead of from "the last step returned nil".
//
// FRICTION: `forge generate --steps mocks` printed `✅ Code generation
// complete!` while regenerating NOTHING — the preset's allowlist omitted
// the emitter for the very workflow it is named after. The allowlist bug
// was fixable; the message that hid it is the deeper defect, because a
// success line that reads identically after 93 writes and after zero
// writes cannot be believed in either case. So the line states counts,
// and the zero-write case is LOUD rather than green.
//
// The three outcomes are genuinely different facts and must not share
// wording:
//
//	Touched == 0  no emitter fired. The pipeline produced nothing at all.
//	              This is the silent-no-op signature; it is a warning with
//	              the two things that cause it and the command that tells
//	              them apart.
//	Changed == 0  every emitter fired and the bytes already matched. This
//	              is the normal, healthy re-run and stays a quiet one-liner.
//	Changed > 0   real work landed. Name the counts; the file names are
//	              already on screen from the per-step output above.
func printGenerateOutcome(w io.Writer, sum checksums.WriteSummary, outside []string, flags pipelineFlags, ranSteps, totalSteps int) {
	if sum.Touched == 0 && sum.Changed() == 0 {
		fmt.Fprintf(w, "⚠️  Code generation ran %d of %d pipeline steps and WROTE NO FILES.\n", ranSteps, totalSteps)
		switch {
		case flags.Steps != "" || flags.TemplatesOnly:
			// The narrowed pipeline is the case that actually shipped
			// broken, so it gets the sharpest next command.
			fmt.Fprintf(w, "    Either the tree already matched every template, or the narrowed step set excludes the emitter you need\n")
			fmt.Fprintf(w, "    (the \"mocks\" preset once omitted its own mock emitter and reported success from exactly here).\n")
			fmt.Fprintf(w, "    Fix: run `forge generate --plan%s` to see which steps the filter selects, or drop the filter to run the full pipeline.\n", planFlagEcho(flags))
		default:
			fmt.Fprintf(w, "    A full pipeline that emits nothing means every codegen step was gated off by project shape.\n")
			fmt.Fprintf(w, "    Fix: run `forge generate --plan` to see each step's [RUN]/[SKIP] verdict and the gate reason behind it.\n")
		}
		return
	}
	if sum.Changed() == 0 {
		fmt.Fprintf(w, "✅ Code generation complete — %d file(s) up to date, 0 changed.\n", sum.Touched)
		return
	}
	detail := fmt.Sprintf("%d new, %d updated", len(sum.Created), len(sum.Updated))
	if len(sum.Deleted) > 0 {
		detail += fmt.Sprintf(", %d removed", len(sum.Deleted))
	}
	fmt.Fprintf(w, "✅ Code generation complete — %d file(s) written, %d changed (%s).\n",
		sum.Touched, sum.Changed(), detail)
	if len(outside) > 0 {
		// Counted above, and named here: no forge writer recorded these, so
		// either a step wrote them directly or another process did while
		// generate ran. Either way the tree moved, and the reader decides.
		fmt.Fprintf(w, "   %d of them changed outside forge's writers: %s\n", len(outside), strings.Join(outside, ", "))
	}
}

// printLeftAlone names, after the counts, every path a writer declined this
// run for a reason the user must act on (checksums.NoteLeftAlone): a
// hand-written file at a path forge generates, TypeScript stubs another
// generator owns. A refusal printed only by the step that made it scrolls
// away, and the closing line then reads as though the run did everything.
func printLeftAlone(w io.Writer, left []checksums.LeftAlone) {
	if len(left) == 0 {
		return
	}
	fmt.Fprintf(w, "⚠️  %d path(s) left as they are — forge did not write them:\n", len(left))
	for _, l := range left {
		fmt.Fprintf(w, "   - %s: %s\n", l.Path, l.Why)
	}
}

// planFlagEcho reproduces the pipeline-narrowing flags so the suggested
// `--plan` command reproduces the run the user just made, rather than
// planning a different (full) pipeline that would not show the gap.
func planFlagEcho(flags pipelineFlags) string {
	var b strings.Builder
	if flags.Steps != "" {
		fmt.Fprintf(&b, " --steps=%s", flags.Steps)
	}
	if flags.TemplatesOnly {
		b.WriteString(" --templates-only")
	}
	return b.String()
}

// failedGenerateDir is where a failed run's generated output is
// preserved (project-relative, mirroring each file's project path).
// The revert is the right default — the user's tree must come back
// clean — but pre-preservation it also destroyed the only sources the
// validation error CITES: `go build` fails with file:line coordinates
// in generated code, the revert deletes that code, and the user is left
// debugging a compiler error against files that no longer exist. The
// next successful generate removes the directory.
const failedGenerateDir = ".forge/failed-generate"

// failedGenerateErrorFile holds the failing step's full error text
// (including the complete compiler output for a `go build` validation
// failure) inside failedGenerateDir.
const failedGenerateErrorFile = "error.txt"

// rollbackGeneratedTree rewinds every file forge wrote this run to its
// captured pre-run state and reports what it recovered. Returns true when
// a rollback actually ran (journaling was armed) so the caller can skip
// the post-write checksums save — a restored tree must not be described
// by a manifest of the writes it just undid. The user-facing message is
// the antidote to fr-40f7ec9bd9: instead of an opaque mid-regen failure
// the user learns the tree is back to its pre-run state and that the
// codegen problem itself is what needs fixing.
//
// Before rewinding, the failed run's output is preserved under
// .forge/failed-generate/ (SnapshotJournalTargets) so the error's
// file:line coordinates stay inspectable, and stepErr's compiler output
// (when it carries any — see validateBuildError) is REPEATED after the
// reverted-file list: the original print scrolls away behind that list,
// and a `tail` of the run must show the error, not just bookkeeping.
//
// onlyReadOnlyRan reports whether every step that ran is declared
// GenStep.ReadOnly. The journal records forge's own writers, not every
// write a step can make (an external tool, a KCL render), so an empty
// journal alone proves nothing about the tree. "No files were changed" is
// printed only when both facts hold; otherwise the message says what the
// journal shows and no more. It used to assert "tree is unchanged" from an
// empty journal alone, over a .forge-kcl/ an unjournaled step had just
// rewritten.
//
// The journal covers external-tool outputs too (GenStep.Writes), and
// rollbackGeneratedTreeVerified checks the restored tree against a pre-run
// fingerprint before claiming it is back to its pre-run state.
func rollbackGeneratedTree(absPath string, stepErr error, onlyReadOnlyRan bool) bool {
	return rollbackGeneratedTreeVerified(absPath, stepErr, onlyReadOnlyRan, nil)
}

// rollbackGeneratedTreeVerified is rollbackGeneratedTree with the pre-run
// fingerprint the pipeline took before its first step (nil: none taken, so
// no whole-tree claim can be verified and none is made).
func rollbackGeneratedTreeVerified(absPath string, stepErr error, onlyReadOnlyRan bool, preRun *workTreeSnapshot) bool {
	if !checksums.RollbackEnabled() {
		return false
	}
	preserved := checksums.SnapshotJournalTargets(absPath, filepath.Join(absPath, failedGenerateDir))
	if len(preserved) > 0 {
		writeFailedGenerateErrorFile(absPath, stepErr)
	}
	changed := checksums.ChangedJournalPaths(absPath)
	journaled := checksums.JournaledPaths()
	born := checksums.ScaffoldsCreatedThisRun()
	restored, failed := checksums.RestoreRollbackReport(absPath)
	var unborn []string
	for _, p := range born {
		if _, statErr := os.Stat(filepath.Join(absPath, filepath.FromSlash(p))); os.IsNotExist(statErr) {
			unborn = append(unborn, p)
		}
	}
	residue := verifyRestoredTree(absPath, preRun, append(journaled, restored...), failed)
	if len(restored) == 0 {
		switch {
		case onlyReadOnlyRan && len(failed) == 0:
			fmt.Fprintln(os.Stderr, "↩️  generate stopped before any step that writes had run. No files were changed.")
		case residue.Clean():
			fmt.Fprintln(os.Stderr, "↩️  generate failed before changing any file — verified: the tree is unchanged.")
		case residue.Checked:
			fmt.Fprintln(os.Stderr, "↩️  generate failed; nothing needed reverting, but these files differ from before the run:")
			writeResidue(os.Stderr, residue.Changed)
		default:
			fmt.Fprintln(os.Stderr, "↩️  generate failed; nothing needed reverting (no write this run was recorded). The rest of the "+
				"tree could not be checked — run `git status` to confirm it is as you left it.")
		}
		reprintCompilerOutput(stepErr, "")
		return true
	}
	// Verify the reassurance before offering it. "Back to your pre-run
	// state" is only true if pre-run state was CONSISTENT, and it is not
	// always: a file kept from an earlier successful run can depend on one
	// this run generated and just reverted (compose.go vs the reverted
	// blobstore/middleware_gen.go). See goBuildRestoredTree.
	//
	// Only when the run touched a Go build input, though. If it did not, the
	// Go sources the compiler would read are the ones the user started with,
	// so the verdict could only describe the pre-run tree — and it cost a
	// full build of the project to learn that (about 9 minutes on reliant,
	// after a run that changed nothing but forge.yaml).
	consistency := rollbackConsistency{SkippedNoGoChange: true}
	if goBuildInputsTouched(changed, residue) {
		consistency = restoredTreeCompileCheck(absPath)
	}

	writeRollbackReport(os.Stderr, rollbackReport{
		Restored:    restored,
		Preserved:   preserved,
		StepErr:     stepErr,
		Consistency: consistency,
		Residue:     residue,
		Unborn:      unborn,
	})

	preservedAt := ""
	if len(preserved) > 0 {
		preservedAt = failedGenerateDir
		fmt.Fprintf(os.Stderr, "\n🗂  The failing generated sources were preserved under %s/ (same relative paths, plus %s with the full error) so you can inspect the code the error points at. The next successful `forge generate` cleans that directory up.\n", failedGenerateDir, failedGenerateErrorFile)
	}
	// Repeat the ROOT CAUSE output last as well. The report above can run
	// long (71 reverted files in the observed run), so a `tail -n` must
	// still land on the error to fix rather than on bookkeeping — the
	// tail-visibility contract from the preceding friction fix. It is the
	// root cause that gets repeated, never the rollback artifacts.
	reprintCompilerOutput(stepErr, preservedAt)
	fmt.Fprintln(os.Stderr, "\n   Fix the ROOT CAUSE above, then re-run `forge generate`.")
	return true
}

// reprintCompilerOutput repeats a validation failure's compiler output
// at the END of the failure report. The original output prints before
// the reverted-file list and the final error line, so on a long run it
// scrolls off screen and `tail -n` shows only bookkeeping — a real
// agent burned a full extra pipeline run piped through grep just to
// re-capture the error. No-op when stepErr carries no build output
// (non-validate step failures). preservedAt names the directory where
// the cited sources now live ("" when nothing was preserved).
func reprintCompilerOutput(stepErr error, preservedAt string) {
	var ve *validateBuildError
	if !errors.As(stepErr, &ve) || strings.TrimSpace(ve.Output) == "" {
		return
	}
	where := ""
	if preservedAt != "" {
		where = fmt.Sprintf(" (cited files preserved under %s/)", preservedAt)
	}
	fmt.Fprintf(os.Stderr, "\n❌ Compiler output, repeated so it is the last thing on screen%s:\n", where)
	lines := strings.Split(strings.TrimRight(ve.Output, "\n"), "\n")
	shown := lines
	if len(shown) > compilerOutputTailLines {
		shown = shown[:compilerOutputTailLines]
	}
	for _, line := range shown {
		fmt.Fprintf(os.Stderr, "   %s\n", line)
	}
	if len(lines) > len(shown) {
		suffix := ""
		if preservedAt != "" {
			suffix = fmt.Sprintf("; full output in %s/%s", preservedAt, failedGenerateErrorFile)
		}
		fmt.Fprintf(os.Stderr, "   … (%d more line(s)%s)\n", len(lines)-len(shown), suffix)
	}
}

// compilerOutputTailLines bounds the repeated compiler output. The
// first errors are the actionable ones (the Go compiler reports in
// file order, and later errors are usually knock-ons), so a bounded
// head keeps the tail of the run useful without re-dumping megabytes
// on a catastrophically broken tree.
const compilerOutputTailLines = 60

// writeFailedGenerateErrorFile records the failing step's full error —
// including the complete compiler output when present — inside the
// failed-generate preserve directory, so the error text survives any
// terminal scrollback limit alongside the sources it cites.
func writeFailedGenerateErrorFile(absPath string, stepErr error) {
	if stepErr == nil {
		return
	}
	var b strings.Builder
	b.WriteString(stepErr.Error())
	b.WriteString("\n")
	var ve *validateBuildError
	if errors.As(stepErr, &ve) && strings.TrimSpace(ve.Output) != "" {
		b.WriteString("\n--- full compiler output ---\n")
		b.WriteString(ve.Output)
		if !strings.HasSuffix(ve.Output, "\n") {
			b.WriteString("\n")
		}
	}
	dest := filepath.Join(absPath, failedGenerateDir, failedGenerateErrorFile)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return
	}
	_ = atomicfile.WriteFile(dest, []byte(b.String()), 0o644)
}

// validateBuildError is the error returned by runGoBuildValidate. It
// carries the captured compiler output so the pipeline's
// failure path can repeat the compiler output at the END of the run
// (reprintCompilerOutput) and preserve it next to the failing sources
// (writeFailedGenerateErrorFile). Unwraps to the user-facing wrapped
// error so errors.Is/As and the printed message are unchanged.
type validateBuildError struct {
	Output string // full captured compiler output
	err    error  // the cliutil-wrapped user-facing error
}

func (e *validateBuildError) Error() string { return e.err.Error() }
func (e *validateBuildError) Unwrap() error { return e.err }

// runGoBuildValidate is the body of stepGoBuildValidate (was Step 9 in
// the pre-refactor pipeline). Kept as a non-step helper so unit tests
// can invoke it directly without spinning up the full GenStep loop.
//
// It compiles every package and every test variant in one pass and links
// nothing (compileTree, generate_validate_compile.go): it fails exactly
// where `go build ./...` would, plus where a forge-owned _test.go does not
// compile.
func runGoBuildValidate(projectDir string) error {
	fmt.Println("\n🔨 Validating generated code...")
	rep, err := compileTree(projectDir, true)
	if err != nil {
		// go list could not run at all. Its verdict is unknown, so ask the
		// build it replaced, whose report of the same problem (a broken
		// go.mod, an unusable toolchain) is the familiar one.
		out, buildErr := goBuildFallback(projectDir, os.Stderr)
		if buildErr != nil {
			return goBuildValidateFailure(projectDir, out, buildErr)
		}
		return nil
	}
	if rep.Build != "" {
		fmt.Fprint(os.Stderr, rep.Build)
		return goBuildValidateFailure(projectDir, rep.Build, errors.New("the tree does not compile"))
	}
	return validateTestFiles(projectDir, rep.TestFiles)
}

// goBuildValidateFailure builds the error a failed validate build returns.
// Its message is the ROOT CAUSE line of the failed-generate report and the
// last line of the run, so it QUOTES the first compiler error verbatim —
// file:line:col and message — rather than `exit status 1`.
//
// The roofers dogfood run is why: generate stopped emitting an op a
// sibling file called, and the ROOT CAUSE line read "go build failed: exit
// status 1. Fix: ensure all referenced types are imported" — wrong advice
// about the wrong thing, while `invoice_ops.go:64:10: s.crudCreatePaymentOp
// undefined` sat further down in the compiler dump. The full output still
// follows the ROOT CAUSE line; this makes the line itself actionable.
func goBuildValidateFailure(projectDir, errOutput string, runErr error) *validateBuildError {
	cause := runErr
	if errs := compilerErrorLines(errOutput); len(errs) > 0 {
		quoted := errs[0]
		if more := len(errs) - 1; more > 0 {
			quoted += fmt.Sprintf(" (+%d more compiler error(s) in the output)", more)
		}
		cause = errors.New(quoted)
	}
	fix := goBuildValidateFixHint(errOutput, func(rel string) goSourceOrigin {
		return goSourceOriginOf(filepath.Join(projectDir, rel))
	})
	return &validateBuildError{
		Output: errOutput,
		err: cliutil.WrapUserErr("forge generate (validate generated code)",
			"go build failed", "", fix, cause),
	}
}

// compilerErrorLines returns the lines of compiler output that carry a
// file:line coordinate, in order — the errors themselves, without the
// `# package` headers and indented continuation lines around them.
func compilerErrorLines(output string) []string {
	var errs []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue // continuation of the error above (have/want, etc.)
		}
		if strings.HasSuffix(line, ": too many errors") {
			continue // the compiler's truncation notice, not an error
		}
		if errorLineFile(line) != "" {
			errs = append(errs, strings.TrimSpace(line))
		}
	}
	return errs
}

// goGeneratedHeader is Go's generated-code convention
// (https://go.dev/s/generatedcode), which buf's stubs follow too.
var goGeneratedHeader = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// reconciledHeader marks a file the author owns but forge keeps a projection
// in sync inside (internal/app/compose.go, internal/app/lifecycle.go).
var reconciledHeader = regexp.MustCompile(`forge-scaffold \(USER-OWNED, (?:ADDITIVELY )?RECONCILED\)`)

// goSourceOrigin is who wrote a Go file, as far as the validate failure's
// remediation is concerned.
type goSourceOrigin int

const (
	// originHandWritten: fix it where it stands.
	originHandWritten goSourceOrigin = iota
	// originGenerated: never edit it; fix what it was generated from.
	originGenerated
	// originReconciled: the author's file, but forge injects lines into it on
	// every generate, so the failing line may be forge's.
	originReconciled
)

// goSourceOriginOf classifies path: generated (a forge:hash marker, or Go's
// generated-code convention), reconciled (forge's owned-but-reconciled
// header), or hand-written. Unreadable reads as hand-written.
func goSourceOriginOf(path string) goSourceOrigin {
	content, err := os.ReadFile(path)
	if err != nil {
		return originHandWritten
	}
	switch {
	case checksums.Verify(content) != checksums.NoMarker || goGeneratedHeader.Match(content):
		return originGenerated
	case reconciledHeader.Match(content):
		return originReconciled
	}
	return originHandWritten
}

// validateTestFiles closes the hole `go build ./...` leaves: it does not
// compile _test.go files, so a GENERATED test helper that is not valid Go
// passed validation and `forge generate` reported "✅ Code generation
// complete" over a tree that cannot build. The user then met the failure
// far from its cause — as `packages failed to load` from the contract
// linter and `[build failed]` from their next test run, neither of which
// names the generated file. Those helpers are Tier-1 (regenerated,
// forge:hash), so the user cannot even edit their way out.
//
// diags are compileTree's TestFiles: the compiler's diagnostics from the
// test builds that cite a _test.go file, relative to projectDir. They come
// from the same `go list -export -test` pass as the non-test verdict, so
// the tree is loaded once; it used to be loaded a second time through
// go/packages, which type-checked every dependency from source to answer
// this.
//
// This is a COMPILE check, deliberately not `go vet ./...`. Vet does
// typecheck test files, but it also runs its analyzer suite over
// HAND-WRITTEN code, and those findings are not build failures: a
// fmt.Printf arity bug in a user's own file makes `go vet` exit 1 while
// `go build` exits 0. Wiring vet in here would fail generation on
// projects that are perfectly valid, so the check is scoped to the one
// question validation should ask — does it compile?
//
// ONLY A FORGE-OWNED TEST FILE FAILS THE RUN. The paragraph above names
// the subject exactly — a file forge regenerates and stamps, which "the
// user cannot edit their way out" of. A user-owned _test.go is the
// opposite case in every respect: forge will not rewrite it, the author
// CAN fix it, and failing generate over it blocks the one command that
// would refresh the code the file is failing against. That is a
// deadlock, not a gate — the tree cannot be regenerated until the user
// edits a file by hand, and `forge generate` is what they would
// reasonably reach for to fix it.
//
// It is also the same false-positive class as the vet findings: a
// half-finished test a user is mid-way through writing has nothing to do
// with whether forge's output is correct.
//
// Ownership is read from forge's own certification marker rather than a
// filename list, so it stays true as emitters are added or renamed, and
// a `forge project disown` (which strips the marker) correctly moves a
// file from fail to warn along with the ownership it transferred.
func validateTestFiles(projectDir string, diags []string) error {
	// Split by OWNERSHIP, because the two halves warrant opposite
	// responses: forge's own output failing to compile is forge's bug and
	// fails the run, while a user-owned test is the user's to fix and must
	// never block the generate that would refresh what it compiles against.
	var owned, theirs []string
	seen := map[string]bool{}
	for _, d := range diags {
		if seen[d] {
			continue
		}
		seen[d] = true
		file := filepath.FromSlash(errorLineFile(d))
		if !filepath.IsAbs(file) {
			file = filepath.Join(projectDir, file)
		}
		if isForgeOwnedFile(file) {
			owned = append(owned, d)
		} else {
			theirs = append(theirs, d)
		}
	}

	// User-owned breakage is reported and moved past. Naming the file and
	// saying plainly that it is theirs is the whole remedy: the previous
	// behavior said "this is a forge codegen bug worth reporting" about a
	// file forge did not write.
	if len(theirs) > 0 {
		sort.Strings(theirs)
		fmt.Fprintf(os.Stderr, "\n⚠️  These _test.go files do not compile. They are YOURS — forge will not "+
			"regenerate them, so update or delete them (generate itself succeeded):\n%s\n",
			strings.Join(theirs, "\n"))
	}

	if len(owned) == 0 {
		return nil
	}

	sort.Strings(owned)
	output := strings.Join(owned, "\n") + "\n"
	fmt.Fprintf(os.Stderr, "\n%s", output)
	// Quote the first error on the ROOT CAUSE line, as goBuildValidateFailure
	// does: its first line, without the continuation lines that follow it.
	quoted, _, _ := strings.Cut(owned[0], "\n")
	if more := len(owned) - 1; more > 0 {
		quoted += fmt.Sprintf(" (+%d more compile error(s) in the output)", more)
	}
	fix := "a forge-generated _test.go file in this tree does not compile — this is a forge codegen bug worth reporting"
	if seam := missingHarnessSeam(owned); seam != "" {
		fix = seam
	}
	return &validateBuildError{
		Output: output,
		err: cliutil.WrapUserErr("forge generate (validate generated code)",
			"generated test files do not compile", "",
			fix,
			errors.New(quoted)),
	}
}

// missingHarnessSeam names the project seam a generated test harness
// failed on, or "" when the errors are not that. The harness authenticates
// its test caller through the project's pkg/middleware.ContextWithClaims —
// the setter forge scaffolds with pkg/middleware. A project adopted in
// place keeps its own pkg/middleware and auth package, and has no such
// function; that is the project lacking a seam, not forge emitting broken
// code, and the fix is one function in the project.
func missingHarnessSeam(errs []string) string {
	for _, e := range errs {
		if strings.Contains(e, "_gen_test.go") && strings.Contains(e, "undefined: middleware.ContextWithClaims") {
			return "the generated test harness installs its caller through pkg/middleware.ContextWithClaims, which this " +
				"project's pkg/middleware does not declare. Add `func ContextWithClaims(ctx context.Context, c *auth.Claims) " +
				"context.Context` (auth is github.com/reliant-labs/forge/pkg/auth) that puts the principal where your own " +
				"auth reads it, so tests run as a signed-in caller the way your interceptor makes one"
		}
	}
	return ""
}

// isForgeOwnedFile reports whether path is a file forge generates and
// certifies — i.e. one carrying the embedded `forge:hash` marker that
// every Tier-1 writer stamps on.
//
// The marker is the authority rather than the filename because it tracks
// ownership as it actually MOVES: `forge project disown` strips the
// marker, and from that moment the file genuinely is the user's, so it
// should stop failing their generate. A hardcoded list of emitted
// basenames would keep failing on a file forge had already handed over,
// and would silently omit the next emitter someone adds.
//
// Unreadable reads as NOT forge-owned. The conservative answer here is
// the one that does not fail the user's run on a file we could not even
// open, which proves nothing about forge's output.
func isForgeOwnedFile(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return checksums.Verify(content) != checksums.NoMarker
}

// contractDerivedSource names the hand-written file a contract package's
// generated artifact is rendered from, or "" when file is not one. The
// decision is the file's name: these four are exactly what contract codegen
// owns in a package directory (generator/contract: retirableArtifacts), and
// helpers_gen_test.go is the harness rendered from the same package's Deps.
func contractDerivedSource(file string) string {
	switch filepath.Base(file) {
	case "mock_gen.go", "middleware_gen.go", "tracing_gen.go", "metrics_gen.go":
		return "contract.go (or deps.go)"
	case "helpers_gen_test.go":
		return "the Deps struct in service.go"
	}
	return ""
}

// goBuildValidateFixHint inspects the `go build ./...` stderr captured
// by runGoBuildValidate and returns the most-actionable single-line
// remediation tip for the failure pattern seen.
//
// Pattern hierarchy (first match wins):
//
//  1. `undefined: orm.Type*` — protoc-gen-forge emitted a reference to
//     an orm.* constant that the project's pinned forge/pkg version
//     does NOT export. This is the "codegen plugin newer than runtime
//     pin" skew kalshi-trader's migration round hit four separate
//     times (TypeDoublePrecision/TypeReal landed in forge/pkg after
//     the project's go.mod pin). The fix is mechanical: bump the
//     forge/pkg pin in both root and gen/ and re-tidy.
//
//  2. `undefined:` against the project's own `pkg/config` package —
//     proto/<project>/config/v1/ likely has no annotated config fields yet.
//
//  3. Any other error with a coordinate — name the first one and who owns
//     the file it cites. originOf(rel) answers that for a
//     project-relative path: generated code is fixed at its inputs, a
//     hand-written file is fixed where it stands, and a reconciled file
//     (compose.go) may hold a line forge injected. Guessing a remedy from
//     the message ("ensure all referenced types are imported") is what
//     this replaced: it was wrong for the usual case, a hand-written file
//     calling something this generate did not emit.
//
//  4. No coordinate at all — generic "ensure imports / re-run generate".
//
// Extracted from runGoBuildValidate so unit tests can pin the hint
// selection without spinning up a tmpdir project + a real go build.
func goBuildValidateFixHint(errOutput string, originOf func(rel string) goSourceOrigin) string {
	if errOutput == "" {
		return "ensure all referenced types are imported and re-run 'forge generate'"
	}
	// Pattern 1: forge/pkg runtime skew. The protoc-gen-forge in PATH
	// is newer than the project's pinned forge/pkg version, so codegen
	// emits constants the runtime doesn't export.
	//
	// We match on `undefined: orm.Type` (covers TypeReal,
	// TypeDoublePrecision, and any future orm.Type<X> constant the
	// plugin emits — the pattern is forward-compatible without a
	// growing per-constant allowlist).
	if strings.Contains(errOutput, "undefined: orm.Type") {
		// The library is github.com/reliant-labs/forge — forge/pkg is a package
		// prefix inside it. `go get .../forge/pkg@latest` resolves the RETIRED
		// forge/pkg module instead, which makes every forge/pkg/* import ambiguous.
		return "forge/pkg pin is older than the codegen plugin (orm.Type* not exported). Run `go get github.com/reliant-labs/forge@latest && go mod tidy` in BOTH the project root and gen/ to bump the pin, then re-run 'forge generate'."
	}
	if strings.Contains(errOutput, "pkg/config") {
		return "ensure proto/<project>/config/v1/ has annotated config fields and re-run 'forge generate'"
	}
	if errs := compilerErrorLines(errOutput); len(errs) > 0 {
		file := errorLineFile(errs[0])
		origin := originHandWritten
		if originOf != nil {
			origin = originOf(file)
		}
		switch origin {
		case originGenerated:
			if src := contractDerivedSource(file); src != "" {
				return file + " is generated from " + src + " beside it and forge rewrites it on every 'forge generate'; " +
					"do not edit it. A compile error in it means this run did not rewrite it (it is disowned, or its bytes " +
					"were edited after forge wrote them — 'forge project audit' names which) or " + src + " no longer " +
					"parses the way the rest of the package expects. Fix that and re-run 'forge generate'; the file follows"
			}
			return file + " is generated — do not edit it. Fix what it was generated from (proto, migrations, " +
				"or the hand-written code it references) and re-run 'forge generate'; if those are correct, " +
				"this is a forge bug worth reporting"
		case originReconciled:
			// Calling compose.go "hand-written" blamed the author for a line
			// forge had injected into it moments earlier.
			return file + " is yours, but forge reconciles it on every generate, so the failing line may be one " +
				"forge injected (a component's construction, an import, a Deps key). If it is, fix what that line " +
				"was derived from (the component's Deps and constructor, or internal/app/providers.go) and re-run " +
				"'forge generate'; if those are correct, this is a forge bug worth reporting. If the line is your " +
				"own, fix it in place"
		}
		return file + " is hand-written, not generated: fix the compiler error quoted above in it, " +
			"then re-run 'forge generate'"
	}
	return "ensure all referenced types are imported and re-run 'forge generate'"
}

// preCodegenContractCheck runs the internal-package contract shape rule
// BEFORE any code generators write files. The bootstrap codegen template
// (internal/templates/project/bootstrap.go.tmpl) hardcodes references to
// <pkg>.Service / <pkg>.Deps / <pkg>.New(...) for every internal package;
// a contract.go that uses different names produces a bootstrap.go that
// doesn't compile. Catching this at validation time (rather than at the
// final `go build` step) gives the user a clear, actionable error
// pointing at their contract.go rather than a build error pointing at
// generated code.
//
// Honors `contracts.exclude` from forge.yaml so analyzer sub-packages and
// other non-bootstrap-managed internal packages can opt out.
//
// Only the contract-names rule runs here, and severity is not the reason:
// deps-are-interfaces is an error too. The question this check answers is
// narrower — would the code forge is about to WRITE fail to compile? A
// non-canonical contract breaks the bootstrap template, so it must stop
// generation. A concrete `Deps` field compiles fine; it just cannot be
// unit-tested, so it fails `forge lint` instead. Keeping the pre-codegen
// check tight to "what would break the next `go build`" is the design
// discipline from the validation-vs-lint split.
// contractExcludesFromConfig returns the contracts.exclude list from the
// project config, or nil when no config is loaded. (A local copy of the
// helper that moved to internal/cli/lint with `forge lint`; generate.go is
// the only remaining internal/cli caller.)
func contractExcludesFromConfig(cfg *config.ProjectConfig) []string {
	if cfg == nil {
		return nil
	}
	return cfg.Contracts.Exclude
}

func preCodegenContractCheck(projectDir string, cfg *config.ProjectConfig) error {
	internalDir := filepath.Join(projectDir, "internal")
	if _, err := os.Stat(internalDir); os.IsNotExist(err) {
		return nil
	}
	excludes := contractExcludesFromConfig(cfg)
	fs, err := contractcheck.Inspect(context.Background(), projectDir, contractcheck.Options{
		Rules:    []contractcheck.Rule{contractcheck.RuleInternalPackageContractNames},
		Excludes: excludes,
	})
	if err != nil {
		// PROMOTED 2026-06-07 from silent warn to hard error: a walk
		// error here (permission denied, transient I/O glitch) means we
		// can't confirm contract shape — proceeding silently would let
		// the pipeline emit bootstrap.go against an unvalidated tree,
		// and the user would diagnose a confusing build failure later.
		// The opt-out (--skip-pre-checks) exists for the parallel-lane
		// scenario where the walk error is expected.
		return cliutil.WrapUserErr("forge generate (pre-codegen contract check)",
			"unable to validate contracts (could not read internal/)",
			"",
			"check filesystem permissions on internal/, or run with --skip-pre-checks if this is a parallel-lane scenario",
			err)
	}
	if !contractcheck.HasErrors(fs) {
		return nil
	}

	// Surface each finding with the same actionable message the lint
	// command would emit, then abort the pipeline.
	fmt.Fprintln(os.Stderr, "\n❌ Internal-package contract convention violations:")
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, contractcheck.AsResult(fs).FormatText())
	fmt.Fprintln(os.Stderr, "Aborting before bootstrap codegen — fix the contract.go names above and retry.")
	return cliutil.UserErr("forge generate (pre-codegen contract check)",
		"internal-package contracts must declare 'type Service interface', 'type Deps struct', and 'func New(Deps) Service'",
		"",
		"fix the offending contract.go files (see findings above), or run 'forge lint --conventions' for the per-file detail")
}

// runGenerateCheck implements `forge generate --check` — the CI guard
// that verifies the tree matches what the generator would produce from
// current proto + forge.yaml + templates. Drift means someone forgot to run
// `forge generate` after editing a proto file or upgrading forge; CI should
// fail loudly so the gap doesn't ship.
//
// It regenerates a scratch copy of the working tree (ci_verify_scratch.go)
// and diffs the copy against itself, so the checkout is never written and
// every change reported is the generator's — the same mechanism as
// `forge ci verify-generated`, minus its self-certification and commit-policy
// passes and its validate build.
//
// The run consults the generate cache like any `forge generate`: a tree that
// a full run verified as its own output cannot drift, so the check passes
// without re-running. It never RECORDS one — it skips the validate build,
// and a record vouches for a validated tree. noCache forces the run.
func runGenerateCheck(noCache bool) error {
	root, err := projectRoot()
	if err != nil {
		return err
	}
	// Regenerate a scratch copy of the working tree, never the checkout
	// itself (ci_verify_scratch.go): every change the diff reports was made
	// by the generator, so uncommitted edits are judged, not misattributed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	scratch, err := newVerifyScratch(ctx, root, os.Stderr)
	if err != nil {
		return cliutil.UserErr("forge generate --check",
			"could not make a scratch copy of the checkout to regenerate", "",
			"--check needs git and a writable directory beside the repository: "+err.Error())
	}
	defer scratch.Remove()

	fmt.Println("[generate --check] running generate against a scratch copy of the working tree...")
	if err := scratchRegenerate(ctx, scratch, noCache, true); err != nil {
		return fmt.Errorf("generate pipeline: %w", err)
	}
	changed, err := scratch.changes(ctx)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		scratch.copyCacheBack()
		fmt.Println("[generate --check] no drift — tree matches generator output.")
		return nil
	}

	fmt.Fprintln(os.Stderr, "[generate --check] drift detected — the tree does not match generator output:")
	fmt.Fprintln(os.Stderr)
	for _, line := range changed {
		fmt.Fprintln(os.Stderr, line)
	}
	fmt.Fprintln(os.Stderr)
	return cliutil.UserErr("forge generate --check",
		"generated artifacts are out of date in the tree",
		"",
		"run 'forge generate' locally, commit the result, and push")
}
