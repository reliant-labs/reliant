// yours: scaffolded once, never touched again — forge will not overwrite this file
// forge writes it ONCE (write-if-absent). The command tree + component wiring is
// owned code — add a component by hand-editing here (or via `forge scaffold`), never a
// re-derivation. Deleting it sticks; `forge project rescaffold <path>` re-creates it from template.
// Source: worker ticker (one file per worker — devspace idiom).
//
// REAL per-worker subcommand, in the workers/ command group. `<bin> ticker`
// runs a supervisor process for ONLY the ticker worker: no Connect services
// are mounted (cmd.MountNone), no other workers/operators run. The command
// composes ITSELF — its Workers closure names the TYPED app.WorkerTicker()
// accessor DIRECTLY; there is no name filter and no re-selection. Sibling
// components are still CONSTRUCTED by the DI graph (cross-component reads stay
// nil-safe); only this worker is supervised.
//
// main.go names this command's exported New…Cmd constructor explicitly and
// passes it to cmd.Execute — the tree is assembled in the composition root,
// not via init() self-registration.
package workers

import (
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/serverkit"

	"example.com/app/cmd/app/cmd"
	"example.com/app/internal/app"
	"example.com/app/pkg/config"
)

// NewTickerCmd is the cobra subcommand that runs only the ticker worker.
func NewTickerCmd(deps cmd.Deps) *cobra.Command {
	c := &cobra.Command{
		Use:   "ticker",
		Short: "Run only the ticker worker",
		Long:  "Run a supervisor process for only the ticker worker. No Connect services are mounted and no other workers/operators run; the DI graph still constructs everything so cross-component reads stay nil-safe.",
		RunE: func(c *cobra.Command, args []string) error {
			deps.Cmd = c
			return cmd.Serve(c.Context(), deps, cmd.ServeSpec{
				Mount:   cmd.MountNone,
				Workers: func(c *app.Components) []serverkit.Worker { return []serverkit.Worker{c.WorkerTicker()} },
			})
		},
	}
	config.RegisterFlags(c)
	return c
}
