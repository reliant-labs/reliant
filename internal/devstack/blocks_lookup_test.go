package devstack

import (
	"os"
	"path/filepath"
	"testing"
)

// LookupBlock is the read-only half of the registry: it answers for a key that
// holds a block and reports found=false for one that does not, and in neither
// case does it write the registry or create the lock — even past the ceiling,
// where an allocation would be refused.
func TestLookupBlockNeverWrites(t *testing.T) {
	dir := t.TempDir()
	SetMaxStacks(2)
	t.Cleanup(func() { SetMaxStacks(0) })

	if _, found, err := LookupBlock(dir, "wt-a"); err != nil || found {
		t.Fatalf("empty registry: found=%v err=%v, want not found", found, err)
	}
	if _, err := os.Stat(registryPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("LookupBlock created the registry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, lockRel)); !os.IsNotExist(err) {
		t.Fatalf("LookupBlock created the lock: %v", err)
	}

	if _, err := AllocateBlock(dir, "wt-a"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(registryPath(dir))

	block, found, err := LookupBlock(dir, "wt-a")
	if err != nil || !found || block != 1 {
		t.Fatalf("LookupBlock(wt-a) = %d, %v, %v; want 1, true, nil", block, found, err)
	}
	// Past the ceiling (2): AllocateBlock would refuse wt-b; LookupBlock
	// just says it has none.
	if _, found, err := LookupBlock(dir, "wt-b"); err != nil || found {
		t.Fatalf("LookupBlock(wt-b) past the ceiling: found=%v err=%v", found, err)
	}
	after, _ := os.ReadFile(registryPath(dir))
	if string(before) != string(after) {
		t.Fatalf("LookupBlock rewrote the registry:\nbefore %s\nafter  %s", before, after)
	}
	if _, _, err := LookupBlock(dir, "prod-"); err == nil {
		t.Fatalf("LookupBlock must validate the key like an allocation does")
	}
}
