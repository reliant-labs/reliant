package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkScratch_SkipsOnlyTheFsyncUnderTheMarkedTree(t *testing.T) {
	synced := 0
	prev := syncFile
	syncFile = func(f *os.File) error { synced++; return prev(f) }
	t.Cleanup(func() { syncFile = prev })

	scratch, durable := t.TempDir(), t.TempDir()
	unmark := MarkScratch(scratch)

	write := func(dir string) {
		t.Helper()
		p := filepath.Join(dir, "sub", "f.txt")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(p); err != nil || string(b) != "x" {
			t.Fatalf("%s = %q, %v; the write must still land", p, b, err)
		}
	}

	write(scratch)
	if synced != 0 {
		t.Fatalf("a write under a scratch tree fsynced %d time(s)", synced)
	}
	write(durable)
	if synced != 1 {
		t.Fatalf("a write outside it fsynced %d time(s), want 1", synced)
	}
	// A sibling that merely shares the prefix is not inside the tree.
	sibling := scratch + "-sibling"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sibling) })
	write(sibling)
	if synced != 2 {
		t.Fatalf("a path sharing only the prefix was treated as scratch (syncs=%d)", synced)
	}

	unmark()
	write(scratch)
	if synced != 3 {
		t.Fatalf("after unmark a write fsynced %d time(s) in total, want 3", synced)
	}
}
