package cli

// The read-through cache of a HOSTED ledger, and the replay of what was
// recorded against it while it was away.
//
// WHY. An env that declares a control plane keeps its ledger there. If that
// control plane is down, `forge env deploy` cannot resolve the env's current
// release, and this machine's ledger holds none of it, so the only fallback
// used to be an unrehearsed fresh cut that rebuilds every image. The cache is
// what `forge env deploy <env> --offline-ledger` deploys from (offline_ledger.go).
//
// A CACHE, NEVER AUTHORITY. It is written on every successful hosted read of
// the env's current promotion and every successful promote, and read ONLY by
// the offline path. An online command never consults it, so a stale cache
// cannot change what an online deploy ships.
//
// REPLAY. A deploy made offline queues its promotion record (plus the apply's
// verdict). The next time the control plane answers a read, the queue is
// replayed against it, idempotently: the promote is a no-op on the server when
// it already runs that release, and the evidence carries the queue key as its
// run id, which the server de-duplicates on.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedMirror is a hosted store's local mirror: the machine-ledger directory
// the snapshot and the replay queue live in, keyed to one endpoint.
type hostedMirror struct {
	store    *ledgerfile.Store
	endpoint string
	warn     sync.Once
}

// hostedCacheNotices is where cache and replay notices go; a seam for tests,
// stderr otherwise so a --json stdout stays one document.
var hostedCacheNotices io.Writer = os.Stderr

// openHostedMirror opens the mirror for projectDir. A project whose machine
// ledger cannot be opened simply has no mirror: the online command it
// accompanies must not fail over a cache it only writes.
func openHostedMirror(projectDir, endpoint string) *hostedMirror {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return nil
	}
	return &hostedMirror{store: store, endpoint: endpoint}
}

// attachHostedMirror gives a hosted ledger its mirror.
func attachHostedMirror(l envLedger, projectDir string) envLedger {
	if hs, ok := l.Bindings.(*hostedStore); ok && hs.mirror == nil {
		hs.mirror = openHostedMirror(projectDir, hs.endpoint)
	}
	return l
}

// warnOnce reports a cache write failure once per process. The online
// operation still succeeded; what is lost is the break-glass path, and the
// operator should hear about it before the outage, not during it.
func (m *hostedMirror) warnOnce(err error) {
	m.warn.Do(func() {
		fmt.Fprintf(hostedCacheNotices, "[ledger] Warning: could not update the offline copy of %s's ledger (%v); "+
			"`forge env deploy --offline-ledger` will use an older one.\n", m.endpoint, err)
	})
}

// offlineWindowEnv sets how many of an env's latest promotions (current
// included) the offline copy keeps.
const offlineWindowEnv = "FORGE_OFFLINE_LEDGER_WINDOW"

const (
	defaultOfflineWindow = 5
	maxOfflineWindow     = 50
)

// offlineWindow is the number of promotions the offline copy keeps: the
// break-glass depth. An unparsable value is the default, never an error — this
// runs inside ordinary ledger reads, which must not fail over a cache setting.
func offlineWindow() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(offlineWindowEnv)))
	switch {
	case err != nil || n < 1:
		return defaultOfflineWindow
	case n > maxOfflineWindow:
		return maxOfflineWindow
	}
	return n
}

// remember records what the control plane just said env runs, and the
// promotions before it. Best-effort.
//
// A refresh whose current promotion is the one already held costs no extra
// call: only its timestamp moves. Otherwise the last N promotions are read and
// each release is fetched once (or reused from the previous copy), because a
// rollback needs the digests a promotion bound, not just its version label.
func (s *hostedStore) remember(ctx context.Context, env string, p release.Promotion, bound bool) {
	m := s.mirror
	if m == nil {
		return
	}
	window := offlineWindow()
	snap := ledgerfile.HostedSnapshot{Env: env, Endpoint: m.endpoint, RefreshedAt: time.Now().UTC().Truncate(time.Second), Bound: bound, Window: window}
	if bound {
		snap.Current = &p
		prev, err := m.store.HostedSnapshot(env)
		if err != nil || prev == nil || prev.Endpoint != m.endpoint {
			prev = nil
		}
		if prev != nil && prev.Bound && prev.Current != nil && prev.Current.ID == p.ID &&
			prev.Release != nil && prev.Window == window {
			snap.Release, snap.History = prev.Release, prev.History
		} else {
			s.fillWindow(ctx, env, p, window, prev, &snap)
		}
	}
	if err := m.store.PutHostedSnapshot(snap); err != nil {
		m.warnOnce(err)
	}
}

// fillWindow reads the promotions before p and the releases they bound.
func (s *hostedStore) fillWindow(ctx context.Context, env string, p release.Promotion, window int, prev *ledgerfile.HostedSnapshot, snap *ledgerfile.HostedSnapshot) {
	known := map[string]*release.Release{}
	if prev != nil {
		if prev.Release != nil {
			known[prev.Release.Version] = prev.Release
		}
		for _, h := range prev.History {
			if h.Release != nil {
				known[h.Release.Version] = h.Release
			}
		}
	}
	releaseOf := func(version string) *release.Release {
		if r, ok := known[version]; ok {
			return r
		}
		r, err := s.Get(ctx, version)
		if err != nil || r == nil {
			return nil
		}
		known[version] = r
		return r
	}
	snap.Release = releaseOf(p.Release)
	snap.History = []ledgerfile.CachedPromotion{}
	if window <= 1 {
		return
	}
	page, err := s.HistoryPage(ctx, env, historyQuery{Limit: window})
	if err != nil {
		// The current promotion is still cached. Window 0 says the history
		// was never read, so the next refresh retries it.
		snap.History, snap.Window = nil, 0
		return
	}
	for _, h := range page.Promotions {
		if h.ID == p.ID {
			continue
		}
		if len(snap.History) >= window-1 {
			break
		}
		snap.History = append(snap.History, ledgerfile.CachedPromotion{Promotion: h, Release: releaseOf(h.Release)})
	}
}

// replayOnContact replays env's queue the first time this process reads the
// control plane successfully, and reports whether anything was replayed.
// Replay failures are reported, never returned: the read succeeded.
func (s *hostedStore) replayOnContact(ctx context.Context, env string) bool {
	if s.mirror == nil {
		return false
	}
	s.mu.Lock()
	if s.replayed == nil {
		s.replayed = map[string]bool{}
	}
	done := s.replayed[env]
	s.replayed[env] = true
	s.mu.Unlock()
	if done {
		return false
	}
	res, err := s.replayQueue(ctx, env, false)
	if err != nil {
		fmt.Fprintf(hostedCacheNotices, "[ledger] Note: queued offline records for %s could not be replayed yet: %v\n", env, err)
	}
	return res.Replayed > 0
}

// replayResult is what one replay pass did.
type replayResult struct {
	Replayed int
	// Refused are queued records the control plane declined (the env moved
	// while this machine was offline). They stay queued.
	Refused []string
	Left    int
}

// replayQueue sends env's queued promotions to the control plane, oldest
// first. discard drops a refused record instead of keeping it.
func (s *hostedStore) replayQueue(ctx context.Context, env string, discard bool) (replayResult, error) {
	var res replayResult
	m := s.mirror
	if m == nil {
		return res, nil
	}
	queue, err := m.store.QueuedPromotions(env)
	if err != nil {
		return res, err
	}
	// landed: queue key -> the promotion id it created, for the records that
	// were made on top of it.
	landed := map[string]string{}
	for _, q := range queue {
		if q.Endpoint != m.endpoint {
			res.Left++
			continue
		}
		if q.AfterKey != "" {
			id, ok := landed[q.AfterKey]
			if !ok {
				// The record this one stands on has not landed (refused or
				// still queued): this one cannot either.
				res.Left++
				continue
			}
			q.AfterKey, q.ExpectedPromotionID = "", id
		}
		done, refusal, err := s.replayOne(ctx, q, landed)
		switch {
		case err != nil:
			return res, err
		case refusal != "":
			res.Refused = append(res.Refused, refusal)
			if discard {
				if derr := m.store.DropQueuedPromotion(env, q.Key); derr != nil {
					return res, derr
				}
				continue
			}
			res.Left++
		case done:
			res.Replayed++
		}
	}
	if res.Replayed > 0 {
		fmt.Fprintf(hostedCacheNotices, "[ledger] Replayed %d offline deploy record(s) of %s to %s.\n", res.Replayed, env, m.endpoint)
	}
	for _, r := range res.Refused {
		fmt.Fprintf(hostedCacheNotices, "[ledger] Not replayed: %s\n", r)
	}
	return res, nil
}

// replayOne replays a single queued promotion and drops it once the control
// plane holds it. A non-empty refusal means the server judged it and said no.
func (s *hostedStore) replayOne(ctx context.Context, q ledgerfile.QueuedPromotion, landed map[string]string) (done bool, refusal string, err error) {
	guard := appendGuard{ExpectedCurrentID: q.ExpectedPromotionID, ExpectUnbound: q.ExpectedPromotionID == ""}
	note := fmt.Sprintf("replayed from an offline deploy made %s", q.QueuedAt.Format(time.RFC3339))
	if q.Note != "" {
		note = q.Note + " (" + note + ")"
	}
	got, err := s.Append(ctx, release.Promotion{
		Env: q.Env, Release: q.Release, Kind: release.KindPromote, Targets: q.Targets, PromotedBy: q.PromotedBy, Note: note,
	}, guard)
	if err != nil {
		var refused *promoteRefusedError
		if errors.As(err, &refused) {
			return false, fmt.Sprintf("the offline deploy of %s to %s (queued %s) was refused: %s",
				q.Release, q.Env, q.QueuedAt.Format(time.RFC3339), refused.Detail), nil
		}
		return false, "", err
	}
	if len(q.Gates) > 0 {
		gates := gateStore{client: s.client, endpoint: s.endpoint}
		for _, g := range q.Gates {
			// The queue key is the idempotency key: the server de-duplicates
			// evidence on (promotion, name, run id), so a replay that died
			// after this call converges instead of recording twice.
			g.RunID = q.Key
			if _, _, err := gates.recordGate(ctx, got.ID, g); err != nil {
				return false, "", err
			}
		}
	}
	landed[q.Key] = got.ID
	if err := s.mirror.store.RebaseQueued(q.Env, q.Key, got.ID); err != nil {
		return false, "", err
	}
	return true, "", s.mirror.store.DropQueuedPromotion(q.Env, q.Key)
}
