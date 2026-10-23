// Tier-2 (user-owned-after-scaffold) exemption for the drift scan.
//
// Some forge-certified files are deliberately NOT stomp-guarded even
// though they carry a forge:hash marker: the upgrade-managed
// "checksum-protected" starters (Dockerfile, Taskfile.yml,
// .golangci.yml, …) and the one-shot .github scaffolds. The marker's
// job there is to let `forge project upgrade` distinguish "still the pristine
// scaffold" (auto-update on version bumps) from "user customized it"
// (skip) — editing them is SANCTIONED, so a failed verification must
// not abort `forge generate`.
//
// The probe itself lives in internal/generator (drift.go), next to
// Tier2ManagedPaths — the registry that defines the exemption — so that
// every gate that reports hand-edited generated files (this stomp guard,
// `forge lint`'s generated-file drift gate, `forge project audit`,
// `forge ci verify-generated`) reads one set and tells one story. This
// file is the pipeline-side handle on it.
//
// (Historical note: this file used to flip stale tier=1 manifest
// entries to tier=2. The manifest is gone — the reclassification story
// is now: the legacy-manifest migration never stamps Tier-2-managed
// paths, the Tier-2 writer un-stamps reclassified pipeline outputs on
// their next scaffold pass, and this exemption covers the rest.)
package cli

import (
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/generator"
)

// scanProjectDrift is the shared drift probe: the raw self-certification
// scan minus the Tier-2-managed exemption.
func scanProjectDrift(root string, cs *generator.FileChecksums) []checksums.Tier1DriftEntry {
	return generator.ScanProjectDrift(root, cs)
}
