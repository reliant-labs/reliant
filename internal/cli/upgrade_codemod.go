package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/buildinfo"
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
		if !targetReaches(to, c.Version) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// targetReaches reports whether an upgrade to target has reached a release. A
// pre-release of a release — the Go pseudo-version of a commit on main, which
// is what a pin bot pins — carries every codemod keyed to that release, since
// the codemods land on main before the tag does; so it is the pre-release's
// core version that is compared, not its SemVer precedence (which orders a
// pre-release below the release it leads to). An unknown target reaches all.
//
// The core version is only Go's guess at that release: a pseudo-version of a
// commit after vX.Y.Z always reads vX.Y.(Z+1)-0.…, whatever NEXT_VERSION says
// the next release is (v0.1.46 was followed by v1.0.0). So a pseudo-version
// whose base is the release this binary descends from (its embedded VERSION)
// reaches every release past that base: this binary has not seen a later
// release, so every codemod it holds past the base is keyed to the one the
// target leads to.
func targetReaches(target, version string) bool {
	if baselineIsUnknown(target) {
		return true
	}
	key := semverKey(target)
	v := semverKey(version)
	if key == "" || v == "" {
		return true
	}
	if base, err := module.PseudoVersionBase(key); err == nil && base != "" &&
		base == semverKey(upgradeLastRelease()) && semver.Compare(v, base) > 0 {
		return true
	}
	if i := strings.IndexByte(key, '-'); i >= 0 {
		key = key[:i]
	}
	return semver.Compare(key, v) >= 0
}

// upgradeLastRelease is the release this binary descends from (targetReaches).
// A var so tests pin it instead of tracking the VERSION file.
var upgradeLastRelease = buildinfo.PublishedFloor

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

// runCodemodSteps is runCodemodChain keeping each codemod's own report, for the
// upgrade report to name which codemod rewrote what.
func runCodemodSteps(projectDir, from, to string, write bool) ([]upgradeCodemodRun, error) {
	out := []upgradeCodemodRun{}
	for _, c := range upgradeCodemods(from, to) {
		rep, err := c.Run(projectDir, codemod.Options{Write: write})
		if err != nil {
			return out, fmt.Errorf("codemod %s (%s): %w", c.Name, c.Version, err)
		}
		out = append(out, upgradeCodemodRun{Name: c.Name, Version: c.Version, Source: "upgrade",
			Rewrites: nonNil(rep.Rewrites), Next: nonNil(rep.Next), Blocked: nonNil(rep.Blocked)})
	}
	return out, nil
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
