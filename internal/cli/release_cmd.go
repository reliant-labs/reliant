package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"

	"github.com/reliant-labs/forge/pkg/release"
)

// `forge release` is the release-ledger noun, and it holds only the verbs that
// act on a ledger ITSELF, independent of any environment: proving its claims
// (`verify`) and locating a version (`where`).
//
// Everything env-scoped lives on the env noun. `forge env build <env>
// --release <v>` CUTS a ledger and `forge env deploy` ADVANCES one, each where
// the thing it acts on lives. `forge release cut` was DELETED for that reason:
// it was the cut without the build, which is `forge env build <env> --release
// <v> --no-build` — one code path for "record a release", differing only in
// whether it builds first. Two spellings of one cut meant two places for the
// completeness gate to drift.
func newReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Inspect and verify release ledgers",
		Long: `Work with the release ledgers ` + "`forge env build <env> --release <version>`" + ` writes.

A release ledger (.forge/releases/<version>.json) names every artifact a
release ships — container images, npm packages, Go modules, published files —
with the coordinate and hash each one was cut with.`,
	}
	cmd.AddCommand(newReleaseVerifyCmd())
	cmd.AddCommand(newReleaseWhereCmd())
	cmd.AddCommand(newReleaseBundleCmd())
	return cmdutil.StrictGroup(cmd)
}

// `forge release convert-ledger` IS DELETED. It converted
// .forge/env-releases.json into .forge/promotions/<env>.jsonl — one retired
// in-checkout format into another. Neither is read any more: the ledger is
// the control plane or the machine store, and the path from a checkout's
// committed ledger to either one is `forge ledger import --from-git`, which
// imports history rather than rewriting files in the tree. A conversion
// command that produced files nothing reads would be worse than its absence.

// defaultVerifyTimeout bounds each registry request. Generous enough for a
// cold TLS handshake to a slow registry, short enough that a hung endpoint
// does not stall a CI job.
const defaultVerifyTimeout = 30 * time.Second

// defaultVerifyConcurrency bounds simultaneous registry reads. Checks are
// independent, so a release with many artifacts should not cost one round-trip
// each in series — but a verifier that opens fifty connections to npmjs.org
// looks like abuse and invites a rate limit, which would surface as
// UNREACHABLE noise.
const defaultVerifyConcurrency = 8

// newReleaseVerifyCmd is `forge release verify <version>`.
func newReleaseVerifyCmd() *cobra.Command {
	var (
		timeout time.Duration
		strict  bool
		workers int
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "verify <version>",
		Short: "Prove every artifact a release names actually exists and matches",
		Long: `Check that every artifact named in a release ledger really exists in its
public registry, and that its bytes match what the ledger recorded.

WHY THIS EXISTS. A ledger that NAMES an artifact is a claim, not a fact. forge
v0.1.12 tagged its web runtime at 0.3.1, recorded the integrity hash of the
tarball on the build machine, and never published it. Nothing compared the two,
so the gap surfaced days later as a scaffolded project failing to install. This
command is that comparison.

WHAT IS CHECKED, PER KIND:

  oci    the registry serves a manifest at the recorded digest. A digest is
         content-addressed, so existence IS the byte check.
  npm    the registry has that exact version AND its dist.integrity equals the
         recorded hash. A mismatch means different bytes shipped under a
         version number that is now permanently taken.
  gomod  the public checksum database has that version AND its h1: module hash
         equals the recorded one.
  file   reported UNVERIFIABLE — nothing yet records where a file artifact is
         published, so there is no URL to fetch.

NO CREDENTIALS. Every read is an anonymous request to a public registry. That
is deliberate: if proving a release were to require a login, only the operator
of that login could prove it, and the check would stop being independently
verifiable by the person who most needs it.

THREE OUTCOMES, NOT TWO:

  VERIFIED      the artifact exists and matches.
  FAILED        proven wrong — absent, or present with different bytes.
  UNVERIFIABLE  a structural gap makes the check impossible (a file artifact
                with no publish URL, a private Go module, an OCI artifact whose
                ledger names no registry). Says nothing about validity.
  UNREACHABLE   the check could not complete — a timeout, DNS failure, or a
                registry demanding credentials. Transient; retry may verify.

EXIT CODES:

  0  nothing failed
  1  at least one artifact FAILED, or --strict was set and something was
     UNVERIFIABLE
  2  a check could not COMPLETE (UNREACHABLE) and nothing outright failed

Exit 2 is separate from 1 on purpose. A network blip is not evidence against a
release, and a gate that reports a missing artifact and a flaky DNS lookup with
the same code is a gate that gets switched off the first week it is wrong.

Examples:
  forge release verify v1.4.0              # check every artifact
  forge release verify v1.4.0 --strict     # also fail on anything unverifiable
  forge release verify v1.4.0 --timeout 1m # slow or distant registry
  forge release verify v1.4.0 --json       # machine-readable, same exit codes

--json emits the same verdicts as a document, with the ledger's git provenance
alongside them. Read the git.dirty field: a release cut from a tree with
uncommitted changes ships bytes that correspond to no reviewable commit, which
no per-artifact check can detect. The four statuses stay four values —
"unverifiable" is not "verified".`,
		Args: cobra.ExactArgs(1),
		// The command reports its own findings; a cobra usage dump on a
		// verification failure would bury them under the flag list.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReleaseVerify(cmd.Context(), args[0], verifyOptions{
				Timeout:     timeout,
				Strict:      strict,
				Concurrency: workers,
				JSON:        asJSON,
			})
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", defaultVerifyTimeout, "Per-request timeout for registry reads")
	cmd.Flags().BoolVar(&strict, "strict", false, "Treat UNVERIFIABLE artifacts as failures (exit 1)")
	cmd.Flags().IntVar(&workers, "concurrency", defaultVerifyConcurrency, "Maximum simultaneous registry requests")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the verification report as JSON (exit code unchanged)")

	return cmd
}

// verifyOptions carries the flags into the run function.
type verifyOptions struct {
	Timeout     time.Duration
	Strict      bool
	Concurrency int
	// JSON swaps the human report for the machine-readable one. It changes
	// only the rendering: the exit code is decided by the same tally either
	// way.
	JSON bool
}

// exitCodeError carries a specific process exit code out through cobra's error
// return, so `forge release verify` can distinguish "artifact is missing" (1)
// from "could not reach the registry" (2) without calling os.Exit from inside
// a command — which would bypass root.go's deferred cleanup.
type exitCodeError struct {
	code int
	msg  string
}

func (e exitCodeError) Error() string { return e.msg }

// ExitCode reports the process exit status this error should produce. main()
// checks for this interface; any other error keeps the default 1.
func (e exitCodeError) ExitCode() int { return e.code }

// runReleaseVerify loads the ledger, checks every artifact, prints a per-
// artifact report, and returns an error carrying the right exit code.
func runReleaseVerify(ctx context.Context, version string, opts verifyOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultVerifyTimeout
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = defaultVerifyConcurrency
	}

	projectDir := projectDirForKCL()
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return err
	}
	rel, err := store.Release(version)
	if err != nil {
		return fmt.Errorf("read release %q: %w", version, err)
	}
	if rel == nil {
		return fmt.Errorf("release %q is not recorded in %s.\n"+
			"  Cut it first with: forge env build <env> --release %s --push",
			version, store.Dir(), version)
	}
	if len(rel.Artifacts) == 0 {
		// An empty ledger cannot fail verification, and reporting "0 failed"
		// would read as success. It is a defective release in its own right.
		return fmt.Errorf("release %q names no artifacts — there is nothing to verify.\n"+
			"  A release is cut empty when no images were pushed and no packages were harvested;\n"+
			"  re-cut it with: forge env build <env> --release %s --push", version, version)
	}

	fetcher := newHTTPFetcher(opts.Timeout)

	if opts.JSON {
		results := verifyReleaseArtifacts(ctx, fetcher, *rel, opts.Concurrency)
		return emitReleaseVerifyJSON(*rel, results, opts.Strict)
	}

	fmt.Printf("Verifying release %s (%d artifact(s))\n", version, len(rel.Artifacts))
	if rel.Git.Commit != "" {
		fmt.Printf("  commit %s", rel.Git.Commit)
		if rel.Git.Tag != "" {
			fmt.Printf(" (tag %s)", rel.Git.Tag)
		}
		if rel.Git.Dirty {
			fmt.Print(" [dirty tree]")
		}
		fmt.Println()
	}
	fmt.Println()

	results := verifyReleaseArtifacts(ctx, fetcher, *rel, opts.Concurrency)

	for _, r := range results {
		fmt.Printf("  %-12s %-5s %s\n", r.Status, r.Kind, r.Name)
		if r.Detail != "" {
			fmt.Printf("      %s\n", r.Detail)
		}
	}

	tally := tallyVerifications(results)
	fmt.Printf("\n%d verified, %d failed, %d unverifiable, %d unreachable\n",
		tally.Verified, tally.Failed, tally.Unverifiable, tally.Unreachable)

	if err := releaseVerifyVerdict(version, tally, opts.Strict); err != nil {
		return err
	}

	if tally.Unverifiable > 0 {
		fmt.Printf("\nNote: %d artifact(s) could not be checked. Re-run with --strict to make that an error.\n", tally.Unverifiable)
	}
	return nil
}

// releaseVerifyVerdict turns a tally into the command's exit status. It is the
// SINGLE place that decision lives, so `--json`'s `ok` field cannot drift from
// what text mode exits with — including the --strict interaction, where an
// unverifiable artifact becomes a failure. A second copy of this switch is how
// the two modes end up disagreeing about whether a release verified.
func releaseVerifyVerdict(version string, tally verifyTally, strict bool) error {
	switch {
	case tally.Failed > 0:
		return exitCodeError{code: 1, msg: fmt.Sprintf(
			"release %s does not verify: %d artifact(s) are missing or do not match what the ledger recorded",
			version, tally.Failed)}
	case strict && tally.Unverifiable > 0:
		return exitCodeError{code: 1, msg: fmt.Sprintf(
			"release %s has %d unverifiable artifact(s) and --strict was set",
			version, tally.Unverifiable)}
	case tally.Unreachable > 0:
		return exitCodeError{code: 2, msg: fmt.Sprintf(
			"release %s could not be fully checked: %d artifact(s) were unreachable (network or credentials), %d verified, 0 failed",
			version, tally.Unreachable, tally.Verified)}
	}
	return nil
}

// releaseVerifyReport is the `--json` document.
//
// The git block is not decoration. `dirty: true` means the release was cut
// from a tree with uncommitted changes — bytes that correspond to no
// reviewable commit — and that is a finding about the release every bit as
// real as a missing artifact, which no per-artifact check can surface. Text
// mode already prints it; omitting it from the machine-readable form would
// make the JSON the weaker of the two.
type releaseVerifyReport struct {
	Release    string                 `json:"release"`
	Git        release.Git            `json:"git"`
	Strict     bool                   `json:"strict"`
	Artifacts  []artifactVerification `json:"artifacts"`
	Summary    verifyTally            `json:"summary"`
	OK         bool                   `json:"ok"`
	ExitCode   int                    `json:"exit_code"`
	Diagnostic string                 `json:"diagnostic,omitempty"`
}

// emitReleaseVerifyJSON writes the report and returns the SAME error text mode
// would have returned, so the exit code is identical in both modes.
func emitReleaseVerifyJSON(rel release.Release, results []artifactVerification, strict bool) error {
	tally := tallyVerifications(results)
	verdict := releaseVerifyVerdict(rel.Version, tally, strict)

	report := releaseVerifyReport{
		Release:   rel.Version,
		Git:       rel.Git,
		Strict:    strict,
		Artifacts: results,
		Summary:   tally,
		OK:        verdict == nil,
		ExitCode:  0,
	}
	if verdict != nil {
		report.Diagnostic = verdict.Error()
		var ec exitCodeError
		if errors.As(verdict, &ec) {
			report.ExitCode = ec.ExitCode()
		} else {
			report.ExitCode = 1
		}
	}
	if report.Artifacts == nil {
		report.Artifacts = []artifactVerification{}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("write release verify report: %w", err)
	}
	return verdict
}
