package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
)

// gitleaksLeakExit is the exit status secret-scan asks gitleaks to use when
// it finds a leak, so a finding is told apart from gitleaks failing to run
// (which exits 1).
const gitleaksLeakExit = 9

// newCISecretScanCmd is `forge ci secret-scan`: gitleaks, at the version forge
// pins, over the checkout's commits. It is the gate's secret-scan row
// (forge.yaml ci.secret_scan), and runs on its own in any project kind — a
// static site included, which has no other secret scanning.
func newCISecretScanCmd() *cobra.Command {
	var (
		since  string
		staged bool
		branch bool
	)
	cmd := &cobra.Command{
		Use:   "secret-scan",
		Short: "Scan the checkout's commits for committed secrets (gitleaks, pinned)",
		Long: "Runs gitleaks " + cigate.Gitleaks.Version + " — the release forge pins, and the one the scaffolded\n" +
			"pre-commit hook runs — over the git history of the project, and fails on any secret it\n" +
			"finds. A gitleaks on PATH at another version is not used: the pin is installed into\n" +
			"forge's tools cache (one time) and run from there.\n\n" +
			"With no flag every commit is scanned. --branch scans the commits this checkout adds\n" +
			"over the branch it merges into (since the merge-base with origin/<default branch>,\n" +
			"or CI_BASE; on the default branch, the last commit) — what `forge ci gate` runs, so a\n" +
			"gate judges the change and never a credential committed long before it. A base with no\n" +
			"merge-base with HEAD (a shallow clone) is an error, never a narrower scan. --since <rev>\n" +
			"scans the commits after <rev> (`<rev>..HEAD`). --staged scans what `git commit` is\n" +
			"about to record, for a pre-commit hook.\n\n" +
			"Findings are printed redacted. A finding that is not a secret (a test fixture, a\n" +
			"public key) is silenced the gitleaks way: its fingerprint in .gitleaksignore, a\n" +
			"`gitleaks:allow` comment on the line, or rules in .gitleaks.toml at the project root.\n" +
			"A secret that was really committed must be revoked: deleting it in a later commit\n" +
			"leaves it in the history.\n\n" +
			"`forge ci gate` (task ci, and the scaffolded CI's checks job) runs `--branch` as its\n" +
			"secret-scan row.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if n := countTrue(staged, since != "", branch); n > 1 {
				return cliutil.UserErr("forge ci secret-scan", "--staged, --branch and --since each select different commits", "",
					"pass one: --staged for the commit being made, --branch for this branch's commits, --since <rev> for the commits after <rev>")
			}
			root, err := cmdutil.ProjectRoot()
			if err != nil {
				return err
			}
			if branch {
				if !isGitWorkTree(root) {
					return notAGitCheckout(root)
				}
				rangeSpec, why, err := cigate.BranchRange(cmd.Context(), root, os.Getenv(cigate.BaseEnv))
				if err != nil {
					return cliutil.UserErr("forge ci secret-scan --branch", err.Error(), root,
						"fetch the full history and the base branch, set "+cigate.BaseEnv+" to the ref this change merges into, or scan every commit with `forge ci secret-scan`")
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "secret-scan: scanning %s\n", why)
				return runSecretScan(cmd, root, rangeSpec, false)
			}
			if since != "" {
				since += "..HEAD"
			}
			return runSecretScan(cmd, root, since, staged)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "scan only the commits after this revision (`<rev>..HEAD`)")
	cmd.Flags().BoolVar(&staged, "staged", false, "scan the staged changes (for a pre-commit hook)")
	cmd.Flags().BoolVar(&branch, "branch", false, "scan only the commits this checkout adds over the branch it merges into (origin/<default>, or CI_BASE)")
	return cmd
}

// secretScanArgv is the gitleaks command line for one scan of root: the
// staged changes, the commits of revRange (`<a>..HEAD`), or, with neither,
// every commit.
func secretScanArgv(bin, root, revRange string, staged bool) []string {
	verb := "detect"
	if staged {
		verb = "protect"
	}
	argv := []string{bin, verb, "--source", root, "--redact", "--no-banner", "--verbose",
		"--exit-code", fmt.Sprint(gitleaksLeakExit)}
	if staged {
		argv = append(argv, "--staged")
	}
	if revRange != "" {
		argv = append(argv, "--log-opts", revRange)
	}
	return argv
}

func countTrue(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

func notAGitCheckout(root string) error {
	return cliutil.UserErr("forge ci secret-scan", "this project is not a git checkout, so it has no commits to scan", root,
		"run `git init` and commit, or run the scan from a clone")
}

func runSecretScan(cmd *cobra.Command, root, revRange string, staged bool) error {
	if !isGitWorkTree(root) {
		return notAGitCheckout(root)
	}
	dir, err := cigate.NewResolver(root, cmd.ErrOrStderr()).Ensure(cmd.Context(), cigate.Gitleaks)
	if err != nil {
		return cliutil.UserErr("forge ci secret-scan",
			fmt.Sprintf("cannot provide gitleaks %s: %v", cigate.Gitleaks.Version, err), "",
			"install gitleaks "+cigate.Gitleaks.Version+" on PATH (https://github.com/gitleaks/gitleaks/releases), or make `go install` work here: forge builds the pin with it")
	}
	bin := "gitleaks"
	if dir != "" {
		bin = filepath.Join(dir, "gitleaks")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
	}
	argv := secretScanArgv(bin, root, revRange, staged)
	c := exec.CommandContext(cmd.Context(), argv[0], argv[1:]...)
	c.Dir = root
	c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	err = c.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "✅ No secrets found (gitleaks %s).\n", cigate.Gitleaks.Version)
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == gitleaksLeakExit:
		return cliutil.UserErr("forge ci secret-scan", "gitleaks found committed secrets (listed above, redacted)", root,
			"revoke each real secret and move it to a secret store (`forge secret`); deleting it in a new commit leaves it in the history. "+
				"A false positive: add its Fingerprint to .gitleaksignore, or a `gitleaks:allow` comment on the line")
	default:
		return fmt.Errorf("gitleaks %s did not complete: %w", cigate.Gitleaks.Version, err)
	}
}

// isGitWorkTree reports whether dir is inside a git work tree.
func isGitWorkTree(dir string) bool {
	c := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	c.Dir = dir
	out, err := c.Output()
	return err == nil && string(out) == "true\n"
}
