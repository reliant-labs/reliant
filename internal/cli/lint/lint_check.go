package lint

// Report-only lint: the default, and the fixers' dry run.
//
// `forge lint` is a GATE. It runs in `task ci`, in every agent's loop between
// phases, and in checkouts other agents are editing at the same moment. A
// gate that rewrites files is a gate whose verdict is about a tree nobody
// wrote — and it auto-fixed by default (#843) until P5b, when several agents
// independently found it had:
//
//   - rewritten a mixed-type const block in reliant's
//     internal/workflow/yaml/triggers.go to satisfy staticcheck SA9004, giving
//     untyped field-name constants the protoreflect.FullName type, which no
//     longer compiled (golangci-lint --fix applies every linter's suggested
//     rewrite, semantic ones included);
//   - rewritten files another PR owned (internal/integrations/manifest/
//     assemble.go, auth.go) because a bare `forge lint` formats the project;
//   - edited go.mod.
//
// Every agent then had to remember --no-fix or revert by hand. So the default
// is the one a gate should have: report, never rewrite. Every file a fixer
// WOULD rewrite is printed as the diff the rewrite would apply, and the run
// fails, so the author sees exactly what to change. Writing is one flag away
// and always deliberate: `forge lint --fix`.
//
// WHEN. resolveFixMode, strongest first:
//
//	--check / --no-fix   report-only (the default; kept as explicit spellings)
//	--fix                fix, anywhere (including under CI)
//	otherwise            report-only
//
// WHAT --fix APPLIES. Only mechanical rewrites: the Go formatter (gofmt, plus
// goimports' grouping when the project's golangci-lint config enables it —
// FormatOnly, so it never adds or drops an import), prettier over a
// frontend's owned sources, eslint --fix, and the tracing lint's
// log-without-ctx rewrite. golangci-lint is NEVER run with --fix: its fixes
// come from staticcheck, gocritic and the rest, and a rewrite that changes a
// type (SA9004) or a call is a code change for the author to make, so those
// stay findings. And every Go rewrite is verified (lint_fix_guard.go): the
// packages it touched are type-checked afterwards, and a package that no
// longer type-checks gets its files back as they were, with the fix reported
// as not applied.
//
// WHAT IS REPORTED. The pre-pass fixers that rewrite source share their walk
// with the fix pass (walkGoFormat, ownedFrontendSources), so the two cannot
// disagree about which files are pending. The fixers that live inside a
// linter (eslint --fix) are simply not asked to fix: the linter reports the
// same issues as findings and fails the lane.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/reliant-labs/forge/internal/config"
)

// formatLaneName is the lane report-only mode adds for pending formatting.
const formatLaneName = "format"

// resolveFixMode decides whether this run rewrites files: only when --fix
// asks, and never when --check / --no-fix also does. The environment (CI)
// no longer matters — report-only is the default everywhere.
func resolveFixMode(f lintFlags) bool {
	return f.fix && !f.noFix && !f.check
}

// autoFix reports whether this run rewrites files.
func (f lintFlags) autoFix() bool {
	return resolveFixMode(f)
}

// formatPending is one file a fixer would rewrite, and the rewrite.
type formatPending struct {
	File string // project-relative, slash-separated
	Tool string // what would rewrite it
	Diff string // unified diff of the rewrite
}

// unifiedDiff renders the change from a to b for the file rel.
func unifiedDiff(rel string, a, b []byte) string {
	d, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(a)),
		B:        difflib.SplitLines(string(b)),
		FromFile: "a/" + rel,
		ToFile:   "b/" + rel,
		Context:  3,
	})
	if err != nil {
		return fmt.Sprintf("(diff unavailable: %v)\n", err)
	}
	return d
}

// collectFormatPending lists every file the fix pre-pass would rewrite under
// the same bounds, without writing any. keep bounds the Go files (nil is all
// of them); withFrontends adds prettier's owned frontend sources. Best
// effort, like the fixer: a tool that cannot run contributes an error and
// whatever was found before it.
func collectFormatPending(ctx context.Context, root string, keep func(rel string) bool, withFrontends bool, cfg *config.ProjectConfig) ([]formatPending, error) {
	var out []formatPending
	err := walkGoFormat(root, keep, func(rel, _ string, src, formatted []byte) error {
		out = append(out, formatPending{File: rel, Tool: "gofmt/goimports", Diff: unifiedDiff(rel, src, formatted)})
		return nil
	})
	if withFrontends {
		fe, ferr := collectFrontendFormatPending(ctx, root, cfg)
		out = append(out, fe...)
		if err == nil {
			err = ferr
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, err
}

// collectFrontendFormatPending is formatFrontendTrees without the write:
// prettier names the files it would change, and formats each one on stdin to
// produce the diff.
func collectFrontendFormatPending(ctx context.Context, root string, cfg *config.ProjectConfig) ([]formatPending, error) {
	var out []formatPending
	for _, fe := range frontendTypecheckTargets(cfg) {
		feDir := filepath.Join(root, fe.dir)
		bin := resolveLocalBin("prettier", feDir, root)
		if bin == "" {
			continue
		}
		files, err := ownedFrontendSources(root, feDir)
		if err != nil {
			return out, fmt.Errorf("%s: %w", fe.name, err)
		}
		for start := 0; start < len(files); start += prettierBatch {
			end := min(start+prettierBatch, len(files))
			differ, err := runPrettierListDifferent(ctx, bin, feDir, files[start:end])
			if err != nil {
				return out, fmt.Errorf("%s: prettier: %w", fe.name, err)
			}
			for _, rel := range differ {
				project := filepath.ToSlash(filepath.Join(fe.dir, rel))
				diff := "(prettier would reformat this file)\n"
				if src, rerr := os.ReadFile(filepath.Join(feDir, rel)); rerr == nil {
					if formatted, perr := runPrettierStdin(ctx, bin, feDir, rel, src); perr == nil {
						diff = unifiedDiff(project, src, formatted)
					}
				}
				out = append(out, formatPending{File: project, Tool: "prettier", Diff: diff})
			}
		}
	}
	return out, nil
}

// runPrettierListDifferent is `prettier --list-different`: it names the files
// that are not formatted and writes nothing. Exit 1 means "some differ", and
// 2 that some file could not be parsed (the rest are still listed); neither
// is a failure to run.
func runPrettierListDifferent(ctx context.Context, bin, feDir string, files []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, prettierTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append([]string{"--list-different"}, files...)...)
	cmd.Dir = feDir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ctx.Err() != nil || !errors.As(err, &exitErr) || (exitErr.ExitCode() != 1 && exitErr.ExitCode() != 2) {
			return nil, err
		}
	}
	var out []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// runPrettierStdin formats src as the file rel (which picks the parser and
// the project's prettier config) and returns the result.
func runPrettierStdin(ctx context.Context, bin, feDir, rel string, src []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, prettierTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--stdin-filepath", rel)
	cmd.Dir = feDir
	cmd.Stdin = bytes.NewReader(src)
	return cmd.Output()
}

// formatFindings turns pending rewrites into findings for the structured
// (--quiet / --scope) report: one anchored finding per file, then the diffs.
func formatFindings(pending []formatPending) []lintJSONFinding {
	var fs []lintJSONFinding
	for _, p := range pending {
		fs = append(fs, lintJSONFinding{
			File:     p.File,
			Severity: lintSevError,
			Rule:     "format",
			Message:  p.Tool + " would reformat this file (nothing was written; the diff follows)",
			FixHint:  "run `forge lint --fix` to apply it",
		})
	}
	for _, p := range pending {
		fs = append(fs, lintJSONFinding{Severity: lintSevError, Rule: "external", Message: strings.TrimRight(p.Diff, "\n")})
	}
	return fs
}

// formatOutcome is the report-only lane in the structured drivers' terms.
func formatOutcome(ctx context.Context, root string, cfg *config.ProjectConfig, scope *lintScope, paths []string, skipFrontends bool) lintStepOutcome {
	keep := formatKeep(scope, paths)
	pending, err := collectFormatPending(ctx, root, keep, formatWithFrontends(scope, paths, skipFrontends), cfg)
	fs := formatFindings(pending)
	if err != nil {
		fs = append(fs, lintJSONFinding{Severity: lintSevWarning, Rule: "format-unavailable", Message: err.Error()})
	}
	return lintStepOutcome{name: formatLaneName, gates: true, ran: true, findings: fs, gated: len(pending) > 0}
}

// formatKeep is the file filter the Go formatter honours: the --scope when
// given, else the positional package paths.
func formatKeep(scope *lintScope, paths []string) func(rel string) bool {
	if scope != nil {
		return scope.contains
	}
	return keepForPaths(paths)
}

// formatWithFrontends reports whether the frontend formatter applies: it
// cannot be confined to Go paths or a scope, so it runs only for the whole
// project, and not under --skip-frontends.
func formatWithFrontends(scope *lintScope, paths []string, skipFrontends bool) bool {
	return scope == nil && !skipFrontends && keepForPaths(paths) == nil
}
