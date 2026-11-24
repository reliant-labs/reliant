package codegen

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// RetireRenamedGenerated deletes the OLD path of a generated file that forge
// has renamed, so the project is left with one copy under the new name rather
// than two that drift apart.
//
// WHY A RENAME NEEDS THIS AT ALL. A generated file's old copy is still on disk
// after the emitter starts writing a new name, and it still carries forge's
// "Code generated — DO NOT EDIT" banner and hash marker. Nothing emits it
// anymore, which makes it exactly what the stale-artifact sweep deletes — but
// the sweep runs after every emitter, and before it the package has two
// declarations of the same symbols and does not compile. Reclaiming it here, in the emitter
// that supersedes it, is what makes a rename a single atomic-looking change to
// the user.
//
// OWNERSHIP IS READ FROM THE FILE, never assumed. A copy that still verifies
// as forge's own render is forge's to remove; a hand-edited one is the user's
// work and is LEFT ALONE, even though that means the project keeps a stale
// file. Deleting a file forge did not write is not a tradeoff worth making to
// tidy a rename: the user can remove it, and forge cannot un-delete it.
//
// A DISOWNED path is neither deleted nor ignored: it is CARRIED. The user
// disowned forge's output; the fact that forge later renamed that output is
// forge's own bookkeeping, not a user action that should void their
// ownership. Leaving the record on the old path was the fr-disown-rename
// bug: the new path looked unowned, so the emitter wrote a second file
// declaring the same symbols beside the user's, silently, and the package
// stopped compiling. CarryDisownAcrossRename moves the user's file (and its
// record) to the new name BEFORE the emitter writes, so the emitter's own
// disown check skips it and a collision cannot occur. See
// checksums/disown_rename.go for the full rationale and the refusal case.
//
// Files with no marker at all fall back to the legacy banner check, which is
// what reaches projects generated before markers existed.
//
// It is deliberately quiet about a path that does not exist — that is the
// common case (a project scaffolded after the rename) and not an event.
func RetireRenamedGenerated(projectDir, oldRelPath string, cs *checksums.FileChecksums) {
	// Every forge rename to date is the same edit: the tier is spelled into
	// the name, `X.ext` → `X_gen.ext`. Deriving the destination here (rather
	// than threading it through ~10 call sites) keeps the retirement helper
	// a one-path API and guarantees the carry-over covers EVERY renamed
	// emitter, including ones added later that follow the convention.
	if newRel := genSuffixedPath(oldRelPath); newRel != "" {
		if carried := checksums.CarryDisownAcrossRename(projectDir, cs, oldRelPath, newRel); carried {
			return // the old path is now the user's file under its new name
		}
	}
	removeRetiredForgeFile(projectDir, oldRelPath, cs)
}

// RetireGenerated deletes a generated file forge has STOPPED EMITTING
// outright — no successor, no rename, the output simply no longer exists.
//
// This is the sibling of RetireRenamedGenerated, and the distinction is the
// carry-over. A rename has a destination, so a disowned copy is MOVED to the
// new name (the user's ownership survives forge's bookkeeping). A removal has
// no destination to carry anything to: the file is either forge's, in which
// case it goes, or the user's, in which case it stays exactly where it is and
// becomes an ordinary hand-maintained module. Routing a removal through the
// rename helper would invent a `_gen` twin nothing writes and move the user's
// file to a name they never chose.
//
// Ownership is read from the file, never assumed — same test as every other
// retirement path: a verifying forge:hash marker certifies the bytes as
// forge's own render and it is removed; a Modified marker means hand-edited
// and it is kept; a disowned path is never touched. A path that does not
// exist is the common case (a project scaffolded after the removal) and is
// silently fine.
func RetireGenerated(projectDir, relPath string, cs *checksums.FileChecksums) {
	removeRetiredForgeFileAt(projectDir, relPath, cs)
}

// genSuffixedPath returns the `_gen`-suffixed twin of relPath — the new name
// forge emits after a tier-spelling rename. Returns "" for a path that is
// already `_gen`-suffixed (nothing to derive) or has no extension.
func genSuffixedPath(relPath string) string {
	slash := filepath.ToSlash(relPath)
	ext := path.Ext(slash)
	if ext == "" {
		return ""
	}
	stem := strings.TrimSuffix(slash, ext)
	if strings.HasSuffix(stem, "_gen") {
		return ""
	}
	return stem + "_gen" + ext
}

// removeRetiredForgeFileAt is the ownership test shared by the retirement
// helpers, split out so the rename path and the CRUD path cannot disagree
// about what "forge's own render" means.
//
// Returns true when the file was removed.
func removeRetiredForgeFileAt(projectDir, relPath string, cs *checksums.FileChecksums) bool {
	if cs != nil && cs.IsDisowned(filepath.ToSlash(relPath)) {
		return false // user-owned by recorded intent
	}
	full := filepath.Join(projectDir, relPath)
	body, err := os.ReadFile(full)
	if err != nil {
		return false
	}
	switch checksums.Verify(body) {
	case checksums.Pristine:
		checksums.RecordPreWriteAbs(full)
		return os.Remove(full) == nil
	case checksums.Modified:
		return false // hand-edited — not forge's to delete
	case checksums.NoMarker:
		// Pre-marker forge output: only sweep what self-identifies.
		if bytes.Contains(body, []byte("Code generated by forge")) {
			checksums.RecordPreWriteAbs(full)
			return os.Remove(full) == nil
		}
	}
	return false
}
