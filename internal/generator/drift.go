// The project drift probe — the ONE answer to "which forge-owned files
// were hand-edited after forge wrote them?".
//
// checksums.ScanTier1Drift is the raw signal: every self-certifying file
// whose embedded forge:hash marker fails to verify, plus every
// comment-incapable output whose recorded body hash mismatches. That raw
// set needs exactly one adjustment before any consumer may act on it:
// the Tier-2-managed starters (Dockerfile, Taskfile.yml, .golangci.yml,
// the one-shot .github scaffolds …) carry a marker so `forge project
// upgrade` can tell "still pristine" from "user customized it" — editing
// them is SANCTIONED, so their drift is not drift.
//
// This file owns that adjustment, and it lives here (next to
// Tier2ManagedPaths, the registry that defines the exemption) rather than
// in any one command so that every gate reads the same set:
//
//   - `forge generate`'s pre-pipeline stomp guard (internal/cli),
//   - `forge lint`'s generated-file drift gate (internal/cli/lint),
//   - `forge project audit`'s codegen category,
//   - `forge ci verify-generated`.
//
// A finding that gates one surface and not another is a bug, not a
// policy — hence one probe.
package generator

import (
	"path/filepath"

	"github.com/reliant-labs/forge/internal/checksums"
)

// tier2ManagedPathSet is a single-load cache of the Tier-2 managed path
// set. It is constant for the binary's lifetime: Tier2ManagedPaths()
// derives it from compiled-in template registries.
var tier2ManagedPathSet = Tier2ManagedPaths()

// IsTier2Managed reports whether relPath's canonical template tier is
// Tier-2 (user-owned after the first write). Hand-edits there are
// sanctioned and must never be reported as drift.
func IsTier2Managed(relPath string) bool { return tier2ManagedPathSet[filepath.ToSlash(relPath)] }

// FilterTier2Managed drops drift entries for Tier-2-managed paths.
func FilterTier2Managed(drift []Tier1DriftEntry) []Tier1DriftEntry {
	kept := drift[:0:0]
	for _, d := range drift {
		if IsTier2Managed(d.Path) {
			continue
		}
		kept = append(kept, d)
	}
	return kept
}

// ScanProjectDrift returns every forge-owned file under root whose
// on-disk bytes are positive evidence of a hand-edit, minus the
// Tier-2-managed exemption.
//
// What it CANNOT report, by construction — the false-positive classes
// every gate depends on being absent:
//
//   - scaffold-once ("yours") files carry no marker at all, so they never
//     enter the scan;
//   - disowned paths are skipped (and disowning strips the marker anyway);
//   - files forge never wrote carry no marker;
//   - a pristine render of an OLDER vintage verifies against its own
//     embedded hash, so "the user hasn't re-run generate yet" is Pristine,
//     not drift;
//   - formatter-only byte churn on .go files re-hashes to the certified
//     render after canonical formatting and is likewise Pristine.
func ScanProjectDrift(root string, cs *FileChecksums) []Tier1DriftEntry {
	return FilterTier2Managed(checksums.ScanTier1Drift(root, cs))
}
