package checksums

import (
	"bytes"
	"encoding/json"
	"path"
	"reflect"
	"sort"
)

// MergeStateFile three-way merges one of the committed state files under
// .forge/ (scaffolded.json, hashes.json, disowned.json) key by key, and
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
//   - scaffolded.json: a birth recorded on either side is recorded; one
//     side's deletion (rescaffold) stands when the other left the entry
//     alone. Both sides recording the same path keep this side's entry —
//     the timestamp is provenance nobody branches on.
//   - hashes.json: both sides re-rendered the same comment-incapable output
//     (an aggregate of every service). The merge driver keeps THIS side's bytes for
//     that output (see the cli merge driver), so this side's hash is the one
//     that certifies them, and the next `forge generate` renders the merged
//     inputs over a file it can still verify.
//   - disowned.json: both sides changed one disown's reason; keep this
//     side's.
//
// ok is false when relPath is not one of the three files, or a side does
// not parse as one — the caller then merges it as text, the historical
// behaviour, so nothing is lost to a format this function does not know.
func MergeStateFile(relPath string, base, ours, theirs []byte) (merged []byte, ok bool) {
	if path.Base(path.Dir(path.Clean("/"+relPath))) != ".forge" {
		return nil, false
	}
	switch path.Base(relPath) {
	case path.Base(ScaffoldedFile):
		var b, o, t scaffoldedJSON
		if !decodeState(base, &b) || !decodeState(ours, &o) || !decodeState(theirs, &t) {
			return nil, false
		}
		out := scaffoldedJSON{
			Files:          mergeKeyed(b.Files, o.Files, t.Files),
			AbsentReported: mergeSet(b.AbsentReported, o.AbsentReported, t.AbsentReported),
		}
		return encodeState(out)
	case path.Base(HashesFile):
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
