// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ── Waking the chats a policy paused (§5.1, §5.2) ─────────────────────
//
// A chat paused on a policy refusal goes on by itself once its org's policy
// changes: a cap raised or removed, a model allowed. Control-plane tells
// reliant nothing, so the Watcher asks: every org a refusal was met in is
// watched, its policy re-resolved every WatchInterval, and each fresh answer
// with a newer version than the last one seen wakes the org's blocked chats (a capacity wake,
// the one adding an account sends). A wake is owed until it reaches every
// chat: a delivery an outage interrupted is retried, with backoff, for the
// runs it missed. A capacity wake is idempotent, so a chat woken twice only
// retries its blocked thread once more. A wake retries each blocked thread once;
// a policy that still refuses pauses it again at the cost of one refused
// request. An org is watched for at most WatchFor after its last refusal.
//
// The set lives in the process that met the refusal; a restart forgets it,
// and those chats wait for a person's resume, as before. A period rollover
// (a monthly cap resetting) is not an edit and wakes nothing.

// WatchInterval is how often a watched org's policy is re-resolved.
const WatchInterval = CacheTTL

// WatchFor is how long after its last refusal an org stays watched.
const WatchFor = 24 * time.Hour

// Waker wakes an org's chats that may have a thread blocked.
type Waker interface {
	// WakeOrgChats wakes every blocked chat of orgID. It returns the runs it
	// could not signal, and an error when it could not list them at all.
	WakeOrgChats(ctx context.Context, orgID, why string) (failed []string, err error)
	// WakeRuns wakes the named runs again and returns those it still could
	// not signal.
	WakeRuns(ctx context.Context, workflowIDs []string, why string) (failed []string)
}

// wakeBackoff bounds the wait between attempts to deliver one org's wake
// after a failed one: WatchInterval, doubling, at most wakeBackoffMax.
const wakeBackoffMax = 10 * time.Minute

// Watcher wakes an org's paused chats when its policy changes. A nil
// *Watcher watches nothing.
type Watcher struct {
	r    *Resolver
	wake Waker
	now  func() time.Time

	mu   sync.Mutex
	orgs map[string]*watched
}

type watched struct {
	// version is the policy version the refusal was met under: the baseline
	// a change is measured from. -1 while it is unknown (the policy could
	// not be read fresh when the refusal was met); the first fresh answer
	// then SETS the baseline and wakes nothing, so an outage's recovery is
	// never mistaken for a policy change.
	version int64
	until   time.Time

	// changed is set once the version moved: from then on the org's wake is
	// owed until it is delivered in full. pending are the runs a partial
	// delivery could not signal (nil: the whole org is still owed); retryAt
	// and attempts pace the next delivery.
	changed  bool
	pending  []string
	retryAt  time.Time
	attempts int
}

// NewWatcher watches through r and wakes through wake; nil when either is
// missing (no policies, or nothing to wake).
func NewWatcher(r *Resolver, wake Waker) *Watcher {
	if r == nil || wake == nil {
		return nil
	}
	return &Watcher{r: r, wake: wake, now: time.Now, orgs: map[string]*watched{}}
}

// Watch starts (or extends) watching orgID after a refusal met in it.
func (w *Watcher) Watch(ctx context.Context, orgID string) {
	if w == nil || orgID == "" {
		return
	}
	version := int64(-1)
	if p, err := w.r.Resolve(ctx, orgID); err == nil && !p.Stale {
		version = p.Version
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	until := w.now().Add(WatchFor)
	if cur, ok := w.orgs[orgID]; ok {
		// The version the first refusal was met under stands: a change since
		// then is what the waiting chats are waiting for. An unknown one is
		// learned from this refusal's fresh answer.
		cur.until = until
		if cur.version < 0 && !cur.changed {
			cur.version = version
		}
		return
	}
	w.orgs[orgID] = &watched{version: version, until: until}
}

// Check re-resolves every watched org once and owes a wake to each whose
// policy moved to a newer version (judged on fresh answers only, against a
// baseline itself taken from a fresh answer), then delivers what is owed. A
// delivery a database or Temporal outage interrupted is retried with backoff,
// and only for the runs it missed. An org stays watched for its whole window
// (WatchFor after its last refusal), so each later loosening wakes it again;
// it is forgotten when the window ends with nothing owed.
func (w *Watcher) Check(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	now := w.now()
	due := make([]string, 0, len(w.orgs))
	for org, wt := range w.orgs {
		if !now.Before(wt.until) && !wt.changed {
			delete(w.orgs, org)
			continue
		}
		due = append(due, org)
	}
	w.mu.Unlock()
	for _, org := range due {
		w.checkOrg(ctx, org)
	}
}

func (w *Watcher) checkOrg(ctx context.Context, org string) {
	w.mu.Lock()
	wt, ok := w.orgs[org]
	if !ok {
		w.mu.Unlock()
		return
	}
	owed, version, retryAt := wt.changed, wt.version, wt.retryAt
	pending := append([]string(nil), wt.pending...)
	w.mu.Unlock()

	if !owed {
		p, err := w.r.Resolve(ctx, org)
		if err != nil || p.Stale {
			// Only a fresh answer says anything about the policy now.
			return
		}
		w.mu.Lock()
		cur, ok := w.orgs[org]
		switch {
		case !ok:
			w.mu.Unlock()
			return
		case cur.version < 0:
			// The first fresh answer since a refusal met during an outage is
			// the baseline, not a change.
			cur.version = p.Version
			w.mu.Unlock()
			return
		case p.Version <= cur.version:
			w.mu.Unlock()
			return
		}
		// A newer policy: the org's wake is owed from here until delivered,
		// and the next change is measured from this version.
		cur.version, cur.changed, cur.pending, cur.attempts = p.Version, true, nil, 0
		w.mu.Unlock()
		logging.InfoContext(ctx, "orgpolicy: the policy of an org with refused work changed; waking its blocked chats",
			"org_id", org, "from_version", version, "to_version", p.Version)
		pending = nil
	} else if w.now().Before(retryAt) {
		return
	}

	const why = "the organization's policy changed"
	var failed []string
	var err error
	if len(pending) > 0 {
		failed = w.wake.WakeRuns(ctx, pending, why)
	} else {
		failed, err = w.wake.WakeOrgChats(ctx, org, why)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	cur, ok := w.orgs[org]
	if !ok {
		return
	}
	if err == nil && len(failed) == 0 {
		// Delivered. The org stays watched until its window ends, so a
		// later change (a second loosening) wakes it again.
		cur.changed, cur.pending, cur.attempts, cur.retryAt = false, nil, 0, time.Time{}
		return
	}
	if err == nil {
		cur.pending = failed
	} // a listing failure leaves pending as it was: nil owes the whole org
	cur.attempts++
	backoff := WatchInterval << min(cur.attempts-1, 5)
	if backoff > wakeBackoffMax {
		backoff = wakeBackoffMax
	}
	cur.retryAt = w.now().Add(backoff)
	// A wake still owed keeps the org watched past its refusal's window.
	if until := cur.retryAt.Add(WatchInterval); cur.until.Before(until) {
		cur.until = until
	}
	logging.WarnContext(ctx, "orgpolicy: could not wake every blocked chat of an org whose policy changed; trying again",
		"org_id", org, "unreached", len(failed), "error", err, "retry_in", backoff)
}

// Run calls Check every WatchInterval until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil {
		return
	}
	t := time.NewTicker(WatchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Check(ctx)
		}
	}
}

// processWatcher is the Watcher refusals in this process register with.
var processWatcher atomic.Pointer[Watcher]

// SetProcessWatcher installs w as the process's Watcher (nil: none).
func SetProcessWatcher(w *Watcher) { processWatcher.Store(w) }

// WatchRefused registers a refusal met in orgID with the process's Watcher,
// so the org's chats are woken when its policy changes.
func WatchRefused(ctx context.Context, orgID string) {
	processWatcher.Load().Watch(ctx, orgID)
}
