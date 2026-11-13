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
// watched, its policy re-resolved every WatchInterval, and the first answer
// with a different version wakes the org's blocked chats (a capacity wake,
// the one adding an account sends). A wake retries each blocked thread once;
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

// Waker wakes an org's chats that may have a thread blocked. It reports how
// many it reached.
type Waker interface {
	WakeOrgChats(ctx context.Context, orgID, why string) int
}

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
	// version is the policy version the refusal was met under; -1 when it
	// could not be read, so the first answer read counts as a change.
	version int64
	until   time.Time
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
	if p, err := w.r.Resolve(ctx, orgID); err == nil {
		version = p.Version
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	until := w.now().Add(WatchFor)
	if cur, ok := w.orgs[orgID]; ok {
		// The version the first refusal was met under stands: a change since
		// then is what the waiting chats are waiting for.
		cur.until = until
		return
	}
	w.orgs[orgID] = &watched{version: version, until: until}
}

// Check re-resolves every watched org once, wakes the chats of each whose
// policy version changed, and forgets those and the expired ones.
func (w *Watcher) Check(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	now := w.now()
	due := make(map[string]int64, len(w.orgs))
	for org, wt := range w.orgs {
		if !now.Before(wt.until) {
			delete(w.orgs, org)
			continue
		}
		due[org] = wt.version
	}
	w.mu.Unlock()
	for org, version := range due {
		p, err := w.r.Resolve(ctx, org)
		if err != nil || p.Stale || p.Version == version {
			continue
		}
		w.mu.Lock()
		delete(w.orgs, org)
		w.mu.Unlock()
		n := w.wake.WakeOrgChats(ctx, org, "the organization's policy changed")
		logging.InfoContext(ctx, "orgpolicy: the policy of an org with refused work changed; woke its blocked chats",
			"org_id", org, "from_version", version, "to_version", p.Version, "woken", n)
	}
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
