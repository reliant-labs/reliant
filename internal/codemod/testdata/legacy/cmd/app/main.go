// yours: scaffolded once, never touched again — forge will not overwrite this file
// forge writes it ONCE (write-if-absent). The command tree + component wiring is
// owned code — add a component by hand-editing here (or via `forge scaffold`), never a
// re-derivation. Deleting it sticks; `forge project rescaffold <path>` re-creates it from template.
// Source: internal/templates/project/cmd-main.go.tmpl.
//
// The COMPOSITION ROOT (devspace idiom): all command logic lives under
// cmd/app/cmd (package cmd — root.go assembles the tree) and its
// command-group SUBPACKAGES (services/, workers/, operators/). This main names
// EVERY group constructor explicitly and passes them to cmd.Execute, which
// builds the root command tree. The tree is therefore READABLE HERE — no init()
// self-registration and no dynamic registry. Because main.go (not package cmd)
// imports the group subpackages, and the groups import cmd only for the shared
// Deps/Serve helpers, there is no cmd↔group import cycle.
//
// The command tree is IDENTICAL in both binary modes; binary=shared changes only
// the DEPLOY story. This binary is a monolith by default: the image's CMD is
// `app server`, which mounts every service and runs every worker in one
// process. Splitting a service into its own Deployment is a manifest change, not
// a rewrite — set that component's `command` in deploy/kcl/<env>/main.k (which is
// what the binary=shared scaffold pre-wires: `command = ["/app/<project>", c.name]`).
//
// To run a specific service: `app <service>`. To list services:
// `app --help`.
package main

import (
	"example.com/app/cmd/app/cmd"
	"example.com/app/cmd/app/cmd/services"
	"example.com/app/cmd/app/cmd/workers"
)

func main() {
	cmd.Execute(
		// services
		services.NewTasksCmd,
		workers.NewTickerCmd,
	)
}
