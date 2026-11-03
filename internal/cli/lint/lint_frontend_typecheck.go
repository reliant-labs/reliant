// File: internal/cli/lint/lint_frontend_typecheck.go
//
// The frontend TypeScript typecheck lane of `forge lint` (pipeline step
// 5b, registered in lint_steps.go).
//
// WHY IT IS ITS OWN STEP. forge generates the frontends, so forge owns
// knowing where they live, that they are TypeScript, and how to check
// them — a caller should never have to hand-roll
//
//	for d in frontends/*/; do (cd "$d" && npx tsc --noEmit) || exit 1; done
//
// to get a correctness signal. Typechecking used to ride along inside the
// eslint lane (lintFrontendDir), which made it invisible in the step
// table, impossible to configure or skip on its own, and — the real
// defect — silently absent whenever the frontend had no `typecheck` npm
// script. Splitting it out gives the lane a name, a severity dial
// (lint.frontend.typecheck), a skip flag (--skip-frontends), and a
// resolver that finds the compiler itself.
//
// WHAT IT RUNS, in order of preference per frontend:
//
//  1. `npm run typecheck` when package.json declares that script — the
//     project's own spelling wins (it may be vue-tsc, a monorepo-aware
//     wrapper, tsc with extra flags).
//  2. the resolved local TypeScript compiler with `--noEmit` when the
//     frontend has a tsconfig.json but no script — this is the case the
//     old inline lane merely nagged about ("add `typecheck`: `tsc
//     --noEmit`") and never checked.
//
// It resolves tsc through node_modules/.bin — the frontend's own, then
// the workspace roots above it (pnpm/npm workspaces hoist) — and NEVER
// through `npx`. `npx tsc` silently downloads TypeScript from the
// registry when it isn't installed, which turns a lint into a network
// fetch, is non-deterministic across machines, and fails opaquely in a
// sandbox. If no compiler is resolvable, that is reported as such.
//
// HONEST DEGRADATION. A typecheck that could not run is never reported
// as a typecheck that passed. Missing node_modules / unresolvable
// compiler produce a finding with rule
// `forge-frontend-typecheck-unavailable` and the command that fixes it,
// not silence and not a bogus type error. The lane gates, so a typecheck
// that could not run FAILS the run (lint_skip.go): a CI job that splits
// the Go and Node work says so with --skip-frontends (no frontend lane in
// this run) or `--allow-skip frontend-typecheck=<why>` / forge.yaml
// lint.allow_skip (this lane may not run here), and either is named in
// the verdict. A project that dialled lint.frontend.typecheck to `warn`
// made the lane advisory, so its could-not-run is a warning there.

package lint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
)

// Rule ids contributed by this lane. New rules on the existing finding
// shape — the additive-extension contract documented in lint_json.go.
const (
	ruleFrontendTypecheck            = "forge-frontend-typecheck"
	ruleFrontendTypecheckUnavailable = "forge-frontend-typecheck-unavailable"
)

// frontendTypecheckConcurrency caps how many frontends are typechecked at
// once. `forge lint` runs inside agent loops where wall-clock matters, so
// a multi-frontend project must not serialize; but tsc is memory-hungry
// (a Next.js app's program routinely peaks past 1 GB), so unbounded
// fan-out on a 20-frontend monorepo would thrash. Bounded at min(NumCPU,
// 4): enough to hide the latency of the common 1-3 frontend project
// entirely, low enough that the cap is a real cap.
const frontendTypecheckConcurrency = 4

// frontendTarget is one frontend the typecheck lane walks: its declared
// name and its directory relative to the project root.
type frontendTarget struct {
	name string
	dir  string
}

// typecheckStatus classifies a single frontend's outcome. The three
// non-passing kinds are deliberately distinct: "did not apply" and "could
// not run" and "found type errors" are three different answers, and
// collapsing them is how a typecheck ends up silently absent.
type typecheckStatus int

const (
	// typecheckNotApplicable — this frontend is not a TypeScript project
	// (no tsconfig.json and no typecheck script). Silent; contributes
	// nothing.
	typecheckNotApplicable typecheckStatus = iota
	// typecheckPassed — the compiler ran and reported nothing.
	typecheckPassed
	// typecheckUnavailable — the check could NOT run (directory missing,
	// deps not installed, no compiler resolvable). Always surfaced.
	typecheckUnavailable
	// typecheckFailed — the compiler ran and reported type errors.
	typecheckFailed
)

// typecheckResult is one frontend's outcome. output holds the captured
// compiler output for the failed case (empty otherwise); reason holds the
// human explanation for the unavailable case.
type typecheckResult struct {
	target frontendTarget
	status typecheckStatus
	// reason explains an unavailable outcome ("node_modules not
	// installed"), phrased to complete "<name>: <reason>".
	reason string
	// fixHint is the remediation for an unavailable outcome.
	fixHint string
	// output is the compiler's captured stdout+stderr for a failed
	// outcome.
	output string
	// command is the resolved command line, for the failure message.
	command string
}

// frontendTypecheckTargets resolves the frontends this lane walks.
//
// The set comes from the project (config.ProjectConfig.ToolchainFrontends
// — the SAME resolution `forge build` filters its build set from), never
// from globbing a hardcoded path: forge.yaml is where a frontend's real
// location lives, custom `path:` entries and all, and
// `stack.frontend.framework: none` is where a project says "forge does
// not drive a Node toolchain here".
//
// The `frontends/` directory scan is the no-config fallback only: linting
// a tree with no forge.yaml (or one that predates a `frontends:` block)
// should still check what is obviously there. A project with neither a
// declared frontend nor a frontends/ directory resolves to nil — a clean
// no-op, not an error.
func frontendTypecheckTargets(cfg *config.ProjectConfig) []frontendTarget {
	if cfg != nil && len(cfg.Frontends) > 0 {
		declared := cfg.ToolchainFrontends()
		out := make([]frontendTarget, 0, len(declared))
		for _, fe := range declared {
			dir, ok := fe.Dir(".")
			if !ok {
				continue
			}
			out = append(out, frontendTarget{name: fe.Name, dir: dir})
		}
		return out
	}
	entries, err := os.ReadDir("frontends")
	if err != nil {
		return nil
	}
	var out []frontendTarget
	for _, e := range entries {
		if e.IsDir() {
			// Slash-separated, the form config.FrontendConfig.Dir returns for a
			// declared frontend, so both sources name a directory the same way.
			out = append(out, frontendTarget{name: e.Name(), dir: path.Join("frontends", e.Name())})
		}
	}
	return out
}

// resolveLocalTSC returns the path to the TypeScript compiler installed
// for feDir, searching node_modules/.bin/tsc in feDir and then in each
// ancestor up to (and including) the project root — the layout npm and
// pnpm workspaces produce when they hoist a shared typescript devDep.
// Returns "" when no compiler is installed.
//
// Deliberately never falls back to `npx tsc` or a PATH lookup: npx
// downloads on miss (see the file header) and a globally-installed tsc
// would typecheck against a different compiler version than the project
// pins, which is worse than not checking — it produces errors the
// project's own toolchain does not have.
func resolveLocalTSC(feDir, projectRoot string) string {
	bin := "tsc"
	if runtime.GOOS == "windows" {
		bin = "tsc.cmd"
	}
	dir, err := filepath.Abs(feDir)
	if err != nil {
		return ""
	}
	root := projectRoot
	if abs, absErr := filepath.Abs(projectRoot); absErr == nil {
		root = abs
	}
	for {
		candidate := filepath.Join(dir, "node_modules", ".bin", bin)
		if st, statErr := os.Stat(candidate); statErr == nil && !st.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		// Stop at the filesystem root, and never walk ABOVE the project
		// root — a tsc in some unrelated ancestor of the checkout is not
		// this project's compiler.
		if parent == dir || dir == root {
			return ""
		}
		dir = parent
	}
}

// anyAncestorHasNodeModules reports whether feDir or any ancestor up to and
// including projectRoot has a node_modules directory — the same walk (and the
// same stop condition) resolveLocalTSC uses, so the "are deps installed?"
// probe and the "where is the compiler?" probe cannot disagree about what a
// workspace layout means.
func anyAncestorHasNodeModules(feDir, projectRoot string) bool {
	dir, err := filepath.Abs(feDir)
	if err != nil {
		return false
	}
	root := projectRoot
	if abs, absErr := filepath.Abs(projectRoot); absErr == nil {
		root = abs
	}
	for {
		if dirExists(filepath.Join(dir, "node_modules")) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir || dir == root {
			return false
		}
		dir = parent
	}
}

// typecheckFrontend runs the typecheck for one frontend and classifies
// the outcome. It never returns an error: every failure mode is a
// classified result, because "the check errored" and "the check found
// errors" must stay distinguishable all the way out to the JSON report.
func typecheckFrontend(ctx context.Context, t frontendTarget, projectRoot string) typecheckResult {
	res := typecheckResult{target: t}

	if !dirExists(t.dir) {
		res.status = typecheckUnavailable
		res.reason = fmt.Sprintf("directory %s not found", t.dir)
		res.fixHint = fmt.Sprintf("fix the `path:` for frontend %q in forge.yaml, or remove the entry", t.name)
		return res
	}

	// package.json is the marker of a Node project at all. Its absence is
	// not a finding — a `frontends/` subdirectory can legitimately be
	// something else (a static export, shared assets).
	scripts, err := readPackageScripts(filepath.Join(t.dir, "package.json"))
	if err != nil {
		return res // typecheckNotApplicable
	}

	hasScript := hasPackageScript(scripts, "typecheck")
	hasTSConfig := fileExists(filepath.Join(t.dir, "tsconfig.json"))
	if !hasScript && !hasTSConfig {
		return res // not a TypeScript project — silent no-op
	}

	// "Are the deps installed?" is an ANCESTOR-WALK question, not a
	// frontend-local one — the same walk resolveLocalTSC already does, and for
	// the same reason. An npm/pnpm WORKSPACE hoists every member's
	// dependencies to the workspace root, leaving <feDir>/node_modules absent
	// even though the install completed perfectly; forge's own dev bridge
	// (internal/generator/frontend_webruntime_devlink.go) makes exactly that
	// layout. Requiring the frontend-local directory reported "node_modules
	// not installed" for a fully installed tree and downgraded the whole run
	// to "1 gating linter could NOT run" — a lane that silently stopped
	// checking, which is the outcome this file's own comments call worse than
	// a failure.
	if !anyAncestorHasNodeModules(t.dir, projectRoot) {
		if err := frontenddeps.Ensure(ctx, "[lint]", t.name, t.dir, "", true); err != nil {
			// The compiler never ran, so this is a typecheck that could NOT
			// run — which fails a gating lane (lint_skip.go) and, unlike a
			// type error, is what --allow-skip can accept.
			res.status = typecheckUnavailable
			res.reason = fmt.Sprintf("dependencies are not installed and installing them failed — typecheck did NOT run: %v", err)
			res.fixHint = npmInstallFix(t.dir)
			return res
		}
	}
	if !anyAncestorHasNodeModules(t.dir, projectRoot) {
		res.status = typecheckUnavailable
		res.reason = "node_modules not installed — typecheck did NOT run"
		res.fixHint = npmInstallFix(t.dir)
		return res
	}

	var cmd *exec.Cmd
	switch {
	case hasScript:
		// The project's own spelling of the check wins.
		if _, lookErr := exec.LookPath("npm"); lookErr != nil {
			res.status = typecheckUnavailable
			res.reason = "npm not found on PATH — typecheck did NOT run"
			res.fixHint = "install Node.js (which ships npm) so forge can run the frontend `typecheck` script"
			return res
		}
		res.command = "npm run typecheck"
		cmd = exec.CommandContext(ctx, "npm", "run", "typecheck")
	default:
		// tsconfig.json but no script: forge runs the compiler itself
		// rather than nagging the user to add a script for it.
		tsc := resolveLocalTSC(t.dir, projectRoot)
		if tsc == "" {
			res.status = typecheckUnavailable
			res.reason = "tsconfig.json present but no TypeScript compiler installed — typecheck did NOT run"
			res.fixHint = fmt.Sprintf("add typescript to devDependencies and `npm install` in %s", t.dir)
			return res
		}
		// --pretty false pins the plain `file(line,col): error TSxxxx:`
		// diagnostic format regardless of terminal detection, so the
		// findings parser sees one stable shape (and no ANSI escapes leak
		// into the JSON report).
		res.command = "tsc --noEmit"
		cmd = exec.CommandContext(ctx, tsc, "--noEmit", "--pretty", "false")
	}

	cmd.Dir = t.dir
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	if runErr == nil {
		res.status = typecheckPassed
		return res
	}

	// Exit 127 (and its shell spelling) means the script's own tool was
	// not found — a broken toolchain, NOT a type error. Reporting it as a
	// type error is exactly the confusing failure this lane must not
	// produce.
	out := buf.String()
	if isCommandNotFound(runErr, out) {
		res.status = typecheckUnavailable
		res.reason = "the `typecheck` script's tool is not installed — typecheck did NOT run"
		res.fixHint = npmInstallFix(t.dir) + " (the `typecheck` script's compiler comes from node_modules)"
		return res
	}

	res.status = typecheckFailed
	res.output = out
	return res
}

// isCommandNotFound reports whether a failed run is "the tool isn't
// installed" rather than "the tool found problems". Shells exit 127 for
// an unresolvable command; npm forwards that code, and the message text
// is checked too because some npm versions remap the exit code.
func isCommandNotFound(runErr error, output string) bool {
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 127 {
		return true
	}
	lower := strings.ToLower(output)
	return strings.Contains(lower, "command not found") || strings.Contains(lower, "not recognized as an internal")
}

// reTSCDiagnostic matches the TypeScript compiler's diagnostic line
// shape, `src/app/page.tsx(12,5): error TS2322: ...`. tsc's own spelling
// uses parentheses, so the shared file:line:col normalizer
// (externalLinesToFindings) cannot parse it and would drop every
// location — turning a precisely-located type error into an unanchored
// string. forge generates these frontends; parsing their compiler's
// native format is part of owning the check.
var reTSCDiagnostic = regexp.MustCompile(`^(\S[^(]*)\((\d+),(\d+)\):\s*(.+)$`)

// tscOutputToFindings normalizes captured compiler output into findings.
//
// Located diagnostics become file-scoped findings at `severity`, anchored
// at the project-relative source path so a consumer can open them from
// the project root. Every OTHER line — npm's `> dashboard@0.1.0
// typecheck` banner, tsc's indented elaborations, summary lines — is
// preserved verbatim (sub-tool output is never silently dropped) but at
// INFO severity: it is transcript, not a defect. Emitting it at error
// severity would make `summary.errors` count npm's own echo, so a single
// type error reads as four, and the count stops being usable as a defect
// count.
//
// The gate does not depend on this classification: the lane's verdict
// finding (emitted by the caller) carries the gating severity, so even a
// compiler whose format changes entirely still fails the run.
func tscOutputToFindings(output, feDir, severity string) []lintJSONFinding {
	var out []lintJSONFinding
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := reTSCDiagnostic.FindStringSubmatch(line); m != nil {
			out = append(out, lintJSONFinding{
				File:     filepath.Join(feDir, m[1]),
				Line:     parseDigitsLint(m[2]),
				Col:      parseDigitsLint(m[3]),
				Severity: severity,
				Rule:     ruleFrontendTypecheck,
				Message:  m[4],
			})
			continue
		}
		out = append(out, lintJSONFinding{
			Severity: lintSevInfo,
			Rule:     ruleFrontendTypecheck,
			Message:  line,
		})
	}
	return out
}

// runFrontendTypechecks typechecks every target, bounded-concurrently,
// and returns the results in TARGET ORDER. Determinism matters: the
// results drive both the text output and the JSON findings, and a report
// whose line order changes run to run is unusable as a diffable artifact
// (and untestable).
func runFrontendTypechecks(ctx context.Context, targets []frontendTarget, projectRoot string) []typecheckResult {
	results := make([]typecheckResult, len(targets))
	limit := frontendTypecheckConcurrency
	if n := runtime.NumCPU(); n < limit {
		limit = n
	}
	if limit < 1 {
		limit = 1
	}

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t frontendTarget) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = typecheckFrontend(ctx, t, projectRoot)
		}(i, t)
	}
	wg.Wait()
	return results
}

// typecheckGates reports whether a type-error result should fail the run,
// per the lint.frontend.typecheck severity dial. "off" never reaches here
// (the step's shouldRun skips the lane entirely).
func typecheckGates(cfg *config.ProjectConfig) bool {
	if cfg == nil {
		return true
	}
	return cfg.Lint.Frontend.EffectiveTypecheck() == "error"
}

// collectFrontendTypecheckJSON is the --json collector for step 5b. It
// mirrors runFrontendTypecheckText's verdict exactly; compiler output is
// normalized through externalLinesToFindings so `file(line,col): message`
// lines become file-scoped findings.
func collectFrontendTypecheckJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	targets := frontendTypecheckTargets(rc.cfg)
	results := runFrontendTypechecks(rc.ctx, targets, rc.cwd)

	gates := typecheckGates(rc.cfg)

	var out []lintJSONFinding
	gated := false
	for _, r := range results {
		switch r.status {
		case typecheckNotApplicable:
			continue
		case typecheckPassed:
			out = append(out, ranNote(fmt.Sprintf("%s: typecheck passed (%s)", r.target.name, r.command)))
		case typecheckUnavailable:
			// Graded by applyGapPolicy (lint_skip.go).
			out = append(out, lintJSONFinding{
				File:     r.target.dir,
				Severity: lintSevWarning,
				Rule:     ruleFrontendTypecheckUnavailable,
				Message:  fmt.Sprintf("%s: %s", r.target.name, r.reason),
				FixHint:  r.fixHint,
			})
		case typecheckFailed:
			sev := lintSevError
			if !gates {
				sev = lintSevWarning
			}
			out = append(out, lintJSONFinding{
				File:     r.target.dir,
				Severity: sev,
				Rule:     ruleFrontendTypecheck,
				Message:  fmt.Sprintf("%s: %s reported type errors", r.target.name, r.command),
			})
			out = append(out, tscOutputToFindings(r.output, r.target.dir, sev)...)
			if gates {
				gated = true
			}
		}
	}
	return out, gated, nil
}
