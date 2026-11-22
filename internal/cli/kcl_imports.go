// File: internal/cli/kcl_imports.go
//
// `forge kcl imports` — print where each forge project this project's KCL
// imports ([forge_projects] in deploy/kcl/kcl.mod) resolves to.
//
// A render reads an imported project's KCL from wherever go.mod resolves its
// module: a replace directory, a go.work use, or the module cache at the
// required version — or, for an import pinned by git (repo + ref), from the
// shared source cache or a local override (internal/kclprojects). That location is a fact a person
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
	var asJSON, refresh bool
	cmd := &cobra.Command{
		Use:   "imports",
		Short: "Show where each forge project this project's KCL imports resolves to",
		Args:  cobra.NoArgs,
		Long: `List the forge projects deploy/kcl/kcl.mod imports under [forge_projects],
and where each one resolves.

An import is pinned one of two ways:

  module = "<go module>"   read at the version this project's go.mod pins that
                           module to — a go.work use or a directory replace
                           reads that directory (a sibling checkout), otherwise
                           the module cache at the required version, verified
                           against go.sum. There is no separate KCL pin.
  repo = "<repo>", ref = "<tag|branch|sha>" [, commit = "<sha>"]
                           a git pin, for a repository that is not a Go
                           dependency (no go.mod needed). Fetched once into the
                           machine-local source cache shared with every
                           forge.GitSource, and overridable to a working copy in
                           .forge/source-overrides.yaml. commit, when set, is
                           the lock: a ref resolving elsewhere is refused.

Resolution refuses an imported project that pins a different forge than this
one (version skew). Running this command fetches every git pin not yet cached,
which is how to prepare a machine to render offline; --refresh fetches them
again (a moved branch or tag).

--json prints the resolution as a JSON array: name, module or repo/ref/commit,
version (empty for a directory), via (the declaration that decided it),
module_dir, dir (the KCL package root), forge_pin, resolved_commit and
overridden.

Examples:
  ` + Name() + ` kcl imports
  ` + Name() + ` kcl imports --refresh
  ` + Name() + ` kcl imports --json | jq -r '.[].dir'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projectDir, err := evalProjectRoot()
			if err != nil {
				return err
			}
			rs, err := kclprojects.ResolveWith(cmd.Context(), kclprojects.FindKCLRoot(filepath.Join(projectDir, "deploy", "kcl")),
				kclprojects.Options{Refresh: refresh})
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
				source := r.Module
				if r.IsGit() {
					source = r.Repo
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, source, version, r.Via, r.Dir)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			// A tag or branch with no `commit` is not locked: a re-pointed tag
			// renders different KCL on a machine that fetches it later.
			for _, r := range rs {
				if r.IsGit() && r.Commit == "" && !r.Overridden && r.ResolvedCommit != "" && r.ResolvedCommit != r.Ref {
					fmt.Fprintf(cmd.ErrOrStderr(), "note: %s pins %s@%s with no commit lock; to lock it, add commit = %q to its entry in %s\n",
						r.Name, r.Repo, r.Ref, r.ResolvedCommit, r.DeclaredIn)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the resolution as JSON")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Drop each git-pinned import's cached copy and fetch it again (a moved branch or tag)")
	return cmd
}
