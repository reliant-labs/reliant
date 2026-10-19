// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // git's index checksum, not a security boundary
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Crash repair for the shared clones and the worktree registrations in them.
//
// A machine that dies mid-write (a VM killed by its host, ENOSPC) leaves files
// whose metadata reached the disk but whose data did not: zero-byte loose
// objects, zero-byte or truncated worktree index files, zero-byte pseudo-refs.
// Git cannot read any of them, so every command that touches one fails, and
// once `git gc --auto` has failed it writes gc.log and refuses to run again
// while that file exists, so loose objects pile up behind the damage. A
// worktree directory removed while git was not looking leaves a registration
// git keeps forever when it is locked — and every reliant checkout is locked.
//
// RepairRepo undoes exactly that damage and nothing else. Every rule is
// "provably crash debris": git writes objects, indexes and refs to a temporary
// or .lock file and renames it into place, so a zero-byte object or a
// zero-byte index at its final name was never written by a running git — only
// a crash produces it. Nothing in a working tree is ever touched.

// Repair defaults.
const (
	// DefaultRepairMinAge: a damaged file younger than this is left for the
	// next pass. Git never shows a zero-byte final file, so this guards
	// against filesystems with odd rename semantics, not against git.
	DefaultRepairMinAge = 10 * time.Minute
	// DefaultStaleTempAge: a tmp_pack_*/tmp_obj_* or index.lock older than
	// this belongs to a process that is gone. No fetch, repack or index write
	// holds one for hours.
	DefaultStaleTempAge = 6 * time.Hour
	// DefaultRepairInterval: how often settle re-checks one repository.
	DefaultRepairInterval = time.Hour
	// fsckTimeout bounds the connectivity check after objects were removed.
	fsckTimeout = 10 * time.Minute
	// maxReportedProblems bounds the fsck lines a report carries.
	maxReportedProblems = 50
)

// RepairOptions configures RepairRepo. The zero value is the production
// default for every field except WorktreeRoot.
type RepairOptions struct {
	// WorktreeRoot is reliant's worktree root (<home>/.reliant/worktrees). A
	// registration locked with an EMPTY reason — what a crash during `git
	// worktree lock` leaves — is unlocked and pruned only when its checkout
	// was under this root. Empty: such registrations are reported, not pruned.
	WorktreeRoot string
	// MinAge and StaleTempAge: see DefaultRepairMinAge, DefaultStaleTempAge.
	MinAge       time.Duration
	StaleTempAge time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Busy reports whether a checkout is being worked on right now (moved
	// into quarantine, removed); its registration is left alone this pass.
	// Nil: nothing is busy.
	Busy func(checkout string) bool
}

func (o RepairOptions) withDefaults() RepairOptions {
	if o.MinAge <= 0 {
		o.MinAge = DefaultRepairMinAge
	}
	if o.StaleTempAge <= 0 {
		o.StaleTempAge = DefaultStaleTempAge
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// RepairReport is what RepairRepo found and did in one repository.
type RepairReport struct {
	// CommonDir is the repository's git common dir (<repo>/.git).
	CommonDir string
	// EmptyObjects are the zero-byte loose objects removed (object ids).
	EmptyObjects []string
	// LostObjects are removed empty objects that exist nowhere else (no pack
	// holds them) AND that fsck says something still references: history
	// that is gone. Reported, never hidden; refetching is the only remedy.
	LostObjects []string
	// FsckProblems are the missing/broken lines fsck printed after the
	// repair, bounded. Empty when fsck was not needed or found nothing.
	FsckProblems []string
	// RebuiltIndexes are the checkouts whose index was empty or truncated and
	// was rebuilt from HEAD (git reset --mixed semantics: the working tree is
	// untouched; staged-but-uncommitted changes were already lost with the
	// index and now show as unstaged).
	RebuiltIndexes []string
	// RestoredHEADs are linked worktrees whose HEAD was empty and was
	// restored, detached, from the last entry of their HEAD reflog.
	RestoredHEADs []string
	// RemovedDebris are the files removed because they were empty or stale
	// leftovers: pseudo-refs, tmp_pack_*, tmp_obj_*, index.lock.
	RemovedDebris []string
	// RemovedGCLog is the content of the gc.log removed once the repository
	// had nothing left wrong with it, so `git gc --auto` runs again.
	RemovedGCLog string
	// PrunedRegistrations are the worktree admin dirs whose checkout is gone,
	// removed as `git worktree prune` would (unlocked first when the lock was
	// reliant's or empty): "<admin name>: <checkout>".
	PrunedRegistrations []string
	// SkippedRegistrations are gone checkouts left registered because someone
	// else locked them: "<admin name>: <checkout> (<reason>)".
	SkippedRegistrations []string
	// Problems are damage found but not repaired, and repair steps that
	// failed. Each one is a sentence.
	Problems []string
}

// Repaired reports whether anything was changed.
func (r RepairReport) Repaired() bool {
	return len(r.EmptyObjects) > 0 || len(r.RebuiltIndexes) > 0 || len(r.RestoredHEADs) > 0 ||
		len(r.RemovedDebris) > 0 || r.RemovedGCLog != "" || len(r.PrunedRegistrations) > 0
}

// Unhealthy reports whether damage remains.
func (r RepairReport) Unhealthy() bool {
	return len(r.LostObjects) > 0 || len(r.FsckProblems) > 0 || len(r.Problems) > 0
}

func (r *RepairReport) problem(format string, args ...any) {
	r.Problems = append(r.Problems, fmt.Sprintf(format, args...))
}

var (
	looseObjectDir  = regexp.MustCompile(`^[0-9a-f]{2}$`)
	looseObjectName = regexp.MustCompile(`^([0-9a-f]{38}|[0-9a-f]{62})$`)
	objectID        = regexp.MustCompile(`\b([0-9a-f]{40}|[0-9a-f]{64})\b`)
)

// pseudoRefs are the per-worktree files git writes once and reads only during
// one operation; an empty one is unreadable debris.
var pseudoRefs = []string{"ORIG_HEAD", "FETCH_HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "AUTO_MERGE", "BISECT_HEAD"}

// RepairRepo detects and repairs crash damage in one repository, given its
// git common dir. It is safe while other git processes run: every write goes
// through git's own lock-file protocol, and every removal is of a file no
// running git can be writing.
func RepairRepo(ctx context.Context, commonDir string, opts RepairOptions) RepairReport {
	opts = opts.withDefaults()
	rep := RepairReport{CommonDir: commonDir}
	if fi, err := os.Stat(filepath.Join(commonDir, "objects")); err != nil || !fi.IsDir() {
		rep.problem("%s is not a git directory (no objects/)", commonDir)
		return rep
	}

	// Objects first: rebuilding an index reads HEAD's tree, which may have
	// been one of the empty objects (git then finds the packed copy).
	removed := repairObjects(commonDir, opts, &rep)

	for _, adm := range worktreeAdminDirs(commonDir) {
		repairRegistration(ctx, adm, opts, &rep)
	}
	if mainCheckout := mainWorktree(commonDir); mainCheckout != "" {
		removeEmptyPseudoRefs(commonDir, opts, &rep)
		repairIndex(ctx, commonDir, mainCheckout, opts, &rep)
	}

	if len(removed) > 0 {
		verifyObjects(ctx, commonDir, removed, &rep)
	}

	// gc.log is stale once the repository has nothing left wrong with it:
	// while it exists `git gc --auto` refuses to run (for gc.logExpiry), so
	// the loose objects the failed gc left keep piling up. Its content goes in
	// the report, so what it complained about is not lost.
	if !rep.Unhealthy() {
		logPath := filepath.Join(commonDir, "gc.log")
		if b, err := os.ReadFile(logPath); err == nil {
			if err := os.Remove(logPath); err == nil {
				rep.RemovedGCLog = strings.TrimSpace(string(b))
				if rep.RemovedGCLog == "" {
					rep.RemovedGCLog = "(empty)"
				}
			}
		}
	}
	return rep
}

// repairObjects removes zero-byte loose objects and stale temporary object
// and pack files. It returns the removed object ids.
func repairObjects(commonDir string, opts RepairOptions, rep *RepairReport) []string {
	objects := filepath.Join(commonDir, "objects")
	now := opts.Now()
	dirs, err := os.ReadDir(objects)
	if err != nil {
		rep.problem("reading %s: %v", objects, err)
		return nil
	}
	var removed []string
	for _, d := range dirs {
		if !d.IsDir() || !looseObjectDir.MatchString(d.Name()) {
			continue
		}
		dir := filepath.Join(objects, d.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			switch {
			case looseObjectName.MatchString(e.Name()):
				if info.Size() != 0 || now.Sub(info.ModTime()) < opts.MinAge {
					continue
				}
				if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
					rep.problem("removing empty object %s: %v", p, err)
					continue
				}
				removed = append(removed, d.Name()+e.Name())
			case strings.HasPrefix(e.Name(), "tmp_obj_"):
				removeStale(p, info, now, opts.StaleTempAge, rep)
			}
		}
	}
	for _, sub := range []string{objects, filepath.Join(objects, "pack")} {
		entries, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "tmp_pack_") && !strings.HasPrefix(e.Name(), "tmp_obj_") && !strings.HasPrefix(e.Name(), "tmp_idx_") {
				continue
			}
			if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
				removeStale(filepath.Join(sub, e.Name()), info, now, opts.StaleTempAge, rep)
			}
		}
	}
	sort.Strings(removed)
	rep.EmptyObjects = append(rep.EmptyObjects, removed...)
	return removed
}

func removeStale(p string, info fs.FileInfo, now time.Time, age time.Duration, rep *RepairReport) {
	if now.Sub(info.ModTime()) < age {
		return
	}
	if err := os.Remove(p); err == nil {
		rep.RemovedDebris = append(rep.RemovedDebris, p)
	} else if !errors.Is(err, fs.ErrNotExist) {
		rep.problem("removing stale %s: %v", p, err)
	}
}

// verifyObjects decides, for each removed empty object, whether anything was
// lost: an object a pack also holds is intact; one nothing references was
// garbage; one that is missing AND referenced is lost history.
func verifyObjects(ctx context.Context, commonDir string, removed []string, rep *RepairReport) {
	var missing []string
	for _, id := range removed {
		if _, err := git(ctx, "", nil, "--git-dir="+commonDir, "cat-file", "-e", id); err != nil {
			missing = append(missing, id)
		}
	}
	fctx, cancel := context.WithTimeout(ctx, fsckTimeout)
	defer cancel()
	out, err := gitRaw(fctx, "", nil, "--git-dir="+commonDir, "fsck", "--connectivity-only", "--no-dangling", "--no-progress")
	text := string(out)
	if err != nil {
		// fsck exits non-zero when it finds problems; its findings are on
		// stdout and stderr both, and gitRaw's error carries stderr.
		text += "\n" + err.Error()
	}
	referenced := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !(strings.Contains(line, "missing") || strings.Contains(line, "broken") ||
			strings.HasPrefix(line, "error") || strings.HasPrefix(line, "fatal")) {
			continue
		}
		for _, m := range objectID.FindAllString(line, -1) {
			referenced[m] = true
		}
		if len(rep.FsckProblems) < maxReportedProblems {
			rep.FsckProblems = append(rep.FsckProblems, line)
		}
	}
	if err != nil && len(rep.FsckProblems) == 0 && fctx.Err() != nil {
		rep.problem("git fsck did not finish in %s; whether the removed empty objects were needed is unknown: %s", fsckTimeout, strings.Join(missing, " "))
	}
	for _, id := range missing {
		if referenced[id] {
			rep.LostObjects = append(rep.LostObjects, id)
		}
	}
}

// worktreeAdminDirs lists <common>/worktrees/*.
func worktreeAdminDirs(commonDir string) []string {
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(commonDir, "worktrees", e.Name()))
		}
	}
	return out
}

// mainWorktree is the main checkout of a non-bare repository whose common dir
// is <checkout>/.git, or "" for a bare repository or an unusual layout.
func mainWorktree(commonDir string) string {
	if filepath.Base(commonDir) != ".git" {
		return ""
	}
	return filepath.Dir(commonDir)
}

// checkoutOf reads an admin dir's gitdir file (<checkout>/.git) and returns
// the checkout, or "" when the file is missing or empty.
func checkoutOf(adminDir string) string {
	b, err := os.ReadFile(filepath.Join(adminDir, "gitdir"))
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(b))
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(adminDir, p)
	}
	return filepath.Dir(filepath.Clean(p))
}

// checkoutGone reports whether a checkout is TRULY gone: the path does not
// exist (ENOENT, not a permission or I/O error, which an unmounted or failing
// volume returns) and every missing ancestor up to the first existing
// directory is ENOENT too. Under the worktree root, the root itself must
// exist: a missing root is an unmounted volume, not deleted worktrees.
func checkoutGone(checkout, root string) bool {
	if checkout == "" || !filepath.IsAbs(checkout) {
		return false
	}
	if _, err := os.Lstat(checkout); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if root != "" && within(root, checkout) {
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			return false
		}
	}
	for dir := filepath.Dir(checkout); ; dir = filepath.Dir(dir) {
		fi, err := os.Stat(dir)
		if err == nil {
			return fi.IsDir() && dir != filepath.Dir(dir) // never "everything up to / is gone"
		}
		if !errors.Is(err, fs.ErrNotExist) || dir == filepath.Dir(dir) {
			return false
		}
	}
}

// repairRegistration repairs one linked worktree's admin dir: a registration
// whose checkout is gone is pruned when it is reliant's to prune, and a live
// one gets its empty pseudo-refs, HEAD and index repaired.
//
// Pruning is narrower than `git worktree prune`, deliberately. Git itself
// keeps a missing, unlocked registration for gc.worktreePruneExpire (three
// months) because the checkout may be on removable media, and never prunes a
// locked one. Here a registration is pruned only when its checkout is TRULY
// gone (checkoutGone) and it is reliant's: under the worktree root, or locked
// with a reliant lock reason. A lock with an empty reason — a crash during
// `git worktree lock` — counts as reliant's only under the root. Any other
// lock (a user's, or git's own "initializing" during `worktree add`) is
// reported and left alone.
func repairRegistration(ctx context.Context, adminDir string, opts RepairOptions, rep *RepairReport) {
	name := filepath.Base(adminDir)
	checkout := checkoutOf(adminDir)
	lockPath := filepath.Join(adminDir, "locked")
	b, lockErr := os.ReadFile(lockPath)
	locked := lockErr == nil
	reason := strings.TrimSpace(string(b))
	if lockErr != nil && !errors.Is(lockErr, fs.ErrNotExist) {
		rep.problem("reading %s: %v", lockPath, lockErr)
		return
	}
	underRoot := opts.WorktreeRoot != "" && checkout != "" && within(opts.WorktreeRoot, checkout)

	gone := false
	if checkout == "" {
		// A torn admin dir with no gitdir file: git cannot say whose it is.
		// Old enough, and not mid-`worktree add` (locked "initializing").
		info, err := os.Stat(adminDir)
		gone = err == nil && opts.Now().Sub(info.ModTime()) >= opts.MinAge && (!locked || reason == "" || ReliantLock(reason))
		underRoot = true
		if !gone {
			return
		}
	} else {
		gone = checkoutGone(checkout, opts.WorktreeRoot)
	}
	if gone {
		if opts.Busy != nil && checkout != "" && opts.Busy(checkout) {
			return
		}
		ours := underRoot || ReliantLock(reason)
		if locked && !(ReliantLock(reason) || (reason == "" && underRoot)) {
			ours = false
		}
		if !ours {
			if locked {
				if reason == "" {
					reason = "locked, no reason"
				}
				rep.SkippedRegistrations = append(rep.SkippedRegistrations, fmt.Sprintf("%s: %s (%s)", name, checkout, reason))
			}
			return // unlocked and outside the root: git's own prune expiry decides
		}
		// Re-check right before acting: a `git worktree move` may have just
		// rewritten the gitdir file.
		if checkout != "" && (checkoutOf(adminDir) != checkout || !checkoutGone(checkout, opts.WorktreeRoot)) {
			return
		}
		if err := os.RemoveAll(adminDir); err != nil {
			rep.problem("pruning registration %s: %v", name, err)
			return
		}
		rep.PrunedRegistrations = append(rep.PrunedRegistrations, name+": "+checkout)
		return
	}
	if fi, err := os.Stat(checkout); err != nil || !fi.IsDir() {
		return // not provably gone, not a usable checkout: leave it
	}
	removeEmptyPseudoRefs(adminDir, opts, rep)
	restoreEmptyHEAD(ctx, adminDir, checkout, opts, rep)
	repairIndex(ctx, adminDir, checkout, opts, rep)
}

func removeEmptyPseudoRefs(gitDir string, opts RepairOptions, rep *RepairReport) {
	now := opts.Now()
	for _, ref := range pseudoRefs {
		p := filepath.Join(gitDir, ref)
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || info.Size() != 0 || now.Sub(info.ModTime()) < opts.MinAge {
			continue
		}
		if err := os.Remove(p); err == nil {
			rep.RemovedDebris = append(rep.RemovedDebris, p)
		}
	}
}

// restoreEmptyHEAD gives a linked worktree whose HEAD file is empty its last
// recorded HEAD back, detached, from the newest entry of its HEAD reflog. The
// working tree is not touched; if it had moved on since, the difference shows
// as changes, not as lost files.
func restoreEmptyHEAD(ctx context.Context, adminDir, checkout string, opts RepairOptions, rep *RepairReport) {
	headPath := filepath.Join(adminDir, "HEAD")
	info, err := os.Lstat(headPath)
	if err != nil || info.Size() != 0 || opts.Now().Sub(info.ModTime()) < opts.MinAge {
		return
	}
	id := lastReflogTarget(filepath.Join(adminDir, "logs", "HEAD"))
	if id == "" {
		rep.problem("%s: HEAD is empty and has no reflog to restore it from; check out a branch there by hand", checkout)
		return
	}
	commonDir := filepath.Dir(filepath.Dir(adminDir))
	if _, err := git(ctx, "", nil, "--git-dir="+commonDir, "cat-file", "-e", id+"^{commit}"); err != nil {
		rep.problem("%s: HEAD is empty and its last reflog entry %s is not a commit in the repository", checkout, id)
		return
	}
	if err := writeViaLock(headPath, []byte(id+"\n")); err != nil {
		rep.problem("%s: restoring HEAD: %v", checkout, err)
		return
	}
	rep.RestoredHEADs = append(rep.RestoredHEADs, fmt.Sprintf("%s (detached at %s)", checkout, id))
}

// lastReflogTarget is the new-value id of the last well-formed reflog line.
func lastReflogTarget(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && objectID.MatchString(fields[1]) && strings.Trim(fields[1], "0") != "" {
			last = fields[1]
		}
	}
	return last
}

// indexDamage says why an index file cannot be read, or "" when it is
// intact. It checks what a crash breaks: an empty or short file, a missing
// signature, and a trailing checksum that does not match (a torn write). An
// all-zero checksum is index.skipHash, which git accepts.
func indexDamage(path string, hashSize int) string {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "" // no index is a valid state (fresh clone, git rm --cached -r)
	}
	if err != nil {
		return "unreadable: " + err.Error()
	}
	if len(b) == 0 {
		return "empty"
	}
	if len(b) < 12+hashSize {
		return fmt.Sprintf("truncated (%d bytes)", len(b))
	}
	if string(b[:4]) != "DIRC" {
		return "bad signature"
	}
	body, sum := b[:len(b)-hashSize], b[len(b)-hashSize:]
	if bytes.Equal(sum, make([]byte, hashSize)) {
		return ""
	}
	var got []byte
	if hashSize == sha256.Size {
		s := sha256.Sum256(body)
		got = s[:]
	} else {
		s := sha1.Sum(body) //nolint:gosec // git's index checksum
		got = s[:]
	}
	if !bytes.Equal(got, sum) {
		return "checksum mismatch (truncated or torn write)"
	}
	return ""
}

// hashSizeOf is the object id size of the repository: 32 for sha256
// repositories (extensions.objectFormat), 20 otherwise.
func hashSizeOf(commonDir string) int {
	b, err := os.ReadFile(filepath.Join(commonDir, "config"))
	if err == nil {
		for _, line := range strings.Split(strings.ToLower(string(b)), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok && strings.TrimSpace(k) == "objectformat" && strings.TrimSpace(v) == "sha256" {
				return sha256.Size
			}
		}
	}
	return sha1.Size
}

// repairIndex rebuilds a damaged index from HEAD without touching the working
// tree, the way `git reset --mixed` would: a new index is built in a scratch
// file (read-tree HEAD, then refresh its stat data) and installed through
// git's own index.lock, after re-checking under the lock that the index is
// still damaged.
func repairIndex(ctx context.Context, gitDir, checkout string, opts RepairOptions, rep *RepairReport) {
	commonDir := gitDir
	if filepath.Base(filepath.Dir(gitDir)) == "worktrees" {
		commonDir = filepath.Dir(filepath.Dir(gitDir))
	}
	hashSize := hashSizeOf(commonDir)
	indexPath := filepath.Join(gitDir, "index")
	lockPath := indexPath + ".lock"
	now := opts.Now()

	// A lock left by a dead process blocks every index write, ours included.
	if info, err := os.Lstat(lockPath); err == nil && info.Mode().IsRegular() {
		removeStale(lockPath, info, now, opts.StaleTempAge, rep)
	}

	why := indexDamage(indexPath, hashSize)
	if why == "" {
		return
	}
	if info, err := os.Lstat(indexPath); err == nil && now.Sub(info.ModTime()) < opts.MinAge {
		return
	}
	scratch := filepath.Join(gitDir, "index.reliant-repair")
	_ = os.Remove(scratch)
	defer func() { _ = os.Remove(scratch) }()
	env := []string{"GIT_INDEX_FILE=" + scratch}
	if _, err := git(ctx, checkout, nil, "rev-parse", "--verify", "-q", "HEAD^{tree}"); err == nil {
		if _, err := git(ctx, checkout, env, "read-tree", "HEAD"); err != nil {
			rep.problem("%s: index is %s and rebuilding it from HEAD failed: %v", checkout, why, err)
			return
		}
	} else if _, err := git(ctx, checkout, env, "read-tree", "--empty"); err != nil {
		rep.problem("%s: index is %s and HEAD has no commit to rebuild it from: %v", checkout, why, err)
		return
	}
	// Exit status 1 only means some files differ from HEAD; the stat data of
	// the rest is filled in either way.
	_, _ = git(ctx, checkout, env, "update-index", "-q", "--refresh")
	content, err := os.ReadFile(scratch)
	if err != nil {
		rep.problem("%s: reading rebuilt index: %v", checkout, err)
		return
	}
	installed, err := installUnderLock(indexPath, content, func() bool { return indexDamage(indexPath, hashSize) != "" })
	if err != nil {
		rep.problem("%s: installing rebuilt index: %v", checkout, err)
		return
	}
	if installed {
		rep.RebuiltIndexes = append(rep.RebuiltIndexes, fmt.Sprintf("%s (was %s)", checkout, why))
	}
}

// installUnderLock writes content to path through git's lock protocol:
// create path.lock exclusively, re-check stillNeeded under the lock, write,
// sync, rename over path. A lock held by someone else is an error; nothing is
// written.
func installUnderLock(path string, content []byte, stillNeeded func() bool) (bool, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false, fmt.Errorf("%s is held by another process: %w", lockPath, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = os.Remove(lockPath)
		}
	}()
	if !stillNeeded() {
		return false, nil
	}
	if _, err := f.Write(content); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(lockPath, path); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}

func writeViaLock(path string, content []byte) error {
	_, err := installUnderLock(path, content, func() bool {
		info, err := os.Lstat(path)
		return err == nil && info.Size() == 0
	})
	return err
}

// DiscoverCommonDirs finds the repositories reliant's checkouts under root
// belong to, by reading the .git file of every checkout at most three levels
// down (<root>/<project>/<workspace>/<repo>). A checkout's .git names its
// admin dir; the admin dir's commondir file names the repository.
func DiscoverCommonDirs(root string) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > shallowDepth {
			return
		}
		if fi, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && fi.Mode().IsRegular() {
			if c := commonDirFromGitFile(filepath.Join(dir, ".git")); c != "" && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != ".git" && e.Name() != "node_modules" && !strings.HasPrefix(e.Name(), ".reclaim") {
				walk(filepath.Join(dir, e.Name()), depth+1)
			}
		}
	}
	walk(root, 0)
	sort.Strings(out)
	return out
}

func commonDirFromGitFile(gitFile string) string {
	b, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	admin := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	if admin == "" {
		return ""
	}
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(filepath.Dir(gitFile), admin)
	}
	common := filepath.Dir(filepath.Dir(admin))
	if c, err := os.ReadFile(filepath.Join(admin, "commondir")); err == nil {
		if p := strings.TrimSpace(string(c)); p != "" {
			if !filepath.IsAbs(p) {
				p = filepath.Join(admin, p)
			}
			common = p
		}
	}
	common = filepath.Clean(common)
	if fi, err := os.Stat(filepath.Join(common, "objects")); err != nil || !fi.IsDir() {
		return ""
	}
	return common
}

// repoCommonDir is the common dir of a parent repository given its checkout
// path: <repo>/.git when that is a directory, otherwise git's answer.
func repoCommonDir(ctx context.Context, repoPath string) string {
	if fi, err := os.Stat(filepath.Join(repoPath, ".git")); err == nil && fi.IsDir() {
		return filepath.Join(repoPath, ".git")
	}
	if c, err := commonDir(ctx, repoPath); err == nil {
		return c
	}
	return ""
}

// repairState memoizes when each repository was last repaired, so the sweep
// checks a repository at most once per RepairInterval.
// Guarded by Reclaimer.mu.
type repairState struct {
	locks map[string]*sync.Mutex
	at    map[string]time.Time
}

// RepairRepos repairs the given repositories (common dirs) now, whatever the
// interval, and reports each. The daemon calls it at startup with
// DiscoverCommonDirs(Root).
func (r *Reclaimer) RepairRepos(ctx context.Context, commonDirs []string) []RepairReport {
	var out []RepairReport
	for _, c := range commonDirs {
		if ctx.Err() != nil {
			break
		}
		out = append(out, r.repairOne(ctx, c, true))
	}
	return out
}

// ensureRepaired repairs the parent repositories of a worktree's checkouts
// before settle reads them, at most once per RepairInterval per repository.
func (r *Reclaimer) ensureRepaired(ctx context.Context, w Worktree) {
	if r.RepairInterval < 0 {
		return
	}
	seen := map[string]bool{}
	for _, c := range w.Checkouts {
		if c.RepoPath == "" || seen[c.RepoPath] {
			continue
		}
		seen[c.RepoPath] = true
		if common := repoCommonDir(ctx, c.RepoPath); common != "" {
			r.repairOne(ctx, common, false)
		}
	}
}

func (r *Reclaimer) repairOne(ctx context.Context, commonDir string, force bool) RepairReport {
	r.mu.Lock()
	if r.repairs.locks == nil {
		r.repairs.locks, r.repairs.at = map[string]*sync.Mutex{}, map[string]time.Time{}
	}
	m := r.repairs.locks[commonDir]
	if m == nil {
		m = &sync.Mutex{}
		r.repairs.locks[commonDir] = m
	}
	r.mu.Unlock()

	m.Lock()
	defer m.Unlock()
	interval := r.RepairInterval
	if interval == 0 {
		interval = DefaultRepairInterval
	}
	r.mu.Lock()
	last := r.repairs.at[commonDir]
	r.mu.Unlock()
	if !force && !last.IsZero() && r.now().Sub(last) < interval {
		return RepairReport{CommonDir: commonDir}
	}
	rep := RepairRepo(ctx, commonDir, RepairOptions{WorktreeRoot: r.Root, Now: r.Now, Busy: r.busy})
	r.mu.Lock()
	r.repairs.at[commonDir] = r.now()
	r.mu.Unlock()
	if r.OnRepair != nil && (rep.Repaired() || rep.Unhealthy() || len(rep.SkippedRegistrations) > 0) {
		r.OnRepair(rep)
	}
	return rep
}

// busy reports whether a settle is working inside checkout right now: its
// worktree's path lock is held. The caller's own settle holds its own lock,
// so a repair from settle never prunes the worktree being settled (settle
// prunes that itself when its directory is gone).
func (r *Reclaimer) busy(checkout string) bool {
	r.mu.Lock()
	locks := make(map[string]*sync.Mutex, len(r.paths))
	for k, m := range r.paths {
		locks[k] = m
	}
	r.mu.Unlock()
	c := resolvePath(checkout)
	for path, m := range locks {
		if c != path && !within(path, c) {
			continue
		}
		if !m.TryLock() {
			return true
		}
		m.Unlock()
	}
	return false
}
