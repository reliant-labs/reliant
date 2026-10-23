package checksums

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// RescueFreshRender settles a Tier-1 drift report against what this run
// would write. The stomp guard reports a file as hand-edited when its bytes
// disagree with forge's record of them — the embedded forge:hash marker, or
// for a comment-incapable format the .forge/hashes.json entry. That record
// can be wrong while the bytes are exactly forge's own:
//
//   - a `forge generate` killed after rewriting outputs but before saving
//     .forge/hashes.json (it saves once, at the end of the run);
//   - a merge that took one side's generated file and the other side's
//     ledger, or git's three-way merge of a stamped file's body under one
//     side's marker.
//
// None of those is a hand-edit, and refusing them made the next run demand
// --force for files no person had touched. So the guard parks a fresh
// render of each drifted path (the side-render redirect) instead of
// failing, and this decides it: when the on-disk bytes are the fresh render
// — modulo the marker line, and for Go modulo canonical formatting — the
// record is repaired to match them. A stamped file is restamped (journaled,
// so a later failure in the run rewinds it); a comment-incapable one gets
// its hashes.json entry, persisted with the rest of the run's state.
//
// Returns false — leaving the drift standing — when no fresh render was
// parked for relPath (its emitter did not run, or forge no longer emits the
// path), when the bytes differ, or when the file cannot be read or
// written. The parked render is removed once consulted.
func RescueFreshRender(root, relPath string, cs *FileChecksums) bool {
	relPath = slashKey(relPath)
	side, err := os.ReadFile(filepath.Join(root, RenderDir, filepath.FromSlash(relPath)))
	if err != nil {
		return false
	}
	defer func() { _ = CleanSideRenders(root, relPath) }()

	full := filepath.Join(root, filepath.FromSlash(relPath))
	onDisk, err := os.ReadFile(full)
	if err != nil {
		return false
	}
	fresh := BodyHash(side)
	if BodyHash(onDisk) != fresh {
		canon, ok := canonicalGoBody(root, relPath, onDisk)
		if !ok || canon != fresh {
			return false
		}
	}

	if !Stampable(relPath) {
		if cs == nil {
			return false
		}
		if cs.Unstampable == nil {
			cs.Unstampable = map[string]string{}
		}
		cs.Unstampable[relPath] = BodyHash(onDisk)
		return true
	}
	stamped, ok := Stamp(relPath, onDisk)
	if !ok {
		return false
	}
	if bytes.Equal(stamped, onDisk) {
		return true
	}
	recordPreWrite(root, relPath)
	return atomicfile.WriteFile(full, stamped, 0o644) == nil
}
