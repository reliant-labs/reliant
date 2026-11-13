// `forge test` was removed. This file is the signpost that replaced it.
//
// The command is hidden (absent from `forge --help`) and runs NO tests — it
// exists only so the old spelling produces the replacement instead of cobra's
// bare `unknown command "test"` plus a usage dump. Every forge-owned caller
// (the generated CI workflow, the reliant one-shot gate, the shipped skills)
// was moved to `task test` in the same change, so nothing reaches this path on
// the golden loop; it is here for muscle memory and for out-of-tree scripts.
//
// Why the removal: a project's test suite is defined in its own Taskfile.yml,
// which is Tier-2 (scaffolded once, user-owned, never regenerated). Any project
// that customised it — extra tags, gotestsum, testcontainers, coverage
// thresholds — got a `forge test` that reported on a DIFFERENT suite than the
// one the project actually runs. That is worse than having no command: it is a
// green exit code for work that was never done. `forge lint` is not in the same
// position and stays: it orchestrates analyzers (contract, scaffold-boundary,
// optional-deps-guard) that exist only inside forge and have no standalone
// binary a Taskfile line could call.
//
// Delete this file once the spelling has stopped showing up in the wild.

package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cliutil"
)

// testReplacement maps the removed subcommand (the first positional, if any)
// to the Task target that replaces it. Keys are the old `forge test <x>` verbs;
// "" is bare `forge test`.
var testReplacement = map[string]string{
	"":            "task test",
	"unit":        "task test",
	"integration": "task test:integration",
	"e2e":         "task test:e2e",
	"migrate-tdd": "forge project migrate tdd",
}

// newTestRemovedCmd returns the hidden `forge test` stub. It always fails.
func newTestRemovedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "test",
		Short:  "Removed — the test suite is defined in your Taskfile.yml (`task test`)",
		Hidden: true,
		// ArbitraryArgs + UnknownFlags: the point of this command is to be
		// REACHED, so it must not fail earlier and differently on the shapes
		// people actually typed. `forge test --coverage` and `forge test unit
		// ./internal/foo/...` would otherwise die on flag parsing with a
		// message that says nothing about the replacement.
		Args:               cobra.ArbitraryArgs,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			sub := ""
			if len(args) > 0 {
				sub = args[0]
			}
			replacement, known := testReplacement[sub]
			if !known {
				// An unrecognised positional is a package pattern in the
				// `forge test ./internal/foo/...` shape, or a typo. Either way
				// the scoped form is the useful answer.
				replacement = "task test -- " + strings.Join(args, " ")
			}
			return cliutil.UserErr("forge test",
				"`forge test` was removed — a project's test suite is defined in its own Taskfile.yml, "+
					"so forge no longer ships a second spelling of it that could disagree",
				"Taskfile.yml",
				"run `"+replacement+"` instead.\n"+
					"  task test:short -- ./internal/foo/...  the inner loop: -short, cached, no race\n"+
					"  task test                              unit + every frontend's tests\n"+
					"  task test -- ./internal/foo/...        only those Go packages (skips the frontend lane)\n"+
					"  task test -- -v ./...                  pass any `go test` flags\n"+
					"  task test:integration                  the `integration`-tagged lane\n"+
					"  task test:e2e                          the end-to-end lane\n"+
					"  task test:all                          unit + frontend + integration\n"+
					"  task coverage                          coverage.out + coverage.html\n"+
					"  forge project migrate tdd              the handler-test codemod (was `forge test migrate-tdd`)")
		},
	}
	return cmd
}
