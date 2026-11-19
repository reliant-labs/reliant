package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/internal/toolpin"
)

// toolPins are the tool versions this forge pins, keyed by binary name and
// spelled exactly as the templates constants are — the strings the scaffolded
// workflows render. It is what `forge tools pins` prints, so a CI workflow
// asks the forge it runs instead of grepping forge's source out of the module
// cache.
func toolPins() map[string]string {
	root, _ := projectRoot()
	return map[string]string{
		"golangci-lint": templates.GolangciLintVersion,
		// The project's buf when its go.mod declares one (internal/toolpin).
		"buf":         toolpin.BufFor(root).Bare(),
		"govulncheck": templates.GovulncheckVersion,
		"go-licenses": templates.GoLicensesVersion,
		"prettier":    templates.PrettierVersion,
		"kcl":         templates.KCLVersion,
	}
}

func newToolsPinsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "pins [tool]",
		Short: "Print the tool versions this forge pins (golangci-lint, buf, …), for CI to read",
		Long: `Print the tool versions this forge pins: the golangci-lint and buf that
'forge lint' gates with, and the other tools forge installs or renders into
scaffolded CI.

With a tool name, print only its version — the form a CI step reads:

  GOLANGCI_LINT_VERSION="$(forge tools pins golangci-lint)"

The versions are this forge binary's, so a workflow that runs the forge its
go.mod pins reads the matching tool pins with no second place to bump. The one
the project can move is buf: inside a project whose go.mod requires
github.com/bufbuild/buf (` + "`go get -tool github.com/bufbuild/buf/cmd/buf@vX`" + `),
buf is that version, the one ` + "`forge generate`" + ` and ` + "`forge ci gate`" + ` run.

Examples:
  forge tools pins
  forge tools pins golangci-lint
  forge tools pins --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pins := toolPins()
			if len(args) == 1 {
				v, ok := pins[args[0]]
				if !ok {
					return cliutil.UserErr("forge tools pins", fmt.Sprintf("no pinned tool named %q", args[0]), "",
						"one of: "+strings.Join(sortedKeys(pins), ", "))
				}
				if asJSON {
					return json.NewEncoder(os.Stdout).Encode(map[string]string{args[0]: v})
				}
				fmt.Println(v)
				return nil
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(pins)
			}
			for _, name := range sortedKeys(pins) {
				fmt.Printf("%-14s %s\n", name, pins[name])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print a JSON object of tool → version")
	return cmd
}
