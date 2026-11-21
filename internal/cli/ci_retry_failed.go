package cli

import (
	"encoding/json"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/testretry"
)

func newCIRetryFailedCmd() *cobra.Command {
	cfg := testretry.Config{Timeout: 2 * time.Minute}
	cmd := &cobra.Command{
		Use:   "retry-failed [flags] -- PACKAGE...",
		Short: "Run ordinary Go tests, retrying exact unchanged failures once",
		Long: `Run one ordinary go test -short -json invocation. If it fails solely
on up to five exact top-level tests whose entire source files are unchanged
from the checked base, retry only those tests, each once with -count=1.
Each must be the only test that began in its package in the original run,
and that package must not define TestMain. Keep the original Go flags;
unsupported or conflicting flags disqualify a retry.

Read structured Go events, not console PASS lines. Package/build failures,
race-enabled runs, panics, resource failures, subtests, and incomplete or
skipped retries stay red. The original NDJSON remains unchanged. A recovered
invocation writes an exclusive JSON record and retry NDJSON alongside it for
a trusted cleanup workflow; it does not change GitHub or delete any tests.

Use only from a credential-free merge test runner, with a verified Forge
binary and a clean checkout. Do not wrap a changed-test repetition lane,
release test lane, or a mixed lint/build/test command.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, packages []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg.Root, cfg.Packages = root, packages
			cfg.Stdout, cfg.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return testretry.Run(cmd.Context(), cfg)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&cfg.Repository, "repo", "", "owner/name from the gate")
	flags.StringVar(&cfg.Base, "base", "", "full SHA of checked base")
	flags.StringVar(&cfg.Head, "head", "", "full SHA of tested commit")
	flags.StringVar(&cfg.RunID, "run", "", "trusted CI execution identifier")
	flags.IntVar(&cfg.PR, "pr", 0, "pull request number, if supplied by the gate")
	flags.StringVar(&cfg.JSONPath, "json", "", "fresh path for original Go NDJSON")
	flags.StringVar(&cfg.ReportPath, "report", "", "fresh absolute path for successful retry report")
	flags.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "same per-package timeout for initial test and retry")
	flags.IntVar(&cfg.Parallel, "parallel", 0, "Go package parallelism (0 keeps Go's default)")
	flags.BoolVar(&cfg.NoBuildVCS, "no-build-vcs", false, "pass -buildvcs=false to both invocations")
	flags.BoolVar(&cfg.Echo, "echo", false, "also stream original Go NDJSON on stdout")
	for _, name := range []string{"repo", "base", "head", "run", "json", "report"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func newCIVerifyTestRetryCmd() *cobra.Command {
	var cfg testretry.Config
	var tree, module string
	cmd := &cobra.Command{
		Use:   "verify-test-retry",
		Short: "Verify recorded test retries against a checked Git tree and event artifacts",
		Long: "Verify the artifact from ci retry-failed against independently checked gate metadata. " +
			"Reads Git objects and event files only; no candidate Go code is executed. Emits the verified report as JSON.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg.Root = root
			report, err := testretry.Verify(cmd.Context(), cfg, tree, module)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&cfg.Repository, "repo", "", "verified repository owner/name")
	flags.StringVar(&cfg.Base, "base", "", "full SHA of checked gate base")
	flags.StringVar(&cfg.Head, "head", "", "full SHA of tested commit")
	flags.StringVar(&tree, "tree", "", "full SHA of tested tree")
	flags.StringVar(&cfg.RunID, "run", "", "verified CI execution identifier")
	flags.IntVar(&cfg.PR, "pr", 0, "pull request number, if supplied by the gate")
	flags.StringVar(&module, "module", "", "verified Go module import path")
	flags.StringVar(&cfg.JSONPath, "json", "", "path to original Go NDJSON artifact")
	flags.StringVar(&cfg.ReportPath, "report", "", "path to retry report artifact")
	for _, name := range []string{"repo", "base", "head", "tree", "run", "module", "json", "report"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
