package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/checksums"
)

// "Your tree is back to its pre-run state" is a claim about the WHOLE tree,
// and the rollback journal only restores the paths it holds: forge's own
// writes plus the outputs each external-tool step declares. A failed
// control-plane run printed that sentence over 32 buf outputs it had not
// journaled, so the user believed a tree that was not true.
//
// Declaring tool outputs fixed those 32. This file makes the sentence
// impossible to print falsely again, whatever the next undeclared writer is:
// the pipeline fingerprints the working tree before any step runs, and after
// the revert compares the tree with that fingerprint. The claim is made only
// when nothing outside the journal changed; otherwise the report names what
// did.
//
// Deliberately a VERIFICATION, never a restore. A path outside the journal
// that changed during the run may have been changed by someone else — another
// agent in a shared checkout, an editor — and reverting it would destroy
// their work to make forge's sentence true.

// fileStamp identifies one file's state. size/modTime/mode are the cheap
// test; sum settles a stamp mismatch, so a file rewritten with identical
// bytes (a tool that rewrites every output, a touch) is not reported as
// changed.
type fileStamp struct {
	size    int64
	modTime time.Time
	mode    fs.FileMode
	sum     [sha256.Size]byte
}

// workTreeSnapshot fingerprints every project file at one instant.
type workTreeSnapshot struct {
	files map[string]fileStamp // slash-separated, project-relative
}

// snapshotWorkTree fingerprints the project's files: in a git work tree,
// exactly the files git would show (tracked, plus untracked-not-ignored);
// elsewhere, every file outside the directories no project owns. Returns nil
// when the tree cannot be listed — no verdict is then offered, and no claim
// made.
func snapshotWorkTree(root string) *workTreeSnapshot {
	paths, ok := listWorkTree(root)
	if !ok {
		return nil
	}
	// Hashing is what makes the verdict exact (a same-bytes rewrite is not
	// a change), and it is I/O-bound, so it is spread over a few workers.
	stamps := make([]fileStamp, len(paths))
	present := make([]bool, len(paths))
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < snapshotWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				stamps[i], present[i] = stampOf(root, paths[i], true)
			}
		}()
	}
	for i := range paths {
		next <- i
	}
	close(next)
	wg.Wait()

	snap := &workTreeSnapshot{files: make(map[string]fileStamp, len(paths))}
	for i, rel := range paths {
		if present[i] {
			snap.files[rel] = stamps[i]
		}
	}
	return snap
}

// snapshotWorkers bounds the pre-run hashing concurrency.
const snapshotWorkers = 8

// changedSince lists the files whose stamp differs from the snapshot —
// modified, created or deleted — skipping the paths in exclude (the ones the
// revert itself restored). Sorted. ok=false when the tree cannot be listed.
func (s *workTreeSnapshot) changedSince(root string, exclude map[string]bool) (changed []string, ok bool) {
	paths, ok := listWorkTree(root)
	if !ok {
		return nil, false
	}
	seen := make(map[string]bool, len(paths))
	for _, rel := range paths {
		seen[rel] = true
		if exclude[rel] {
			continue
		}
		now, exists := stampOf(root, rel, false)
		before, existed := s.files[rel]
		switch {
		case exists != existed:
			changed = append(changed, rel)
		case exists && !sameStamp(now, before):
			// The cheap identity moved; only different bytes (or mode)
			// are a change.
			if now.mode != before.mode {
				changed = append(changed, rel)
			} else if sum, ok := fileSum(root, rel); !ok || sum != before.sum {
				changed = append(changed, rel)
			}
		}
	}
	for rel := range s.files {
		if !seen[rel] && !exclude[rel] {
			if _, exists := stampOf(root, rel, false); !exists {
				changed = append(changed, rel) // deleted
			}
		}
	}
	sort.Strings(changed)
	return changed, true
}

func sameStamp(a, b fileStamp) bool {
	return a.size == b.size && a.mode == b.mode && a.modTime.Equal(b.modTime)
}

// stampOf returns rel's stamp, hashing its bytes when withSum is set.
func stampOf(root, rel string, withSum bool) (fileStamp, bool) {
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || info.IsDir() {
		return fileStamp{}, false
	}
	st := fileStamp{size: info.Size(), modTime: info.ModTime(), mode: info.Mode()}
	if withSum && info.Mode().IsRegular() {
		if sum, ok := fileSum(root, rel); ok {
			st.sum = sum
		}
	}
	return st, true
}

func fileSum(root, rel string) ([sha256.Size]byte, bool) {
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return [sha256.Size]byte{}, false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [sha256.Size]byte{}, false
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, true
}

// verifyRootExcluded are directories never part of the comparison: forge's
// own state (.forge holds the run's lock, logs, and the failed-generate
// preserve directory the rollback writes), VCS metadata, and dependency
// installs.
// The files under an excluded .forge/ that are still the project's — the
// committed ownership state, birth ledger and JSON certificates
// (checksums.IsCommittedState) — are listed all the same: a run that
// rewrites one has changed the project, and both verdicts must see it.
var verifyRootExcluded = map[string]bool{".forge": true, ".git": true, "node_modules": true}

// listWorkTree returns the project's files, slash-separated and relative to
// root.
func listWorkTree(root string) ([]string, bool) {
	if paths, ok := gitListWorkTree(root); ok {
		return paths, true
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && verifyRootExcluded[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, false
	}
	out = append(out, checksums.CommittedStatePaths(root)...)
	return out, true
}

// gitListWorkTree lists root's files the way `git status` sees them, so
// ignored output (build dirs, caches, a dev server's churn) is never
// mistaken for a change the run made. ok=false outside a git work tree.
func gitListWorkTree(root string) ([]string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false
	}
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	var paths []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || seen[rel] {
			continue
		}
		top, _, _ := strings.Cut(rel, "/")
		if verifyRootExcluded[top] && !checksums.IsCommittedState(rel) {
			continue
		}
		seen[rel] = true
		paths = append(paths, rel)
	}
	return paths, true
}

// rollbackResidue is the verdict on the claim "the tree is back to its
// pre-run state".
type rollbackResidue struct {
	// Checked is false when no verdict was reachable (no pre-run snapshot,
	// or the tree could not be listed). The claim is then not made.
	Checked bool
	// Changed are files outside the journal that differ from before the
	// run, plus journaled files the revert failed to restore.
	Changed []string
}

// Clean reports whether the restore was verified complete.
func (r rollbackResidue) Clean() bool { return r.Checked && len(r.Changed) == 0 }

// verifyRestoredTree compares the tree after a revert with the pre-run
// snapshot. journaled are the paths the revert owned; failed are those it
// could not put back.
func verifyRestoredTree(root string, pre *workTreeSnapshot, journaled, failed []string) rollbackResidue {
	if pre == nil {
		if len(failed) > 0 {
			// No fingerprint, but a failed restore is a certain verdict.
			return rollbackResidue{Checked: true, Changed: append([]string(nil), failed...)}
		}
		return rollbackResidue{}
	}
	exclude := make(map[string]bool, len(journaled))
	for _, p := range journaled {
		exclude[p] = true
	}
	for _, p := range failed {
		delete(exclude, p)
	}
	changed, ok := pre.changedSince(root, exclude)
	if !ok {
		return rollbackResidue{}
	}
	// A failed restore of a path git ignores is still a change.
	for _, p := range failed {
		if !slices.Contains(changed, p) {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	return rollbackResidue{Checked: true, Changed: changed}
}

// residueListLimit caps the residue list so a pathological run does not bury
// the root cause.
const residueListLimit = 25

// writeResidue prints the files the revert did not put back, and why that is
// not necessarily forge's doing.
func writeResidue(w io.Writer, changed []string) {
	shown := changed
	if len(shown) > residueListLimit {
		shown = shown[:residueListLimit]
	}
	for _, p := range shown {
		fmt.Fprintf(w, "   ! %s\n", p)
	}
	if len(changed) > len(shown) {
		fmt.Fprintf(w, "   … and %d more\n", len(changed)-len(shown))
	}
	fmt.Fprintf(w, "   forge did NOT revert these. If another process (an editor, another agent in this checkout) changed them\n")
	fmt.Fprintf(w, "   during the run, that is expected and they are theirs. Otherwise a generate step wrote them without\n")
	fmt.Fprintf(w, "   declaring it — a forge bug: report it with this list. Inspect with `git diff -- <path>`.\n")
}
