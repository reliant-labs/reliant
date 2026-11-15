// Package cli — `forge doctor` external-builds checks (Phase 3 of the
// build_cmd refactor).
//
// For every KCL service whose Service.BuildCmd is non-empty doctor
// emits three observations:
//
//   - warn when the resolved build_cwd (or projectDir when BuildCwd is
//     unset) is missing on disk. Build-side semantics already
//     skip-with-warn at run time; doctor surfaces the gap up-front so
//     the user can fix it before invoking `forge build`.
//   - warn when the first token of BuildCmd (split on whitespace, take
//     index 0) isn't on PATH. Heuristic — skipped when the command
//     opens with `cd ` or contains a `KEY=value` env-var assignment,
//     because those need a real parse pass that's out of scope for v1.
//   - info recording the substituted BuildCmd against a synthetic
//     Spec, so the user can spot ${X} substitution errors before
//     running build.
//
// All three observations live under one doctor check per service
// (Name="external-build: <service>") so the report stays compact even
// for projects with many external-build services. The check status
// rolls up: any warn beats pass; the heuristic-skipped paths still
// emit the info line so the substituted command is always visible.
//
// The decision core (buildExternalBuildDoctorChecks) is pure — it
// takes the resolved KCL service set + a PATH lookup function + a
// stat function. Production passes exec.LookPath / os.Stat; unit tests
// pass map-backed stubs. Mirrors doctor_tools.go's seam.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

// statFunc reports whether path exists on disk. Production uses
// os.Stat; tests inject a map-backed stub. Returns the same error
// semantics — os.IsNotExist on the returned error is the signal we
// care about.
type statFunc func(path string) (os.FileInfo, error)

// runExternalBuildDoctorChecks is the side-effecting wrapper invoked
// from runDoctor. Honors the doctor signal filter the same way
// runIngressDoctorChecks does: only run when signal is empty
// ("all checks") or equals "external-builds".
//
// Returns nil when no service declares build_cmd or features.build is
// off — both are "category not in play" states and doctor output
// stays tidy by omitting the checks entirely (matches the ingress
// pattern).
func runExternalBuildDoctorChecks(ctx context.Context, cfg *config.ProjectConfig, projectDir, signal string) []doctor.CheckResult {
	if cfg == nil {
		return nil
	}
	if !cfg.Features.BuildEnabled() {
		return nil
	}
	// Only the unfiltered pass. `--signal` selects among the values
	// doctor.RunFiltered accepts (metrics/traces/logs/profiles/deploy); it
	// REJECTS anything else before a check runs, so a guard naming its own
	// signal here could never be true and advertised a filter that does not
	// exist.
	if signal != "" {
		return nil
	}
	// We only render the dev env — same as auditExternalBuilds /
	// auditIngress. The check fires on the declared build_cmd shape,
	// not on per-env state.
	entities, err := RenderKCL(ctx, projectDir, "dev")
	if err != nil {
		// Environmental: kcl not on PATH, no deploy/kcl/dev/. Same
		// disposition as ingress — emit one skipped check explaining
		// why rather than failing or staying silent.
		return []doctor.CheckResult{{
			Name:     "external-builds",
			Status:   doctor.StatusSkip,
			Message:  "could not evaluate dev KCL",
			Evidence: err.Error(),
		}}
	}
	svcs := externalBuildServices(entities)
	if len(svcs) == 0 {
		return nil
	}
	start := time.Now()
	results := buildExternalBuildDoctorChecks(svcs, projectDir, exec.LookPath, os.Stat)
	per := time.Since(start) / time.Duration(len(results))
	for i := range results {
		results[i].Duration = per
	}
	return results
}

// buildExternalBuildDoctorChecks is the pure decision core. Given a
// resolved service set plus the two seam functions, return one
// doctor.CheckResult per service. No I/O of its own — everything goes
// through the seams.
//
// Status mapping per service:
//
//   - cwd missing OR first token not on PATH → StatusWarn
//   - otherwise                              → StatusPass
//
// Evidence is always populated with severity-prefixed lines and a
// resolved-command preview, so a user troubleshooting "why does my
// build_cmd fail" sees both the substituted shell command AND why
// any prerequisite is missing in one place.
func buildExternalBuildDoctorChecks(services []WorkloadEntity, projectDir string, lookup binaryLookupFunc, stat statFunc) []doctor.CheckResult {
	results := make([]doctor.CheckResult, 0, len(services))
	for _, svc := range services {
		results = append(results, evaluateExternalBuildCheck(svc, projectDir, lookup, stat))
	}
	return results
}

// evaluateExternalBuildCheck is the per-service decision. Split out
// so buildExternalBuildDoctorChecks reads as "for every service,
// evaluate" and this function holds the heuristic branches.
func evaluateExternalBuildCheck(svc WorkloadEntity, projectDir string, lookup binaryLookupFunc, stat statFunc) doctor.CheckResult {
	name := "external-build: " + svc.Name
	result := doctor.CheckResult{Name: name, Status: doctor.StatusPass}

	var evidence []string
	hasWarn := false

	buildCmd := svc.EffectiveBuildCmd()
	buildCwd := svc.EffectiveBuildCwd()

	// CWD existence. Empty cwd → check projectDir itself so the
	// user gets a clear hit when forge is invoked from a wonky cwd.
	resolved := buildCwd
	switch {
	case resolved == "":
		resolved = projectDir
	case !filepath.IsAbs(resolved):
		resolved = filepath.Join(projectDir, resolved)
	}
	if _, err := stat(resolved); err != nil {
		if os.IsNotExist(err) {
			hasWarn = true
			evidence = append(evidence, fmt.Sprintf("warn: build_cwd %s does not exist on disk — build will skip-with-warn", resolved))
		} else {
			// stat error other than NotExist (perm denied, broken symlink
			// target). Still warn — the build_cmd won't be able to cd into
			// it either.
			hasWarn = true
			evidence = append(evidence, fmt.Sprintf("warn: stat build_cwd %s: %v", resolved, err))
		}
	} else {
		evidence = append(evidence, fmt.Sprintf("ok: build_cwd %s present", resolved))
	}

	// First-token PATH check. Heuristic — skipped when the command
	// opens with `cd ` (we'd need to skip past the path) or contains
	// a `KEY=value` env-var assignment in the first token (Make-style
	// `CGO_ENABLED=0 go build ...`). Both need a real parse pass and
	// the brief explicitly defers that to a future revision.
	cmdToken, skipReason := firstCommandToken(buildCmd)
	switch {
	case skipReason != "":
		evidence = append(evidence, fmt.Sprintf("info: first-token PATH check skipped (%s)", skipReason))
	case cmdToken == "":
		evidence = append(evidence, "info: build cmd has no resolvable first token")
	default:
		if _, err := lookup(cmdToken); err != nil {
			hasWarn = true
			evidence = append(evidence, fmt.Sprintf("warn: %s not found on PATH (first token of the build cmd)", cmdToken))
		} else {
			evidence = append(evidence, fmt.Sprintf("ok: %s on PATH", cmdToken))
		}
	}

	// The command forge will exec, verbatim. There is nothing to preview
	// a substitution of: the KCL render already resolved every value, and
	// forge hands this exact string to `sh -c`. Any `$VAR` left in it is
	// the shell's own, resolved from the process env plus the declared
	// `env` map.
	evidence = append(evidence, "info: build cmd (run verbatim): "+buildCmd)

	if hasWarn {
		result.Status = doctor.StatusWarn
		result.Message = fmt.Sprintf("%s: build_cmd prerequisites incomplete", svc.Name)
	} else {
		result.Message = fmt.Sprintf("%s: build_cmd ready", svc.Name)
	}
	result.Evidence = strings.Join(evidence, "\n")
	return result
}

// firstCommandToken extracts the first whitespace-separated token of
// the build command for the PATH heuristic. Returns ("", reason) when
// the heuristic punts — opening `cd ` and `KEY=value` first-tokens
// both need a real shell parse to do correctly, and false-positives
// on those forms would be noisier than skipping.
//
// Pure helper so it's trivially unit-testable from a string literal.
func firstCommandToken(cmd string) (token string, skipReason string) {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return "", ""
	}
	// Multi-line build_cmds (the canonical shape — `cd … && go build
	// && docker build …`) start with the cd, so we punt rather than
	// asserting `cd` is on PATH (which it always is, but the check
	// would always pass and the user wouldn't learn anything).
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "cd ") || strings.HasPrefix(lower, "cd\t") {
		return "", "build_cmd starts with `cd` — first-token heuristic doesn't apply"
	}
	// Split on first whitespace boundary.
	idx := strings.IndexAny(trimmed, " \t\n\r")
	first := trimmed
	if idx >= 0 {
		first = trimmed[:idx]
	}
	// Env-var assignment heuristic: `KEY=value` as the first token is
	// shell-syntax for "set KEY for the next command". Skip rather
	// than warn that "KEY=value" isn't on PATH.
	if i := strings.Index(first, "="); i > 0 && isShellEnvKey(first[:i]) {
		return "", "build_cmd starts with env-var assignment — first-token heuristic doesn't apply"
	}
	// Shell builtins are not executables and are never on PATH, so looking
	// one up always "fails". forge runs build_cmd THROUGH A SHELL, which is
	// what makes `set -e` (the canonical opener for a multi-line build_cmd
	// that must abort on the first failing step) both correct and idiomatic
	// — so warning about it told a project its build was broken when it was
	// exemplary. Observed on two real targets, workspace-base and
	// daemon-gateway, whose only sin was failing safe.
	if isShellBuiltin(first) {
		return "", "build_cmd starts with the shell builtin `" + first + "` — first-token heuristic doesn't apply"
	}
	return first, ""
}

// shellBuiltins are the builtins plausible as the FIRST token of a
// build_cmd. Deliberately not the full builtin list: this decides only
// whether to skip a PATH lookup, and a word that is both a builtin and a
// real binary (`echo`, `test`, `[`) is fine to look up either way.
//
// `set` is the one that matters in practice — `set -e` / `set -eu` /
// `set -euo pipefail` opens most multi-step build_cmds.
var shellBuiltins = map[string]bool{
	"set":    true,
	"export": true,
	"unset":  true,
	"shift":  true,
	"exec":   true,
	"eval":   true,
	"source": true,
	".":      true,
	"umask":  true,
	"trap":   true,
	"local":  true,
	"read":   true,
}

// isShellBuiltin reports whether tok is a shell builtin that would never
// resolve on PATH.
func isShellBuiltin(tok string) bool { return shellBuiltins[tok] }

// isShellEnvKey returns true when s looks like a shell env-var key
// (uppercase letters, digits, underscore, must start with a letter or
// underscore). We're not trying to be POSIX-perfect — just to detect
// the canonical `CGO_ENABLED=0 go build` form.
func isShellEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r == '_':
			// ok
		case (r >= '0' && r <= '9') && i > 0:
			// digits after position 0
		default:
			return false
		}
	}
	return true
}

// appendExternalBuildChecksToReport mutates report to add the
// external-build checks and re-rolls the Overall status. Mirrors
// appendIngressChecksToReport exactly so the two integration sites
// can be folded together in a future refactor.
func appendExternalBuildChecksToReport(report *doctor.Report, extras []doctor.CheckResult) {
	// Same semantics as appendIngressChecksToReport — reuse it so
	// the rollup rules stay consistent.
	appendIngressChecksToReport(report, extras)
}
