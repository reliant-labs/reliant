// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	gitindex "github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/google/uuid"
)

const RecoveryIdlePeriod = 7 * 24 * time.Hour
const activityPoll = 30 * time.Second
const activityMaxGap = 2 * time.Minute
const activityMaxEntries = 100000
const activityMaxBytes int64 = 1 << 30

// ActivityObserver persists hashes and times, never file contents or file names.
// Its state directory MUST be outside the observed workspace. An observer is
// daemon-lifetime scoped: reopening it never inherits a previous process's proof.
// Start must run continuously; Observe alone cannot establish watcher coverage.
//
// Every observed workspace shares ONE inotify instance. inotify instances are
// a per-user kernel budget (fs.inotify.max_user_instances, 128 by default) that
// the user's own dev servers, gopls and test runs draw on too. A watcher per
// workspace, held for the daemon's lifetime, spent that budget on its own: a
// managed machine with a few hundred worktrees had all 128 held by the daemon
// (52 of them for worktrees already deleted), and every other inotify user on
// the machine failed with "too many open files".
type ActivityObserver struct {
	mu        sync.Mutex
	statePath string
	now       func() time.Time
	records   map[string]*recoveryActivityRecord
	running   map[string]*activityWatch
	// watcher is the shared inotify instance, created by the first Start.
	// watchRefs counts the observations holding each watched path, so one
	// workspace releasing a path never removes another's watch on it.
	watcher   *fsnotify.Watcher
	watchRefs map[string]int
	// routes is a snapshot of running for the dispatcher, which must never
	// wait on mu: mu is held across state fsyncs, and a stalled dispatcher
	// lets the shared kernel queue overflow for every workspace.
	routes atomic.Pointer[[]*activityWatch]
}
type recoveryActivityRecord struct {
	Evidence RecoveryActivity `json:"evidence"`
	Digest   string           `json:"digest"`
}

// activityEventBuffer bounds one workspace's undelivered events. A full
// buffer is lost coverage for that workspace alone (see dispatch).
const activityEventBuffer = 1024

type activityWatch struct {
	cancel         context.CancelFunc
	seq            uint64
	watched        map[string]bool
	scanMu         sync.Mutex
	root           string
	gitDir         string
	gone           bool                // workspace deleted or moved away; guarded by ActivityObserver.mu
	events         chan fsnotify.Event // this workspace's share of the shared watcher's events
	errors         chan error          // coverage loss; one pending error is enough
	injectedErrors <-chan error        // deterministic coverage-failure test seam
}

func newActivityWatch(root, gitDir string, cancel context.CancelFunc) *activityWatch {
	return &activityWatch{
		cancel:  cancel,
		watched: map[string]bool{},
		root:    root,
		gitDir:  gitDir,
		events:  make(chan fsnotify.Event, activityEventBuffer),
		errors:  make(chan error, 1),
	}
}

// covers reports whether an event path belongs to this observation: its
// workspace or its (possibly external) git administration directory.
func (w *activityWatch) covers(path string) bool {
	return overlapActivityPath(w.root, path) || (w.gitDir != "" && overlapActivityPath(w.gitDir, path))
}

// lose records lost coverage without blocking the dispatcher.
func (w *activityWatch) lose(err error) {
	select {
	case w.errors <- err:
	default:
	}
}

// ensureWatcherLocked creates the shared inotify instance and its dispatcher
// on first use. Caller holds o.mu.
func (o *ActivityObserver) ensureWatcherLocked() error {
	if o.watcher != nil {
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	o.watcher = watcher
	o.watchRefs = map[string]int{}
	go o.dispatch(watcher)
	return nil
}

// publishRoutesLocked republishes the dispatcher's view of running.
// Caller holds o.mu.
func (o *ActivityObserver) publishRoutesLocked() {
	routes := make([]*activityWatch, 0, len(o.running))
	for _, w := range o.running {
		routes = append(routes, w)
	}
	o.routes.Store(&routes)
}

// dispatch routes the shared watcher's events to every observation whose
// workspace or git directory contains the path. Delivering to more than one
// is safe: an extra event only ever counts as activity, which delays reclaim
// and never certifies idleness. A watcher error (a kernel queue overflow)
// cannot be attributed, so every observation loses coverage.
func (o *ActivityObserver) dispatch(watcher *fsnotify.Watcher) {
	broadcast := func(err error) {
		if routes := o.routes.Load(); routes != nil {
			for _, w := range *routes {
				w.lose(err)
			}
		}
	}
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				broadcast(errors.New("activity watcher closed"))
				o.dropWatcher(watcher)
				return
			}
			routes := o.routes.Load()
			if routes == nil {
				continue
			}
			for _, w := range *routes {
				if !w.covers(event.Name) {
					continue
				}
				select {
				case w.events <- event:
				default:
					w.lose(errors.New("activity event buffer overflow"))
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				broadcast(errors.New("activity watcher closed"))
				o.dropWatcher(watcher)
				return
			}
			broadcast(err)
		}
	}
}

// dropWatcher forgets a closed shared watcher so the next Start makes a new one.
func (o *ActivityObserver) dropWatcher(watcher *fsnotify.Watcher) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.watcher == watcher {
		o.watcher = nil
		o.watchRefs = nil
	}
}

// addWatch adds path to the shared watcher on behalf of w.
func (o *ActivityObserver) addWatch(w *activityWatch, path string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if w.watched[path] {
		return nil
	}
	if o.watcher == nil {
		return errors.New("activity watcher closed")
	}
	if o.watchRefs[path] == 0 {
		if err := o.watcher.Add(path); err != nil {
			return err
		}
	}
	o.watchRefs[path]++
	w.watched[path] = true
	return nil
}

// forgetWatchLocked drops w's hold on path, removing the kernel watch when no
// other observation holds it. A deleted directory's watch is already gone, so
// the removal error is expected and ignored. Caller holds o.mu.
func (o *ActivityObserver) forgetWatchLocked(w *activityWatch, path string) {
	if !w.watched[path] {
		return
	}
	delete(w.watched, path)
	if o.watchRefs == nil {
		return
	}
	o.watchRefs[path]--
	if o.watchRefs[path] > 0 {
		return
	}
	delete(o.watchRefs, path)
	if o.watcher != nil {
		_ = o.watcher.Remove(path)
	}
}

// Running reports whether root has a live observation. An observation ends
// when its context ends, its coverage is lost, or its workspace is deleted;
// the caller must Start it again, which begins a new epoch.
func (o *ActivityObserver) Running(root string) bool {
	_, key, err := activityKey(root)
	if err != nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.running[key] != nil
}

func NewActivityObserver(statePath string, now func() time.Time) (*ActivityObserver, error) {
	if statePath == "" {
		return nil, errors.New("activity state path required")
	}
	if now == nil {
		now = time.Now
	}
	o := &ActivityObserver{statePath: statePath, now: now, records: map[string]*recoveryActivityRecord{}, running: map[string]*activityWatch{}}
	data, err := os.ReadFile(statePath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(data) != 0 {
		if len(data) > 8<<20 {
			return nil, errors.New("activity state exceeds budget")
		}
		if err := json.Unmarshal(data, &o.records); err != nil {
			return nil, fmt.Errorf("invalid activity state: %w", err)
		}
	}
	if o.records == nil {
		o.records = map[string]*recoveryActivityRecord{}
	}
	for k, r := range o.records {
		if r == nil {
			delete(o.records, k)
			continue
		}
		o.resetLocked(r)
	}
	if err := o.saveLocked(); err != nil {
		return nil, err
	}
	return o, nil
}

func activityKey(root string) (string, string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256([]byte(canonical))
	return canonical, hex.EncodeToString(digest[:]), nil
}
func (o *ActivityObserver) recordLocked(key string) *recoveryActivityRecord {
	r := o.records[key]
	if r == nil {
		r = &recoveryActivityRecord{}
		o.resetLocked(r)
		o.records[key] = r
	}
	return r
}
func (o *ActivityObserver) resetLocked(r *recoveryActivityRecord) {
	now := o.now()
	r.Evidence.Generation++
	r.Evidence.ObservationID = uuid.NewString()
	r.Evidence.ObservedSince, r.Evidence.LastActivity, r.Evidence.ObservedThrough = now, now, now
	r.Evidence.Complete = false
}
func (o *ActivityObserver) saveLocked() error {
	data, err := json.Marshal(o.records)
	if err != nil {
		return err
	}
	dir := filepath.Dir(o.statePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".activity-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), o.statePath); err != nil {
		return err
	}
	// The rename must survive a crash too, not just the temporary file's bytes.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Invalidate records lost/unknown observation (overflow, downtime, permission
// failure, coverage loss). It starts a NEW epoch; no old idle age survives.
func (o *ActivityObserver) Invalidate(root string) error {
	_, key, err := activityKey(root)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if w := o.running[key]; w != nil {
		w.seq++
	}
	o.resetLocked(o.recordLocked(key))
	return o.saveLocked()
}

// Start establishes recursive event coverage before the first inventory, then
// reconciles content periodically. Call once per workspace. Cancellation and
// every watcher error invalidate evidence; there is no polling-only fallback.
func (o *ActivityObserver) Start(ctx context.Context, root string) error {
	if runtime.GOOS != "linux" {
		return errors.New("recovery activity observation unsupported on this platform")
	}
	root, key, err := activityKey(root)
	if err != nil {
		return err
	}
	state, err := filepath.Abs(o.statePath)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(state))
	if err != nil {
		return err
	}
	state = filepath.Join(parent, filepath.Base(state))
	if overlapActivityPath(root, state) {
		return errors.New("activity state must be outside workspace")
	}
	gitDir, err := activityGitDir(root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	w := newActivityWatch(root, gitDir, cancel)
	o.mu.Lock()
	if o.running[key] != nil {
		o.mu.Unlock()
		cancel()
		return errors.New("workspace observation already running")
	}
	if err := o.ensureWatcherLocked(); err != nil {
		o.mu.Unlock()
		cancel()
		return err
	}
	o.running[key] = w
	o.publishRoutesLocked()
	o.resetLocked(o.recordLocked(key))
	err = o.saveLocked()
	if err != nil {
		cancel()
		delete(o.running, key)
		o.publishRoutesLocked()
		o.mu.Unlock()
		return err
	}
	o.mu.Unlock()
	go o.events(ctx, key, w)
	_, err = o.Observe(ctx, root)
	return err
}
func (o *ActivityObserver) events(ctx context.Context, key string, w *activityWatch) {
	ticker := time.NewTicker(activityPoll)
	defer ticker.Stop()
	defer func() {
		w.cancel()
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.running[key] == w {
			delete(o.running, key)
			o.publishRoutesLocked()
		}
		for path := range w.watched {
			o.forgetWatchLocked(w, path)
		}
		if w.gone {
			// Nothing left to prove idle; keep the state file from growing
			// with every worktree this daemon ever saw.
			delete(o.records, key)
		} else {
			o.resetLocked(o.recordLocked(key))
		}
		// A failed persistence cannot resurrect evidence: restart resets it again.
		_ = o.saveLocked()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.errors:
			// Coverage is no longer trustworthy. Require explicit Start after stopping,
			// rather than pretending one subsequent status poll repairs an overflow.
			return
		case event := <-w.events:
			if event.Name == w.root && event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				// The workspace itself was deleted or moved away: end the
				// observation and release its watches.
				o.mu.Lock()
				w.gone = true
				o.mu.Unlock()
				return
			}
			// Git administration writes are reconciled semantically below. An index
			// stat refresh or reflog rewrite must not keep a workspace alive forever.
			if w.gitDir != "" && overlapActivityPath(w.gitDir, event.Name) {
				o.mu.Lock()
				w.seq++
				o.recordLocked(key).Evidence.Complete = false
				o.mu.Unlock()
				continue
			}
			o.mu.Lock()
			w.seq++
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				o.forgetWatchLocked(w, event.Name)
			}
			r := o.recordLocked(key)
			r.Evidence.Generation++
			r.Evidence.LastActivity = o.now()
			if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
				// Newly created directory trees were not watched before this event.
				o.resetLocked(r)
			}
			r.Evidence.Complete = false
			err := o.saveLocked()
			o.mu.Unlock()
			if err != nil {
				return
			}
		case <-ticker.C:
			if _, err := o.Observe(ctx, w.root); err != nil {
				// An error already reset/persisted the epoch. Keep listening, but a later
				// successful full coverage pass must begin the new seven-day period.
				if ctx.Err() != nil {
					return
				}
			}
		}
	}
}

// Observe reconciles exact content, modes and file metadata, not porcelain
// status or directory age. It returns incomplete evidence on every uncertain
// inventory. All ignored files participate; no secret bytes are persisted.
func (o *ActivityObserver) Observe(ctx context.Context, root string) (RecoveryActivity, error) {
	root, key, err := activityKey(root)
	if err != nil {
		return RecoveryActivity{}, err
	}
	o.mu.Lock()
	w := o.running[key]
	if w == nil {
		r := o.recordLocked(key)
		o.resetLocked(r)
		err := o.saveLocked()
		e := r.Evidence
		o.mu.Unlock()
		return e, errors.Join(errors.New("workspace watcher not running"), err)
	}
	o.mu.Unlock()
	w.scanMu.Lock()
	defer w.scanMu.Unlock()
	o.mu.Lock()
	seq := w.seq
	o.mu.Unlock()
	digest, scanErr := o.inventory(ctx, root, w)
	// Drain queued watcher errors before issuing any complete evidence. A later
	// overflow still invalidates the epoch in the event loop.
	select {
	case <-w.errors:
		scanErr = errors.New("activity watcher coverage lost")
		w.cancel()
	default:
	}
	select {
	case <-w.injectedErrors:
		scanErr = errors.New("activity watcher coverage lost")
		w.cancel()
	default:
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	r := o.recordLocked(key)
	// The reconciliation goroutine must not certify through events queued
	// while it was reading files (including edits reverted before the scan).
	draining := true
	for draining {
		select {
		case event := <-w.events:
			w.seq++
			if event.Name == w.root && event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				w.gone = true
				scanErr = errors.New("workspace removed during observation")
				w.cancel()
			}
			if w.gitDir == "" || !overlapActivityPath(w.gitDir, event.Name) {
				r.Evidence.Generation++
				r.Evidence.LastActivity = o.now()
				if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
					o.resetLocked(r)
					o.forgetWatchLocked(w, event.Name)
				}
			}
		default:
			draining = false
		}
	}
	now := o.now()
	gap := now.Sub(r.Evidence.ObservedThrough)
	if gap < 0 || gap > activityMaxGap || scanErr != nil || o.running[key] != w {
		o.resetLocked(r)
	}
	if scanErr == nil && o.running[key] == w {
		if r.Digest != digest {
			r.Evidence.Generation++
			r.Evidence.LastActivity = now
			r.Digest = digest
		}
		r.Evidence.ObservedThrough = now
		r.Evidence.Complete = seq == w.seq
	}
	if err := o.saveLocked(); err != nil {
		o.resetLocked(r)
		return r.Evidence, err
	}
	return r.Evidence, scanErr
}

func overlapActivityPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && (rel == "." || len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator))
}

// RecoveryInactive requires fresh complete continuous evidence. Server activity
// generation is separately fenced by the DB; this does not replace that check.
func RecoveryInactive(e RecoveryActivity, now time.Time) bool {
	if !e.Complete || e.ObservationID == "" || e.Generation <= 0 || e.ObservedSince.IsZero() || e.LastActivity.IsZero() {
		return false
	}
	if now.Before(e.ObservedThrough) || now.Sub(e.ObservedThrough) > activityMaxGap || e.ObservedThrough.Before(e.ObservedSince) || e.ObservedThrough.Before(e.LastActivity) {
		return false
	}
	start := e.ObservedSince
	if e.LastActivity.After(start) {
		start = e.LastActivity
	}
	return !now.Before(start.Add(RecoveryIdlePeriod))
}

func (o *ActivityObserver) inventory(ctx context.Context, root string, w *activityWatch) (string, error) {
	h := sha256.New()
	var total int64
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path != root && d.Name() == ".git" {
			// Registered nested checkouts require their own observer; never silently
			// omit an unregistered repository from evidence.
			if filepath.Dir(path) != root {
				return errors.New("nested checkout requires independent observation")
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		count++
		if count > activityMaxEntries {
			return errors.New("activity inventory entry budget exceeded")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%d:%s:%d:", len(rel), rel, info.Mode())
		if d.IsDir() {
			o.mu.Lock()
			watched := w.watched[path]
			o.mu.Unlock()
			if !watched {
				if err := o.addWatch(w, path); err != nil {
					return err
				}
			}
			return nil // Directory timestamps are deliberately not activity evidence.
		}
		fmt.Fprintf(h, "%d:%d:%s:", info.Size(), info.ModTime().UnixNano(), activityFileMetadata(info))
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%d:%s", len(target), target)
		} else if info.Mode().IsRegular() {
			total += info.Size()
			if total > activityMaxBytes {
				return errors.New("activity inventory byte budget exceeded")
			}
			f, err := activityOpenRegular(path)
			if err != nil {
				return err
			}
			opened, err := f.Stat()
			if err == nil && !sameReflog(info, opened) {
				err = errors.New("activity file changed while opening")
			}
			var n int64
			if err == nil {
				n, err = io.Copy(h, io.LimitReader(f, info.Size()+1))
			}
			after, statErr := f.Stat()
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if statErr != nil {
				return statErr
			}
			if closeErr != nil {
				return closeErr
			}
			current, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if n != info.Size() || !sameReflog(info, after) || !sameReflog(info, current) {
				return errors.New("activity file changed while reading")
			}
		} else {
			return errors.New("unsupported activity file type")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// Linked-worktree administration lives outside root. Its semantic index and
	// reflog events participate without counting cache/stat maintenance writes.
	currentGitDir, err := activityGitDir(root)
	if err != nil {
		return "", err
	}
	if currentGitDir != w.gitDir {
		return "", errors.New("git administration location changed during observation")
	}
	if w.gitDir != "" {
		o.mu.Lock()
		watched := w.watched[w.gitDir]
		o.mu.Unlock()
		if !watched {
			if err := o.addWatch(w, w.gitDir); err != nil {
				return "", err
			}
		}
		metadata, err := activityGitDigest(ctx, w.gitDir)
		if err != nil {
			return "", err
		}
		fmt.Fprint(h, metadata)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func activityReadRegular(path string, limit int64) ([]byte, error) {
	f, err := activityOpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if before.Size() > limit {
		return nil, errors.New("activity file budget exceeded")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit || int64(len(data)) != before.Size() || !sameReflog(before, after) || !sameReflog(before, current) {
		return nil, errors.New("activity file changed while reading")
	}
	return data, nil
}

func activityGitDir(root string) (string, error) {
	path := filepath.Join(root, ".git")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return path, nil
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errors.New("unsupported git administration pointer")
	}
	data, err := activityReadRegular(path, 4096)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(data))
	if !strings.HasPrefix(text, "gitdir: ") {
		return "", errors.New("malformed git administration pointer")
	}
	target := strings.TrimPrefix(text, "gitdir: ")
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	return filepath.EvalSymlinks(target)
}
func activityGitDigest(ctx context.Context, dir string) (string, error) {
	h := sha256.New()
	head, err := activityReadRegular(filepath.Join(dir, "HEAD"), 4096)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "HEAD:%s", head)
	indexPath := filepath.Join(dir, "index")
	info, err := os.Lstat(indexPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return "", errors.New("unsupported activity index or budget exceeded")
	}
	f, err := activityOpenRegular(indexPath)
	if err != nil {
		return "", err
	}
	var idx gitindex.Index
	err = gitindex.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&idx)
	after, statErr := f.Stat()
	f.Close()
	if err != nil {
		return "", fmt.Errorf("unsupported activity index: %w", err)
	}
	if statErr != nil {
		return "", statErr
	}
	if !sameReflog(info, after) {
		return "", errors.New("index changed during activity scan")
	}
	sort.Slice(idx.Entries, func(i, j int) bool {
		a, b := idx.Entries[i], idx.Entries[j]
		if a.Name == b.Name {
			return a.Stage < b.Stage
		}
		return a.Name < b.Name
	})
	for _, entry := range idx.Entries {
		fmt.Fprintf(h, "%d:%s:%s:%d:%d:%t:%t;", len(entry.Name), entry.Name, entry.Hash, entry.Mode, entry.Stage, entry.SkipWorktree, entry.IntentToAdd)
	}
	// Keep actual event times; file rewrite time and empty-log mtimes are not
	// activity. Hash all events' semantic OID/time fields (not identities/text).
	logPath := filepath.Join(dir, "logs", "HEAD")
	logInfo, err := os.Lstat(logPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err == nil && !logInfo.Mode().IsRegular() {
		return "", errors.New("unsupported activity reflog file type")
	}
	if err == nil && logInfo.Size() > 0 {
		// Validate stability with the existing reflog parser, then hash each
		// OID+event time. Rewrites of identities/messages/mtimes are maintenance;
		// an actual new checkout/commit/reset event is activity, even if its clock
		// timestamp is older than a preceding record.
		data, err := activityReadRegular(logPath, maxActivityReflogBytes)
		if err != nil {
			return "", err
		}
		if len(data) > maxActivityReflogBytes {
			return "", errors.New("activity reflog budget exceeded")
		}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		scanner.Buffer(make([]byte, 4096), maxActivityReflogLine)
		if len(data) == 0 || data[len(data)-1] != '\n' {
			return "", errors.New("incomplete activity reflog")
		}
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			line := scanner.Text()
			event, err := reflogEventTime(line)
			if err != nil {
				return "", err
			}
			parts := strings.SplitN(line, " ", 3)
			fmt.Fprintf(h, "reflog:%s:%s:%d;", parts[0], parts[1], event.UnixNano())
		}
		if err := scanner.Err(); err != nil {
			return "", err
		}
		after, err := os.Stat(logPath)
		if err != nil {
			return "", err
		}
		if !sameReflog(logInfo, after) {
			return "", errors.New("reflog changed during activity scan")
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
