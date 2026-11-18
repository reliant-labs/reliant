// yours: scaffolded once, never touched again — forge will not overwrite this file
// forge writes it ONCE (write-if-absent). Deleting it sticks;
// `forge project rescaffold <path>` re-creates it from template.
// Source: internal/templates/project/cmd-worker-register.go.tmpl.
//
// Each worker's subcommand lives in its own <name>.go in this package as an
// exported New<Worker>Cmd constructor; main.go names them explicitly and passes
// them to cmd.Execute (the tree is assembled in the composition root, not via
// init() self-registration). This file is the group ANCHOR: it guarantees the
// package compiles even with zero workers.
//
// It is scaffold-once rather than regenerated because it projects NOTHING:
// `forge generate` does no worker discovery, so this file's content is the
// same in every project and there is no fact for a re-render to update. A
// file forge rewrites but never changes only creates the risk that an edit
// puts it in permanent drift. Once the anchor exists, it is yours — as is
// every other file in this directory.
package workers
