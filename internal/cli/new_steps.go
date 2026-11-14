package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
)

// newProjectLog is where `project new` keeps the output of its steps — the
// package manager's install, the bootstrap generate, go mod tidy — that it
// reports as one line each. Under .forge/, which the scaffold gitignores, so
// the initial commit never carries it.
const newProjectLog = ".forge/logs/project-new.log"

// printStepLog says where the steps' output went, once they are done.
func printStepLog(steps *cliutil.StepRunner) {
	steps.PrintLogPath(os.Stdout)
}

var (
	// pnpm: "Packages: +631" (one per install).
	pnpmPackagesAdded = regexp.MustCompile(`(?m)^Packages: \+(\d+)`)
	// npm: "added 631 packages in 6s" / "added 1 package".
	npmPackagesAdded = regexp.MustCompile(`\badded (\d+) packages?\b`)
)

// installSummary reduces a frontend install's output to how many packages it
// installed, summed over every install in it; "" when it does not say.
func installSummary(output string) string {
	matches := pnpmPackagesAdded.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		matches = npmPackagesAdded.FindAllStringSubmatch(output, -1)
	}
	total := 0
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			total += n
		}
	}
	switch {
	case len(matches) == 0:
		return ""
	case total == 1:
		return "1 package installed"
	default:
		return fmt.Sprintf("%d packages installed", total)
	}
}

// generateFilesWritten matches the count in generate's closing line,
// "✅ Code generation complete — 82 file(s) written, …".
var generateFilesWritten = regexp.MustCompile(`Code generation complete — (\d+) file\(s\) written`)

// generateSummary reduces the bootstrap generate's output to the files it
// wrote, and says so when it skipped its compile check, which only --verify
// runs inside `project new`.
func generateSummary(output string) string {
	var parts []string
	if m := generateFilesWritten.FindStringSubmatch(output); m != nil {
		parts = append(parts, m[1]+" files written")
	}
	if strings.Contains(output, "compile check skipped") {
		parts = append(parts, "compile check skipped (--verify runs it)")
	}
	return strings.Join(parts, "; ")
}

// initGitStep is the git init step of `project new`: the repository, hook
// activation and the initial commit, as one line. A commit skipped for want
// of a git identity is not a failed step — the repository exists and every
// file is staged — so it is reported by reportGitInitError, once, after the
// step line, exactly as it was before steps were captured.
func initGitStep(ctx context.Context, steps *cliutil.StepRunner, path string, activateHooks, verbose bool) {
	var gitErr error
	_ = steps.Run(cliutil.Step{
		Title:  "git repository",
		Header: "\n🔧 Initializing git repository...",
		Summary: func(string) string {
			if gitErr != nil {
				return "initial commit skipped"
			}
			return "initial commit made"
		},
		Run: func() error {
			gitErr = initGitRepository(ctx, path, activateHooks)
			var idErr *gitIdentityError
			if errors.As(gitErr, &idErr) {
				return nil
			}
			return gitErr
		},
	})
	if gitErr != nil {
		reportGitInitError(os.Stdout, gitErr, verbose)
	}
}
