// File: internal/cli/lint/lint_proto_format.go
//
// `forge lint --proto-format`: every .proto in the project's buf workspace is
// what `buf format` makes of it.
//
// The scaffolded pre-commit config gates protos on `buf format -d
// --exit-code`, run from the project root. `forge lint` did not, so a project
// heard "all gating linters passed" and then had its commit refused over a
// proto diff — most of it in text forge itself had just written (an import
// out of order, a one-field option literal spread over three lines, a
// `(buf.validate.field)` aggregate on one line). Forge now writes buf's output
// (internal/protofmt); this lane holds the line for every proto, whoever
// wrote it, so the gate and the commit hook cannot disagree.
//
// It runs the hook's own command — `buf format -d` over the whole workspace,
// with the buf the project pins (resolveLintBuf: go.mod's pin, from PATH when
// it is that release, else forge's tool cache; with no pin declared, PATH's,
// else forge's) — and reports each file buf would rewrite with buf's diff.
// When go.mod's pin cannot be installed and PATH has another release, the
// lane refuses with one finding rather than report that release's formatting
// as diffs (protoFormatPinRefusal). `--fix` writes buf's output
// instead (`buf format -w --path <file>` per pending file). A project with no
// buf.yaml has nothing for buf to format, and the lane is silently absent.
//
// The rule gates through the warn-first rollout (lint_rollout_table.go), and
// `lint.lanes: {proto-format: off}` turns the lane off for a project whose
// protos are formatted another way.

package lint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ruleProtoFormat tags a proto `buf format` would rewrite.
const ruleProtoFormat = "forge-proto-format"

// protoFormatFixHint is how to clear a forge-proto-format finding.
const protoFormatFixHint = "run `forge lint --proto-format --fix`, or `buf format -w` from the project root " +
	"(a directory inside the module needs `--path`: `buf format -w --path proto/services`)"

// collectProtoFormatJSON is the lane. root is the project root (the lint
// run's working directory). keep (formatKeep: --scope, else the positional
// paths; nil is every file) bounds the report AND the fix: it is applied
// before anything is written, so `--fix --scope proto/services/a` never
// rewrites a proto outside the slice — another agent's work in a shared
// checkout. Filtering only the findings after the lane ran, as the
// structured driver's scopeOutcome does, would be too late for the fix.
func collectProtoFormatJSON(ctx context.Context, root string, keep func(rel string) bool, fix bool) ([]lintJSONFinding, bool, error) {
	if !fileExists(filepath.Join(root, "buf.yaml")) {
		return nil, false, nil
	}
	b, ok := resolveLintBuf(ctx, root)
	if !ok {
		return []lintJSONFinding{{Severity: lintSevWarning, Rule: ruleBufLintUnavailable,
			Message: "buf is not on PATH, so the proto-format lane cannot run", FixHint: bufMissingFix()}}, false, nil
	}
	if b.standIn != nil {
		return []lintJSONFinding{protoFormatPinRefusal(ctx, b)}, false, nil
	}
	bin := b.bin
	pending, err := bufFormatPending(ctx, bin, root)
	if err != nil {
		return []lintJSONFinding{{Severity: lintSevError, Rule: ruleProtoFormat,
			Message: err.Error(), FixHint: "fix the proto buf cannot parse; `buf lint` names the line"}}, true, nil
	}
	var fs []lintJSONFinding
	for _, p := range pending {
		if keep != nil && !keep(p.File) {
			continue
		}
		if fix {
			if ferr := bufFormatWrite(ctx, bin, root, p.File); ferr != nil {
				fs = append(fs, lintJSONFinding{File: p.File, Severity: lintSevError, Rule: ruleProtoFormat,
					Message: fmt.Sprintf("buf format -w failed: %v", ferr), FixHint: protoFormatFixHint})
			}
			continue
		}
		fs = append(fs, lintJSONFinding{
			File:     p.File,
			Severity: lintSevError,
			Rule:     ruleProtoFormat,
			Message:  "buf format would reformat this file — the pre-commit buf-format hook refuses it (nothing was written; the diff follows)",
			FixHint:  protoFormatFixHint,
		}, lintJSONFinding{
			Severity: lintSevError, Rule: "external", Message: strings.TrimRight(p.Diff, "\n"),
			// The diff is the finding's body: it is graded, softened and
			// switched off with it (lint_rollout.go's pseudo-rule key).
			rollout: ruleProtoFormat,
		})
	}
	return fs, len(fs) > 0, nil
}

// protoFormatPinRefusal is the lane's one finding when the buf go.mod pins
// could not be installed and PATH has another release. Two buf releases
// format the same proto differently (multi-line option literals, say), so
// running PATH's buf would report its own formatting as a diff in every proto
// the pinned buf left as it is: reliant, pinning 1.57.0 on a machine with
// 1.71.0, got 29 such findings, and `forge project upgrade` 29 needs_human
// items, for a tree the pin reports clean. The lane did not run, so it says
// that once, as a `-unavailable` gap (lint.allow_skip governs it), instead.
func protoFormatPinRefusal(ctx context.Context, b lintBuf) lintJSONFinding {
	have := lintBufVersion(ctx, b.bin)
	if have == "" {
		have = "of unknown version"
	}
	return lintJSONFinding{
		Severity: lintSevWarning,
		Rule:     ruleBufLintUnavailable,
		Message: fmt.Sprintf("the proto-format lane did not run: the buf on PATH (%s) is %s, but go.mod pins buf %s, "+
			"which could not be installed (%v); another buf release formats protos differently, so its diffs would not be this project's",
			b.bin, have, b.pin.Bare(), b.standIn),
		FixHint: fmt.Sprintf("put buf %s where forge finds it: re-run with the Go module proxy reachable (forge builds the pin into its tool cache), "+
			"or `go install %s@%s` into a directory ahead of %s on PATH, then re-run `forge lint --proto-format`",
			b.pin.Bare(), bufModulePath, b.pin.Version, filepath.Dir(b.bin)),
	}
}

// bufFormatPending runs `buf format -d` from root — the pre-commit hook's
// command, without --exit-code — and splits buf's diff into one formatPending
// per file. buf heads each file's diff with `diff -u <file>.orig <file>`.
func bufFormatPending(ctx context.Context, bin, root string) ([]formatPending, error) {
	cmd := exec.CommandContext(ctx, bin, "format", "-d")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("buf format failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return splitBufFormatDiff(stdout.String()), nil
}

// splitBufFormatDiff splits `buf format -d` output into its per-file diffs.
func splitBufFormatDiff(out string) []formatPending {
	var pending []formatPending
	var cur *formatPending
	for _, line := range strings.SplitAfter(out, "\n") {
		if strings.HasPrefix(line, "diff ") {
			fields := strings.Fields(line)
			pending = append(pending, formatPending{File: filepath.ToSlash(fields[len(fields)-1]), Tool: "buf format"})
			cur = &pending[len(pending)-1]
			continue
		}
		if cur != nil {
			cur.Diff += line
		}
	}
	return pending
}

// bufFormatWrite formats one project-relative file in place.
func bufFormatWrite(ctx context.Context, bin, root, rel string) error {
	cmd := exec.CommandContext(ctx, bin, "format", "-w", "--path", rel)
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
