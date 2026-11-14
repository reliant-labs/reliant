package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/templates"
)

// newProjectFrontendsCmd is `forge project frontends`: the directories in
// this repository that hold a frontend, one per line.
//
// It is the inventory `forge ci gate` and the scaffolded CI workflows drive a
// Node toolchain for (ciFrontends): every in-repo frontend the KCL of any env
// declares, wherever its path points (`web/`, `apps/console`), plus the
// frontends/<name> directories on disk. A Taskfile loops over it instead of
// globbing `frontends/*`, which missed every frontend declared elsewhere — an
// in-place adopter's `web/` was never installed, built or tested.
func newProjectFrontendsCmd() *cobra.Command {
	var asJSON, withManager bool
	cmd := &cobra.Command{
		Use:   "frontends",
		Short: "Print the directories of this project's in-repo frontends, one per line (what task and the CI gate loop over)",
		Long: `Print the project-relative directory of every frontend in this repository,
one per line, sorted by name.

The list is the one ` + "`forge ci gate`" + ` and the scaffolded workflows use: each
frontend a forge.Frontend declaration in deploy/kcl/<env>/main.k names (any
env, at any in-repo path), plus the frontends/<name> directories on disk. A
frontend outside the repository (a sibling checkout, a cross-repo source pin)
is not listed: nothing here can install or build it.

Scripts loop over it rather than globbing frontends/*:

  for dir in $(forge project frontends); do (cd "$dir" && npm test); done

--json prints [{"name","path"}] instead.
--with-package-manager prints manager:path rows using forge's install/CI
resolver, including the declared manager's own lockfile when several exist.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := projectRoot()
			if err != nil {
				return err
			}
			store, err := loadProjectStore()
			if err != nil {
				return err
			}
			// Discovery renders every env's KCL, and a render reports what it
			// noticed on stdout. stdout here is the list a script splits on
			// whitespace, so anything else goes to stderr.
			var fes []templates.FrontendCIConfig
			_ = withStdout(cmd.ErrOrStderr(), func() error {
				fes = ciFrontends(root, store.Config())
				return nil
			})
			out := cmd.OutOrStdout()
			if asJSON {
				type row struct {
					Name string `json:"name"`
					Path string `json:"path"`
				}
				rows := make([]row, 0, len(fes))
				for _, fe := range fes {
					rows = append(rows, row{Name: fe.Name, Path: fe.Path})
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			for _, fe := range fes {
				line := fe.Path
				if withManager {
					line = fe.PM() + ":" + fe.Path
				}
				if _, err := fmt.Fprintln(out, line); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print [{name, path}] as JSON")
	cmd.Flags().BoolVar(&withManager, "with-package-manager", false, "Print manager:path rows using the same resolver as installs and CI")
	cmd.MarkFlagsMutuallyExclusive("json", "with-package-manager")
	return cmd
}
