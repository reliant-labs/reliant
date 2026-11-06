// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// The `<bin> tasks` subcommand — a REAL first-class cobra command with its
// own -h / Short / Long, in the services/ command group.
//
// This is the file to edit when running ONE service needs to differ from
// running the others: add a flag, read an extra env var, wrap RunE, change the
// Long text. It is ordinary cobra code and forge will not revert it.
//
// Its RunE calls cmd.Serve() with the TYPED mount method expression from
// tasks_mount_gen.go — selection is compile-time typed, never a string.
// That one expression is the only derived thing here, which is why it lives
// next door: adding or renaming a service must not rewrite this file.
//
// Sibling services are still CONSTRUCTED (the DI graph builds everything;
// cross-service in-process reads stay nil-safe); only their routes are not
// registered, and this single-service process runs no workers/operators.
//
// main.go names this command's exported New…Cmd constructor explicitly and
// passes it to cmd.Execute — the tree is assembled in the composition root,
// not via init() self-registration.
package services

import (
	"github.com/spf13/cobra"

	"example.com/app/cmd/app/cmd"
	"example.com/app/pkg/config"
)

// NewTasksCmd is the REAL cobra subcommand for the tasks service.
func NewTasksCmd(deps cmd.Deps) *cobra.Command {
	c := &cobra.Command{
		Use:   "tasks",
		Short: "Run only the tasks service",
		Long:  "Run only the tasks service: the canonical server pipeline (config, OTel, listener, graceful shutdown) with this service mounted via its TYPED mount method (no string selection). Sibling services are still constructed (cross-service reads stay nil-safe) but their routes are not registered, and this process runs no workers/operators.",
		RunE: func(c *cobra.Command, args []string) error {
			deps.Cmd = c
			// TYPED mount: the method expression, named in tasks_mount_gen.go.
			// No workers/operators — a single service is an HTTP-only process.
			return cmd.Serve(c.Context(), deps, cmd.ServeSpec{Mount: mountTasks})
		},
	}
	config.RegisterFlags(c)
	return c
}
