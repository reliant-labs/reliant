package fix

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/contractsrc"
	"github.com/reliant-labs/forge/internal/derivecontract"
	"github.com/reliant-labs/forge/internal/generator/contract"
)

func newDeriveContractCmd(f *factory.Factory) *cobra.Command {
	var dryRun, allowDirty bool
	cmd := &cobra.Command{
		Use:   "derive-contract <package-dir>",
		Short: "Replace a package's hand-written Service interface with //forge:contract on its one implementation",
		Long: `Migrate one package from a hand-written contract to a derived one.

The package states its API once instead of twice: the hand-written
` + "`type Service interface`" + ` in contract.go is deleted, its single implementation
gains ` + "`//forge:contract`" + `, and ` + "`forge generate`" + ` derives Service into contract_gen.go
from that type's exported methods (mock_gen.go and middleware_gen.go follow).
Opt-in, per package: an interface with several implementations IS the
abstraction, and keeps its contract.go.

The contract does not change. Service's method docs move onto the methods
that have none (a forge:no-observe moves either way), and its own doc onto
the type. What else contract.go declared stays: the dependency interfaces
move to deps.go (the ` + "`// forge:deps`" + ` file forge mocks beside the derived
Service) and contract.go is removed when nothing else is left in it;
otherwise contract.go stays with the Deps struct, constants and functions it
holds. Package comments and directives on contract.go move to doc.go, or the
implementation's file.

The implementation is the type checker's: the one type whose method set, or
whose pointer's, implements Service. Refused, with every reason and nothing
written, when the package does not type-check (or build constraints leave a
source file out of the build), or when deriving would change the contract:
no implementation, or several; a Service that embeds an interface or is
generic; a contract interface not named Service; an implementation that is
generic, gets a Service method only through an embedded field, or exports a
method Service does not have; a package that already derives its contract,
has no contract.go, or opts out with //forge:exclude-contract. Also refused:
a package directory, or a file it would write, that resolves outside the
project through a symlink. Before writing, it type-checks the rewritten
package with the contract_gen.go forge would derive, and compares that
Service with the one it replaces, method by method. The files are written
as one transaction: if a write fails, every file is restored.`,
		Example: "  forge fix derive-contract internal/billing --dry-run\n  forge fix derive-contract internal/billing",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := cmdutil.ProjectRoot()
			if err != nil {
				return err
			}
			dir := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(args[0], "./")))
			scope, err := codemod.ParseScope(root, []string{dir})
			if err != nil {
				return cliutil.UserErr("forge fix derive-contract", err.Error(), "",
					"name the package directory, relative to the project root, e.g. internal/billing")
			}
			if !dryRun && !allowDirty {
				if dirty := uncommitted(root, scope); dirty != "" {
					return cliutil.UserErr("forge fix derive-contract",
						dir+" has uncommitted changes:\n"+dirty, "",
						"commit or stash them first: this rewrites files you own, and `git diff` is how you review it (or pass --allow-dirty)")
				}
			}
			m, err := derivecontract.Plan(root, dir)
			var refused *derivecontract.RefusedError
			if errors.As(err, &refused) {
				fmt.Fprintf(f.Out, "  derive-contract: nothing written — %s keeps its hand-written contract:\n", refused.Dir)
				for _, r := range refused.Reasons {
					fmt.Fprintf(f.Out, "  ✗ %s\n", r)
				}
				return cliutil.UserErr("forge fix derive-contract",
					fmt.Sprintf("%d reason(s) the derived contract would not be the Service it replaces", len(refused.Reasons)), "",
					"clear each item listed above and re-run, or keep the hand-written contract.go (both forms are supported)")
			}
			if err != nil {
				return err
			}
			printDeriveContract(f.Out, m, dryRun)
			if dryRun {
				return nil
			}
			if err := derivecontract.Apply(m); err != nil {
				return err
			}
			return writeDerived(f.Out, root, m)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List what would be rewritten; change nothing")
	cmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "Run over uncommitted changes in the package")
	return cmd
}

func printDeriveContract(out io.Writer, m *derivecontract.Migration, dryRun bool) {
	mark := "✅"
	if dryRun {
		mark = "would rewrite:"
	}
	for _, r := range m.Rewrites {
		fmt.Fprintf(out, "  %s %s\n", mark, r)
	}
	fmt.Fprintf(out, "  Service: %s\n", strings.Join(m.Methods, ", "))
}

// writeDerived writes contract_gen.go right away, so the package builds
// before the next full generate; mock_gen.go and middleware_gen.go are
// generate's to refresh.
func writeDerived(out io.Writer, root string, m *derivecontract.Migration) error {
	dir := filepath.Join(root, filepath.FromSlash(m.Dir))
	next := "run `forge generate` (mock_gen.go and middleware_gen.go follow contract_gen.go), then `go build ./... && forge lint`"
	cs, err := checksums.Load(root)
	if err != nil {
		return fmt.Errorf("the package is rewritten, but contract_gen.go was not written (%w); %s", err, next)
	}
	if _, err := contract.SyncDerivedContract(dir, contract.Options{ProjectRoot: root, Checksums: cs}); err != nil {
		return fmt.Errorf("the package is rewritten, but contract_gen.go was not written: %w; fix that, then %s", err, next)
	}
	if err := checksums.Save(root, cs); err != nil {
		return err
	}
	fmt.Fprintf(out, "  ✅ %s/%s: derived from %s\n", m.Dir, contractsrc.DerivedFileName, m.Type)
	fmt.Fprintf(out, "\n  Next: %s\n", next)
	return nil
}
