// File: internal/cli/lint/lint_skip.go
//
// A gating lane that could NOT run fails `forge lint` — and the one named,
// reasoned way to accept that it may not.
//
// ── Why "could not run" fails by default ──────────────────────────────────
//
// A lane has three outcomes (see laneUnavailableError in lint.go): it did not
// APPLY, it RAN, or it could NOT RUN. The third used to be a warning unless
// the caller passed --strict, so a frontend with no node_modules, a buf
// missing from PATH or a typecheck with no compiler printed a ⚠️ verdict and
// exited 0. Every CI that gated on the exit code passed over a check that
// never executed; control-plane's gate ended up grepping the verdict TEXT for
// "could NOT run" to get the failure the exit code should have carried. That
// is the silent green forge #906 ("lint never skips golangci-lint silently")
// removed for one lane, back again for the others.
//
// And --strict was the wrong switch: it also promotes ADVISORY findings to
// errors, so a gate that wanted "fail when a lane did not run" had to buy
// "fail on every advisory rule" with it. Two concerns, one flag.
//
// So: a GATING lane that could not run fails the run, naming the lane, the
// reason and the command that fixes it. --strict keeps its one meaning —
// advisory findings become errors — which includes an ADVISORY lane's own
// "could not run" report (the typed-config guardrail's): that report is an
// advisory finding like any other, and an advisory lane cannot fail the run
// over its absence when its findings could not fail it either.
//
// ── The escape hatch ──────────────────────────────────────────────────────
//
// Some runs are meant not to run a lane: a backend-only CI job with no Node
// toolchain, a machine where buf is deliberately absent. The 20% are never
// disempowered, so the acceptance is explicit, per lane, and carries a reason:
//
//	forge lint --allow-skip 'frontend-typecheck=frontends typecheck in the web job'
//
//	# forge.yaml
//	lint:
//	  allow_skip:
//	    - lane: frontend-typecheck
//	      reason: frontends typecheck in the web job
//
// An accepted lane is still listed in the verdict, as SKIPPED (allowed), with
// its reason — never silently dropped. It accepts only "could not run": a lane
// that ran and found problems still fails. And it is not the same thing as a
// lane --scope does not select (a whole-project lane under --scope): that one
// was never asked to run, is reported as "not scoped, so not run", and never
// fails.
//
// (--skip-frontends remains the switch for "this run has no frontend lane at
// all": those lanes then do not apply and are reported as skipped.)

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
)

// gapVerdict is what a lane that could not run does to the run.
type gapVerdict int

const (
	// gapFails: a gating lane (or, under --strict, an advisory one) that
	// could not run. Fails the run.
	gapFails gapVerdict = iota + 1
	// gapWarns: an advisory lane that could not run. Named in the verdict;
	// does not fail.
	gapWarns
	// gapAllowed: a lane the project or the caller accepted may not run
	// (--allow-skip / lint.allow_skip). Listed as SKIPPED (allowed).
	gapAllowed
)

// laneGap is one lane that could not run, as the verdict reports it.
type laneGap struct {
	lane string
	// reason completes "<lane> did NOT run: <reason>".
	reason string
	// fix is what to do about it — the command, where there is one.
	fix     string
	verdict gapVerdict
	// allowedBecause is the stated reason a gapAllowed lane may not run.
	allowedBecause string
}

// skipAllowances maps a pipeline lane name to the stated reason it may not
// run. A nil map accepts nothing.
type skipAllowances map[string]string

// laneFlagName is a lane's spelling on the command line and in forge.yaml:
// the verdict's name, lowercased, spaces as dashes ("frontend lint" →
// "frontend-lint"). Both spellings are accepted.
func laneFlagName(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), " ", "-")
}

// resolveSkippableLane maps either spelling of a pipeline lane to its name.
func resolveSkippableLane(raw string) (string, bool) {
	want := laneFlagName(raw)
	for _, step := range lintPipeline() {
		if laneFlagName(step.name) == want {
			return step.name, true
		}
	}
	return "", false
}

// skippableLaneList renders every lane --allow-skip accepts, for the error
// that rejects one it does not.
func skippableLaneList() string {
	var names []string
	for _, step := range lintPipeline() {
		names = append(names, laneFlagName(step.name))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// parseSkipAllowances merges forge.yaml's lint.allow_skip with the
// --allow-skip flag values (`<lane>[,<lane>…]=<reason>`); a flag beats the
// file for the same lane. Every entry must name a known lane and give a
// reason: a typo'd lane would accept nothing and leave the user believing
// they had opted out, and a reasonless acceptance is a suppression nobody can
// audit.
func parseSkipAllowances(flagValues []string, cfg *config.ProjectConfig) (skipAllowances, error) {
	out := skipAllowances{}
	add := func(source, lane, reason string) error {
		name, ok := resolveSkippableLane(lane)
		if !ok {
			return cliutil.UserErr(source, fmt.Sprintf("%q is not a forge lint lane", strings.TrimSpace(lane)), "",
				"name one of: "+skippableLaneList())
		}
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return cliutil.UserErr(source, fmt.Sprintf("accepting that %q may not run needs a reason", name), "",
				fmt.Sprintf("say why it may not run here, e.g. --allow-skip '%s=<why>' (or `reason:` in forge.yaml lint.allow_skip)",
					laneFlagName(name)))
		}
		out[name] = reason
		return nil
	}
	if cfg != nil {
		for _, a := range cfg.Lint.AllowSkip {
			if err := add("forge.yaml lint.allow_skip", a.Lane, a.Reason); err != nil {
				return nil, err
			}
		}
	}
	for _, v := range flagValues {
		lanes, reason, ok := strings.Cut(v, "=")
		if !ok {
			return nil, cliutil.UserErr("forge lint --allow-skip", fmt.Sprintf("%q has no reason", v), "",
				fmt.Sprintf("spell it <lane>[,<lane>…]=<reason>, e.g. --allow-skip '%s=<why>'", laneFlagName(lanes)))
		}
		for _, lane := range strings.Split(lanes, ",") {
			if err := add("forge lint --allow-skip", lane, reason); err != nil {
				return nil, err
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// lintAllowances is parseSkipAllowances for a driver: the flags and the
// project config it loaded.
func lintAllowances(flags lintFlags, cfg *config.ProjectConfig) (skipAllowances, error) {
	return parseSkipAllowances(flags.allowSkip, cfg)
}

// judgeGap decides what the lane that could not run does to the run. gates
// is whether the lane's verdict gates in this project (laneGates).
func (rc *lintRunCtx) judgeGap(lane string, gates bool) (gapVerdict, string) {
	if why, ok := rc.allowSkip[lane]; ok {
		return gapAllowed, why
	}
	if gates || rc.strict {
		return gapFails, ""
	}
	return gapWarns, ""
}

// laneGates reports whether a step's verdict gates in this run: the step
// gates, and the project has not dialled it to advisory. A lane whose
// findings cannot fail the run cannot fail it by being absent either.
func laneGates(step linterStep, rc *lintRunCtx) bool {
	if !step.gates {
		return false
	}
	return step.gatesIn == nil || step.gatesIn(rc)
}

// isGapRule reports whether a finding is a lane's "could not run" headline:
// the `-unavailable` rule family lint_json.go documents.
func isGapRule(rule string) bool {
	return strings.HasSuffix(rule, "-unavailable")
}

// applyGapPolicy grades a structured outcome whose lane could not run: its
// `-unavailable` headline findings become errors (and the lane gates), stay
// warnings, or become info notes of an accepted skip. Collectors report the
// condition; this decides what it means, so the text and structured drivers
// cannot disagree. Run after --scope filtering: a frontend outside the scope
// that could not be checked is not this slice's gap.
//
// Info-severity findings under a gap rule are the evidence (a tool's own
// output explaining why it could not start) and are left alone.
func applyGapPolicy(o *lintStepOutcome, gates bool, rc *lintRunCtx) {
	var heads []int
	for i, f := range o.findings {
		if isGapRule(f.Rule) && f.Severity != lintSevInfo {
			heads = append(heads, i)
		}
	}
	if len(heads) == 0 {
		return
	}
	verdict, why := rc.judgeGap(o.name, gates)
	g := &laneGap{lane: o.name, verdict: verdict, allowedBecause: why}
	var reasons, fixes []string
	for _, i := range heads {
		f := &o.findings[i]
		reasons = append(reasons, f.Message)
		if f.FixHint != "" && !contains(fixes, f.FixHint) {
			fixes = append(fixes, f.FixHint)
		}
		switch verdict {
		case gapFails:
			f.Severity = lintSevError
		case gapWarns:
			f.Severity = lintSevWarning
		case gapAllowed:
			f.Severity = lintSevInfo
			f.Message += " — SKIPPED (allowed: " + why + ")"
		}
	}
	g.reason = strings.Join(reasons, "; ")
	g.fix = strings.Join(fixes, "; ")
	o.gap = g
	if verdict == gapFails {
		o.gated = true
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// acceptSkipHint names the opt-out for the lanes that could not run.
func acceptSkipHint(gaps []laneGap) string {
	names := make([]string, 0, len(gaps))
	for _, g := range gaps {
		names = append(names, laneFlagName(g.lane))
	}
	return fmt.Sprintf("if it is meant not to run here, accept that by name with a reason: --allow-skip '%s=<why>' "+
		"(or lint.allow_skip in forge.yaml)", strings.Join(names, ","))
}

// describeGaps renders lanes with their reasons for the verdict line.
func describeGaps(gaps []laneGap) string {
	parts := make([]string, 0, len(gaps))
	for _, g := range gaps {
		parts = append(parts, fmt.Sprintf("%s (%s)", g.lane, g.reason))
	}
	return strings.Join(parts, "; ")
}

// describeAllowed renders accepted skips for the verdict line.
func describeAllowed(gaps []laneGap) string {
	parts := make([]string, 0, len(gaps))
	for _, g := range gaps {
		parts = append(parts, fmt.Sprintf("%s — %s", g.lane, g.allowedBecause))
	}
	return strings.Join(parts, "; ")
}

// npmInstallFix is the remediation for a frontend whose dependencies are
// not installed: the exact npm command, at the directory npm installs from
// (a workspace root adopts its members), with the verb a reproducible
// install takes there (`npm ci` with a lockfile, `npm install` without).
func npmInstallFix(feDir string) string {
	root := frontenddeps.NpmInstallRoot(feDir)
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, root); err == nil && !strings.HasPrefix(rel, "..") {
			root = rel
		}
	}
	return fmt.Sprintf("run `npm %s` in %s, then re-run `forge lint`", frontenddeps.NpmInstallVerb(feDir), filepath.ToSlash(root))
}
