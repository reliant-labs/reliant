// Package fix holds the `forge fix` command group: codemods that rewrite the
// project's own code to what a lint asks for, run on request. Each is
// deliberate — never part of `forge generate` — because it edits files that
// are the user's.
//
//	forge fix narrow-deps <pkg>   retype a component's wide foreign Deps
//	                              interfaces to the calls it makes
//	                              (forgeconv-deps-surface)
//	forge fix <codemod>           one release codemod (internal/codemod):
//	                              the mechanical half of a migration skill
package fix

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/depsurface"
	"github.com/reliant-labs/forge/internal/generator/contract"
)

func init() { factory.Register(newCmd) }

func newCmd(f *factory.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fix",
		Short: "Rewrite your code to what a lint asks for (codemods, run on request)",
		Long:  "Codemods that rewrite the project's own code to what a forge lint rule asks for. Each runs only when you name it, and edits files that are yours: review the diff.",
	}
	cmd.AddCommand(newNarrowDepsCmd(f))
	for _, c := range codemod.All() {
		cmd.AddCommand(newCodemodCmd(f, c))
	}
	return cmdutil.StrictGroup(cmd)
}

// newCodemodCmd is `forge fix <name>` for one release codemod
// (internal/codemod): run it over the project, or with --dry-run list what
// it would rewrite.
func newCodemodCmd(f *factory.Factory, c codemod.Codemod) *cobra.Command {
	var dryRun, allowDirty bool
	var rawScope []string
	cmd := &cobra.Command{
		Use:   c.Name,
		Short: c.Short,
		Long:  c.Long + "\n\nThe rest of the " + c.Version + " story is the migration skill: `forge skill load migrations/" + c.Version + "`.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := cmdutil.ProjectRoot()
			if err != nil {
				return err
			}
			scope, err := codemod.ParseScope(root, rawScope)
			if err != nil {
				return cliutil.UserErr("forge fix "+c.Name+" --scope", err.Error(), "",
					"pass paths inside the project, relative to its root, e.g. --scope internal/handlers/jobs")
			}
			if c.CleanTree && !dryRun && !allowDirty {
				if dirty := uncommitted(root, scope); dirty != "" {
					return cliutil.UserErr("forge fix "+c.Name,
						"the project has uncommitted changes:\n"+dirty,
						"",
						"commit or stash them first: this codemod replaces files you own, and `git diff` is how you review it (or pass --allow-dirty)")
				}
			}
			rep, err := c.Run(root, codemod.Options{Write: !dryRun, Scope: scope})
			if err != nil {
				return err
			}
			printCodemodReport(f.Out, c, rep, dryRun, scope)
			if len(rep.Blocked) > 0 {
				return cliutil.UserErr("forge fix "+c.Name,
					fmt.Sprintf("%d shape(s) the codemod cannot convert without deciding for you; nothing was written", len(rep.Blocked)),
					"",
					"clear each item listed above, then re-run; `forge skill load migrations/"+c.Version+"` is the manual path")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List what would be rewritten; change nothing")
	if c.CleanTree {
		cmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "Run over uncommitted changes")
	}
	if c.Scoped {
		cmd.Flags().StringSliceVar(&rawScope, "scope", nil, "Rewrite only files under `PATH` (repeatable or comma-separated, project-relative), as forge lint --scope lints one slice of a shared checkout. What decides a rewrite is still read from the whole project; only the writes, and the report, are bounded")
	}
	return cmd
}

// uncommitted lists the working tree's uncommitted changes (git status
// --porcelain), "" for a clean tree or a directory git does not track. Under
// a scope only changes inside it count: another lane's uncommitted work in a
// shared checkout is not this run's to review.
func uncommitted(root string, scope *codemod.Scope) string {
	// Pathspecs resolve against -C's directory, the project root.
	args := append([]string{"-C", root, "status", "--porcelain", "--"}, scope.Paths()...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

func printCodemodReport(out io.Writer, c codemod.Codemod, rep codemod.Report, dryRun bool, scope *codemod.Scope) {
	if len(rep.Blocked) > 0 {
		fmt.Fprintf(out, "  %s: nothing written — these shapes need your decision first:\n", c.Name)
		for _, b := range rep.Blocked {
			fmt.Fprintf(out, "  ✗ %s\n", b)
		}
		for _, n := range rep.Next {
			fmt.Fprintf(out, "\n  Next: %s\n", n)
		}
		return
	}
	if !rep.Changed() && len(rep.Next) == 0 {
		where := "the project is"
		if scope != nil {
			where = scope.String() + " is"
		}
		fmt.Fprintf(out, "  %s: nothing to rewrite — %s already in its %s shape\n", c.Name, where, c.Version)
		return
	}
	mark := "✅"
	if dryRun {
		mark = "would rewrite:"
	}
	for _, r := range rep.Rewrites {
		fmt.Fprintf(out, "  %s %s\n", mark, r)
	}
	if len(rep.Next) > 0 {
		fmt.Fprintln(out, "\n  Next:")
		for i, n := range rep.Next {
			fmt.Fprintf(out, "  %d. %s\n", i+1, n)
		}
	}
	if !dryRun && len(rep.Verify) > 0 {
		fmt.Fprintf(out, "\n  Verify: %s\n", strings.Join(rep.Verify, " && "))
	}
}

func newNarrowDepsCmd(f *factory.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "narrow-deps <package-dir>",
		Short: "Declare the interfaces a component's Deps actually use, and retype its fields to them",
		Long: `Narrow a component's Deps to what it uses (composition graph, least knowledge).

For every Deps field typed as an interface DECLARED IN ANOTHER PACKAGE that has
methods the package never calls through it (forgeconv-deps-surface), write an
interface holding exactly the methods it calls, in the package itself — its
contract.go, or deps.go — and retype the field to it. The composition is
untouched: the value internal/app/compose.go hands the field still satisfies
the narrower interface, structurally. Its mock is written beside it
(mock_gen.go), and ` + "`forge generate`" + ` keeps it in step.

A field the package passes on as an interface — a parameter, a struct field —
needs that interface's methods too: the new interface embeds it, so every call
that passes the value on still type-checks. A field whose value also leaves as
` + "`any`" + `, a concrete type or a return value is left alone (what it needs is
not all visible here), as is one it never uses (delete it) and an interface
the package already declares (edit that one).

The generated code is goimports-clean for the project (stdlib, third-party,
then the module — or the local-prefixes of its golangci config). A narrowed
interface still wider than the project's interfacebloat limit (read from its
golangci config; golangci's default is 10) is reported, with the remedy. When
the project's config runs interfacebloat, the type is also written with a
reasoned //nolint:interfacebloat rather than split into embedded roles to fit:
the lint counts an embedded interface as one entry, so a split would pass the
count with the dependency exactly as wide. The interface is what the package
calls; to shrink it, split the package.`,
		Example: "  forge fix narrow-deps internal/handlers/tools_daemon",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			dir := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(args[0], "./")))
			res, err := depsurface.Narrow(cwd, dir)
			if err != nil {
				return err
			}
			out := f.Out
			for _, s := range res.Skipped {
				fmt.Fprintf(out, "  ⏭️  Deps.%s (%s): %s\n", s.Field, s.Type, s.Reason)
			}
			if len(res.Narrowed) == 0 {
				fmt.Fprintf(out, "  %s: nothing to narrow — every foreign interface field is used in full\n", res.Dir)
				return nil
			}
			for _, n := range res.Narrowed {
				embeds := ""
				if len(n.Embeds) > 0 {
					embeds = "embeds " + strings.Join(n.Embeds, ", ") + "; "
				}
				fmt.Fprintf(out, "  ✅ Deps.%s: %s → %s (%s%d method(s): %s)\n", n.Field, n.From, n.To, embeds, len(n.Methods), strings.Join(n.Methods, ", "))
			}
			target := filepath.Join(cwd, filepath.FromSlash(res.File))
			if err := contract.GenerateWithOptions(target, contract.Options{ProjectRoot: cwd, Resolver: contract.NewResolver(target)}); err != nil {
				return fmt.Errorf("write the mocks for %s: %w", res.File, err)
			}
			for _, b := range res.Bloated {
				fmt.Fprintf(out, "  ⚠️  %s\n", b)
			}
			fmt.Fprintf(out, "  ✅ declared in %s, mocked in %s\n", res.File, filepath.ToSlash(filepath.Join(res.Dir, "mock_gen.go")))
			return nil
		},
	}
}
