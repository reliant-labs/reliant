package checksums

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func ledgerBytes(t *testing.T, files map[string]ScaffoldEntry, absent ...string) []byte {
	t.Helper()
	data, ok := encodeState(scaffoldedJSON{Files: files, AbsentReported: absent})
	if !ok {
		t.Fatal("encode")
	}
	return data
}

// gitMergeFileConflicts runs git's own three-way line merge, the baseline
// the state merge replaces.
func gitMergeFileConflicts(t *testing.T, base, ours, theirs []byte) bool {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string][]byte{"base": base, "ours": ours, "theirs": theirs} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := exec.Command("git", "merge-file", "-p", filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs")).Run()
	return err != nil
}

// Two lanes each adopting a service record births that sort between the
// same two existing entries: git's line merge conflicts on every such pair.
// Merged as a map, both land, rendered exactly as forge writes the ledger.
func TestMergeStateFile_LedgerBirthsFromBothSides(t *testing.T) {
	e := ScaffoldEntry{BirthHash: "b"}
	base := ledgerBytes(t, map[string]ScaffoldEntry{".github/workflows/ci.yml": e, "internal/handlers/system/a_test.go": e})
	ours := ledgerBytes(t, map[string]ScaffoldEntry{".github/workflows/ci.yml": e, "internal/handlers/question/q_test.go": e, "internal/handlers/system/a_test.go": e})
	theirs := ledgerBytes(t, map[string]ScaffoldEntry{".github/workflows/ci.yml": e, "internal/handlers/run/r_test.go": e, "internal/handlers/system/a_test.go": e}, "internal/gone.go")
	if !gitMergeFileConflicts(t, base, ours, theirs) {
		t.Fatal("precondition: git's line merge conflicts on adjacent births")
	}
	merged, ok := MergeStateFile(".forge/scaffolded.json", base, ours, theirs)
	if !ok {
		t.Fatal("the ledger must merge as a map")
	}
	want := ledgerBytes(t, map[string]ScaffoldEntry{
		".github/workflows/ci.yml": e, "internal/handlers/question/q_test.go": e,
		"internal/handlers/run/r_test.go": e, "internal/handlers/system/a_test.go": e,
	}, "internal/gone.go")
	if string(merged) != string(want) {
		t.Errorf("merged ledger:\n%s\nwant:\n%s", merged, want)
	}
}

// A one-sided deletion (rescaffold) stands; a key both sides changed keeps
// ours — for hashes.json that is the hash of the bytes the driver keeps.
func TestMergeStateFile_HashesPerKey(t *testing.T) {
	enc := func(m map[string]string) []byte {
		data, _ := encodeState(hashesJSON{Files: m})
		return data
	}
	base := enc(map[string]string{"spec_gen.json": "b", "gone_gen.json": "g", "kept_gen.json": "k"})
	ours := enc(map[string]string{"spec_gen.json": "o", "kept_gen.json": "k", "q_gen.json": "q"})
	theirs := enc(map[string]string{"spec_gen.json": "t", "gone_gen.json": "g", "kept_gen.json": "k2", "r_gen.json": "r"})
	merged, ok := MergeStateFile("sub/.forge/hashes.json", base, ours, theirs)
	if !ok {
		t.Fatal("hashes.json must merge as a map")
	}
	var got hashesJSON
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"spec_gen.json": "o", "kept_gen.json": "k2", "q_gen.json": "q", "r_gen.json": "r"}
	if !reflect.DeepEqual(got.Files, want) {
		t.Errorf("merged hashes = %v, want %v", got.Files, want)
	}
}

func TestMergeStateFile_OnlyForgesStateFiles(t *testing.T) {
	if _, ok := MergeStateFile("deploy/hashes.json", nil, nil, nil); ok {
		t.Error("a hashes.json outside .forge/ is not forge's")
	}
	if _, ok := MergeStateFile(".forge/scaffolded.json", []byte("{"), nil, nil); ok {
		t.Error("an unparseable side falls back to the line merge")
	}
	// An add/add merge has an empty base.
	if _, ok := MergeStateFile(".forge/disowned.json", nil, []byte(`{"files":{"a":{"reason":"x"}}}`), []byte(`{"files":{"b":{"reason":"y"}}}`)); !ok {
		t.Error("an empty base is the empty state")
	}
}
