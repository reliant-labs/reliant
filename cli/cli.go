// Package cli provides the public entry point for embedding Forge's CLI
// as a subcommand in other cobra-based CLIs.
package cli

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	internalcli "github.com/reliant-labs/forge/internal/cli"
	"github.com/reliant-labs/forge/internal/exitcode"
)

// NewRootCmd returns the fully assembled Forge root command.
// When embedded in another CLI (e.g. "reliant forge"), the internal
// Name() helper automatically adjusts user-facing command references.
func NewRootCmd() *cobra.Command {
	return internalcli.NewRootCmd()
}

// HandOffToPinned runs args — forge's own arguments, after the host's route
// (for `reliant forge generate -C x`, ["generate", "-C", "x"]) — under the
// forge the project in scope pins, when that is not this forge. A host that
// embeds forge calls it before executing its command tree, so a command the
// pinned forge has and this one lacks still reaches it.
//
// It returns handed=false, err=nil when this forge is the one to run (no
// project, no pin — said on stderr when forge is embedded — the pin is this
// forge, or the user overrode the check), and an error, with nothing run, when
// the pinned forge is wanted and cannot be obtained. On success it replaces
// the process and does not return.
func HandOffToPinned(ctx context.Context, args []string, stderr io.Writer) (handed bool, err error) {
	return internalcli.HandOffToPinned(ctx, args, stderr)
}

// PinnedForge is the forge a project pins, resolved and ready to run: its
// executable, a Key naming exactly which forge it is (for caching what it
// prints), and Want, which forge the project asked for.
type PinnedForge = internalcli.PinnedForge

// ResolvePinnedForge makes HandOffToPinned's decision for the forge command
// command ("skill load", "skill list") in projectRoot, and resolves the forge
// without running it, for a host that runs the project's forge itself. ok is
// false when this forge is the one to run command there; err, with the reason,
// when the project's forge is wanted and cannot be obtained — a host must then
// not substitute this forge's answer. log receives the one-time build notice.
func ResolvePinnedForge(ctx context.Context, projectRoot, command string, log io.Writer) (PinnedForge, bool, error) {
	return internalcli.ResolvePinnedForge(ctx, projectRoot, command, log)
}

// ResolvePinnedForgePath is `forge version --pinned` for a host: the forge the
// project at or above dir ("" for the working directory) pins, ready to run,
// whether or not it is this one. Unlike ResolvePinnedForge, which answers only
// when the command must be handed elsewhere, it always names an executable:
// this forge's own (os.Executable(), Source "self", and Args ["forge"] when
// forge is embedded in it) when this forge already is the pin — and that
// executable, run as `Path Args... version`, reports the pin (when it does
// not, this forge is treated as not the pin); otherwise the
// pinned forge, obtained exactly as HandOffToPinned obtains it — the linked
// checkout's build, the project's .tools/bin/forge, the cache, or a build —
// and checked to report the pinned version. It errors, running nothing, when
// there is no project, no forge_version, or the pinned forge cannot be had.
// The hand-off's overrides (FORGE_ALLOW_VERSION_SKEW) do not change the
// answer: they say which forge to run, not which one is pinned. log receives
// the one-time build notice.
func ResolvePinnedForgePath(ctx context.Context, dir string, log io.Writer) (PinnedForge, error) {
	return internalcli.ResolvePinnedForgePath(ctx, dir, log)
}

// PinnedSourceSelf is PinnedForge.Source when this forge is the project's.
const PinnedSourceSelf = internalcli.PinnedSourceSelf

// SetVersion sets the version, build date, and git commit for the Forge CLI.
func SetVersion(version, buildDate, gitCommit string) {
	internalcli.SetVersion(version, buildDate, gitCommit)
}

// ExitCode is the process status err should produce: the code the error
// carries (forge's agent verbs print the same number as their document's
// exit_code), else 1. A host that embeds forge's command tree must exit with
// it instead of a flat 1.
func ExitCode(err error) int {
	return exitcode.Of(err)
}

// ErrorLine is the line a host that embeds forge's command tree prints on
// stderr for err: "Error: <err>", or the message alone when err is a verdict
// the exit code carries rather than a failure (`project upgrade` exiting 9:
// everything mechanical applied, the rest needs a person).
func ErrorLine(err error) string {
	return exitcode.Line(err)
}
