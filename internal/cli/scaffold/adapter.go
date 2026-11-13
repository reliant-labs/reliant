// File: internal/cli/scaffold/adapter.go
//
// `forge scaffold adapter <name>` is the first-class verb for scaffolding an
// outbound boundary translator — the hexagonal-architecture "adapter"
// shape (HTTP client, queue producer, storage gateway). It is a thin
// wrapper around the existing `forge scaffold package --type adapter` flow:
// same template tree, same forge.yaml mutation, same lint marker.
//
// Why a dedicated verb when `--type adapter` already works?
//
//   - Discoverability. Workers / binaries / services have first-class
//     verbs; adapters were only reachable via a flag on `scaffold package`,
//     which means every cp-forge / kalshi-trader migration round
//     rediscovers the long form by reading source or grepping help.
//   - Symmetry. The shape map is "forge scaffold <thing> <name>" everywhere
//     else; routing adapters through a flag broke the pattern for the
//     one component class users add most often after services.
//
// `--type adapter` on `forge scaffold package` stays wired (package.go owns
// it) so existing scripts and skill docs keep working. This file just
// adds the shorter path.

package scaffold

import (
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
)

// newAdapterCmd is the cobra surface for `forge scaffold adapter <name>`.
//
// It delegates straight to runPackageNew with --type=adapter pre-set,
// so the scaffold tree, forge.yaml mutation, and validation all flow
// through the single code path in package.go. That keeps `--type
// adapter` and `scaffold adapter` semantically identical — they have to
// agree, since both shipping under different names with subtly
// different behavior would be a worse failure mode than the original
// discoverability gap.
func newAdapterCmd(f *factory.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adapter <name>",
		Short: "Scaffold an outbound adapter (HTTP client, queue producer, storage gateway)",
		Long: `Scaffold an outbound adapter into an existing Forge project.

An adapter is an outbound boundary translator — it owns the implementation
of a third-party API, queue, storage gateway, or similar downstream
behind a narrow Go interface. Callers depend on
the adapter's Service interface, never on the concrete type, so business
logic stays free of vendor specifics and the adapter stays unit-testable
with a mocked downstream.

This scaffolds internal/<name>/ with:
  contract.go        // forge:outbound-io Service interface
  adapter.go         Deps + service struct + New(Deps) (Service, error)
  adapter_test.go    tests (httptest-stub round trips with --base-url)
  cache.go           empty stub — fill in TTL + per-host rate budget,
                     or delete the file if the adapter is fully local

No configuration is assumed: an adapter's downstream may be a queue, a
vendor SDK or a storage gateway rather than one HTTP API, so the scaffold
takes a logger and the traced HTTP client, and its HealthCheck fails until you
write a probe. --base-url scaffolds the HTTP-API shape instead: a config
block config.<Name>Config with one base_url leaf, bound to <NAME>_BASE_URL (or
to the env var --base-url-env=STRIPE_API_URL names), and a HealthCheck against
<base_url>/health with httptest-stub tests.

It ends with a 'forge generate' run, which compiles the config block (when
there is one) and wires the adapter into internal/app/compose.go, so the next
'forge lint' is green. --no-generate skips it; --dry-run prints the files and
config edits it would make and writes nothing.

This is equivalent to 'forge scaffold package --type adapter <name>'; that
form stays wired for existing scripts. Use whichever is shorter for you.

Skill: forge skill load adapter

Example:
  forge scaffold adapter stripe --base-url
  forge scaffold adapter stripe --base-url-env STRIPE_API_URL
  forge scaffold adapter pubsubpull
  forge scaffold adapter pricing_feed --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: packageNewThenGenerate(f),
	}

	// runPackageNew reads --type and --kind off the command. The adapter
	// verb owns the --type default, so we register the flag with
	// "adapter" baked in. --kind stays declared (and empty) so the
	// runPackageNew mutual-exclusion guard runs: passing --kind alongside
	// --type=adapter is still rejected with the same message the package
	// form produces, keeping the two paths semantically aligned.
	cmd.Flags().String("type", "adapter", "package shape (locked to 'adapter' for this verb)")
	cmd.Flags().String("kind", "", "package kind template (not valid with --type=adapter; flag registered for shared runPackageNew flow)")
	// The --type flag is locked to "adapter" — exposing it as user-
	// configurable on this verb would defeat the point of having a
	// dedicated subcommand. Hide it so `--help` shows a clean surface.
	_ = cmd.Flags().MarkHidden("type")
	_ = cmd.Flags().MarkHidden("kind")
	cmd.Flags().Bool("no-generate", false, noGenerateFlagUsage)
	cmdutil.AddPackageScaffoldFlags(cmd)

	return cmd
}
