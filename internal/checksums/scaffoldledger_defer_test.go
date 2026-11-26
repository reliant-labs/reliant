package checksums

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ledgerFileBytes is the whole on-disk ledger — every entry file under
// ScaffoldedDir, by path, plus a legacy one-file ledger — as one byte
// string: nil when there is none.
func ledgerFileBytes(t *testing.T, root string) []byte {
	t.Helper()
	var out []byte
	if b, err := os.ReadFile(filepath.Join(root, LegacyScaffoldedFile)); err == nil {
		out = append(append(out, LegacyScaffoldedFile+"\n"...), b...)
	}
	_ = filepath.WalkDir(filepath.Join(root, ScaffoldedDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out = append(append(out, filepath.ToSlash(rel)+"\n"...), b...)
		return nil
	})
	return out
}

// A deferred batch answers from memory, writes the file once when it closes,
// and leaves exactly the bytes immediate writes would have.
func TestDeferScaffoldLedger_WritesOnceAtTheEnd(t *testing.T) {
	record := func(root string) {
		RecordScaffold(root, "a.txt")
		RecordScaffoldWithHash(root, "b.txt", []byte("b"))
		RecordScaffold(root, "c.txt")
	}
	// Timestamps differ between the two runs, so compare the recorded paths
	// and whether b.txt's hash was kept rather than raw bytes.
	snapshot := func(root string) []string {
		paths := ScaffoldLedgerPaths(root)
		if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !ScaffoldIsPristine(root, "b.txt") {
			t.Errorf("%s: b.txt's birth hash was not kept", root)
		}
		return paths
	}

	immediate := t.TempDir()
	ResetScaffoldLedgerCache()
	t.Cleanup(ResetScaffoldLedgerCache)
	record(immediate)
	want := snapshot(immediate)

	deferred := t.TempDir()
	ResetScaffoldLedgerCache()
	flush := DeferScaffoldLedger(deferred)
	record(deferred)
	if !ScaffoldRecorded(deferred, "a.txt") || !ScaffoldRecorded(deferred, "c.txt") {
		t.Fatal("a deferred batch must answer from the in-memory ledger")
	}
	if b := ledgerFileBytes(t, deferred); b != nil {
		t.Fatalf("the ledger file was written inside the batch:\n%s", b)
	}
	flush()
	if ledgerFileBytes(t, deferred) == nil {
		t.Fatal("closing the batch did not write the ledger")
	}
	ResetScaffoldLedgerCache()
	got := snapshot(deferred)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("deferred ledger has %v, immediate has %v", got, want)
	}
}

func TestDeferScaffoldLedger_NestsAndWritesNothingWhenNothingChanged(t *testing.T) {
	root := t.TempDir()
	ResetScaffoldLedgerCache()
	t.Cleanup(ResetScaffoldLedgerCache)

	outer := DeferScaffoldLedger(root)
	inner := DeferScaffoldLedger(root)
	RecordScaffold(root, "a.txt")
	inner()
	if ledgerFileBytes(t, root) != nil {
		t.Fatal("closing the inner batch wrote the ledger while the outer one was open")
	}
	outer()
	if ledgerFileBytes(t, root) == nil {
		t.Fatal("closing the outermost batch did not write the ledger")
	}

	quiet := t.TempDir()
	DeferScaffoldLedger(quiet)()
	if ledgerFileBytes(t, quiet) != nil {
		t.Fatal("a batch that recorded nothing wrote a ledger")
	}
}

// A cache reset inside a batch must not turn the close into a write of an
// empty ledger over the real one.
func TestDeferScaffoldLedger_ResetInsideTheBatchDoesNotEmptyTheFile(t *testing.T) {
	root := t.TempDir()
	ResetScaffoldLedgerCache()
	t.Cleanup(ResetScaffoldLedgerCache)
	RecordScaffold(root, "kept.txt")

	flush := DeferScaffoldLedger(root)
	RecordScaffold(root, "new.txt")
	ResetScaffoldLedgerCache()
	flush()
	if !ScaffoldRecorded(root, "kept.txt") {
		t.Fatal("the ledger on disk lost an entry recorded before the batch")
	}
}
