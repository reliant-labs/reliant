// `forge project upgrade --check <path>` — the detail half of the report.
//
// The summary (upgrade_report.go) deliberately prints no diffs: at 91
// reported files, inline diffs were 14,000 of the 14,244 lines emitted and
// were the reason the check went unread. Suppressing them is only honest
// if the diff is one command away, and this file is that command.
//
// It spans BOTH lanes on purpose. The user reading the summary sees one
// list of paths; making them know whether a path is a Tier-2 managed file
// or a scaffold-once advisory row before they can ask about it would leak
// forge's internal tiering into the one gesture that should be trivial.
// The lane still decides what the footer says, because the remedies really
// are different — that difference is information, the lookup is not.
package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// runUpgradeDetail prints the full diff for each named path.
//
// Read-only without exception: it runs the same inspection the report runs
// with checkOnly set and force selecting nothing, so naming a path here can
// never write it. `--force <path>` remains the only gesture that adopts,
// which is what makes it safe to point every truncated list at this view.
func runUpgradeDetail(projectDir string, cfg *config.ProjectConfig, paths []string) error {
	managed, err := generator.UpgradeSelection(projectDir, cfg, generator.ForceNone(), true)
	if err != nil {
		return err
	}
	advisoryFiles, ciNote, err := upgradeAdvisoryFiles(projectDir, cfg)
	if err != nil {
		return err
	}
	if ciNote != "" {
		fmt.Printf("%s\n\n", ciNote)
	}
	cs, err := generator.LoadChecksums(projectDir)
	if err != nil {
		return fmt.Errorf("load .forge ownership state: %w", err)
	}
	advisory, err := generator.InspectAdvisories(projectDir, cs, advisoryFiles, generator.ForceNone(), true)
	if err != nil {
		return err
	}

	for i, raw := range paths {
		rel := upgradeDetailKey(strings.TrimSpace(raw))
		if i > 0 {
			fmt.Println()
		}
		if err := printOneFileDetail(rel, managed, advisory); err != nil {
			return err
		}
	}
	return nil
}

// upgradeDetailKey is the comparable form of a project-relative path: cleaned
// and slash-separated, the form the upgrade results report. filepath.Clean
// alone turned the user's no/such/file.ts into no\such\file.ts on Windows,
// so a refusal named a path they never typed.
func upgradeDetailKey(p string) string { return path.Clean(filepath.ToSlash(p)) }

// printOneFileDetail prints one path's diff and the remedies for its lane.
func printOneFileDetail(rel string, managed []generator.UpgradeResult, advisory []generator.AdvisoryResult) error {
	for _, r := range managed {
		if upgradeDetailKey(r.Path) != rel {
			continue
		}
		printManagedDetail(r)
		return nil
	}
	for _, r := range advisory {
		if upgradeDetailKey(r.Path) != rel {
			continue
		}
		printAdvisoryDetail(r)
		return nil
	}
	return unknownDetailPath(rel, managed, advisory)
}

// printManagedDetail is the Tier-2 view: the diff, then BOTH remedies.
// Adopt and disown are not interchangeable — one takes the template, the
// other records that the file is yours for good — so the file the user is
// actually looking at is the right place to say so.
func printManagedDetail(r generator.UpgradeResult) {
	fmt.Printf("%s\n", r.Path)
	switch {
	case r.Status == generator.UpgradeUpToDate:
		fmt.Println("  Up to date with the current template.")
		return
	case r.Absent:
		// Checked before the Skipped arm: a file that is merely MISSING
		// is reported as skipped under --check (nothing was written),
		// but it is a pure add, not a file upgrade declined to manage.
		fmt.Printf("  %s\n", detailMeasure(r.Missing, r.Local, true))
		printIndentedDiff(r.Diff)
		fmt.Println()
		fmt.Println("  Adopting it adds a file and destroys nothing:")
		fmt.Printf("    %s project upgrade   # writes every managed file this project lacks\n", Name())
		return
	case r.LeftDeleted:
		fmt.Println("  A scaffold-once file you deleted, declined or disowned: upgrade leaves it absent.")
		fmt.Printf("  Bring it back fresh: %s project rescaffold %s\n", Name(), r.Path)
		return
	case r.Status == generator.UpgradeSkipped:
		fmt.Println("  Not upgrade-managed in this project (disowned, or a legacy layout).")
		fmt.Println("  Nothing here is compared against a template.")
		return
	}
	fmt.Printf("  %s\n", detailMeasure(r.Missing, r.Local, r.Absent))
	printIndentedDiff(r.Diff)
	fmt.Println()
	fmt.Println("  Adopt the current template (your edits in this file are discarded):")
	fmt.Printf("    %s project upgrade --force %s\n", Name(), r.Path)
	fmt.Println("  Or claim it as yours — upgrade never touches it again, even with --force:")
	fmt.Printf("    %s project disown %s --reason \"<what the template can't express>\"\n", Name(), r.Path)
}

// printAdvisoryDetail is the scaffold-once view. The remedy split is the
// same judgement the section-level report makes: a file with no lines of
// its own can be adopted with one command, and a file carrying its own
// lines cannot — taking the template there is a merge, and forge is not
// the one who knows which of those lines matter.
func printAdvisoryDetail(r generator.AdvisoryResult) {
	fmt.Printf("%s\n", r.Path)
	if !r.Behind() {
		fmt.Println("  Up to date: the current template has nothing this file lacks.")
		return
	}
	fmt.Printf("  %s\n", detailMeasure(r.Missing, r.Local, r.Status == generator.AdvisoryAbsent))
	printIndentedDiff(r.Diff)
	fmt.Println()
	fmt.Println("  This file is yours: forge wrote it once and has not touched it since.")
	if r.Status == generator.AdvisoryDiverged {
		fmt.Println("  It carries lines no forge template has, so taking the template is a merge you")
		fmt.Println("  make, not an overwrite forge can make for you. Forcing it discards those lines.")
		fmt.Printf("    %s project upgrade list   # migrations that explain a change a diff can't\n", Name())
		return
	}
	fmt.Println("  Take the current template for it (its contents are replaced):")
	fmt.Printf("    %s project upgrade --force %s\n", Name(), r.Path)
}

// detailMeasure states the size of the difference above the diff, so the
// numbers the summary ranked on are visible in the view the summary sends
// you to.
func detailMeasure(missing, local int, absent bool) string {
	if absent {
		return fmt.Sprintf("Not in this project — the template ships %s.", lineCount(missing))
	}
	if missing == 0 && local == 0 {
		return "The bytes differ but no line does — formatting only."
	}
	if local == 0 {
		return fmt.Sprintf("Behind by %s; nothing here is unaccounted for.", lineCount(missing))
	}
	return fmt.Sprintf("Behind by %s, and %s here %s yours alone.",
		lineCount(missing), lineCount(local), plural(local, "is", "are"))
}

// printIndentedDiff prints a diff indented under its file. This is the one
// place in the command that prints a diff at all.
func printIndentedDiff(diff string) {
	if diff == "" {
		return
	}
	for _, line := range splitLines(diff) {
		fmt.Fprintf(os.Stdout, "    %s\n", line)
	}
}

// unknownDetailPath refuses an unrecognized path instead of printing
// nothing.
//
// Silence here would be read as "no differences" — the opposite of the
// truth for a typo'd path — so the refusal names the mistake and shows the
// paths that DO have something to say, which is a far shorter list than
// every path upgrade owns.
func unknownDetailPath(rel string, managed []generator.UpgradeResult, advisory []generator.AdvisoryResult) error {
	var reportable []string
	for _, r := range managed {
		if r.Status == generator.UpgradeUserModified || r.Status == generator.UpgradeUpdated {
			reportable = append(reportable, r.Path)
		}
	}
	for _, r := range advisory {
		if r.Behind() {
			reportable = append(reportable, r.Path)
		}
	}
	sort.Strings(reportable)

	hint := fmt.Sprintf("run '%s project upgrade --check --all' to list every reported path", Name())
	if len(reportable) > 0 && len(reportable) <= 20 {
		hint = fmt.Sprintf("these paths have something to report:\n    %s", strings.Join(reportable, "\n    "))
	}
	return cliutil.UserErr(
		fmt.Sprintf("%s project upgrade --check %s", Name(), rel),
		fmt.Sprintf("not a path this upgrade reports on: %s", rel),
		"",
		hint,
	)
}
