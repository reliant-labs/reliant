// `forge project scaffolded` — list the scaffold-once files forge has
// written in this project, and which of them are currently absent.
//
// This is the on-demand half of a trade. `forge generate` used to print the
// absent set on EVERY run: in control-plane, the same 38 paths plus five
// lines of explanation, on a clean tree with nothing changed. That is a
// standing fact rendered as a recurring notice, and a recurring notice is
// one the reader stops seeing. Generate now reports only the MOMENT the set
// changes (see generate_missing_scaffold.go); the standing fact moved here,
// where it is answered when someone asks.
//
// Why a `forge project <noun>` verb rather than a flag on rescaffold: this
// is a QUESTION about the project, and it sits with the other questions —
// `forge project map`, `shapes`, `graph`, `features`, `capabilities`. A
// `--list` flag on rescaffold would put the read behind a write verb, which
// is both harder to find and easier to run by accident.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/checksums"
)

func newScaffoldedCmd() *cobra.Command {
	var absentOnly bool

	cmd := &cobra.Command{
		Use:   "scaffolded",
		Short: "List the scaffold-once files forge has written, and which are absent",
		Long: `List every scaffold-once ("yours") file forge has written in this project,
from the birth ledger at ` + checksums.ScaffoldedDir + "/" + `.

forge writes a scaffold-once file exactly once and then it is yours. Deleting
one is an act of ownership the ledger records, so ` + "`forge generate`" + ` leaves it
deleted — here and in every clone. ` + "`forge generate`" + ` reports that set only when
it CHANGES; this command answers it any time.

  present   the file is on disk — your bytes, forge leaves them alone
  ABSENT    you deleted it, and forge is respecting that

To have forge write an absent one again, fresh against the project as it
stands today:

  ` + Name() + ` project rescaffold <path>

Examples:
  ` + Name() + ` project scaffolded
  ` + Name() + ` project scaffolded --absent`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := projectRoot()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			absent := checksums.AbsentScaffolds(root)
			absentSet := make(map[string]bool, len(absent))
			for _, p := range absent {
				absentSet[p] = true
			}

			if absentOnly {
				if len(absent) == 0 {
					_, _ = fmt.Fprintf(out, "No absent scaffold-once files — every file forge has scaffolded is on disk.\n")
					return nil
				}
				_, _ = fmt.Fprintf(out, "%d absent scaffold-once file(s) — deleted on purpose, left deleted:\n", len(absent))
				for _, p := range absent {
					_, _ = fmt.Fprintf(out, "  %s\n", p)
				}
				_, _ = fmt.Fprintf(out, "\nRe-create one: %s\n", rescaffoldCmd("<path>"))
				return nil
			}

			all := checksums.ScaffoldLedgerPaths(root)
			if len(all) == 0 {
				_, _ = fmt.Fprintf(out, "No scaffold-once files recorded — %s is empty or absent.\n", checksums.ScaffoldedDir+"/")
				return nil
			}
			declined := checksums.DeclinedScaffolds(root)
			_, _ = fmt.Fprintf(out, "%d scaffold-once file(s) recorded in %s (%d absent, %d declined):\n",
				len(all), checksums.ScaffoldedDir+"/", len(absent), len(declined))
			for _, p := range all {
				status := "present "
				switch {
				case absentSet[p]:
					status = "ABSENT  "
				case declined[p] != "":
					status = "DECLINED"
				}
				line := fmt.Sprintf("  %s  %s", status, p)
				if why := declined[p]; why != "" {
					line += "  — " + why
				}
				_, _ = fmt.Fprintln(out, line)
			}
			if len(absent) > 0 || len(declined) > 0 {
				_, _ = fmt.Fprintf(out, "\nRe-create an absent or declined one: %s\n", rescaffoldCmd("<path>"))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&absentOnly, "absent", false, "List only the files that are absent (deleted on purpose)")
	return cmd
}
