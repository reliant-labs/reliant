package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/gocacheprog"
)

// newCacheCmd builds `forge cache`: this machine's shared Go build cache,
// split into a trusted tier only main's builds write and an overlay per
// untrusted context (internal/gocacheprog; docs/go-cache.md).
//
// It is about this machine, not a project, so it runs as this forge in any
// project (handOffExempt): `cache prog` runs once per go command, and a
// project pinning an older forge that lacks it would fail every go build.
func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Share one Go build cache per machine: a trusted tier main seeds, an overlay per untrusted build",
		Long: "One Go build cache per machine, shared by every agent, worktree, CI runner and gate on it,\n" +
			"without letting an untrusted build poison what main's builds read.\n\n" +
			"The TRUSTED tier (" + gocacheprog.DefaultTrustedDir + ", or $" + gocacheprog.TrustedEnv + ")\n" +
			"is written only by trusted builds: `forge cache seed` on every merge to main, and the gate on\n" +
			"main, running plain go with GOCACHE set to it. Every other build runs go with\n" +
			"GOCACHEPROG=\"forge cache prog\" and GOCACHE set to its own OVERLAY: lookups read the trusted\n" +
			"tier first, then the overlay; writes go only to the overlay. `forge cache env` prints the\n" +
			"settings. Both tiers are plain Go cache directories, trimmed by `forge storage`.\n\n" +
			"Nothing is shared between machines.",
	}
	cmd.AddCommand(newCacheProgCmd(), newCacheSeedCmd(), newCacheEnvCmd())
	return cmdutil.StrictGroup(cmd)
}

func newCacheProgCmd() *cobra.Command {
	var cfg gocacheprog.Config
	cmd := &cobra.Command{
		Use:   "prog [--trusted DIR] [--overlay DIR]",
		Short: "Serve go's GOCACHEPROG protocol: read the trusted tier, then the overlay; write only the overlay",
		Long: "The GOCACHEPROG helper. go starts it once per command and speaks JSON on its stdin and\n" +
			"stdout; it is not meant to be run by hand.\n\n" +
			"--overlay defaults to $GOCACHE (go sets it for the helper). --trusted defaults to $" + gocacheprog.TrustedEnv + ",\n" +
			"else " + gocacheprog.DefaultTrustedDir + " when it exists. A trusted tier that is missing or unreadable\n" +
			"is not an error: the build runs on the overlay alone. An overlay that is the trusted tier is refused.\n\n" +
			"$" + gocacheprog.StatsEnv + "=<file> appends one JSON line of counters per go command at exit.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return gocacheprog.Run(cfg, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&cfg.Trusted, "trusted", "", "the trusted tier, read-only (default $"+gocacheprog.TrustedEnv+", else "+gocacheprog.DefaultTrustedDir+" if it exists)")
	cmd.Flags().StringVar(&cfg.Overlay, "overlay", "", "the tier this build writes (default $GOCACHE)")
	return cmd
}

// isCacheProgInvocation reports whether args (os.Args[1:]) run `cache prog`.
// go starts the helper once per command, so it skips everything else forge
// does at startup: the hand-off to a pinned forge, usage events and the full
// command tree (runCacheProgFast).
func isCacheProgInvocation(args []string) bool {
	return len(args) >= 2 && args[0] == "cache" && args[1] == "prog"
}

// runCacheProgFast runs `cache prog` from its own command alone. A fatal
// startup error is the one thing it prints, on stderr, which go shows.
func runCacheProgFast(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := newCacheProgCmd()
	cmd.SetArgs(args[2:])
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if err := cmd.Execute(); err != nil {
		return fmt.Errorf("forge cache prog: %w", err)
	}
	return nil
}

func newCacheEnvCmd() *cobra.Command {
	var trusted string
	cmd := &cobra.Command{
		Use:   "env [--trusted DIR]",
		Short: "Print the environment an untrusted build needs to read the trusted tier",
		Long: "Prints, as KEY=value lines, what an untrusted context (an agent, a worktree, a CI runner,\n" +
			"a merge-queue gate) sets to read the machine's trusted Go cache and write its own overlay.\n" +
			"GOCACHE stays the context's own cache: it becomes the overlay. The go env -w form is printed\n" +
			"as a comment, for setting it once per user instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			argv, err := forgeExecCommand()
			if err != nil {
				return err
			}
			dir := gocacheprog.TrustedDirForSeed(trusted, os.Getenv)
			fmt.Fprint(cmd.OutOrStdout(), cacheEnvLines(argv, dir))
			return nil
		},
	}
	cmd.Flags().StringVar(&trusted, "trusted", "", "the trusted tier as this context sees it (default $"+gocacheprog.TrustedEnv+", else "+gocacheprog.DefaultTrustedDir+")")
	return cmd
}

// cacheEnvLines is `cache env`'s output for the forge invoked as argv.
func cacheEnvLines(argv []string, trusted string) string {
	prog := strings.Join(append(append([]string{}, argv...), "cache", "prog"), " ")
	var b strings.Builder
	b.WriteString("# GOCACHE stays this context's own cache: it is the overlay, the only tier this build writes.\n")
	fmt.Fprintf(&b, "GOCACHEPROG=%s\n", prog)
	fmt.Fprintf(&b, "%s=%s\n", gocacheprog.TrustedEnv, trusted)
	b.WriteString("# -trimpath keys every checkout's packages alike, so one tier serves them all.\n")
	b.WriteString("GOFLAGS=-trimpath\n")
	b.WriteString("# Or once per user, in go's env file (an exported variable still overrides it):\n")
	fmt.Fprintf(&b, "#   go env -w GOCACHEPROG=%q GOFLAGS=-trimpath\n", prog+" --trusted "+trusted)
	return b.String()
}
