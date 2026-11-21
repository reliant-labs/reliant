package changes

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// EnvFile names the environment variable that holds the snapshot file's
// path. Reliant hosting sets it on every hosted workload
// (FORGE_CHANGES_FILE=/etc/forge/changes/snapshot.json, a mounted file the
// platform rewrites when a change's delivery moves); in local dev point it at
// any file, such as .forge/changes/dev.json. This package never reads the
// environment itself, so the app passes the value in:
//
//	store := changes.OpenFile(ctx, os.Getenv(changes.EnvFile), changes.Options{})
//	defer store.Close()
const EnvFile = "FORGE_CHANGES_FILE"

// DefaultPollInterval is how often a Store re-reads its file when Options
// names no interval.
const DefaultPollInterval = 10 * time.Second

// Options configures a Store. The zero value is ready to use.
type Options struct {
	// PollInterval is how often the file is re-read. Zero is
	// DefaultPollInterval.
	PollInterval time.Duration
	// OnEvaluate observes every Store.Evaluate. Nil records the evaluation
	// as the product event flag_evaluated (see Store.Evaluate). A non-nil
	// function replaces that recording; pass a function that does nothing
	// to record nothing.
	OnEvaluate func(ctx context.Context, key string, r Result)
	// Logger receives the Store's warnings: a snapshot it could not read or
	// parse. Nil is slog.Default().
	Logger *slog.Logger
}

// Store is the last good snapshot of a file, kept current by polling. It is
// safe for concurrent use; Evaluate never blocks on I/O.
//
// A Store never fails. A file that is missing (or a path that is empty) is an
// empty snapshot: every key evaluates as ReasonUnknown, so every call site
// gets the default written in code. A file that cannot be read or parsed
// leaves the last good snapshot in place, with one warning per distinct
// error.
type Store struct {
	path       string
	interval   time.Duration
	onEvaluate func(ctx context.Context, key string, r Result)
	logger     *slog.Logger
	recorder   *recorder

	snap atomic.Pointer[Snapshot]

	// The file as last read, owned by reload (one caller at a time: the
	// poll loop, or a test).
	mu      sync.Mutex
	read    bool     // a read has completed
	missing bool     // the last read found no file
	sum     [32]byte // content hash of the last file read
	lastErr string   // the last read or parse error warned about

	stop      context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// OpenFile returns a Store over the snapshot file at path. It reads the file
// once before returning, then re-reads it every PollInterval until ctx is
// done or Close is called, and swaps in a new snapshot only when the file's
// content changed and parses. An empty path is an empty snapshot that is
// never polled.
func OpenFile(ctx context.Context, path string, opts Options) *Store {
	s := newStore(path, opts)
	if path == "" {
		close(s.done)
		return s
	}
	t := time.NewTicker(s.interval)
	s.start(ctx, t.C, t.Stop)
	return s
}

// start reads the file once, then polls it on every tick in a goroutine
// until ctx is done or Close is called. stopTick runs as the loop exits.
// Tests drive it with a channel of their own, so no test waits on a clock.
func (s *Store) start(ctx context.Context, tick <-chan time.Time, stopTick func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.reload()
	ctx, s.stop = context.WithCancel(ctx)
	go func() {
		defer stopTick()
		s.poll(ctx, tick)
	}()
}

func newStore(path string, opts Options) *Store {
	s := &Store{
		path:       path,
		interval:   opts.PollInterval,
		onEvaluate: opts.OnEvaluate,
		logger:     opts.Logger,
		stop:       func() {},
		done:       make(chan struct{}),
	}
	if s.interval <= 0 {
		s.interval = DefaultPollInterval
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	if s.onEvaluate == nil {
		s.recorder = newRecorder()
	}
	return s
}

// poll re-reads the file on every tick until ctx is done. It is the Store's
// only goroutine; Close waits for it.
func (s *Store) poll(ctx context.Context, tick <-chan time.Time) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			s.reload()
		}
	}
}

// reload reads the file and swaps in its snapshot when the content changed.
// A snapshot file is a few kilobytes, so it is read whole on every poll and
// compared by content hash: unlike a modification time, that cannot miss a
// rewrite inside the filesystem's timestamp granularity, nor a mounted
// ConfigMap whose update swaps a symlink.
func (s *Store) reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		if s.read && s.missing {
			return
		}
		s.read, s.missing, s.sum = true, true, [32]byte{}
		s.snap.Store(nil)
		s.logger.Info("changes: no snapshot file; every change evaluates to the default in code", slog.String("path", s.path))
		return
	}
	if err != nil {
		s.warnOnce("changes: cannot read the snapshot file; keeping the last good snapshot", err)
		return
	}
	sum := sha256.Sum256(data)
	if s.read && !s.missing && sum == s.sum {
		return
	}
	snap, err := ParseSnapshot(data)
	if err != nil {
		s.warnOnce("changes: cannot parse the snapshot file; keeping the last good snapshot", err)
		return
	}
	s.read, s.missing, s.sum, s.lastErr = true, false, sum, ""
	s.snap.Store(snap)
}

func (s *Store) warnOnce(msg string, err error) {
	if err.Error() == s.lastErr {
		return
	}
	s.lastErr = err.Error()
	s.logger.Warn(msg, slog.String("path", s.path), slog.String("error", s.lastErr))
}

// Evaluate decides the change key for ec from the current snapshot, exactly
// as the package-level Evaluate does, then reports the result to
// Options.OnEvaluate.
//
// With no OnEvaluate it records the product event flag_evaluated (through
// pkg/observe's Track, so it carries the caller's user and org ids) with the
// properties flag, variant, provider=forge, run, unit, reason and revision.
// Only results a readout or a dashboard can use are recorded: enrolled ones
// (ReasonRollout, ReasonExperiment), which are a unit's assignment, plus
// ReasonPreview and ReasonEveryone. One event per unit, change, run and
// variant per UTC day per Store: the first evaluation is the assignment, and
// it is recorded before the app acts on it, so a broken variant cannot hide
// from its own denominator.
func (s *Store) Evaluate(ctx context.Context, key string, ec EvalContext) Result {
	snap := s.snap.Load()
	r := Evaluate(snap, key, ec)
	if s.onEvaluate != nil {
		s.onEvaluate(ctx, key, r)
	} else {
		s.recorder.record(ctx, snap, key, ec, r)
	}
	return r
}

// Revision is the current snapshot's revision, 0 when there is none.
func (s *Store) Revision() int64 {
	return s.snap.Load().Revision()
}

// Close stops polling and waits for the poll loop to exit. The Store keeps
// answering Evaluate from its last snapshot. Close is idempotent and never
// fails.
func (s *Store) Close() error {
	s.closeOnce.Do(s.stop)
	<-s.done
	return nil
}
