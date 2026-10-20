// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"context"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/providerusage"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	defaultFlushEvery = 5 * time.Second
	writeTimeout      = 15 * time.Second
	// forgetIdleAfter drops the in-memory entry of an account nothing has
	// observed for this long, so the map follows the accounts in use.
	forgetIdleAfter = time.Hour
)

// snapshotStore is the slice of the account store the Recorder writes through.
type snapshotStore interface {
	UpsertAccountLimitSnapshot(ctx context.Context, s core.LimitState) error
	BenchAccount(ctx context.Context, userID, accountID string, until time.Time, window string, probe bool) error
	GetAccountLimits(ctx context.Context, accountID string) (*core.LimitState, error)
	EnsureUsagePollScheduled(ctx context.Context, accountID string, next time.Time, providers []string) error
}

// Recorder persists the rate-limit snapshots LLM responses carry, per account.
//
// ObserveRateLimits runs on the request path, so it does no I/O and takes no
// lock a slow store could hold: it folds the snapshot into an in-memory entry
// and returns. A background flusher writes an entry when its status or a
// window's reset changed, when a used fraction moved by 2 points, or when a
// minute has passed since the last write and anything was observed. An active
// agent (≈10 calls/min) therefore costs about one write a minute per account.
// The store's upsert is monotonic in observed_at, so several workers writing
// the same account never regress it.
type Recorder struct {
	store snapshotStore
	clock func() time.Time

	flushEvery time.Duration
	wake       chan struct{}
	stop       chan struct{}
	done       chan struct{}
	closeOnce  sync.Once

	mu       sync.Mutex
	accounts map[string]*recorded
}

// recorded is the in-memory state of one account.
type recorded struct {
	userID string
	// latest is the newest snapshot observed; pending says it is not written.
	latest  core.LimitState
	pending bool
	// urgent says the pending snapshot should not wait for the heartbeat.
	urgent bool
	// written is the last state the store acknowledged.
	written    core.LimitState
	hasWritten bool
	wroteAt    time.Time
}

// NewRecorder returns a Recorder and starts its flusher. clock may be nil
// (time.Now). Close it on shutdown to flush what is pending.
func NewRecorder(store snapshotStore, clock func() time.Time) *Recorder {
	r := newRecorder(store, clock, defaultFlushEvery)
	go r.run()
	return r
}

func newRecorder(store snapshotStore, clock func() time.Time, flushEvery time.Duration) *Recorder {
	if clock == nil {
		clock = time.Now
	}
	return &Recorder{
		store:      store,
		clock:      clock,
		flushEvery: flushEvery,
		wake:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		accounts:   map[string]*recorded{},
	}
}

// For returns the Observer to install (ratelimit.WithObserver) on a request
// served by the account.
func (r *Recorder) For(userID, accountID string) ratelimit.Observer {
	return accountObserver{r: r, userID: userID, accountID: accountID}
}

type accountObserver struct {
	r         *Recorder
	userID    string
	accountID string
}

func (o accountObserver) ObserveRateLimits(_ context.Context, s ratelimit.Snapshot) {
	o.r.observe(o.userID, o.accountID, s)
}

func (r *Recorder) observe(userID, accountID string, s ratelimit.Snapshot) {
	if accountID == "" || userID == "" {
		return
	}
	st := LimitStateFromSnapshot(userID, accountID, SourceHeaders, s, r.clock())

	r.mu.Lock()
	e := r.accounts[accountID]
	if e == nil {
		e = &recorded{userID: userID}
		r.accounts[accountID] = e
	}
	// Responses finish out of order; the newest observation wins.
	if e.pending && st.ObservedAt.Before(e.latest.ObservedAt) {
		r.mu.Unlock()
		return
	}
	e.latest, e.pending = st, true
	urgent := !e.hasWritten || materiallyDiffers(e.written, st)
	if urgent {
		e.urgent = true
	}
	r.mu.Unlock()

	if urgent {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// Bench records the account refused until `until`; the zero time means the
// provider did not say, and the account is benched for the next rung of the
// probe ladder (5 m, 15 m, 30 m, 1 h repeating). It returns the bench in
// force, which can be later than asked when the account was already benched
// past it. window uses the vocabulary of ratelimit.UsageLimitError.Window
// ("five_hour", "seven_day").
//
// A poll is scheduled to confirm the reset 30 s after the bench ends.
func (r *Recorder) Bench(ctx context.Context, userID, accountID string, until time.Time, window string) (time.Time, error) {
	eff, err := bench(ctx, r.store, r.clock(), userID, accountID, until, window)
	if err != nil {
		return time.Time{}, err
	}
	r.schedulePoll(ctx, accountID, eff.Add(confirmAfterReset))
	return eff, nil
}

// Close stops the flusher after one last flush.
func (r *Recorder) Close() {
	r.closeOnce.Do(func() { close(r.stop) })
	<-r.done
}

func (r *Recorder) run() {
	defer close(r.done)
	t := time.NewTicker(r.flushEvery)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			r.flush(context.Background(), true)
			return
		case <-r.wake:
		case <-t.C:
		}
		r.flush(context.Background(), false)
	}
}

type flushItem struct {
	accountID string
	userID    string
	state     core.LimitState
}

// flush writes every pending entry that is due (all of them when force).
func (r *Recorder) flush(ctx context.Context, force bool) {
	now := r.clock()

	r.mu.Lock()
	var due []flushItem
	for id, e := range r.accounts {
		switch {
		case e.pending && (force || e.urgent || !e.hasWritten || now.Sub(e.wroteAt) >= heartbeat):
			due = append(due, flushItem{accountID: id, userID: e.userID, state: e.latest})
			e.pending, e.urgent = false, false
		case !e.pending && e.hasWritten && now.Sub(e.wroteAt) >= forgetIdleAfter:
			delete(r.accounts, id)
		}
	}
	r.mu.Unlock()

	for _, it := range due {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		err := r.write(wctx, it, now)
		cancel()

		r.mu.Lock()
		e := r.accounts[it.accountID]
		switch {
		case e == nil:
		case err != nil:
			// Keep the newer observation if there is one; retry on the next tick.
			if !e.pending {
				e.latest = it.state
			}
			e.pending, e.urgent = true, true
		default:
			e.written, e.hasWritten, e.wroteAt = it.state, true, now
		}
		r.mu.Unlock()
		if err != nil {
			logging.WarnContext(ctx, "persisting rate-limit snapshot failed", "account_id", it.accountID, "error", err)
		}
	}
}

func (r *Recorder) write(ctx context.Context, it flushItem, now time.Time) error {
	if err := r.store.UpsertAccountLimitSnapshot(ctx, it.state); err != nil {
		return err
	}
	// A refused request with a known reset benches the account for it.
	if ex := exhausted(it.state.Windows, it.state.RepresentativeWindow, it.state.Status, now); ex.Exhausted && !ex.Until.IsZero() {
		if eff, err := bench(ctx, r.store, now, it.userID, it.accountID, ex.Until, ex.Window); err != nil {
			logging.WarnContext(ctx, "benching account from its rate-limit snapshot failed", "account_id", it.accountID, "error", err)
		} else {
			r.schedulePoll(ctx, it.accountID, eff.Add(confirmAfterReset))
		}
		return nil
	}
	// Headers are fresher than a usage reading for a while: make sure the
	// account gets looked at once they stop coming.
	r.schedulePoll(ctx, it.accountID, it.state.ObservedAt.Add(headerFreshFor))
	return nil
}

// schedulePoll asks for a usage poll at `at` unless the account already has
// one scheduled. Best effort: a missed schedule only costs a poll.
func (r *Recorder) schedulePoll(ctx context.Context, accountID string, at time.Time) {
	if err := r.store.EnsureUsagePollScheduled(ctx, accountID, at, pollableProviders()); err != nil {
		logging.DebugContext(ctx, "scheduling usage poll failed", "account_id", accountID, "error", err)
	}
}

// pollableProviders are the providers whose usage API the poller may call.
func pollableProviders() []string {
	var out []string
	for _, p := range []string{providerusage.ProviderClaude, providerusage.ProviderCodex} {
		if providerusage.UsageAPIEnabled(p) {
			out = append(out, p)
		}
	}
	return out
}
