package checksums

import (
	"bytes"
	"encoding/json"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// MergeStateFile three-way merges one of the committed state files under
// .forge/ (disowned.json, the legacy scaffolded.json, a certificate under
// hashes/, a ledger entry under scaffolded/) key by key, and
// renders the result exactly as forge's own writers do.
//
// WHY. Every branch that adds a service records its births in the scaffold
// ledger and the hashes of its JSON outputs, so two such branches always
// change the same small files. git merges them as lines, and adjacent
// insertions — two services sorting between the same two existing entries,
// or two outputs' hashes that sort side by side — conflict on every
// merge and every rebase. The P3b pilot, 33 parallel lanes each adopting one
// service, resolved them by hand in each lane (F16). The files are maps:
// merged as maps, the only collision left is one key whose value both sides
// changed, and for these files that has a forge answer:
//
//   - a ledger entry under scaffolded/ (and the legacy scaffolded.json, key
//     by key): a birth recorded on either side is recorded; one
//     side's deletion (rescaffold) stands when the other left the entry
//     alone. Both sides recording the same path keep this side's entry —
//     the timestamp is provenance nobody branches on.
//   - a certificate under .forge/hashes/ (and the legacy hashes.json, key
//     by key): both sides re-rendered the same comment-incapable output. The
//     merge driver keeps THIS side's bytes for that output (see the cli merge
//     driver), so this side's hash is the one that certifies them, and the
//     next `forge generate` renders the merged inputs over a file it can
//     still verify. One side's change alone (or deletion) stands.
//   - disowned.json: both sides changed one disown's reason; keep this
//     side's.
//
// ok is false when relPath is none of these files, or a side does not parse
// as one — the caller then merges it as text, the historical behaviour, so
// nothing is lost to a format this function does not know.
func MergeStateFile(relPath string, base, ours, theirs []byte) (merged []byte, ok bool) {
	if isCertPathAnywhere(relPath) {
		return mergeHashCert(base, ours, theirs)
	}
	if isScaffoldEntryAnywhere(relPath) {
		return mergeScaffoldEntry(base, ours, theirs)
	}
	if path.Base(path.Dir(path.Clean("/"+relPath))) != ".forge" {
		return nil, false
	}
	switch path.Base(relPath) {
	case path.Base(LegacyScaffoldedFile):
		var b, o, t scaffoldedJSON
		if !decodeState(base, &b) || !decodeState(ours, &o) || !decodeState(theirs, &t) {
			return nil, false
		}
		out := scaffoldedJSON{
			Files:          mergeKeyed(b.Files, o.Files, t.Files),
			AbsentReported: mergeSet(b.AbsentReported, o.AbsentReported, t.AbsentReported),
		}
		return encodeState(out)
	case path.Base(LegacyHashesFile):
		var b, o, t hashesJSON
		if !decodeState(base, &b) || !decodeState(ours, &o) || !decodeState(theirs, &t) {
			return nil, false
		}
		return encodeState(hashesJSON{Files: mergeKeyed(b.Files, o.Files, t.Files)})
	case path.Base(DisownedFile):
		var b, o, t disownedJSON
		if !decodeState(base, &b) || !decodeState(ours, &o) || !decodeState(theirs, &t) {
			return nil, false
		}
		return encodeState(disownedJSON{Files: mergeKeyed(b.Files, o.Files, t.Files)})
	}
	return nil, false
}

// isCertPathAnywhere reports whether relPath, relative to the top of the
// work tree (the project may sit below it), is a certificate file.
func isCertPathAnywhere(relPath string) bool {
	rel := path.Clean("/" + filepath.ToSlash(relPath))
	i := strings.LastIndex(rel, "/"+HashesDir+"/")
	return i >= 0 && IsHashCert(rel[i+1:])
}

// isScaffoldEntryAnywhere reports whether relPath, relative to the top of
// the work tree, is a scaffold ledger entry file.
func isScaffoldEntryAnywhere(relPath string) bool {
	rel := path.Clean("/" + filepath.ToSlash(relPath))
	i := strings.LastIndex(rel, "/"+ScaffoldedDir+"/")
	if i < 0 {
		return false
	}
	_, ok := ScaffoldEntryTarget(rel[i+1:])
	return ok
}

// mergeScaffoldEntry merges one scaffold ledger entry: a side that changed
// it (or deleted it: a rescaffold) wins over one that did not; when both
// changed it, this side's stands — a birth is a birth, and the timestamp is
// provenance nobody branches on. A side that does not parse leaves the file
// to the text merge.
func mergeScaffoldEntry(base, ours, theirs []byte) ([]byte, bool) {
	for _, side := range [][]byte{base, ours, theirs} {
		var e ScaffoldEntry
		if len(bytes.TrimSpace(side)) > 0 && json.Unmarshal(side, &e) != nil {
			return nil, false
		}
	}
	if bytes.Equal(bytes.TrimSpace(ours), bytes.TrimSpace(base)) {
		return theirs, true
	}
	return ours, true
}

// mergeHashCert merges one certificate file: a side that changed it (or
// deleted it) wins over one that did not, and when both changed it, this
// side's stands — the driver keeps this side's render of the output it
// certifies. A side that does not parse leaves the file to the text merge.
func mergeHashCert(base, ours, theirs []byte) ([]byte, bool) {
	for _, side := range [][]byte{base, ours, theirs} {
		if len(bytes.TrimSpace(side)) == 0 {
			continue
		}
		if _, ok := ParseHashCert(side); !ok {
			return nil, false
		}
	}
	same := func(a, b []byte) bool { return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b)) }
	if same(ours, base) {
		return theirs, true
	}
	return ours, true
}

// decodeState unmarshals one side. An empty side is the empty state — the
// base of an add/add merge, or a side that deleted the file.
func decodeState(data []byte, v any) bool {
	if len(bytes.TrimSpace(data)) == 0 {
		return true
	}
	return json.Unmarshal(data, v) == nil
}

// encodeState renders v the way saveStateFile and the ledger's save do.
func encodeState(v any) ([]byte, bool) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, false
	}
	return append(data, '\n'), true
}

// mergeKeyed is the per-key three-way merge: a key one side changed (added,
// edited or deleted) takes that side's state; a key both sides changed
// alike takes it; a key both changed differently keeps ours.
func mergeKeyed[V any](base, ours, theirs map[string]V) map[string]V {
	keys := map[string]bool{}
	for _, m := range []map[string]V{base, ours, theirs} {
		for k := range m {
			keys[k] = true
		}
	}
	out := map[string]V{}
	for k := range keys {
		b, inB := base[k]
		o, inO := ours[k]
		t, inT := theirs[k]
		oursChanged := inO != inB || (inO && !reflect.DeepEqual(o, b))
		theirsChanged := inT != inB || (inT && !reflect.DeepEqual(t, b))
		switch {
		case theirsChanged && !oursChanged:
			if inT {
				out[k] = t
			}
		case inO:
			out[k] = o
		}
	}
	return out
}

// mergeSet is mergeKeyed over a sorted string set.
func mergeSet(base, ours, theirs []string) []string {
	asMap := func(s []string) map[string]bool {
		m := map[string]bool{}
		for _, v := range s {
			m[v] = true
		}
		return m
	}
	merged := mergeKeyed(asMap(base), asMap(ours), asMap(theirs))
	if len(merged) == 0 {
		return nil
	}
	out := make([]string, 0, len(merged))
	for v := range merged {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
