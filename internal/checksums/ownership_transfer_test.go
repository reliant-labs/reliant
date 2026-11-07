package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnershipTransferRequiresPreservedBytesAndRunIdentity(t *testing.T) {
	ResetPerRunState()
	t.Cleanup(ResetPerRunState)
	root := t.TempDir()
	old := []byte("edited content")
	if err := os.WriteFile(filepath.Join(root, "owned.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	if PreservedOwnershipTransfer(root, "legacy.json", BodyHash(old)) {
		t.Fatal("unrecorded transfer accepted")
	}
	RecordPreservedOwnershipTransfer(root, "legacy.json", "owned.json", old)
	if !PreservedOwnershipTransfer(root, "legacy.json", BodyHash(old)) {
		t.Fatal("preserved transfer not recognized")
	}
	otherRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherRoot, "owned.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	if PreservedOwnershipTransfer(otherRoot, "legacy.json", BodyHash(old)) {
		t.Fatal("cross-project receipt accepted")
	}
	if PreservedOwnershipTransfer(root, "legacy.json", BodyHash([]byte("other edit"))) {
		t.Fatal("different quarantined edit accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "owned.json"), []byte("lost edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if PreservedOwnershipTransfer(root, "legacy.json", BodyHash(old)) {
		t.Fatal("lost edit accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "legacy.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	if !PreservedOwnershipTransfer(root, "legacy.json", BodyHash(old)) {
		t.Fatal("retained independent legacy edit not recognized")
	}
	ResetPerRunState()
	if PreservedOwnershipTransfer(root, "legacy.json", BodyHash(old)) {
		t.Fatal("stale prior-run transfer accepted")
	}
}
