// Package generator: checksums shim.
//
// The canonical ownership-state type (FileChecksums: disowned paths +
// the scoped unstampable-format hashes) and the self-certification
// write helpers live in internal/checksums so that internal/codegen
// (per-feature emitters) can also import them without an import cycle
// (generator already imports codegen). Existing call sites in the cli/
// and generator/ packages continue to work via the re-exports below.
package generator

import (
	"github.com/reliant-labs/forge/internal/checksums"
)

// FileChecksums is re-exported as a type alias from internal/checksums.
type FileChecksums = checksums.FileChecksums

// DisownedEntry is re-exported as a type alias from internal/checksums.
type DisownedEntry = checksums.DisownedEntry

// LoadChecksums loads the project ownership state
// (.forge/disowned.json + .forge/hashes.json).
func LoadChecksums(root string) (*FileChecksums, error) {
	return checksums.Load(root)
}

// SaveChecksums persists the project ownership state. Empty state
// deletes the files — no bookkeeping churn in the steady state.
func SaveChecksums(root string, cs *FileChecksums) error {
	return checksums.Save(root, cs)
}

// HashContent returns the sha256 hex digest of content.
func HashContent(content []byte) string {
	return checksums.Hash(content)
}

// WriteGeneratedFile writes a Tier-1 file through the certification
// chokepoint. See checksums.WriteGeneratedFile for full semantics.
func WriteGeneratedFile(root, relPath string, content []byte, cs *FileChecksums, force bool) (bool, error) {
	return checksums.WriteGeneratedFile(root, relPath, content, cs, force)
}

// WriteGeneratedFileTier1 writes a Tier-1 (regenerated-every-run,
// self-certifying) file. See checksums.WriteGeneratedFileTier1.
func WriteGeneratedFileTier1(root, relPath string, content []byte, cs *FileChecksums, force bool) (bool, error) {
	return checksums.WriteGeneratedFileTier1(root, relPath, content, cs, force)
}

// WriteScaffoldIfMissing writes a scaffold ("yours", user-owned) file
// only when it does not already exist; once on disk forge never
// overwrites it. See checksums.WriteScaffoldIfMissing.
func WriteScaffoldIfMissing(root, relPath string, content []byte) (bool, error) {
	return checksums.WriteScaffoldIfMissing(root, relPath, content)
}

// Tier1DriftEntry is re-exported as a type alias from internal/checksums.
type Tier1DriftEntry = checksums.Tier1DriftEntry
