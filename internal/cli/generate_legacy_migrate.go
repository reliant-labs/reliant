// One-time pipeline migration off the legacy .forge/checksums.json
// global manifest onto self-certifying files (forge:hash markers).
//
// The conversion rules live in internal/checksums/migrate.go. This file
// owns the pipeline integration:
//
//   - stepMigrateLegacyManifest runs between state load and the Tier-1
//     stomp guard. Pristine legacy entries get stamped; disowned ones
//     convert to .forge/disowned.json; entries whose bytes match
//     NOTHING the manifest recorded (kalshi fr-9a54388f0b: a manifest
//     committed from a different work lane) are quarantined on
//     ctx.LegacyUnverified with their writes side-render redirected.
//   - finishLegacyMigration runs after the step loop: each quarantined
//     path whose fresh side render matches the on-disk bytes is proven
//     pristine and stamped; everything else is stamped with the
//     unverified-legacy sentinel and reported through the standard
//     drift error (guard semantics: --force regenerates exactly the
//     named files, `forge project disown` keeps them).
//
// The legacy manifest is deleted by the migration itself — durable even
// when the run later aborts. The unverified sentinel keeps the guard
// honest across runs until the user resolves each file.
package cli

import (
	"fmt"
	"os"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/generator"
)

// legacyMigrationStampable reports whether the migration may certify a
// legacy Tier-1 entry at relPath. Paths whose canonical template tier
// is Tier-2 (user-owned starters: Dockerfile, Taskfile.yml,
// .github/CODEOWNERS, …) are excluded — they are user-owned from
// birth, and a certification marker there would misrepresent
// sanctioned edits as drift.
func legacyMigrationStampable(relPath string) bool {
	return !generator.IsTier2Managed(relPath)
}

// stepMigrateLegacyManifest performs the one-time conversion when a
// legacy manifest is present. No-op otherwise (the steady state).
func stepMigrateLegacyManifest(ctx *pipelineContext) error {
	outcome, err := checksums.MigrateLegacyManifest(ctx.AbsPath, ctx.Checksums, legacyMigrationStampable)
	if err != nil {
		return fmt.Errorf("legacy checksums migration: %w", err)
	}
	if outcome == nil {
		return nil
	}

	fmt.Printf("📜 Migrating off the legacy .forge/checksums.json (%d entries) — generated files are self-certifying now (forge:hash markers):\n", outcome.Total())
	if n := len(outcome.Stamped); n > 0 {
		fmt.Printf("   ✓ %d pristine file(s) stamped with their embedded content hash\n", n)
	}
	if n := len(outcome.Fallback); n > 0 {
		fmt.Printf("   ✓ %d comment-incapable file(s) recorded in %s\n", n, checksums.HashesFile)
	}
	if n := len(outcome.DisownedConverted); n > 0 {
		fmt.Printf("   ✓ %d disowned file(s) converted to %s\n", n, checksums.DisownedFile)
	}
	if n := len(outcome.DroppedTier2) + len(outcome.DroppedUnknown); n > 0 {
		fmt.Printf("   ✓ %d user-owned file(s) released (scaffold-once starters and retired paths need no record)\n", n)
	}
	if n := len(outcome.MissingOnDisk); n > 0 {
		fmt.Printf("   ✓ %d tracked-but-deleted path(s) forgotten\n", n)
	}

	if len(outcome.Unverified) > 0 {
		// Kalshi's exact mess: the manifest was recorded from a
		// different lane than these bytes. Quarantine: redirect their
		// writes to side renders so finishLegacyMigration can compare
		// against a fresh render of the current templates before
		// deciding pristine vs hand-edited.
		fmt.Fprintf(os.Stderr, "⚠️  %d file(s) match nothing the legacy manifest recorded (it was likely committed from a different work lane). Each gets one fresh-render comparison this run:\n", len(outcome.Unverified))
		for _, p := range outcome.Unverified {
			fmt.Fprintf(os.Stderr, "   ? %s\n", p)
			checksums.AddSideRenderOnly(p)
		}
		ctx.LegacyUnverified = outcome.Unverified
	}
	fmt.Printf("   ✓ legacy manifest deleted — commit the file deletions/stamps along with your next change\n")
	return nil
}

// finishLegacyMigration adjudicates the quarantined paths after the
// emitters have run. Returns the standard drift-shaped error when any
// path stays unverified; nil when everything was rescued (or nothing
// was quarantined).
func finishLegacyMigration(ctx *pipelineContext) error {
	if len(ctx.LegacyUnverified) == 0 {
		return nil
	}
	var unresolved []checksums.Tier1DriftEntry
	rescued := 0
	for _, p := range ctx.LegacyUnverified {
		if checksums.RescueUnverified(ctx.AbsPath, p) {
			rescued++
			continue
		}
		// Stamp the sentinel so the stomp guard keeps naming this file
		// on every subsequent run until the user resolves it.
		checksums.StampUnverified(ctx.AbsPath, p)
		unresolved = append(unresolved, checksums.Tier1DriftEntry{Path: p, Unverified: true})
	}
	if rescued > 0 {
		fmt.Printf("   ✓ %d quarantined file(s) matched a fresh render — pristine, stamped\n", rescued)
	}
	if len(unresolved) == 0 {
		ctx.LegacyUnverified = nil
		return nil
	}
	return fmt.Errorf("%s\n%s", tier1DriftSummaryLine(unresolved), formatTier1DriftReport(unresolved))
}
