// Tests for the DELETE side of the rollback journal (RemoveJournaled).
//
// The journal was write-only: it captured a path's pre-run bytes at the write
// chokepoint, and a raw os.Remove was invisible to it. That is not merely a
// gap — it INVERTS the rewind, because the journal is lazy and first-write-wins.
// Delete a file without journaling, let a later step rewrite it, and the
// capture that finally runs sees no file and records "absent before this run",
// which RestoreRollback honours by deleting. The rewind that exists to hand
// the tree back clean becomes the thing that removes the file.
//
// TestRemoveJournaled_DeleteThenRewriteStillRestoresOriginal is the one that
// pins that ordering; it is the shape the shared-checkout incident took.
package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// seedFile writes a file with known pre-run bytes and returns its full path.
func seedFile(t *testing.T, root, rel, body string) string {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestRemoveJournaled_RestoresDeletedFile(t *testing.T) {
	root := t.TempDir()
	const rel = "internal/coupon/middleware_gen.go"
	const preRun = "package coupon // the bytes every other package compiles against\n"
	full := seedFile(t, root, rel, preRun)

	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	if err := RemoveJournaled(full); err != nil {
		t.Fatalf("RemoveJournaled: %v", err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("precondition: the file should be gone (stat err = %v)", err)
	}

	restored := RestoreRollback(root)
	if len(restored) != 1 || restored[0] != rel {
		t.Fatalf("restored = %v, want exactly [%s]", restored, rel)
	}
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("aborted run left the deleted file gone: %v", err)
	}
	if string(got) != preRun {
		t.Fatalf("restored bytes = %q, want %q", got, preRun)
	}
}

// TestRemoveJournaled_DeleteThenRewriteStillRestoresOriginal is the exact
// incident ordering: delete, then regenerate onto the same path, then abort.
// The capture at the DELETE must win, because it holds the true pre-run bytes.
func TestRemoveJournaled_DeleteThenRewriteStillRestoresOriginal(t *testing.T) {
	root := t.TempDir()
	const rel = "internal/coupon/middleware_gen.go"
	const preRun = "package coupon // pre-run, committed\n"
	full := seedFile(t, root, rel, preRun)

	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	// Step 1: the sweep deletes it.
	if err := RemoveJournaled(full); err != nil {
		t.Fatalf("RemoveJournaled: %v", err)
	}
	// Step 2: the emitter rewrites it this same run.
	if _, err := WriteGeneratedFile(root, rel, []byte("package coupon // regenerated\n"), nil, true); err != nil {
		t.Fatalf("WriteGeneratedFile: %v", err)
	}
	// Step 3: a later step fails.
	RestoreRollback(root)

	got, err := os.ReadFile(full)
	if err != nil {
		// The pre-fix failure: the unjournaled delete made the later write
		// record existed=false, so the rewind DELETED the file outright.
		t.Fatalf("rewind deleted the file instead of restoring it — the tree is "+
			"less buildable than the run found it: %v", err)
	}
	if string(got) != preRun {
		t.Fatalf("restored bytes = %q, want the pre-run %q", got, preRun)
	}
}

// TestRemoveJournaled_MissingFileIsNotAnError keeps the sweeps' steady state
// cheap: most runs have nothing to retire.
func TestRemoveJournaled_MissingFileIsNotAnError(t *testing.T) {
	root := t.TempDir()
	BeginRollbackJournal(root)
	t.Cleanup(CommitRollback)

	if err := RemoveJournaled(filepath.Join(root, "internal", "nope", "gone_gen.go")); err != nil {
		t.Fatalf("RemoveJournaled on a missing path should be a no-op, got %v", err)
	}
}

// TestRemoveJournaled_NoJournalStillDeletes: outside the generate pipeline
// (journaling OFF) the delete must still happen — those callers have their
// own recovery stories and must not silently become no-ops.
func TestRemoveJournaled_NoJournalStillDeletes(t *testing.T) {
	root := t.TempDir()
	full := seedFile(t, root, "some/file.go", "package some\n")

	CommitRollback() // ensure journaling is OFF
	if err := RemoveJournaled(full); err != nil {
		t.Fatalf("RemoveJournaled: %v", err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("delete must still happen with journaling off (stat err = %v)", err)
	}
}
