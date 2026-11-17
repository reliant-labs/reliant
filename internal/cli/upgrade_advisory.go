// `forge project upgrade`'s scaffold-once report.
//
// The engine — which files are in the tier, and how a difference is
// classified — lives in internal/generator/upgrade_advisory.go. What lives
// here is the report, because "here is what you are missing, and here is
// what taking it would cost you" is a human-facing judgement rather than a
// generator concern.
//
// The whole section is advisory: nothing in it is written unless the user
// named that exact path after --force.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// upgradeAdvisoryFiles is every scaffold-once row `forge project upgrade`
// reports on: generator.AdvisoryFilesFor's, plus the GitHub Actions
// workflows, whose render needs the KCL topology only this package reads
// (ciInputs) — the same inputs `forge generate` writes them from.
//
// When an env fails to render, forge cannot know which workflows the
// project has or what they should hold (release.yml exists only for hosted
// envs, deploy.yml lists the rest, ci.yml drives the frontends the render
// declares). Comparing against a guess would report drift forge invented,
// so the workflow rows are left out and ciNote says why and what to run.
func upgradeAdvisoryFiles(projectDir string, cfg *config.ProjectConfig) (rows []generator.AdvisoryFile, ciNote string, err error) {
	rows, err = generator.AdvisoryFilesFor(projectDir, cfg)
	if err != nil {
		return nil, "", err
	}
	if cfg == nil || (cfg.CI.Provider != "" && cfg.CI.Provider != "github") {
		return rows, "", nil
	}
	if topo := discoverCIHostedEnvs(projectDir); len(topo.Unrendered) > 0 {
		ciNote = fmt.Sprintf("GitHub workflows not compared: what they should hold depends on every env's render, and %s did not render — run '%s env render %s' to see why",
			strings.Join(topo.Unrendered, ", "), Name(), topo.Unrendered[0])
		return rows, ciNote, nil
	}
	return append(rows, generator.CIWorkflowAdvisoryFiles(projectDir, cfg, ciInputs(projectDir, cfg))...), "", nil
}

// runAdvisoryPass inspects every scaffold-once row and prints the report.
// Returns the results so callers can act on them; the printing is the
// product.
func runAdvisoryPass(projectDir string, cfg *config.ProjectConfig, selection generator.ForceSelection, check, showAll bool) ([]generator.AdvisoryResult, error) {
	files, ciNote, err := upgradeAdvisoryFiles(projectDir, cfg)
	if err != nil {
		return nil, err
	}
	if ciNote != "" {
		defer fmt.Printf("\n%s\n", ciNote)
	}
	cs, err := generator.LoadChecksums(projectDir)
	if err != nil {
		return nil, fmt.Errorf("load .forge ownership state: %w", err)
	}
	results, err := generator.InspectAdvisories(projectDir, cs, files, selection, check)
	if err != nil {
		return nil, err
	}
	printAdvisoryReport(results, check, showAll)
	return results, nil
}

// printAdvisoryReport prints the scaffold-once section: what differs,
// sized, grouped by what adopting it costs — and no inline diffs.
//
// This section is where the volume problem was worst, and where the
// ranking matters most. On a mature project the bulk of these rows are a
// customized frontend's auth and layout modules diverging from the current
// template, which is not a defect — it is what a customized frontend LOOKS
// like. Printed at the same volume and prominence as `.github/CODEOWNERS`
// being two lines behind, they buried the one row that was a trivial
// adopt. Grouping by cost puts the trivial adopts first and lets the
// expected divergence be a count with a pointer.
//
// Silence is still the normal outcome and is still a real answer — a
// project whose mechanism files match their templates is told nothing.
func printAdvisoryReport(results []generator.AdvisoryResult, check, showAll bool) {
	var behind []generator.AdvisoryResult
	var adopted []generator.AdvisoryResult
	for _, r := range results {
		if !r.Behind() {
			continue
		}
		if r.Adopted || r.Selected {
			adopted = append(adopted, r)
			continue
		}
		behind = append(behind, r)
	}
	if len(behind) == 0 && len(adopted) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("Files you own, whose forge templates have moved on:")

	// Rows this run adopted (or would) are the ACTION, not the backlog:
	// they are named in full and never truncated, because a file forge
	// is about to rewrite must never be hidden behind a "... 12 more".
	if len(adopted) > 0 {
		for _, r := range adopted {
			fmt.Fprintf(os.Stdout, "    %-55s %s\n", r.Path, advisoryLabel(r, check))
		}
	}
	if len(behind) == 0 {
		return
	}

	rows := make([]driftRow, 0, len(behind))
	for _, r := range behind {
		rows = append(rows, driftRow{
			Path:    r.Path,
			Missing: r.Missing,
			Local:   r.Local,
			Absent:  r.Status == generator.AdvisoryAbsent,
		})
	}
	cheap, merge, absent := groupDriftRows(rows)

	printCountsLine(
		countPart(len(cheap), "cheap adopt(s)"),
		countPart(len(merge), "merge(s)"),
		countPart(len(absent), "absent here"),
	)
	printDriftGroup("Cheap adopts (no local edits):", cheap, showAll)
	printDriftGroup("Merges (your lines + the template's):", merge, showAll)
	printDriftGroup("Shipped by the template, absent here:", absent, showAll)

	// Under --all, name the rows this section is normally silent about,
	// so "--all shows everything" is true of this lane too rather than
	// true only of the lane that happened to be audited.
	if showAll {
		var current []string
		for _, r := range results {
			if !r.Behind() {
				current = append(current, r.Path)
			}
		}
		printNamedPathList("Current — the template has nothing these lack:", current)
	}

	printAdvisoryRemedies(cheap, merge, absent, showAll)
}

// advisoryLabel names, in one phrase, what forge found and how sure it is.
//
// The distinction the wording has to carry: a file that is merely OLD and
// a file someone deliberately changed are different situations with
// different remedies, and calling the second one "outdated" writes off
// whoever did the work. So the label reports the evidence — lines the
// template has that this copy does not, lines this copy has that no
// template line accounts for — and lets that speak.
func advisoryLabel(r generator.AdvisoryResult, check bool) string {
	switch {
	case r.Adopted:
		return "adopted the current template"
	case r.Selected && check:
		return "would adopt the current template"
	case r.Status == generator.AdvisoryAbsent:
		return fmt.Sprintf("not in this project — the template ships %s you don't have", lineCount(r.Missing))
	case r.Status == generator.AdvisoryDiverged:
		return fmt.Sprintf("behind by %s, and %s here %s yours alone",
			lineCount(r.Missing), lineCount(r.Local), plural(r.Local, "is", "are"))
	case r.Proven:
		return fmt.Sprintf("behind by %s (untouched since forge wrote it)", lineCount(r.Missing))
	default:
		return fmt.Sprintf("behind by %s (nothing here is unaccounted for)", lineCount(r.Missing))
	}
}

// lineCount renders a line tally in English.
func lineCount(n int) string { return fmt.Sprintf("%d %s", n, plural(n, "line", "lines")) }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// printAdvisoryRemedies closes the section with what the user can do.
//
// Two audiences, and the split is the point. A file with no unaccounted
// lines can be adopted wholesale — forge can name the exact command. A
// file carrying lines of its own cannot: taking the template there is a
// MERGE, and forge is not the one who knows which of those lines matter.
// Saying so plainly is the difference between a report that respects the
// work in the file and one that reads as "you are out of date".
//
// No per-file migration-skill pointer is printed, because none would be
// true: nothing in skills/forge/migrations covers a frontend mechanism
// template today, and a pointer that is usually absent teaches readers to
// stop looking for it. When a template change IS too big to read off a
// diff, the place that says so is the migration catalogue this same
// command already surfaces — so the merge case names it.
// Both lists are ONE COMMAND PER LINE and capped. The adopt line used to
// name every adoptable path in a single --force — 34 of them on a real
// project, hundreds of characters wrapping across the terminal, adopting
// all 34 in one irreversible gesture. The merge sentence had the same
// defect: "37 file(s) carry lines no forge template has —" followed by 37
// inline paths. Both are per-line now; the paths that do not fit are one
// `--all` away, which the truncation says where it happens.
func printAdvisoryRemedies(cheap, merge, absent []driftRow, showAll bool) {
	if len(cheap) == 0 && len(merge) == 0 && len(absent) == 0 {
		return
	}

	fmt.Println()
	fmt.Println("  These files are yours: forge wrote them once and has not touched them since.")
	fmt.Println("  Nothing above was changed — this section only reports.")
	if adoptable := append(append([]driftRow{}, cheap...), absent...); len(adoptable) > 0 {
		sortDriftRows(adoptable)
		fmt.Println("  Take the current template for a file (its contents are replaced):")
		printAdoptCommands(adoptable, showAll)
	}
	if len(merge) > 0 {
		fmt.Printf("  %d file(s) carry lines no forge template has. Those are a merge you make, not\n", len(merge))
		fmt.Println("  an overwrite forge can make for you: read the diff, take the template's change,")
		fmt.Println("  keep your own. Forcing one discards your lines.")
		fmt.Printf("    %s project upgrade list   # migrations that explain a change a diff can't\n", Name())
	}
	printDetailPointer()
}
