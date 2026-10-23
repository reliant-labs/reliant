// `forge generate --plan` and the loud-by-default helpers.
//
// The plan mode complements --explain (provenance log after the run) and
// --check (drift detector that runs the pipeline) by inspecting the
// pipeline WITHOUT running anything: it prints the step list with a
// [RUN] / [SKIP] annotation per step, derived from each step's Gate(ctx)
// against the configured project state, then exits.
//
// This is the diagnostic that answers "what would forge generate do here?"
// without the side-effects of actually doing it — useful when planning a
// big refactor, debugging "why didn't generate touch X?", and writing CI
// guards that need to know whether a pipeline change would alter the step
// set for a given project shape.
//
// The warnOrFail helper lives here too because it shares the loud-by-
// default thesis: a step that routes its failure through it lets the run
// continue with the steps that do not need it, and the run still exits
// non-zero; --strict stops the run at that step instead.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// runGeneratePlan implements `forge generate --plan`. It mirrors the
// pre-Run setup of runGeneratePipelineFlags exactly (build the context,
// honor --steps / --templates-only filters) but skips the for-loop over
// step.Run() — instead it prints each step's annotation and exits 0.
//
// Gates are pure (TestGenerateStepsGatesAreSideEffectFree), so calling
// them without running the step is safe. The context still needs the
// project-shape flags populated to make gate evaluation meaningful — we
// run the prefix of "always" steps (load config, load checksums, detect
// proto dirs) before the print loop so HasServices / Cfg / etc. are
// realistic.
//
// Hold generateMu around the whole call because we read the .forge state files
// and forge.yaml — concurrent `forge scaffold` would otherwise see a window
// where checksums are loaded but not refreshed.
func runGeneratePlan(projectDir string, flags pipelineFlags) error {
	generateMu.Lock()
	defer generateMu.Unlock()

	ctx, err := newPipelineContextWithFlags(projectDir, flags)
	if err != nil {
		return err
	}

	steps := generateSteps()

	// Honor --steps and --templates-only filters for parity with the
	// real run — same map plumbing as runGeneratePipelineFlags. Use the
	// allowlist intersection when both flags are set.
	stepsAllow, presetSet := planStepAllow(flags)
	templatesAllow := flags.TemplatesOnly

	// Pre-fly the gate-input setup steps so the Gate predicates see a
	// realistic ctx (HasServices, HasConfig, etc. are populated by
	// stepDetectProtoDirs; Cfg by stepLoadConfig; Checksums by
	// stepLoadChecksums). We run the smallest prefix needed to make the
	// gates meaningful, NOT the full pipeline.
	for _, name := range planSetupSteps {
		for _, s := range steps {
			if s.Name != name {
				continue
			}
			if err := s.Run(ctx); err != nil {
				return fmt.Errorf("plan setup step %q: %w", name, err)
			}
		}
	}

	// Tally before printing so the header is accurate.
	willRun := 0
	annotated := make([]planEntry, 0, len(steps))
	for _, step := range steps {
		entry := planEntry{Name: step.Name, GateName: gateName(step.Gate)}
		if presetSet && !stepsAllow[step.Name] {
			entry.Status = "SKIP"
			entry.Reason = fmt.Sprintf("not in --steps=%s preset", flags.Steps)
			annotated = append(annotated, entry)
			continue
		}
		if templatesAllow && !templatesOnlyStepAllow[step.Name] {
			entry.Status = "SKIP"
			entry.Reason = "not in --templates-only allowlist"
			annotated = append(annotated, entry)
			continue
		}
		if !step.Gate(ctx) {
			entry.Status = "SKIP"
			entry.Reason = gateSkipReason(step)
			annotated = append(annotated, entry)
			continue
		}
		entry.Status = "RUN"
		willRun++
		annotated = append(annotated, entry)
	}

	w := io.Writer(os.Stdout)
	fmt.Fprintf(w, "Pipeline plan (%d steps, %d will run):\n", len(steps), willRun)
	for _, e := range annotated {
		if e.Status == "RUN" {
			fmt.Fprintf(w, "  [RUN]  %s\n", e.Name)
			continue
		}
		fmt.Fprintf(w, "  [SKIP] %-44s (%s)\n", e.Name, e.Reason)
	}
	return nil
}

// planEntry is the per-step record the plan mode prints.
type planEntry struct {
	Name     string
	GateName string
	Status   string // "RUN" or "SKIP"
	Reason   string // populated only for SKIP entries
}

// planSetupSteps is the minimal prefix of generateSteps() the plan mode
// runs before evaluating gates. Each of these is gated by the always
// predicate AND has no codegen-side-effect — they populate ctx.Cfg /
// ctx.Checksums / ctx.HasServices / etc. so the downstream gate
// evaluations are meaningful.
//
// We do NOT run the Tier-1 drift guard or the pre-codegen contract check
// here: --plan is "show me what would happen", not "validate the tree".
// A user with mid-merge drift should still get a plan; the drift guard
// would error out, defeating the whole point.
var planSetupSteps = []string{
	"load project config",
	"load checksums",
	"detect proto directories",
}

// planStepAllow returns the --steps preset allowlist (or nil, false when
// --steps was not passed). Mirrors the lookup in runGeneratePipelineFlags
// so the plan output stays in lockstep with the real run's filtering.
func planStepAllow(flags pipelineFlags) (map[string]bool, bool) {
	if flags.Steps == "" {
		return nil, false
	}
	allow, ok := stepPresetAllowlist[flags.Steps]
	if !ok {
		// Unknown preset → no allowlist filtering applied; the real run
		// would error here, but for --plan we surface the steps as-if
		// the preset matched everything. The user fixes the preset typo
		// when they re-run without --plan.
		return nil, false
	}
	return allow, true
}

// gateSkipReason returns the user-facing reason a step was skipped.
// Prefers the step's registered GateReason (a hand-written explanation
// in the FALSE-condition voice like "no services in forge.yaml") and
// falls back to "gate <gateName> returned false" so steps that haven't
// been annotated yet still get a readable label.
//
// Used by both `--plan`'s `[SKIP]` lines and the `--verbose` "⏩ skipped"
// printer so the two surfaces stay byte-identical on the reason text.
func gateSkipReason(step GenStep) string {
	if step.GateReason != "" {
		return step.GateReason
	}
	return fmt.Sprintf("gate %s returned false", gateName(step.Gate))
}

// gateName returns a human-readable label for a step.Gate predicate. We
// reflect on the func pointer to recover the source-level name (e.g.
// "gateCodegenHasServices") rather than printing "<func>" — the gate name
// is the actionable signal in --plan output and verbose skip messages.
//
// runtime.FuncForPC returns "<pkg>.<fn>" so we trim the package prefix
// before display; anonymous closures fall back to "<anonymous>".
func gateName(g func(*pipelineContext) bool) string {
	if g == nil {
		return "<nil>"
	}
	pc := reflect.ValueOf(g).Pointer()
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "<unknown>"
	}
	name := f.Name()
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return "<anonymous>"
	}
	return name
}

// warnOrFail is how a step fails without stopping the run: the steps that
// do not depend on it still run. With --strict (ctx.Strict=true) the error
// is returned and the run stops (and rolls back) at this step. Otherwise
// the failure is printed to stderr where it happened, recorded in
// ctx.SoftFailures, and nil is returned; runGeneratePipelineFlags then
// names it under the completion line and returns a
// *factory.StepsFailedError, so the run still exits non-zero. A step that
// failed is never a success, whichever way the run continues.
//
// Usage:
//
//	return ctx.warnOrFail("frontend hooks generation", err)
//
// Returns nil when err is nil (no-op so the caller can wrap unconditionally).
//
// The decision to land this as a method on pipelineContext (rather than a
// free function taking ctx as the first arg) is for grep-friendliness —
// `grep ctx.warnOrFail` finds every adopter, which makes the per-release
// "convert another N sites to the new helper" rollouts easy to track.
func (ctx *pipelineContext) warnOrFail(stepName string, err error) error {
	if err == nil {
		return nil
	}
	var fetchErr *pgtest.FetchError
	if errors.As(err, &fetchErr) {
		// A failed postgres download means every schema-derived output (ORM,
		// factories, mocks) would be silently empty; never succeed with zero output.
		return fmt.Errorf("%s failed: %w", stepName, err)
	}
	if ctx.Strict {
		return fmt.Errorf("%s failed: %w (--strict)", stepName, err)
	}
	fmt.Fprintf(os.Stderr, "❌ %s failed: %v — the steps that do not need it still run; this run will exit non-zero (--strict stops it here)\n", stepName, err)
	// One entry per step: a step may fail several times (one stale
	// scaffold test per file), and the summary counts steps.
	if !slices.Contains(ctx.SoftFailures, stepName) {
		ctx.SoftFailures = append(ctx.SoftFailures, stepName)
	}
	return nil
}
