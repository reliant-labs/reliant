package checksums

import (
	"path/filepath"
	"strings"
)

// slashKey is relPath in the one form every path-keyed set in this package
// stores: forward slashes.
//
// The keys are not filesystem paths; they are project-relative identities.
// They are written to .forge/disowned.json and .forge/hashes.json (committed,
// and shared by every OS that checks the repo out), produced by ScanMarkers
// (which ToSlash-es its walk), and compared against the run sets that the
// writers below populate. Callers, though, build relPath with filepath.Join —
// backslash-separated on Windows. Storing that verbatim gave one file two
// identities, and every lookup across the two missed:
//
//   - a disowned file was overwritten, because IsDisowned saw
//     internal\x\x.go while the record said internal/x/x.go;
//   - the stale sweep flagged a mock written this very run, because
//     WrittenThisRun held the other spelling;
//   - a failed run's rewind DELETED a file it had just captured, because
//     RemoveJournaled and the later rewrite each journaled it under a
//     different key and the second capture recorded it as absent.
//
// So every exported entry point that takes a relPath normalizes it here, once,
// and on POSIX this is a no-op. filepath.Join(root, slashKey(p)) still yields
// a native path on Windows, which accepts forward slashes throughout.
func slashKey(relPath string) string {
	return filepath.ToSlash(relPath)
}

// slashKeys re-keys a state file's map by slashKey's form, for Load.
//
// The state files are committed and read on every OS, and a Windows forge
// that predates slashKey wrote backslashed keys into them. So a backslash
// is rewritten here on EVERY host — filepath.ToSlash would leave it alone on
// POSIX, where the file is just as likely to be read — rather than trusting
// the writer. A repo-relative path forge records never contains a literal
// backslash on purpose.
func slashKeys[V any](m map[string]V) map[string]V {
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[strings.ReplaceAll(k, `\`, "/")] = v
	}
	return out
}
