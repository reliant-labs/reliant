// Copyright (c) 2025 Reliant Labs
package provetest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The journal is what makes "always restore" true even when the process
// running the proof dies — the desktop app quits mid-test, the OOM killer
// picks the daemon, the machine loses power. A deferred restore cannot cover
// any of those, and the cost of missing one is the worst outcome this tool
// has: the fix silently replaced by the old code, with nobody told.
//
// Layout, under <git-dir>/reliant-prove-test/:
//
//	locks/<key>.lock          one OS lock per file, held for the whole proof
//	journal/<key>/entry.json  what was swapped, written BEFORE the swap
//	journal/<key>/snapshot    the bytes the swap replaced (the fix)
//	conflicts/<when>-<key>/   entries whose file someone else changed; kept
//	                          until a human or agent deals with them
//
// One entry per FILE, not per proof. A file's lock is exclusive, so at most
// one live proof can have an entry for it — which makes orphan detection
// trivial: holding a file's lock while its entry exists proves the proof that
// wrote the entry is gone.

// journalVersion is bumped if the entry format changes incompatibly.
const journalVersion = 1

type journal struct {
	dir string
}

// journalEntry records one swapped file.
type journalEntry struct {
	Version   int       `json:"version"`
	Path      string    `json:"path"` // repository-relative, slash-separated
	RunID     string    `json:"run_id"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Baseline  string    `json:"baseline_commit"`
	// Original is the file before the swap (the fix); its bytes are in
	// snapshot. Swapped is what the swap wrote. Before the swap lands this
	// carries content only; afterwards, the write's identity too.
	Original fileState `json:"original"`
	Swapped  fileState `json:"swapped"`
	// CreatedDirs are repository-relative directories made to hold a file
	// that exists only at baseline, deepest first, removed again on restore.
	CreatedDirs []string `json:"created_dirs,omitempty"`
}

func journalKey(rel string) string {
	sum := sha256.Sum256([]byte(rel))
	return hex.EncodeToString(sum[:16])
}

func (j journal) entryDir(key string) string { return filepath.Join(j.dir, "journal", key) }

func (j journal) lockPath(key string) string { return filepath.Join(j.dir, "locks", key+".lock") }

func (j journal) write(key string, entry journalEntry, snapshot []byte) error {
	dir := j.entryDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if snapshot != nil {
		if err := writeFileSync(filepath.Join(dir, "snapshot"), snapshot); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "entry.json.tmp")
	if err := writeFileSync(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "entry.json")); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

func (j journal) read(key string) (journalEntry, []byte, error) {
	dir := j.entryDir(key)
	var entry journalEntry
	data, err := os.ReadFile(filepath.Join(dir, "entry.json"))
	if err != nil {
		return entry, nil, err
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return entry, nil, fmt.Errorf("corrupt journal entry %s: %w", dir, err)
	}
	if entry.Version != journalVersion {
		return entry, nil, fmt.Errorf("journal entry %s has version %d, want %d", dir, entry.Version, journalVersion)
	}
	var snapshot []byte
	if entry.Original.Exists {
		if snapshot, err = os.ReadFile(filepath.Join(dir, "snapshot")); err != nil {
			return entry, nil, err
		}
		if hashBytes(snapshot) != entry.Original.SHA256 {
			return entry, nil, fmt.Errorf("journal snapshot %s does not match its recorded hash", dir)
		}
	}
	return entry, snapshot, nil
}

func (j journal) remove(key string) error {
	return os.RemoveAll(j.entryDir(key))
}

// preserve moves an entry out of the active journal so nothing ever acts on
// it again automatically, and returns where the preserved bytes are.
func (j journal) preserve(key string, originalExisted bool) (string, error) {
	conflicts := filepath.Join(j.dir, "conflicts")
	if err := os.MkdirAll(conflicts, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(conflicts, time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+key)
	if err := os.Rename(j.entryDir(key), dest); err != nil {
		return "", err
	}
	if originalExisted {
		return filepath.Join(dest, "snapshot"), nil
	}
	return dest, nil
}

// keys lists the active entries.
func (j journal) keys() []string {
	entries, err := os.ReadDir(filepath.Join(j.dir, "journal"))
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	sort.Strings(keys)
	return keys
}

var errLockBusy = errors.New("lock is held")

// fileLock is an exclusive OS lock on a lock file. Lock files are never
// deleted: removing one while another process waits on it would let two
// holders lock two different inodes under one name.
type fileLock struct {
	f *os.File
}

func (j journal) tryLock(key string) (*fileLock, error) {
	path := j.lockPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ok, err := tryLockExclusive(f)
	if err != nil || !ok {
		_ = f.Close()
		if err == nil {
			err = errLockBusy
		}
		return nil, err
	}
	return &fileLock{f: f}, nil
}

// lockWait polls for the lock until deadline. Polling rather than a blocking
// lock keeps the wait cancellable and bounded on every platform.
func (j journal) lockWait(ctx context.Context, key string, deadline time.Time) (*fileLock, error) {
	interval := 25 * time.Millisecond
	for {
		l, err := j.tryLock(key)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, errLockBusy) {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, errLockBusy
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if interval < 250*time.Millisecond {
			interval *= 2
		}
	}
}

func (l *fileLock) release() {
	_ = unlockFile(l.f)
	_ = l.f.Close()
}

// target is one file a proof swaps.
type target struct {
	file     *resolvedFile
	key      string
	original fileState // the fix, content included
	swapped  fileState // what the swap wrote
	restored fileState // what the restore wrote, for the post-run check
	// createdDirs are absolute, deepest first.
	createdDirs []string
}

// proof is the state of one Prove call once its locks are held.
type proof struct {
	repo    *repo
	targets []*target
	report  *Report
	runID   string
	commit  string
}

// testHookAfterSwap runs once every file is swapped, before the before-run.
// Tests use it to inject a panic at the worst possible moment.
var testHookAfterSwap func()

// swapAndRun swaps every target to its baseline, runs the command, and
// restores. The restore is deferred, so it happens however this returns —
// including by panic, which unwinds through here before anything recovers it.
func (p *proof) swapAndRun(ctx context.Context, run Runner, backstop time.Duration) (*RunOutcome, error) {
	var swapped []*target
	defer func() {
		p.report.Restored = p.restoreAll(swapped)
	}()
	for _, t := range p.targets {
		did, err := p.swap(t)
		if did {
			swapped = append(swapped, t)
		}
		if err != nil {
			return nil, fmt.Errorf("could not revert %s, so nothing was run: %w", t.file.rel, err)
		}
		change := ChangeReverted
		switch {
		case !t.file.baseline.Exists:
			change = ChangeRemoved
		case !t.original.Exists:
			change = ChangeRecreated
		}
		p.report.Files = append(p.report.Files, FileChange{Path: t.file.rel, Change: change})
	}
	if testHookAfterSwap != nil {
		testHookAfterSwap()
	}
	out := runGuarded(ctx, run, backstop)
	return &out, nil
}

// swap journals a target, then replaces it with its baseline. It reports
// whether the file on disk was changed, so a failure after the change still
// gets restored.
func (p *proof) swap(t *target) (bool, error) {
	f := t.file
	t.key = journalKey(f.rel)
	entry := journalEntry{
		Version:   journalVersion,
		Path:      f.rel,
		RunID:     p.runID,
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
		Baseline:  p.commit,
		Original:  t.original,
		Swapped:   fileState{Exists: f.baseline.Exists, SHA256: f.baseline.SHA256, Size: f.baseline.Size},
	}
	var snapshot []byte
	if t.original.Exists {
		snapshot = t.original.Content
		if snapshot == nil {
			snapshot = []byte{}
		}
	}
	// Directories a recreated file needs are journalled before they exist,
	// so a crash between making them and the entry update cannot strand them.
	if f.baseline.Exists {
		missing, err := missingDirs(filepath.Dir(f.abs))
		if err != nil {
			return false, err
		}
		t.createdDirs = missing
		for _, d := range missing {
			if rel, relErr := filepath.Rel(p.repo.top, d); relErr == nil {
				entry.CreatedDirs = append(entry.CreatedDirs, filepath.ToSlash(rel))
			}
		}
	}
	if err := p.repo.journal.write(t.key, entry, snapshot); err != nil {
		return false, fmt.Errorf("journalling the snapshot: %w", err)
	}

	// The snapshot was taken a moment ago; if the file moved since, another
	// writer got in between and the snapshot no longer holds their work.
	now, err := statState(f.abs)
	if err == nil && statChanged(t.original, now) {
		err = errors.New("it changed on disk while prove_test was preparing (another writer is active)")
	}
	if err != nil {
		_ = p.repo.journal.remove(t.key)
		return false, err
	}

	if f.baseline.Exists {
		mode := f.baseline.Mode
		if t.original.Exists {
			mode = t.original.Mode
		}
		mkErr := os.MkdirAll(filepath.Dir(f.abs), 0o755)
		if mkErr == nil {
			mkErr = writeAtomic(f.abs, f.baseline.Content, mode)
		}
		if mkErr != nil {
			removeEmptyDirs(t.createdDirs)
			_ = p.repo.journal.remove(t.key)
			return false, mkErr
		}
	} else if err := os.Remove(f.abs); err != nil {
		_ = p.repo.journal.remove(t.key)
		return false, err
	}

	// Record the write's identity so restore can tell it from a later write
	// of the same bytes. If this update is lost to a crash, recovery falls
	// back to comparing content.
	t.swapped = entry.Swapped
	if st, statErr := statState(f.abs); statErr == nil {
		st.SHA256 = f.baseline.SHA256
		t.swapped = st
	}
	entry.Swapped = t.swapped
	if err := p.repo.journal.write(t.key, entry, nil); err != nil {
		return true, fmt.Errorf("updating the journal: %w", err)
	}
	return true, nil
}

// restoreAll puts every swapped target back, last swapped first. It takes no
// context on purpose: a cancelled call is exactly when this must still run.
func (p *proof) restoreAll(swapped []*target) []RestoreResult {
	results := make([]RestoreResult, 0, len(swapped))
	for i := len(swapped) - 1; i >= 0; i-- {
		results = append(results, p.repo.restore(swapped[i]))
	}
	// Report in the order the files were named.
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results
}

// restore puts one target back, or, if someone else wrote it meanwhile,
// leaves their content and preserves ours. Shared by the live path and by
// recovery of an orphaned entry, so both apply the same rule.
func (r *repo) restore(t *target) RestoreResult {
	res := RestoreResult{Path: t.file.rel}
	cur, err := readState(t.file.abs)
	switch {
	case err == nil && stillWhatWeWrote(cur, t.swapped):
		if t.original.Exists {
			err = writeAtomic(t.file.abs, t.original.Content, t.original.Mode)
		} else {
			err = os.Remove(t.file.abs)
			if err == nil {
				removeEmptyDirs(t.createdDirs)
			}
		}
		if err == nil {
			cur, err = readState(t.file.abs)
			if err == nil && !restoredExactly(cur, t.original) {
				err = errors.New("the file read back after restoring does not match the snapshot")
			}
		}
		if err != nil {
			res.Outcome = RestoreFailed
			res.Detail = err.Error()
			if t.original.Exists {
				res.Preserved = filepath.Join(r.journal.entryDir(t.key), "snapshot")
			}
			return res
		}
	case err == nil && restoredExactly(cur, t.original):
		// Already holds the fix: an earlier restore landed and only the
		// journal cleanup was lost.
	default:
		// Changed by someone else while it held the baseline. Their write is
		// newer than ours and may be real work, so it stays.
		res.Outcome = RestoreConflict
		res.Detail = "the file was modified by someone else while the fix was reverted; their content was kept"
		if err != nil {
			res.Detail = fmt.Sprintf("the file could not be read back (%v); it was left as is", err)
		}
		preserved, pErr := r.journal.preserve(t.key, t.original.Exists)
		if pErr != nil {
			res.Detail += fmt.Sprintf("; preserving your version failed (%v), it is still in %s", pErr, r.journal.entryDir(t.key))
		} else if t.original.Exists {
			res.Preserved = preserved
		} else {
			res.Detail += "; your version had no such file"
		}
		return res
	}
	t.restored = cur
	res.Outcome = RestoreOK
	if rmErr := r.journal.remove(t.key); rmErr != nil {
		res.Detail = fmt.Sprintf("restored, but the journal entry could not be removed (%v)", rmErr)
	}
	return res
}

// recoverOrphans restores files left swapped by a proof that died. Entries
// for this proof's own files are always handled: their locks are held, so
// whoever wrote them is gone. Any other entry is handled only if its lock can
// be taken without waiting — a live proof holds the lock of every entry it
// owns, so an entry whose lock is free is an orphan.
func (r *repo) recoverOrphans(held *heldLocks) []RestoreResult {
	var results []RestoreResult
	for _, key := range r.journal.keys() {
		if held.has(key) {
			results = append(results, r.recoverEntry(key))
			continue
		}
		l, err := r.journal.tryLock(key)
		if err != nil {
			continue
		}
		results = append(results, r.recoverEntry(key))
		l.release()
	}
	return results
}

func (r *repo) recoverEntry(key string) RestoreResult {
	entry, snapshot, err := r.journal.read(key)
	if err != nil {
		res := RestoreResult{Path: entry.Path, Outcome: RestoreFailed, Detail: err.Error()}
		if res.Path == "" {
			res.Path = r.journal.entryDir(key)
		}
		if preserved, pErr := r.journal.preserve(key, false); pErr == nil {
			res.Detail += "; the entry was moved to " + preserved + " for inspection"
		}
		return res
	}
	original := entry.Original
	original.Content = snapshot
	t := &target{
		file:     &resolvedFile{abs: filepath.Join(r.top, filepath.FromSlash(entry.Path)), rel: entry.Path},
		key:      key,
		original: original,
		swapped:  entry.Swapped,
	}
	for _, d := range entry.CreatedDirs {
		t.createdDirs = append(t.createdDirs, filepath.Join(r.top, filepath.FromSlash(d)))
	}
	res := r.restore(t)
	if res.Outcome == RestoreOK {
		res.Detail = fmt.Sprintf("an earlier prove_test run (pid %d, started %s) was interrupted with this file reverted; your version was put back",
			entry.PID, entry.StartedAt.Format(time.RFC3339))
	}
	return res
}

// changedSinceRestore warns about files that moved during the after-run: the
// command itself rewrote them, or another writer did, and either way the
// after-run may not have tested the fix as written.
func (p *proof) changedSinceRestore() []string {
	var warnings []string
	for _, t := range p.targets {
		now, err := statState(t.file.abs)
		if err != nil || statChanged(t.restored, now) {
			warnings = append(warnings, fmt.Sprintf(
				"%s changed while the after-run was executing (the command or another writer modified it), so the after-run may not have tested your fix as written", t.file.rel))
		}
	}
	return warnings
}

// missingDirs lists the directories os.MkdirAll(dir) would create, deepest
// first, so they can be removed again.
func missingDirs(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	return missing, nil
}

// removeEmptyDirs removes directories deepest first, stopping at the first
// one something else has put a file in.
func removeEmptyDirs(dirs []string) {
	for _, d := range dirs {
		if os.Remove(d) != nil {
			return
		}
	}
}

func newRunID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}
