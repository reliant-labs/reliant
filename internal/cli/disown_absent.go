// `forge project disown <path>... --absent --reason <text>` — decline a
// scaffold-once file: forge never writes it in this project.
//
// WHY. A scaffold-once file is yours from birth, and deleting it is the
// ordinary way to say "not this one": the birth ledger records the birth, so
// "recorded + absent" reads as deliberately absent and forge leaves it so.
// That only works for a birth that SURVIVES its run. Reliant moved its cobra
// tree to cmd/reliant/cmd/, forge scaffolded serve.go there, serve.go's
// presence switched internal/app composition on, composition failed on 104
// missing providers, and the rollback removed serve.go together with its
// birth record — correctly, since a record for a file the rollback deleted
// would have consumed the path's only birth. Every later generate repeated
// the cycle, so there was never a record to delete the file against; the
// only way out was hand-writing JSON into the scaffold ledger.
//
// So declining is a first-class, declared act: a ledger entry carrying the
// reason (ScaffoldEntry.Declined), written by this command OUTSIDE generate.
// Every scaffold-once writer already asks the ledger before a birth
// (checksums.ScaffoldOnceDecision), so the decline is honored everywhere with
// no list for a writer to forget, and a failed generate — whose rollback
// restores the ledger to its pre-run bytes — keeps it.
//
// Why the ledger and not a forge.yaml list: forge.yaml declares what the
// project IS, and scaffold ownership is already the ledger's subject — a
// second list in forge.yaml would be a second answer to "may forge write
// this path?", consulted by some writers and not others. The ledger is
// committed and human-readable, so the decline is still a reviewed line in a
// file under version control; this command is the safe way to write it.
//
// Why `disown`: it is already the verb for "this path is mine, forge keep
// off". Without --absent it takes a forge-generated file as it stands; with
// it, it takes a scaffold's absence.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cliutil"
)

// runDisownAbsent is the --absent body, split out so tests can drive it.
func runDisownAbsent(w io.Writer, args []string, reason string, dryRun bool) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return cliutil.UserErr("forge project disown --absent",
			"--reason is required: declining a scaffold is design feedback, and the reason is the payload",
			"",
			"re-run as `forge project disown <path> --absent --reason \"<why this project does not want forge's file>\"`")
	}
	root, err := projectRoot()
	if err != nil {
		return err
	}
	if dryRun {
		// A dry run writes nothing, the lock file included.
		release, err := observeGenerateLock(root)
		if err != nil {
			return err
		}
		defer release()
		return declineScaffolds(w, root, args, reason, dryRun)
	}
	return withGenerateLock(root, func() error {
		return declineScaffolds(w, root, args, reason, dryRun)
	})
}

// declineScaffolds validates every path up front — a typo on path 3 must
// not half-apply paths 1-2 — then records each decline.
func declineScaffolds(w io.Writer, root string, args []string, reason string, dryRun bool) error {
	cs, err := checksums.Load(root)
	if err != nil {
		return cliutil.WrapUserErr("forge project disown --absent",
			"failed to load the .forge ownership state", "",
			"verify .forge/disowned.json and .forge/hashes.json are valid JSON; if hand-edited, restore them from git", err)
	}

	var targets, present, generated, outside []string
	for _, raw := range args {
		p := filepath.ToSlash(filepath.Clean(normalizeProjectRelPath(strings.TrimSpace(raw))))
		if filepath.IsAbs(raw) {
			if rel, rerr := filepath.Rel(root, raw); rerr == nil {
				p = filepath.ToSlash(rel)
			}
		}
		switch {
		case p == "." || p == ".." || strings.HasPrefix(p, "../") || filepath.IsAbs(p) || strings.HasPrefix(p, ".forge/"):
			outside = append(outside, raw)
			continue
		case cs.IsDisowned(p):
			generated = append(generated, p)
			continue
		}
		if _, ok := cs.Unstampable[p]; ok {
			generated = append(generated, p)
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if readErr == nil {
			if _, marked := checksums.ExtractMarker(content); marked {
				generated = append(generated, p)
			} else {
				present = append(present, p)
			}
			continue
		}
		targets = append(targets, p)
	}
	if len(outside) > 0 {
		return cliutil.UserErr("forge project disown --absent",
			fmt.Sprintf("%s %s not a project file forge could scaffold", strings.Join(outside, ", "), plural(len(outside), "is", "are")),
			"",
			"pass paths relative to the project root, outside .forge/ (forge's own state)")
	}
	if len(generated) > 0 {
		return cliutil.UserErr("forge project disown --absent",
			fmt.Sprintf("%s %s forge-generated (Tier-1), regenerated on every run — --absent declines scaffold-once files only", strings.Join(generated, ", "), plural(len(generated), "is", "are")),
			"",
			"to own a generated file as it stands, run `forge project disown <path> --reason \"…\"` without --absent")
	}
	if len(present) > 0 {
		return cliutil.UserErr("forge project disown --absent",
			fmt.Sprintf("%s %s on disk — a scaffold-once file on disk is already yours, and --absent records that a path stays ABSENT", strings.Join(present, ", "), plural(len(present), "is", "are")),
			"",
			"delete the file first if the project should not have it, then re-run; or keep it — forge never rewrites a scaffold-once file")
	}

	sort.Strings(targets)
	targets = uniqueStrings(targets)
	var fresh []string
	for _, p := range targets {
		if prev, ok := checksums.ScaffoldDeclined(root, p); ok && prev == reason {
			_, _ = fmt.Fprintf(w, "  ⏩ %s (already declined; nothing to do)\n", p)
			continue
		}
		fresh = append(fresh, p)
	}
	if len(fresh) == 0 {
		_, _ = fmt.Fprintln(w, "Nothing to decline.")
		return nil
	}
	if dryRun {
		_, _ = fmt.Fprintf(w, "\n--dry-run: would decline %d scaffold(s) in %s:\n", len(fresh), checksums.ScaffoldedDir+"/")
		for _, p := range fresh {
			_, _ = fmt.Fprintf(w, "  - %s\n", p)
		}
		_, _ = fmt.Fprintln(w, "\nNothing modified.")
		return nil
	}
	for _, p := range fresh {
		checksums.DeclineScaffold(root, p, reason)
		_, _ = fmt.Fprintf(w, "  ✓ declined %s\n", p)
	}
	_, _ = fmt.Fprintf(w, "\n✅ Declined %d scaffold(s): forge will not write %s in this project, and a failed `forge generate` keeps the decline (recorded in %s — commit it).\n",
		len(fresh), plural(len(fresh), "it", "them"), checksums.ScaffoldedDir+"/")
	_, _ = fmt.Fprintf(w, "   To have forge scaffold one after all: %s\n", rescaffoldCmd("<path>"))
	return nil
}
