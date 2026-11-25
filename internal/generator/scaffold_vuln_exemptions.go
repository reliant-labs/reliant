package generator

// The npm advisories a scaffolded frontend carries from birth, and the
// reviewed acceptance forge writes for them.
//
// A frontend template is a dependency set forge chose, so an advisory that
// set pulls in is forge's to know about — and when it has NO fixed release
// there is nothing to upgrade, so a fresh project's own vuln-npm gate is red
// on day 0 (issue #754). The fix for an unfixable advisory is the gate's
// reviewed escape hatch, ci.vuln_scan.exemptions: accepted by id only, with
// the reachability argument in `reason` and a review date in `expires`.
//
// Forge writes that entry rather than leaving every user to rediscover and
// justify it, because forge is the party that can make the argument: it knows
// which of its own template's packages pull the advisory in, and why that
// path cannot reach the app. The entry lands in forge.yaml, in the user's
// hands, where it can be read, disputed and deleted — never as an acceptance
// baked into the binary that the project cannot see. And it expires: the
// review window starts when the frontend is scaffolded, so the project
// re-examines it on a date even if forge never ships again.
//
// An advisory that HAS a fixed release does not belong here: it gets a version
// bump or a scoped `overrides` entry in the template instead.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/config"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// scaffoldNPMAdvisory is one no-fix advisory a frontend template carries.
type scaffoldNPMAdvisory struct {
	// ID is the GHSA id npm audit reports.
	ID string
	// FrontendTypes are the scaffolded frontend types (nextjs / vite-spa /
	// react-native) whose dependency set carries it.
	FrontendTypes []string
	// Reason is the reachability argument written into the entry.
	Reason string
}

// scaffoldNPMAdvisories is every no-fix advisory a scaffolded frontend
// carries. Remove an entry the moment upstream ships a fix — and bump the
// template so the fix is what a new project installs. Whether an entry is
// still needed is a fact about the npm registry, not this repo; the real npm
// scan reports an entry as matching nothing once its advisory is gone.
var scaffoldNPMAdvisories = []scaffoldNPMAdvisory{
	{
		// braces through 3.0.3 — every release — recurses without a depth
		// guard; first_patched_version is null upstream. The Next.js
		// scaffold reaches it only through dev tooling: stylelint (globby,
		// fast-glob, micromatch) and eslint-config-next
		// (@next/eslint-plugin-next, fast-glob). The Vite SPA scaffold does
		// not carry it.
		ID:            "GHSA-vfj7-8cjw-p6xm",
		FrontendTypes: []string{"nextjs"},
		Reason: "braces has no fixed release (every version through 3.0.3 is affected). " +
			"It reaches this project only through the Next.js frontend's dev lint toolchain " +
			"(stylelint and eslint-config-next, via fast-glob/micromatch), which expands glob " +
			"patterns from the project's own config files, never from untrusted input, and is " +
			"not in the production bundle (npm audit --omit=dev does not report it). Remove this " +
			"entry if braces ever enters a production dependency, and once a fixed braces release " +
			"makes forge ci vuln-scan report it as matching nothing.",
	},
}

// scaffoldExemptionReview is how long a scaffold-written exemption is
// honored before the gate makes the project re-review it.
const scaffoldExemptionReview = 90 * 24 * time.Hour

// topLevelCIKeyRE matches a top-level `ci:` mapping key in forge.yaml.
var topLevelCIKeyRE = regexp.MustCompile(`(?m)^ci\s*:`)

// EnsureScaffoldNPMExemptions makes forge.yaml accept the no-fix npm
// advisories a newly scaffolded frontend of frontendType carries, so the
// project's vuln-npm gate is green on the day the frontend is born.
//
// It only ever ADDS, and only what is missing: an id the project already
// lists — accepted, disputed with a different reason, or re-dated — is the
// project's decision and is left exactly as it is.
//
// forge.yaml is the user's file, so the edit is an append of a whole `ci:`
// section, made only when the file has no `ci:` section yet; that cannot
// reorder, reformat or lose anything already there. When a `ci:` section
// exists, merging into it would mean re-serializing the user's YAML, so forge
// prints the entry to paste instead and touches nothing. Every failure is a
// printed warning: no bookkeeping here justifies failing a scaffold.
func EnsureScaffoldNPMExemptions(projectDir, frontendType string, now time.Time) {
	path := filepath.Join(projectDir, "forge.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		return // no manifest — nothing to reconcile
	}

	missing := missingScaffoldExemptions(original, frontendType)
	if len(missing) == 0 {
		return
	}
	expires := now.Add(scaffoldExemptionReview).Format("2006-01-02")

	if topLevelCIKeyRE.Match(original) {
		fmt.Printf("\n⚠️  The %s frontend's dependencies carry npm advisories with NO fixed release, so the\n"+
			"   vuln-npm gate (`forge ci vuln-scan`) fails until they are accepted. forge.yaml already\n"+
			"   has a `ci:` section, so forge left it alone — add these under ci.vuln_scan.exemptions:\n\n%s\n",
			frontendType, renderExemptionEntries(missing, expires, "      "))
		return
	}

	sep := "\n"
	if !strings.HasSuffix(string(original), "\n") {
		sep = "\n\n"
	}
	updated := []byte(string(original) + sep + scaffoldExemptionsBlock(missing, expires))
	// Never hand back a forge.yaml that no longer parses, or one that
	// parses without the entries this was meant to add.
	if len(missingScaffoldExemptions(updated, frontendType)) != 0 {
		fmt.Fprintf(os.Stderr, "warning: could not record the scaffold's npm advisory exemptions in %s; "+
			"add them under ci.vuln_scan.exemptions yourself:\n%s\n", path, renderExemptionEntries(missing, expires, "      "))
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if err := atomicfile.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", path, err)
		return
	}
	ids := make([]string, 0, len(missing))
	for _, a := range missing {
		ids = append(ids, a.ID)
	}
	fmt.Printf("  + forge.yaml: accepted %s (npm, no fixed release; reviewed until %s) — see ci.vuln_scan.exemptions\n",
		strings.Join(ids, ", "), expires)
}

// missingScaffoldExemptions returns the advisories frontendType carries that
// the forge.yaml content does not list yet. Unparseable content reports every
// advisory missing, which makes the caller's post-write check fail closed.
func missingScaffoldExemptions(forgeYAML []byte, frontendType string) []scaffoldNPMAdvisory {
	var listed struct {
		CI config.CIConfig `yaml:"ci"`
	}
	parsed := yaml.Unmarshal(forgeYAML, &listed) == nil

	var missing []scaffoldNPMAdvisory
	for _, a := range scaffoldNPMAdvisories {
		if !slices.Contains(a.FrontendTypes, frontendType) {
			continue
		}
		found := false
		for _, ex := range listed.CI.VulnScan.Exemptions {
			if strings.EqualFold(strings.TrimSpace(ex.ID), a.ID) {
				found = true
				break
			}
		}
		if !parsed || !found {
			missing = append(missing, a)
		}
	}
	return missing
}

// scaffoldExemptionsBlock renders the whole `ci:` section appended to a
// forge.yaml that has none.
func scaffoldExemptionsBlock(advisories []scaffoldNPMAdvisory, expires string) string {
	return "# Written when the frontend was scaffolded. The vuln-npm gate (`forge ci vuln-scan`,\n" +
		"# a row of `task ci`) fails on high and critical npm advisories. The ones below are\n" +
		"# carried by the scaffolded frontend's own dependencies and have NO fixed release, so\n" +
		"# there is nothing to upgrade to. Each is accepted by id only — a new advisory in the\n" +
		"# same package still fails — with the reason it cannot reach this app, until\n" +
		"# `expires`. On that date the gate fails again: re-review, then extend the date or\n" +
		"# delete the entry. Delete it sooner when the gate reports it as matching nothing.\n" +
		"ci:\n" +
		"  vuln_scan:\n" +
		"    exemptions:\n" +
		renderExemptionEntries(advisories, expires, "      ")
}

// renderExemptionEntries renders exemption list items at indent (the column
// of each item's `- `).
func renderExemptionEntries(advisories []scaffoldNPMAdvisory, expires, indent string) string {
	var b strings.Builder
	for _, a := range advisories {
		b.WriteString(indent + "- id: " + a.ID + "\n")
		b.WriteString(indent + "  reason: >-\n")
		for _, line := range wrapWords(a.Reason, 72) {
			b.WriteString(indent + "    " + line + "\n")
		}
		b.WriteString(indent + "  expires: \"" + expires + "\"\n")
	}
	return b.String()
}

// wrapWords greedily wraps s at width columns on word boundaries, for a YAML
// folded scalar (which joins the lines back with single spaces).
func wrapWords(s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) > width:
			lines = append(lines, line)
			line = w
		default:
			line += " " + w
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
