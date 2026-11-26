package cli

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// `--help-dev` prints a command's hidden flags; it is a flag, not cobra's help
// command, so the root pre-run hook runs for it. It must not configure the
// clone (git hooks, the generated-file merge driver) — forge bug report
// 44e774fb saw `forge generate --help-dev` rewrite a shared git config.
func TestHookActivationSkipped_HelpDev(t *testing.T) {
	cmd := &cobra.Command{Use: "generate", RunE: func(*cobra.Command, []string) error { return nil }}
	cmdutil.HideDevFlags(cmd)
	if hookActivationSkipped(cmd) {
		t.Fatal("hookActivationSkipped = true before --help-dev was passed")
	}
	if err := cmd.Flags().Parse([]string{"--help-dev"}); err != nil {
		t.Fatal(err)
	}
	if !hookActivationSkipped(cmd) {
		t.Fatal("hookActivationSkipped = false for --help-dev; want the help-only run to leave git config alone")
	}
}
