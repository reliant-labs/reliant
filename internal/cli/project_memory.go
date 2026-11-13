package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cliutil"
)

// newProjectMemoryCmd prints the framework guide a harness injects into an
// agent session for this project: what RenderProjectMemory returns, and what
// `forge project new` writes for harnesses that read a file.
//
// It exists for a host that embeds a DIFFERENT forge than the project pins.
// The guide is a function of the forge rendering it — its commands, rules and
// layout — so reliant asks the project's pinned forge for it (with the
// hand-off, `reliant forge project memory` in a pinned project runs that
// forge) instead of rendering the one compiled into reliant.
func newProjectMemoryCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Print the framework guide an agent harness injects for this project",
		Long: `Print the framework guide an agent harness injects into every session for
this project (the quick reference Reliant prepends to the project's memory).

--json prints {"forge_version": "...", "memory": "..."}: the guide and the
forge that rendered it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, _ := findProjectRoot()
			if root == "" {
				return cliutil.UserErr(Name()+" project memory", "not inside a forge project", "",
					"run it in a directory with forge.yaml, or name one with -C <dir>")
			}
			body, err := RenderProjectMemory(root)
			if err != nil {
				return fmt.Errorf("render the project memory: %w", err)
			}
			if !jsonOut {
				_, err = cmd.OutOrStdout().Write(body)
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(struct {
				ForgeVersion string `json:"forge_version"`
				Memory       string `json:"memory"`
			}{buildinfo.Version(), string(body)})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the guide and the forge version that rendered it as JSON")
	return cmd
}
