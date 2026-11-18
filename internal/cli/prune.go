package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// newPruneCmd is `forge project prune --orphans`: delete the files a feature
// pinned off left behind, when they are still exactly what forge wrote.
func newPruneCmd() *cobra.Command {
	var orphans, dryRun bool
	cmd := &cobra.Command{
		Use:   "prune --orphans [--dry-run]",
		Short: "Delete unmodified forge-generated files no step writes any more",
		Long: `Delete the orphans the last full ` + "`forge generate`" + ` listed: files forge generated
for a feature that forge.yaml now pins off (` + "`features: {observability: false}`" + `), which no
step writes any more. ` + "`forge generate`" + ` records them in ` + orphansRel + `.

Only files whose bytes are still exactly what forge rendered are deleted — the
embedded forge:hash marker (or the certificate under .forge/hashes/) must still
verify. A file anyone edited is listed and kept, and so is anything under
forge.yaml's scaffold.exclude or disowned with ` + "`forge project disown`" + `.

The record must match the current forge.yaml: if a feature was switched back on
since, run ` + "`forge generate`" + ` first and its files are no longer orphans.

Examples:
  forge project prune --orphans --dry-run
  forge project prune --orphans`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !orphans {
				return errors.New("nothing selected: pass --orphans (the files a feature pinned off left behind)")
			}
			root, err := projectRoot()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var res pruneResult
			if err := withGenerateLock(root, func() error {
				var perr error
				res, perr = pruneOrphans(root, dryRun)
				return perr
			}); err != nil {
				return err
			}
			verb := "Removed"
			if dryRun {
				verb = "Would remove"
			}
			if len(res.Removed)+len(res.Kept)+len(res.Gone) == 0 {
				_, _ = fmt.Fprintf(out, "No orphans recorded — `%s generate` lists them when a feature is pinned off.\n", Name())
				return nil
			}
			_, _ = fmt.Fprintf(out, "%s %d orphaned file(s):\n", verb, len(res.Removed))
			for _, p := range res.Removed {
				_, _ = fmt.Fprintf(out, "  - %s\n", p)
			}
			if len(res.Kept) > 0 {
				_, _ = fmt.Fprintf(out, "Kept %d — edited since forge wrote them, excluded, or disowned:\n", len(res.Kept))
				for _, p := range res.Kept {
					_, _ = fmt.Fprintf(out, "  - %s\n", p)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&orphans, "orphans", false, "Delete the unmodified files a feature pinned off left behind")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List what would be removed and delete nothing")
	return cmd
}
