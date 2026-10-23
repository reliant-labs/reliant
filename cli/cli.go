// Package cli provides the public entry point for embedding Forge's CLI
// as a subcommand in other cobra-based CLIs.
package cli

import (
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
