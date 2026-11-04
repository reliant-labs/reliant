package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/testreport"
)

// newCIVerifyTestRunCmd builds `forge ci verify-test-run`.
//
// WHY THIS EXISTS. A Go suite that declines to run its tests exits 0. On the
// reference project, one package and one environment variable apart:
//
//	internal/threads WITHOUT DATABASE_URL:    9 pass / 124 skip → exit 0
//	internal/threads WITH    DATABASE_URL:  103 pass /   1 skip → exit 0
//
// Same exit code. The suite looked green while running 7% of its tests, and no
// tool in the pipeline had a word to say about it. This command is that word.
//
// It does NOT run tests, and that is not an oversight. `forge test` was
// REMOVED (see test_removed.go) precisely because a second spelling of the
// project's suite reports on a different suite than the one the project runs —
// a green exit code for work that never happened. So this reads the record of
// the run the project ALREADY did: the `go test -json` stream, from a pipe or
// a file. The signal costs one extra flag on a run that was happening anyway.
func newCIVerifyTestRunCmd() *cobra.Command {
	var (
		from     string
		maxRatio float64
		minTests int
		warnOnly bool
		gateJSON string
	)

	cmd := &cobra.Command{
		Use:     "verify-test-run",
		Aliases: []string{"verify-tests", "test-skips"},
		Short:   "Verify a `go test -json` run actually ran its tests (reads output; runs nothing)",
		Long: "Reads a `go test -json` stream and reports the packages that skipped so much\n" +
			"that their pass proves nothing.\n\n" +
			"This command RUNS NO TESTS. It reads the record of the run your project\n" +
			"already did, so it costs one extra flag and no extra time:\n\n" +
			"  go test -json ./... | tee test.json | forge ci verify-test-run\n" +
			"  go test -json ./... > test.json; forge ci verify-test-run --from test.json\n\n" +
			"`go test -json` swallows the human-readable output, hence the `tee` — keep the\n" +
			"raw stream for a human and hand a copy to forge.\n\n" +
			"TWO RULES, AND WHY NOT MORE. Skips are legitimate: `-short` exists, framework\n" +
			"limitations get documented skips, and one reference package keeps a genuine\n" +
			"unconditional skip even when fully configured. A gate that fires on every skip\n" +
			"is a gate that gets switched off. So:\n\n" +
			"  zero-evidence   every test in the package skipped — its \"ok\" is a statement\n" +
			"                  about nothing. No sample-size floor; unambiguous at any size.\n" +
			"  mass-skip       the package skipped more than --max-skip-ratio of its tests\n" +
			"                  and has at least --min-tests of them.\n\n" +
			"Healthy packages are never listed. When a package's heavy skipping is genuinely\n" +
			"expected — an integration-only package on a machine with no docker — declare it\n" +
			"once in forge.yaml, with a reason:\n\n" +
			"  ci:\n" +
			"    test_skips:\n" +
			"      allow:\n" +
			"        - package: internal/dockerintegration\n" +
			"          reason: \"every test here needs a live docker daemon\"\n\n" +
			"The reason is required and is read by humans, not by forge: an exemption nobody\n" +
			"had to justify is one nobody will revisit. A declaration that stops suppressing\n" +
			"anything is reported as no longer needed rather than left to rot.\n\n" +
			"WHICH RUN TO POINT IT AT. The one whose green you are treating as coverage.\n" +
			"A deliberately-reduced run (`-short`, a single package, a -run filter) is not a\n" +
			"claim about the whole suite, so gating it teaches people to ignore the gate;\n" +
			"forge cannot see the flags a stream was produced with and will not guess.\n\n" +
			"THREE STATES. Input that carries no `go test -json` events, or a stream that\n" +
			"ends mid-run, is UNDETERMINED — forge could not obtain the facts. That is not a\n" +
			"pass and it exits non-zero: this command never reports a clean run it did not\n" +
			"read. Failures in the stream also fail the command, because\n" +
			"`go test -json ./... | forge ci verify-test-run` in a shell without\n" +
			"`set -o pipefail` reports only the LAST command's status — a checker that\n" +
			"ignored them would launder a red suite green.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// The check's own window starts here. It measures the
			// VERIFICATION, not the test run — forge did not run the
			// tests (see the comment above), and claiming their
			// duration would be inventing a measurement.
			startedAt := time.Now()
			policy, projectNote, err := resolveTestSkipPolicy(cmd, maxRatio, minTests)
			if err != nil {
				return err
			}

			in, closeIn, err := openTestRunInput(cmd, "forge ci verify-test-run", from)
			if err != nil {
				return err
			}
			defer closeIn()

			report := testreport.New(testreport.Deps{})
			run, err := report.Parse(in)
			if err != nil {
				// Unreadable input is UNDETERMINED, never a pass. Say
				// which half failed — an empty pipe and a bad path are
				// different problems with different fixes.
				where := "standard input"
				if from != "" {
					where = from
				}
				if errors.Is(err, testreport.ErrNoInput) {
					return cliutil.UserErr("forge ci verify-test-run",
						"UNDETERMINED — "+where+" was empty, so forge has no run to verify (this is not a pass)",
						where,
						"produce the stream first: `go test -json ./... > test.json` then `forge ci verify-test-run --from test.json`")
				}
				return cliutil.WrapUserErr("forge ci verify-test-run",
					"UNDETERMINED — could not read "+where+" (this is not a pass)", where,
					"check the path and permissions, or re-run the suite with `-json`", err)
			}

			analysis := report.Analyze(run, policy)
			if projectNote != "" {
				fmt.Fprintln(cmd.OutOrStdout(), projectNote)
			}
			report.Render(cmd.OutOrStdout(), analysis)

			// The gate document, before the exit-code switch below,
			// so the evidence is written for EVERY outcome — a
			// failing or undetermined run is the one most worth
			// recording, and returning first would write it only
			// for the green case.
			if gateJSON != "" {
				if err := writeTestRunGate(gateJSON, analysis, startedAt); err != nil {
					return err
				}
			}

			switch analysis.Status() {
			case testreport.StatusUndetermined:
				// Undetermined outranks --warn-only. The flag is an
				// adoption ramp for FINDINGS ("show me, don't block
				// me"); it is not permission to report a pass over
				// facts forge never obtained.
				return cliutil.UserErr("forge ci verify-test-run",
					fmt.Sprintf("UNDETERMINED — forge could not obtain the facts for %d item(s), so it will not report a pass", len(analysis.Undetermined)),
					"",
					"re-run the suite and capture the whole stream (`go test -json ./... > test.json`); a truncated log cannot be verified")
			case testreport.StatusFail:
				if analysis.Totals.SuiteFailed {
					// Failures are never downgraded by --warn-only:
					// swallowing them is the pipefail trap the command
					// exists to close.
					return cliutil.UserErr("forge ci verify-test-run",
						fmt.Sprintf("the run contains failures in %d package(s)", analysis.Totals.FailedPackages),
						"",
						"fix the failing tests — and note that `go test | forge ...` hides go test's exit status unless the shell has `set -o pipefail`")
				}
				if warnOnly {
					fmt.Fprintln(cmd.OutOrStdout(), "\n(--warn-only: reported, not enforced)")
					return nil
				}
				return cliutil.UserErr("forge ci verify-test-run",
					fmt.Sprintf("%d package(s) skipped so much of their suite that the pass proves nothing", len(analysis.Findings)),
					"",
					"supply what the skips are waiting on (a database URL, docker, a service), drop `-short`, or — if the skipping is legitimate — declare it in forge.yaml under `ci.test_skips.allow` with a reason")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&from, "from", "", "Read the 'go test -json' stream from this file instead of stdin")
	cmd.Flags().Float64Var(&maxRatio, "max-skip-ratio", testreport.DefaultPolicy().MaxSkipRatio,
		"Share of a package's tests that may skip before it is reported")
	cmd.Flags().IntVar(&minTests, "min-tests", testreport.DefaultPolicy().MinTests,
		"Sample-size floor for the mass-skip rule (a package that skipped EVERY test is reported regardless)")
	cmd.Flags().BoolVar(&warnOnly, "warn-only", false,
		"Report skip findings without failing (adoption ramp; UNDETERMINED and test failures still fail)")
	cmd.Flags().StringVar(&gateJSON, "gate-json", "",
		"Also write this run's result to `FILE` as a gate document, for `forge gate record` or `forge env deploy --gate`. "+
			"A FILE, not a stdout mode: the report and the exit code are unchanged.")

	return cmd
}

// resolveTestSkipPolicy layers the policy: shipped defaults, then forge.yaml,
// then explicit flags.
//
// The project is OPTIONAL here, unlike forge's other ci subcommands. Those
// read the project's own files (generated code, migrations, frontends) and
// have nothing to say without one. This one's input is a `go test -json`
// stream — a Go fact, not a forge fact — so refusing to look at it for want of
// a forge.yaml would only mean the signal is unavailable exactly where a
// pipeline most needs it. Without a project the shipped defaults apply and the
// caller is told so, since declared exemptions live in forge.yaml and a user
// who expected theirs to apply must not have to guess why they did not.
func resolveTestSkipPolicy(cmd *cobra.Command, maxRatio float64, minTests int) (testreport.Policy, string, error) {
	policy := testreport.DefaultPolicy()
	note := ""

	store, err := loadProjectStore()
	switch {
	case errors.Is(err, ErrProjectConfigNotFound):
		note = "ℹ️  No forge.yaml here — using the shipped defaults (declared exemptions live in forge.yaml under ci.test_skips.allow)."
	case err != nil:
		return policy, "", err
	default:
		if !isFeatureEnabled(store, config.FeatureCI) {
			return policy, "", config.DisabledFeatureError(config.FeatureCI)
		}
		cfg := store.CI().TestSkips
		if cfg.MaxSkipRatio > 0 {
			policy.MaxSkipRatio = cfg.MaxSkipRatio
		}
		if cfg.MinTests > 0 {
			policy.MinTests = cfg.MinTests
		}
		for _, a := range cfg.Allow {
			policy.Allow = append(policy.Allow, testreport.Exemption{
				Package:      a.Package,
				Reason:       a.Reason,
				MaxSkipRatio: a.MaxSkipRatio,
			})
		}
	}

	// Flags win, but only when the user actually typed them — a default
	// value carried in a flag must not override forge.yaml.
	if cmd.Flags().Changed("max-skip-ratio") {
		policy.MaxSkipRatio = maxRatio
	}
	if cmd.Flags().Changed("min-tests") {
		policy.MinTests = minTests
	}
	if policy.MaxSkipRatio <= 0 || policy.MaxSkipRatio > 1 {
		return policy, "", cliutil.UserErr("forge ci verify-test-run",
			fmt.Sprintf("--max-skip-ratio must be between 0 (exclusive) and 1, got %v", policy.MaxSkipRatio),
			"", "pass a share such as 0.5 (report a package that skipped more than half its tests)")
	}

	// A declaration that does not parse must never read as a declaration
	// that applied — a silently-dropped suppression is indistinguishable
	// from a suppression that worked, right up until it does not.
	if errs := policy.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(cmd.ErrOrStderr(), "  - %v\n", e)
		}
		return policy, "", cliutil.UserErr("forge ci verify-test-run",
			fmt.Sprintf("%d invalid skip exemption(s) in forge.yaml", len(errs)),
			"forge.yaml (ci.test_skips.allow)",
			"give every entry a `package` and a `reason`, and keep `max_skip_ratio` between 0 and 1")
	}
	return policy, note, nil
}

// openTestRunInput resolves --from / stdin.
//
// The TTY guard matters more than it looks: with no --from and no pipe, the
// command would block on an interactive terminal forever, which reads as a
// hang rather than as a mistake. Naming the mistake costs one stat call.
func openTestRunInput(cmd *cobra.Command, name, from string) (io.Reader, func(), error) {
	if from != "" {
		f, err := os.Open(from)
		if err != nil {
			return nil, func() {}, cliutil.WrapUserErr(name,
				"could not open "+from, from,
				"check the path — it should hold the output of `go test -json ...`", err)
		}
		return f, func() { _ = f.Close() }, nil
	}
	in := cmd.InOrStdin()
	// The guard applies only when the command is genuinely reading the
	// process's stdin. A caller that injected a reader (a test, an
	// embedding) has already answered the question the stat would ask.
	if in != os.Stdin {
		return in, func() {}, nil
	}
	if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice != 0 {
		return nil, func() {}, cliutil.UserErr(name,
			"nothing is piped in, so there is no test run to read",
			"",
			"pipe a stream (`go test -json ./... | "+name+"`) or point at a saved one (`--from test.json`)")
	}
	return in, func() {}, nil
}
