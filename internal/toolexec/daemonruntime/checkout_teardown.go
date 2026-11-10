// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// Checkout teardowns by operation id (reliant-labs/reliant#2429, design 1).
//
// git.remove and git.reclone delete a project's checkout, and every branch
// workspace of the project on this machine is a git worktree of it. The
// server holds workspace creation back while a delete may still be running,
// and it can only release that hold on what this machine tells it. A reply is
// not enough: the server stops waiting after its timeout while the delete goes
// on, a retry can arrive before the first delivery, and the daemon can
// restart in the middle. So a request that carries an op_id is run as a
// durable operation:
//
//   - The op is journaled as running (fsynced, under the data dir) BEFORE
//     anything is deleted, and its outcome is journaled when it ends.
//   - A second delivery of the same op id — a retry, a redelivery — never
//     runs it again: it waits for the running one, or answers the outcome.
//   - The op runs detached from the request, so the server's timeout does not
//     abort it half way; the request answers "running" when it stops waiting.
//   - git.teardown_status answers an op's state. For an op id it has never
//     seen it records a tombstone first ("fenced") and says so, so a delivery
//     still in flight can never run after the server was told it did not.
//   - An op journaled running by a daemon that is gone (a restart) is
//     "interrupted": nothing is running it any more.
//
// A request without an op_id (an older server) runs exactly as before.

// checkoutTeardownDirName is the journal's directory under the data dir.
const checkoutTeardownDirName = "checkout-teardowns"

// Op states this daemon reports (the op_state field).
const (
	teardownRunning     = "running"
	teardownDone        = "done"
	teardownFailed      = "failed"
	teardownInterrupted = "interrupted"
	teardownFenced      = "fenced"
)

// teardownMaxDuration bounds one detached teardown (a reclone of a large
// repository included). A package var so a test can shrink it.
var teardownMaxDuration = 30 * time.Minute

// Settled records are never collected. A server row whose answer was lost
// can be retried at any time later, and a late delivery of a fenced op can
// arrive at any time later: an id this daemon forgot would be run as new,
// deleting twice, or let a fenced op run after the server released
// workspace creation. A record is a few hundred bytes per remove or reclone,
// so the journal stays small for the life of the data dir.

// teardownOpIDPattern keeps an op id usable as a file name.
var teardownOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// teardownRecord is one op, as journaled and as answered.
type teardownRecord struct {
	OpID      string    `json:"op_id"`
	Kind      string    `json:"kind,omitempty"`
	Path      string    `json:"path,omitempty"`
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	Removed   bool      `json:"removed,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// teardownJournal is the daemon's record of every op it has seen.
type teardownJournal struct {
	mu sync.Mutex
	// dir is the journal directory; "" keeps the records in memory only (a
	// runtime with no data dir).
	dir     string
	records map[string]*teardownRecord
	// running holds the done channel of each op this process is running.
	running map[string]chan struct{}
	// paths serializes ops on one checkout path, whatever their ids.
	paths map[string]*sync.Mutex
}

func newTeardownJournal(dir string) *teardownJournal {
	return &teardownJournal{
		dir:     dir,
		records: map[string]*teardownRecord{},
		running: map[string]chan struct{}{},
		paths:   map[string]*sync.Mutex{},
	}
}

// checkoutTeardowns is the runtime's journal. Command handlers are package
// functions, so it lives here; initializeCheckoutTeardowns opens it under the
// data dir at startup. Until it has loaded — before startup, in a runtime with
// no data dir, or when the journal could not be read — acknowledged teardowns
// are refused (err says why), never run on a memory-only stand-in: a record
// that a restart forgets is exactly a delete that could run twice, or a fence
// that stops holding.
var checkoutTeardowns = struct {
	sync.Mutex
	j   *teardownJournal
	err error
}{err: errors.New("the daemon has not opened its data directory yet")}

// currentTeardownJournal is the durable journal, or why there is none.
func currentTeardownJournal() (*teardownJournal, error) {
	checkoutTeardowns.Lock()
	defer checkoutTeardowns.Unlock()
	if checkoutTeardowns.j == nil {
		return nil, fmt.Errorf("this machine cannot record removes and reclones durably (%v), so it refuses them rather than risk "+
			"deleting a checkout twice; restart the daemon once its data directory is readable and writable", checkoutTeardowns.err)
	}
	return checkoutTeardowns.j, nil
}

// setTeardownJournal installs j, or with j nil records err as why acknowledged
// teardowns are refused.
func setTeardownJournal(j *teardownJournal, err error) {
	checkoutTeardowns.Lock()
	defer checkoutTeardowns.Unlock()
	checkoutTeardowns.j, checkoutTeardowns.err = j, err
}

// initializeCheckoutTeardowns loads the journal under dataDir: an op the last
// daemon left running is now interrupted. A journal that cannot be loaded is
// reported, and acknowledged teardowns are refused until a daemon loads it;
// it never stops the daemon.
func initializeCheckoutTeardowns(ctx context.Context, dataDir string) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		setTeardownJournal(nil, errors.New("this daemon has no data directory"))
		return
	}
	j, err := loadTeardownJournal(filepath.Join(dataDir, checkoutTeardownDirName), time.Now())
	if err != nil {
		logging.WarnContext(ctx, logPrefix+" Checkout teardown journal unavailable; removes and reclones the server acknowledges are refused until it loads", "error", err)
		setTeardownJournal(nil, fmt.Errorf("its journal could not be loaded: %w", err))
		return
	}
	setTeardownJournal(j, nil)
}

func loadTeardownJournal(dir string, now time.Time) (*teardownJournal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	j := newTeardownJournal(dir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var rec teardownRecord
		if err := json.Unmarshal(data, &rec); err != nil || !teardownOpIDPattern.MatchString(rec.OpID) || name != rec.OpID+".json" {
			// Writes are atomic renames, so a record of ours is never torn:
			// an unreadable one means the journal cannot be trusted to
			// remember every id, which is the one thing it is for.
			return nil, fmt.Errorf("unreadable teardown record %s", name)
		}
		if rec.State == teardownRunning {
			rec.State = teardownInterrupted
			rec.Error = "the machine restarted while it ran; the checkout may be partly removed"
			rec.UpdatedAt = now
			if err := j.persist(&rec); err != nil {
				return nil, err
			}
		}
		j.records[rec.OpID] = &rec
	}
	return j, nil
}

// persist writes rec durably. Memory-only journals write nothing.
func (j *teardownJournal) persist(rec *teardownRecord) error {
	if j.dir == "" {
		return nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(j.dir, rec.OpID+".json"), data, 0o600); err != nil {
		return err
	}
	// The rename is durable only once the directory is.
	d, err := os.Open(j.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// errTeardownMismatch is a known op id asked for a different checkout or kind.
var errTeardownMismatch = errors.New("operation id already names a different teardown")

// run runs op once per op id. fn does the deletion and reports its outcome; it
// runs on a context detached from ctx, bounded by teardownMaxDuration. run
// answers the op's record: its outcome, or state running when ctx ends first.
// An op this daemon already knows is never run again.
func (j *teardownJournal) run(ctx context.Context, opID, kind, path string, fn func(context.Context) (removed bool, err error)) (teardownRecord, error) {
	if !teardownOpIDPattern.MatchString(opID) {
		return teardownRecord{}, fmt.Errorf("invalid op_id %q", opID)
	}
	j.mu.Lock()
	if rec, ok := j.records[opID]; ok {
		if rec.Kind != "" && (rec.Kind != kind || rec.Path != path) {
			j.mu.Unlock()
			return teardownRecord{}, fmt.Errorf("%w: %s is a %s of %s", errTeardownMismatch, opID, rec.Kind, rec.Path)
		}
		done := j.running[opID]
		j.mu.Unlock()
		return j.await(ctx, opID, done), nil
	}
	rec := &teardownRecord{OpID: opID, Kind: kind, Path: path, State: teardownRunning, UpdatedAt: time.Now().UTC()}
	// Journaled before anything is deleted: a delete the journal does not
	// know about is one the server could never be told the outcome of.
	if err := j.persist(rec); err != nil {
		j.mu.Unlock()
		return teardownRecord{}, fmt.Errorf("cannot journal the teardown, so nothing was removed: %w", err)
	}
	j.records[opID] = rec
	done := make(chan struct{})
	j.running[opID] = done
	pathLock := j.paths[path]
	if pathLock == nil {
		pathLock = &sync.Mutex{}
		j.paths[path] = pathLock
	}
	j.mu.Unlock()

	go func() {
		defer close(done)
		opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownMaxDuration)
		defer cancel()
		pathLock.Lock()
		removed, err := fn(opCtx)
		pathLock.Unlock()
		j.mu.Lock()
		defer j.mu.Unlock()
		final := &teardownRecord{OpID: opID, Kind: kind, Path: path, State: teardownDone, Removed: removed, UpdatedAt: time.Now().UTC()}
		if err != nil {
			final.State, final.Error = teardownFailed, err.Error()
		}
		if perr := j.persist(final); perr != nil {
			// The outcome stands in memory; after a restart the op reads
			// interrupted, which the server treats as failed — never as done.
			logging.WarnContext(opCtx, logPrefix+" Failed to journal a checkout teardown's outcome", "op_id", opID, "error", perr)
		}
		j.records[opID] = final
		delete(j.running, opID)
	}()
	return j.await(ctx, opID, done), nil
}

// await waits for op's done channel (nil: not running in this process) or
// ctx, then answers its record.
func (j *teardownJournal) await(ctx context.Context, opID string, done chan struct{}) teardownRecord {
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return *j.records[opID]
}

// status answers op's state. An op id never seen is fenced: recorded so a
// delivery still in flight is refused, then answered as fenced.
func (j *teardownJournal) status(opID string) (teardownRecord, error) {
	if !teardownOpIDPattern.MatchString(opID) {
		return teardownRecord{}, fmt.Errorf("invalid op_id %q", opID)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if rec, ok := j.records[opID]; ok {
		return *rec, nil
	}
	rec := &teardownRecord{OpID: opID, State: teardownFenced, UpdatedAt: time.Now().UTC()}
	if err := j.persist(rec); err != nil {
		// Without the tombstone a late delivery could still run, so this
		// must not answer fenced.
		return teardownRecord{}, fmt.Errorf("cannot record the fence: %w", err)
	}
	j.records[opID] = rec
	return *rec, nil
}

// =============================================================================
// git.teardown_status
// =============================================================================

type gitTeardownStatusRequest struct {
	OpID string `json:"op_id"`
}

type gitTeardownStatusResponse struct {
	OpID    string `json:"op_id"`
	OpState string `json:"op_state"`
	Error   string `json:"error,omitempty"`
	Removed bool   `json:"removed,omitempty"`
}

func handleGitTeardownStatus(_ context.Context, payload []byte) ([]byte, error) {
	var req gitTeardownStatusRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	j, err := currentTeardownJournal()
	if err != nil {
		return nil, err
	}
	rec, err := j.status(req.OpID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(gitTeardownStatusResponse{OpID: rec.OpID, OpState: rec.State, Error: rec.Error, Removed: rec.Removed})
}
