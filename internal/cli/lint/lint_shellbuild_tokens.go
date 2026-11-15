// File: internal/cli/lint/lint_shellbuild_tokens.go
//
// shellbuild-tokens — flags a retired forge substitution token left in a
// ShellBuild `cmd`.
//
// forge once substituted a fixed ${NAME} vocabulary into a ShellBuild command
// before running it. It no longer does: a `cmd` is plain KCL and forge runs the
// rendered string byte-for-byte. So a leftover token is now a SHELL variable
// that nothing sets.
//
// This GATES, unlike most of the advisory rules around it, because one member
// of the retired set fails silently. `GOARCH=${TARGETARCH}` becomes `GOARCH=`,
// which Go reads as "unset" and builds for the host — so an amd64 cluster gets
// an arm64 binary and the only symptom is `exec format error` in a crash-loop,
// with nothing naming the build that produced it. The rest (`docker push /:`)
// fail loudly and would be caught by the build anyway; that one would not be
// caught at all, and a warning nobody reads is not a guard against it.
//
// See internal/shellbuildtokens for why the check reads KCL SOURCE rather than
// a render, and for the per-token replacement each finding names.

package lint

import (
	"github.com/reliant-labs/forge/internal/shellbuildtokens"
)

// deployKCLDirDefault is the tree that holds a project's hand-written env KCL,
// which is where every ShellBuild is declared.
const deployKCLDirDefault = "deploy/kcl"

// collectShellBuildTokensJSON is the JSON arm. Every finding is an error and
// the step gates, matching text mode.
func collectShellBuildTokensJSON(dir string) ([]lintJSONFinding, bool, error) {
	findings, err := shellbuildtokens.ScanKCLTree(dir)
	if err != nil {
		return nil, false, err
	}
	out := make([]lintJSONFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: "error",
			Rule:     "shellbuild-tokens/retired-token",
			Message:  f.Message(),
			FixHint:  f.Remediation(),
		})
	}
	return out, len(out) > 0, nil
}
