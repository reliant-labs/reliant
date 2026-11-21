package checksums

import (
	"testing"
)

func TestMergeStateFile_OnlyForgesStateFiles(t *testing.T) {
	if _, ok := MergeStateFile("deploy/disowned.json", nil, nil, nil); ok {
		t.Error("a disowned.json outside .forge/ is not forge's")
	}
	if _, ok := MergeStateFile(".forge/scaffolded.json", nil, nil, nil); ok {
		t.Error("the retired one-file scaffold ledger is no longer a state file")
	}
	if _, ok := MergeStateFile(".forge/disowned.json", []byte("{"), nil, nil); ok {
		t.Error("an unparseable side falls back to the line merge")
	}
	// An add/add merge has an empty base.
	if _, ok := MergeStateFile(".forge/disowned.json", nil, []byte(`{"files":{"a":{"reason":"x"}}}`), []byte(`{"files":{"b":{"reason":"y"}}}`)); !ok {
		t.Error("an empty base is the empty state")
	}
}
