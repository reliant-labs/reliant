package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/codemod"
)

// upgrade_codemod.go — how `forge project upgrade` runs the release codemods.
//
// A codemod (internal/codemod) is a deterministic rewrite of user-owned Go,
// keyed — like a migration skill — to the RELEASE that broke the shape it
// rewrites, not to a pair of versions. Upgrade runs every codemod marked
// Upgrade whose release the project's baseline predates (an unknown baseline
// predates every release) and the target has reached, then folds what they
// rewrote into UPGRADE_NOTES.md. Each codemod decides from the tree, so on a
// project without the old shape it rewrites nothing.
//
// Keying by release is what lets a codemod ride a patch release: forge is
// pre-1.0 and ships its breaking changes in patches, so a registry keyed by
// minor hop (as this once was) could only ever hold codemods nobody needed.
// A codemod that restructures rather than renames is not an Upgrade codemod:
// it runs only as `forge fix <name>`, which its migration skill names.
//
// The intent-bearing half of an upgrade lives in the per-release migration
// skill (upgrade_migrations.go): prose an agent reads, not a report a codemod
// fills in.

// CodemodReport is what the codemods an upgrade ran rewrote, merged in
// release order.
type CodemodReport = codemod.Report

// upgradeCodemods returns the codemods a from → to upgrade runs: every
// Upgrade codemod whose release from predates and to has reached.
func upgradeCodemods(from, to string) []codemod.Codemod {
	var out []codemod.Codemod
	for _, c := range codemod.All() {
		if !c.Upgrade {
			continue
		}
		if !baselineIsUnknown(from) && !baselinePrecedes(from, c.Version) {
			continue
		}
		if !baselineIsUnknown(to) && baselinePrecedes(to, c.Version) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// runCodemodChain runs the codemods a from → to upgrade crosses, in release
// order, and merges their reports. With write false nothing is written and
// the report says what would be.
func runCodemodChain(projectDir, from, to string, write bool) (CodemodReport, error) {
	var merged CodemodReport
	for _, c := range upgradeCodemods(from, to) {
		rep, err := c.Run(projectDir, codemod.Options{Write: write})
		if err != nil {
			return merged, fmt.Errorf("codemod %s (%s): %w", c.Name, c.Version, err)
		}
		merged.Rewrites = append(merged.Rewrites, rep.Rewrites...)
		merged.Next = append(merged.Next, rep.Next...)
		merged.Verify = appendNew(merged.Verify, rep.Verify...)
	}
	return merged, nil
}

func appendNew(have []string, add ...string) []string {
	for _, a := range add {
		dup := false
		for _, h := range have {
			dup = dup || h == a
		}
		if !dup {
			have = append(have, a)
		}
	}
	return have
}

// normalizeMinor strips a leading "v" and any trailing patch component
// from a version string. "v0.1.3" → "0.1", "0.1" → "0.1", "v0.2" →
// "0.2", "" → "" (caller treats empty as "no version declared").
func normalizeMinor(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return ""
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return parts[0]
}

// minorHopDistance returns the number of minor versions between from
// and to, or -1 if either side can't be parsed cleanly. "0.1" → "0.2"
// returns 1; "0.1" → "0.5" returns 4; "0.1" → "1.0" returns -1
// (major hops are not supported by the staged upgrade window).
func minorHopDistance(from, to string) int {
	fMaj, fMin, ok1 := splitMinor(from)
	tMaj, tMin, ok2 := splitMinor(to)
	if !ok1 || !ok2 {
		return -1
	}
	if fMaj != tMaj {
		// Different majors — not a minor hop. Caller decides whether
		// to error out or fall through to "unknown hop".
		return -1
	}
	d := tMin - fMin
	if d < 0 {
		// Downgrades aren't staged. -1 to surface "no window".
		return -1
	}
	return d
}

// splitMinor returns (major, minor, ok) for a SemVer-ish "vMaj.Min(.patch)"
// string. ok=false on any parse hiccup so callers fall through cleanly.
func splitMinor(v string) (int, int, bool) {
	v = normalizeMinor(v)
	if v == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	maj, err := atoiSafe(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err := atoiSafe(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return maj, minor, true
}

// atoiSafe is strconv.Atoi with the import dependency hidden. Kept
// inline to avoid yet another import line in this file.
func atoiSafe(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit %q", c)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// writeUpgradeNotes serializes a CodemodReport to UPGRADE_NOTES.md
// at the project root. The file is overwritten on each run — it's
// meant as a per-upgrade scratch pad, not a long-term log. Users
// review and delete after the upgrade lands.
func writeUpgradeNotes(projectDir, fromVersion, toVersion string, report CodemodReport) error {
	path := filepath.Join(projectDir, "UPGRADE_NOTES.md")

	verify := report.Verify
	if len(verify) == 0 {
		verify = []string{
			"go build ./...",
			"go test -count=1 ./...",
			"forge lint",
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Forge upgrade notes — %s → %s\n\n", fromVersion, toVersion)
	fmt.Fprintf(&sb, "_Generated by `forge project upgrade` at %s. Review, then delete this file once the migration lands._\n\n",
		time.Now().UTC().Format(time.RFC3339))

	if len(report.Rewrites) > 0 {
		sb.WriteString("## Auto-applied changes\n\n")
		sb.WriteString("These changes were applied by `forge project upgrade`'s codemod runner. They are deterministic — re-running the upgrade is idempotent.\n\n")
		for _, line := range report.Rewrites {
			fmt.Fprintf(&sb, "- %s\n", line)
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("## Auto-applied changes\n\n")
		sb.WriteString("_None — the codemods found no patterns to rewrite: the project was already in v" + normalizeMinor(toVersion) + " shape._\n\n")
	}
	if len(report.Next) > 0 {
		sb.WriteString("## Left to you\n\n")
		sb.WriteString("What the codemods could not decide, in order:\n\n")
		for i, line := range report.Next {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, line)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Verification\n\n")
	sb.WriteString("Run the following from the project root after applying any manual changes above:\n\n")
	sb.WriteString("```bash\n")
	for _, c := range verify {
		fmt.Fprintf(&sb, "%s\n", c)
	}
	sb.WriteString("```\n\n")
	// Point at the worklist command rather than a constructed skill path:
	// migration skills are per-RELEASE and pruned once a release ages out
	// of the supported window, so a path built from a version pair names a
	// skill that may never have existed. `upgrade list` always answers
	// correctly, including when the answer is "none".
	sb.WriteString("Clean compile + green tests + clean lint = upgrade done. Run `" + Name() +
		" project upgrade list` for the per-release migration skills this project still needs — they carry the intent-level story the codemod cannot.\n")

	return atomicfile.WriteFile(path, []byte(sb.String()), 0o644)
}
