// File: internal/cli/kcl_imports.go
//
// `forge kcl imports` — print where each forge project this project's KCL
// imports ([forge_projects] in deploy/kcl/kcl.mod) resolves to.
//
// A render reads an imported project's KCL from wherever go.mod resolves its
// module: a replace directory, a go.work use, or the module cache at the
// required version (internal/kclprojects). That location is a fact a person
// debugging a render needs ("which reliant did this import?"), and one a Go
// test that renders through a forge child process needs too: the child's
// reads are invisible to Go's test cache, so a cache-sound render test stats
// the imported files itself, and this is how it learns where they are.
package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/kclprojects"
)

func newKCLImportsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "imports",
		Short: "Show where each forge project this project's KCL imports resolves to",
		Args:  cobra.NoArgs,
		Long: `List the forge projects deploy/kcl/kcl.mod imports under [forge_projects],
and where each one resolves.

An imported project's KCL is read at the version this project's go.mod pins
its Go module to — a go.work use or a directory replace reads that directory
(a sibling checkout), otherwise the module cache at the required version,
verified against go.sum. There is no separate KCL pin. Resolution refuses an
imported project that pins a different forge than this one (version skew).

--json prints the resolution as a JSON array: name, module, version (empty
for a directory), via (the declaration that decided it), module_dir, dir (the
KCL package root) and forge_pin.

Examples:
  ` + Name() + ` kcl imports
  ` + Name() + ` kcl imports --json | jq -r '.[].dir'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectDir, err := evalProjectRoot()
			if err != nil {
				return err
			}
			rs, err := kclprojects.Resolve(kclprojects.FindKCLRoot(filepath.Join(projectDir, "deploy", "kcl")))
			if err != nil {
				cmd.SilenceUsage = true
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if rs == nil {
					rs = []kclprojects.Resolved{}
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rs)
			}
			if len(rs) == 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "deploy/kcl/kcl.mod imports no forge projects (no [%s] table)\n", kclprojects.TableName)
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tMODULE\tVERSION\tVIA\tDIR")
			for _, r := range rs {
				version := r.Version
				if version == "" {
					version = "(directory)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Module, version, r.Via, r.Dir)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the resolution as JSON")
	return cmd
}
