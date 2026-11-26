package changes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
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
//
// OpenFromEnv reads it too, and falls back to fetching from the control
// plane (EnvURL) when it is unset.
const EnvFile = "FORGE_CHANGES_FILE"

// DefaultPollInterval is how often a Store re-reads its file when Options
// names no interval.
const DefaultPollInterval = 10 * time.Second

// Options configures a Store. The zero value is ready to use.
type Options struct {
	// PollInterval is how often the source is re-read. Zero is
	// DefaultPollInterval for a file (OpenFile) and DefaultFetchInterval
	// for the control plane (OpenControlPlane).
	PollInterval time.Duration
	// OnEvaluate observes every Store.Evaluate. Nil records the evaluation
	// as the product event flag_evaluated (see Store.Evaluate). A non-nil
	// function replaces that recording; pass a function that does nothing
	// to record nothing.
	OnEvaluate func(ctx context.Context, key string, r Result)
	// Logger receives the Store's warnings: a snapshot it could not read,
	// fetch or parse. Nil is slog.Default().
	Logger *slog.Logger
}

// Store is the last good snapshot of a source — a file (OpenFile) or the
// control plane (OpenControlPlane) — kept current by polling. It is safe for
// concurrent use; Evaluate never blocks on I/O.
//
// A Store never fails. A file that is missing (or a path that is empty) is an
// empty snapshot: every key evaluates as ReasonUnknown, so every call site
// gets the default written in code. A snapshot that cannot be read, fetched
// or parsed leaves the last good snapshot in place, with one warning per
// distinct error. An old snapshot is never dropped for being old: falling
// back to defaults would flip every unit mid-run, which is worse than a stale
// policy.
type Store struct {
	src        snapshotSource
	interval   time.Duration
	onEvaluate func(ctx context.Context, key string, r Result)
	logger     *slog.Logger
	recorder   *recorder

	snap atomic.Pointer[Snapshot]

	// The source as last read, owned by reload (one caller at a time: the
	// poll loop, or a test).
	mu      sync.Mutex
	read    bool   // a read has completed
	missing bool   // the last read found no snapshot
	version string // what identifies the last snapshot read (hash or etag)
	lastErr string // the last read or parse error warned about

	// Backoff, for a source whose failures should slow its polling (a
	// fetch). failures counts consecutive ones; skip is how many ticks pass
	// before the next attempt; maxSkip caps it; jitter picks a wait in
	// [ceil(w/2), w] ticks so a fleet does not retry in step.
	failures int
	skip     int
	maxSkip  int
	jitter   func(w int) int

	stop      context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// snapshotSource is where a Store's snapshot comes from. read is called by
// one goroutine at a time.
type snapshotSource interface {
	read(ctx context.Context) sourceRead
	// noun names the snapshot in a warning: "the snapshot file".
	noun() string
	// attrs identify the source in a log line. Never a credential.
	attrs() []any
	// monotonic reports whether a snapshot older than the last good one is
	// refused (a fetch: an older revision is a lagging server, and applying
	// it would move units back). A file is the operator's to rewrite.
	monotonic() bool
}

// sourceRead is one read of a source.
type sourceRead struct {
	data    []byte // the snapshot document
	missing bool   // the source has no snapshot: an empty store
	// version identifies data: a read whose version equals the last good
	// one's is not parsed again.
	version string
	err     error
	msg     string // the warning (err) or the info line (missing)
	backoff bool   // err is the source's: poll it less often until it recovers
}

// fileSource reads a snapshot file whole.
type fileSource struct{ path string }

// read reads the file whole: a snapshot file is a few kilobytes, and
// comparing it by content hash cannot miss a rewrite inside the filesystem's
// timestamp granularity, nor a mounted ConfigMap whose update swaps a
// symlink, as a modification time can.
func (f fileSource) read(context.Context) sourceRead {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return sourceRead{missing: true, msg: "changes: no snapshot file; every change evaluates to the default in code"}
	}
	if err != nil {
		return sourceRead{err: err, msg: "changes: cannot read the snapshot file; keeping the last good snapshot"}
	}
	sum := sha256.Sum256(data)
	return sourceRead{data: data, version: hex.EncodeToString(sum[:])}
}

func (f fileSource) noun() string    { return "the snapshot file" }
func (f fileSource) attrs() []any    { return []any{slog.String("path", f.path)} }
func (f fileSource) monotonic() bool { return false }

// OpenFile returns a Store over the snapshot file at path. It reads the file
// once before returning, then re-reads it every PollInterval until ctx is
// done or Close is called, and swaps in a new snapshot only when the file's
// content changed and parses. An empty path is an empty snapshot that is
// never polled.
func OpenFile(ctx context.Context, path string, opts Options) *Store {
	if path == "" {
		s := newStore(nil, opts)
		close(s.done)
		return s
	}
	s := newStore(fileSource{path: path}, opts)
	t := time.NewTicker(s.interval)
	s.start(ctx, t.C, t.Stop)
	return s
}

// start reads the source once, then polls it on every tick in a goroutine
// until ctx is done or Close is called. stopTick runs as the loop exits.
// Tests drive it with a channel of their own, so no test waits on a clock.
func (s *Store) start(ctx context.Context, tick <-chan time.Time, stopTick func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.reload(ctx)
	ctx, s.stop = context.WithCancel(ctx)
	go func() {
		defer stopTick()
		s.poll(ctx, tick)
	}()
}

func newStore(src snapshotSource, opts Options) *Store {
	s := &Store{
		src:        src,
		interval:   opts.PollInterval,
		onEvaluate: opts.OnEvaluate,
		logger:     opts.Logger,
		jitter:     jitter,
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

// jitter is a uniform wait in [ceil(w/2), w] ticks.
func jitter(w int) int {
	lo := (w + 1) / 2
	return lo + rand.IntN(w-lo+1)
}

// poll re-reads the source on every tick until ctx is done. It is the
// Store's only goroutine; Close waits for it.
func (s *Store) poll(ctx context.Context, tick <-chan time.Time) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			s.onTick(ctx)
		}
	}
}

// onTick reloads, unless a backoff says to let this tick pass.
func (s *Store) onTick(ctx context.Context) {
	s.mu.Lock()
	if s.skip > 0 {
		s.skip--
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.reload(ctx)
}

// reload reads the source and swaps in its snapshot when it changed, parses,
// and (for a monotonic source) is not older than the last good one.
func (s *Store) reload(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.src.read(ctx)
	if r.err != nil {
		s.warnOnce(ctx, r.msg, r.err)
		if r.backoff {
			s.backOff()
		}
		return
	}
	s.failures, s.skip = 0, 0
	if r.missing {
		if s.read && s.missing {
			return
		}
		s.read, s.missing, s.version = true, true, ""
		s.snap.Store(nil)
		s.logger.InfoContext(ctx, r.msg, s.src.attrs()...)
		return
	}
	if s.read && !s.missing && r.version == s.version {
		return
	}
	snap, err := ParseSnapshot(r.data)
	if err != nil {
		s.warnOnce(ctx, "changes: cannot parse "+s.src.noun()+"; keeping the last good snapshot", err)
		return
	}
	if cur := s.snap.Load(); s.src.monotonic() && cur != nil && snap.Revision() < cur.Revision() {
		s.warnOnce(ctx, "changes: "+s.src.noun()+" is older than the last good snapshot; keeping the last good snapshot",
			fmt.Errorf("revision %d is older than %d", snap.Revision(), cur.Revision()))
		return
	}
	s.read, s.missing, s.version, s.lastErr = true, false, r.version, ""
	s.snap.Store(snap)
}

// backOff records one more consecutive failure and lets the next
// min(2^(n-1), maxSkip) ticks, jittered, pass before the next attempt.
func (s *Store) backOff() {
	s.failures++
	w := s.maxSkip
	if s.failures-1 < 30 && 1<<(s.failures-1) < w {
		w = 1 << (s.failures - 1)
	}
	if w < 1 {
		w = 1
	}
	s.skip = s.jitter(w) - 1
}

func (s *Store) warnOnce(ctx context.Context, msg string, err error) {
	if err.Error() == s.lastErr {
		return
	}
	s.lastErr = err.Error()
	s.logger.WarnContext(ctx, msg, append(s.src.attrs(), slog.String("error", s.lastErr))...)
}

// Evaluate decides the change key for ec from the current snapshot, exactly
// as the package-level Evaluate does, then reports the result to
// Options.OnEvaluate.
//
// With no OnEvaluate it records the product event flag_evaluated (through
// pkg/observe's Track, so it carries the caller's user and org ids) with the
// properties flag, variant, provider=forge, run, unit, reason and revision,
// plus account (the unit id) when the change's unit is account.
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
