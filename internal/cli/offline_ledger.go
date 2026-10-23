package cli

// `forge env deploy <env> --offline-ledger` — the break-glass for a hosted
// ledger whose control plane is down.
//
// THE GAP THIS CLOSES. An env that declares forge.ControlPlane keeps its
// release ledger there. With the control plane unreachable, the deploy cannot
// read what the env runs; FORGE_LEDGER=machine is no way out (it hits the
// unimported-ledger refusal and holds none of the live history), and the only
// remaining move was a fresh cut that rebuilds every image, unrehearsed.
//
// WHAT IT DOES. It deploys a release from the copy of the ledger this machine
// holds (hosted_cache.go keeps it fresh on every successful ledger read or
// write): the env's current release, or — the realistic outage is a bad
// release that takes the control plane down — one of the last N promotions'
// releases (`--to <release>`, `--to previous`), by the digests the control
// plane recorded for it, with no build and no contact with the control plane.
// The promotion record is queued locally and replayed on the next successful
// contact.
//
// WHAT IT DELIBERATELY DOES NOT DO.
//   - It never deploys a release that is not in the copy: a version this
//     machine never saw cannot be pinned to digests, and cutting one needs the
//     ledger.
//   - It refuses an env whose control plane converges part of it (a hosted
//     tier, or a hub-converged cluster): there the control plane IS the
//     applier, and nothing local can stand in for it.
//   - It never runs unless asked. Without the flag the unimported-ledger
//     refusal and every hosted read behave exactly as before, so a stale cache
//     can never change what an ordinary deploy ships.
//
// BECAUSE THE CACHE MAY BE STALE, the deploy prints its age and what it will
// replay, and the replay is a compare-and-set against the promotion the cache
// said was current: if the control plane moved on while this machine was
// offline, the replay is refused (and kept queued) instead of overwriting it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

type offlineLedgerCtxKey struct{}

// withOfflineLedger marks ctx as running under --offline-ledger. A context
// value because the ledger is selected deep inside the deploy (a dozen call
// sites take ctx and none takes a flag), and a flag threaded to each would be
// forgotten by exactly the one that then reaches the dead control plane.
func withOfflineLedger(ctx context.Context) context.Context {
	return context.WithValue(ctx, offlineLedgerCtxKey{}, true)
}

func offlineLedgerRequested(ctx context.Context) bool {
	v, _ := ctx.Value(offlineLedgerCtxKey{}).(bool)
	return v
}

// staleOfflineCache is the age past which the banner stops being a note and
// becomes a warning to check what the env really runs.
const staleOfflineCache = 7 * 24 * time.Hour

// offlineBindings is the bindingStore under --offline-ledger: the cached
// promotions, and a queue instead of a write.
type offlineBindings struct {
	store    *ledgerfile.Store
	snap     ledgerfile.HostedSnapshot
	endpoint string
	now      func() time.Time

	// releases are the cached releases that carry digests, by version.
	releases map[string]release.Release
	// timeline is the cached releases newest first, consecutive repeats
	// collapsed: what "previous" walks.
	timeline []string

	mu sync.Mutex
	// current is the effective current promotion: the cached one, or — when an
	// earlier offline deploy of this outage has not replayed yet — the one it
	// queued. afterKey is that queued record's key ("" for the cached one).
	current  release.Promotion
	afterKey string
	lastKey  string // queue key of the record this process queued
}

// openOfflineLedger builds the offline ledger for env from its cache. The
// caller has already checked that the env's shape is one an offline deploy can
// serve.
func openOfflineLedger(projectDir, env, endpoint string, base envLedger) (envLedger, error) {
	store, err := openMachineLedger(projectDir)
	if err != nil {
		return envLedger{}, err
	}
	snap, err := store.HostedSnapshot(env)
	if err != nil {
		return envLedger{}, fmt.Errorf("read the offline copy of %s's ledger: %w", env, err)
	}
	if snap == nil {
		return envLedger{}, fmt.Errorf("--offline-ledger: this machine has no offline copy of %s's ledger from %s.\n"+
			"  forge writes it whenever it reads or writes that ledger while it is reachable, so it exists only on a machine\n"+
			"  that has run a ledger command for %s before the outage. Once %s answers again, run\n"+
			"  `forge env status %s` (and rehearse this flag) so the next outage has one",
			env, endpoint, env, endpoint, env)
	}
	if snap.Endpoint != endpoint {
		return envLedger{}, fmt.Errorf("--offline-ledger: the offline copy of %s's ledger came from %s, but %s now declares %s; it is not used for a different control plane",
			env, snap.Endpoint, env, endpoint)
	}
	b := &offlineBindings{store: store, snap: *snap, endpoint: endpoint, now: time.Now, releases: map[string]release.Release{}}
	if snap.Bound && snap.Current != nil {
		b.current = *snap.Current
		b.addCached(snap.Current.Release, snap.Release)
		for _, h := range snap.History {
			b.addCached(h.Promotion.Release, h.Release)
		}
	}
	// A second offline deploy in one outage stands on the first one's queued
	// record, not on a promotion the control plane has not seen yet.
	if queued, qerr := store.QueuedPromotions(env); qerr == nil {
		for i := len(queued) - 1; i >= 0; i-- {
			q := queued[i]
			if q.Endpoint != endpoint {
				continue
			}
			rel, ok := b.releases[q.Release]
			if !ok {
				break
			}
			b.current = release.Promotion{
				ID: queuedPromotionPrefix + q.Key, Env: env, Release: q.Release, Kind: release.KindPromote,
				Resolved: rel.SharedDigests(), Sources: rel.Sources(), Targets: q.Targets, PromotedBy: q.PromotedBy, Note: q.Note, PromotedAt: q.QueuedAt,
			}
			b.afterKey = q.Key
			break
		}
	}
	base.Bindings = b
	base.Releases = offlineReleases{byVersion: b.releases, timeline: b.timeline, endpoint: endpoint}
	base.Hosted = false
	base.Offline = b
	return base, nil
}

// queuedPromotionPrefix marks the id of a promotion that exists only in this
// machine's queue.
const queuedPromotionPrefix = "queued:"

// addCached records a release as deployable offline, when its digests are
// known, and appends its version to the timeline.
func (b *offlineBindings) addCached(version string, rel *release.Release) {
	if n := len(b.timeline); n == 0 || b.timeline[n-1] != version {
		b.timeline = append(b.timeline, version)
	}
	if rel == nil {
		return
	}
	if _, ok := b.releases[version]; !ok {
		b.releases[version] = *rel
	}
}

func (b *offlineBindings) Location() string {
	return fmt.Sprintf("offline copy of %s (refreshed %s)", b.endpoint, b.snap.RefreshedAt.Format(time.RFC3339))
}

// Current is the effective current promotion, or "never promoted" when the
// control plane said so.
func (b *offlineBindings) Current(_ context.Context, env string) (release.Promotion, bool, error) {
	if env != b.snap.Env || !b.snap.Bound {
		return release.Promotion{}, false, nil
	}
	return b.current, true, nil
}

// cachedList names what the copy holds, for refusals: "v3 (current), v2, v1".
func (b *offlineBindings) cachedList() string {
	if len(b.timeline) == 0 {
		return "nothing"
	}
	var parts []string
	for _, v := range b.timeline {
		label := v
		if _, ok := b.releases[v]; !ok {
			label += " (digests not cached)"
		}
		if v == b.current.Release {
			label += " (current)"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

// resolveTarget turns --to into a cached release version. to is "previous", a
// release version, or a cached promotion id.
func (b *offlineBindings) resolveTarget(env, to string) (string, error) {
	version := to
	if to == "previous" {
		idx := -1
		for i, v := range b.timeline {
			if v == b.current.Release {
				idx = i
				break
			}
		}
		version = ""
		for i := idx + 1; idx >= 0 && i < len(b.timeline); i++ {
			if b.timeline[i] != b.current.Release {
				version = b.timeline[i]
				break
			}
		}
		if version == "" {
			return "", fmt.Errorf("--offline-ledger --to previous: the offline copy of %s's ledger holds no release before %s (cached: %s)",
				env, b.current.Release, b.cachedList())
		}
	} else if b.snap.Current != nil && b.snap.Current.ID == to {
		version = b.snap.Current.Release
	} else {
		for _, h := range b.snap.History {
			if h.Promotion.ID == to {
				version = h.Promotion.Release
			}
		}
	}
	rel := b.releases
	if _, ok := rel[version]; ok {
		return version, nil
	}
	for _, v := range b.timeline {
		if v == version {
			return "", fmt.Errorf("--offline-ledger: release %s is in %s's offline copy but its digests were not cached (it could not be read when the copy was refreshed), so it cannot be deployed by digest.\n"+
				"  deployable offline: %s", version, env, b.cachedList())
		}
	}
	return "", fmt.Errorf("--offline-ledger: release %q is not in this machine's offline copy of %s's ledger, so there are no recorded digests to deploy it by (refreshed %s).\n"+
		"  deployable offline: %s\n"+
		"  The copy holds the last %d promotions; anything older, or never deployed to %s, needs the control plane (or a rebuild)",
		to, env, b.snap.RefreshedAt.Format(time.RFC3339), b.cachedList(), max(b.snap.Window, defaultOfflineWindow), env)
}

// Append queues the promotion for replay and returns what the env now runs.
//
// Only a release in the copy can be named; an unknown one cannot be pinned to
// digests. A redeploy of the current release changes no binding (release.
// Decide's no-op) and returns the existing promotion; a rollback queues a new
// promotion, which exists only here until it replays.
func (b *offlineBindings) Append(_ context.Context, p release.Promotion, guard appendGuard) (release.Promotion, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var history []release.Promotion
	if b.snap.Bound {
		history = []release.Promotion{b.current}
	}
	existing, err := admitPromotion(history, p, guard)
	if err != nil {
		return release.Promotion{}, err
	}
	rel, cached := b.releases[p.Release]
	if existing == nil && !cached {
		return release.Promotion{}, fmt.Errorf("--offline-ledger: %s is not in the offline copy of %s's ledger; deployable offline: %s",
			p.Release, p.Env, b.cachedList())
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	q := ledgerfile.QueuedPromotion{
		Key: hex.EncodeToString(raw[:]), Env: p.Env, Endpoint: b.endpoint, Release: p.Release,
		ExpectedPromotionID: b.current.ID, PromotedBy: p.PromotedBy, Note: p.Note, Targets: p.Targets,
		QueuedAt: b.now().UTC().Truncate(time.Second), CacheRefreshedAt: b.snap.RefreshedAt,
	}
	if b.afterKey != "" {
		q.AfterKey, q.ExpectedPromotionID = b.afterKey, ""
	}
	if err := b.store.QueuePromotion(q); err != nil {
		return release.Promotion{}, fmt.Errorf("queue the offline promotion of %s: %w", p.Env, err)
	}
	b.lastKey = q.Key
	if existing != nil {
		return *existing, nil
	}
	moved := release.Promotion{
		ID: queuedPromotionPrefix + q.Key, Env: p.Env, Release: p.Release, Kind: release.KindPromote,
		Resolved: rel.SharedDigests(), Sources: rel.Sources(), Targets: p.Targets, PromotedBy: p.PromotedBy, Note: p.Note, PromotedAt: q.QueuedAt,
	}
	b.current, b.afterKey = moved, q.Key
	return moved, nil
}

// attachGate files the apply's verdict on the queued record, to replay with it.
func (b *offlineBindings) attachGate(env string, g release.Gate) error {
	b.mu.Lock()
	key := b.lastKey
	b.mu.Unlock()
	if key == "" {
		return errors.New("nothing was queued by this deploy")
	}
	return b.store.AttachQueuedGate(env, key, g)
}

// offlineReleases serves the releases the copy holds digests for.
type offlineReleases struct {
	byVersion map[string]release.Release
	timeline  []string
	endpoint  string
}

func (r offlineReleases) Location() string {
	return fmt.Sprintf("offline copy of %s", r.endpoint)
}

func (r offlineReleases) Cut(context.Context, release.Release) (bool, error) {
	return false, errors.New("--offline-ledger cannot cut a release: the ledger is not being consulted")
}

func (r offlineReleases) Get(_ context.Context, version string) (*release.Release, error) {
	rel, ok := r.byVersion[version]
	if !ok {
		return nil, nil
	}
	return &rel, nil
}

// List is newest first, the ordering both real backends return.
func (r offlineReleases) List(context.Context) ([]release.Release, error) {
	var out []release.Release
	seen := map[string]bool{}
	for _, v := range r.timeline {
		if rel, ok := r.byVersion[v]; ok && !seen[v] {
			seen[v] = true
			out = append(out, rel)
		}
	}
	return out, nil
}

// offlineShapeError says why an env cannot be served from the cache.
func offlineShapeError(env, endpoint string, units unitSplit, tiers []hostedTier) error {
	switch {
	case units.AnyHub():
		return fmt.Errorf("--offline-ledger cannot deploy %s: %s's hub converges %s, so the control plane is the applier of those units and nothing on this machine can stand in for it", env, endpoint, strings.Join(units.Hub, ", "))
	case len(tiers) > 0:
		return fmt.Errorf("--offline-ledger cannot deploy %s: it binds %s to %s, which the control plane publishes and converges", env, joinTiers(tiers), endpoint)
	default:
		return fmt.Errorf("--offline-ledger has nothing to apply for %s: it declares no workload this machine applies", env)
	}
}

// offlineBanner prints the cache's age and what this deploy will replay.
// target is the release being deployed.
func offlineBanner(w io.Writer, env, target string, l envLedger) {
	b := l.Offline
	if b == nil || !b.snap.Bound {
		return
	}
	age := b.now().Sub(b.snap.RefreshedAt).Round(time.Minute)
	fmt.Fprintf(w, "[offline-ledger] %s's ledger on %s is NOT being consulted.\n", env, b.endpoint)
	fmt.Fprintf(w, "[offline-ledger]   cache refreshed %s ago (%s)\n", age, b.snap.RefreshedAt.Format(time.RFC3339))
	if target == b.current.Release {
		fmt.Fprintf(w, "[offline-ledger]   redeploying the current release %s (promotion %s) by its recorded digests; nothing is built\n",
			b.current.Release, b.current.ID)
	} else {
		fmt.Fprintf(w, "[offline-ledger]   ROLLING %s from %s to %s by its recorded digests; nothing is built\n", env, b.current.Release, target)
	}
	if age > staleOfflineCache {
		fmt.Fprintf(w, "[offline-ledger]   WARNING: the cache is older than %s. If %s was promoted since, this deploys over a release the cache has never heard of;\n"+
			"[offline-ledger]   the replay will be refused (it compare-and-sets against promotion %s), but the apply cannot be undone by it\n",
			staleOfflineCache, env, b.snap.Current.ID)
	}
	if queued, err := b.store.QueuedPromotions(env); err == nil && len(queued) > 0 {
		fmt.Fprintf(w, "[offline-ledger]   %d earlier offline record(s) are already waiting to replay\n", len(queued))
	}
	fmt.Fprintf(w, "[offline-ledger]   deployable offline: %s\n", b.cachedList())
	fmt.Fprintf(w, "[offline-ledger]   this deploy queues 1 promotion record of %s (with the apply's verdict), replayed idempotently to %s\n"+
		"[offline-ledger]   on the next successful ledger contact (any `forge env status %s` or `forge env deploy %s` once it answers);\n"+
		"[offline-ledger]   it is refused, not forced, if %s was promoted meanwhile\n",
		target, b.endpoint, env, env, env)
}

// recordOfflineApplyOutcome files the apply's verdict on the queued record.
// Best-effort, like its hosted twin: the deploy's outcome is the apply's.
func recordOfflineApplyOutcome(env string, plan promotePlan, ledger envLedger, started time.Time, applyErr error, o promoteFollowOptions) {
	gate := applyOutcomeGate(started, time.Now(), applyErr,
		appliedBundleDigest(env, plan.Recorded.Release, o.clientDeploy), o.planDigest)
	if err := ledger.Offline.attachGate(env, gate); err != nil {
		o.notice("[deploy] Note: the apply's %s verdict could not be queued for replay: %v\n", gate.Status, err)
	}
}

// offlineFlagsError refuses the combinations that would read the ledger.
func offlineFlagsError(f deployCmdFlags) error {
	var bad []string
	if f.explain {
		bad = append(bad, "--explain")
	}
	if f.dryRun {
		bad = append(bad, "--dry-run")
	}
	if f.promote.fromSource().requested() {
		bad = append(bad, "--from")
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("--offline-ledger cannot be combined with %s: it deploys the cached current release and nothing else (preview it with --plan)",
		strings.Join(bad, ", "))
}

// dispatchOfflineDeploy is `forge env deploy <env> --offline-ledger [--to X]`.
func dispatchOfflineDeploy(ctx context.Context, envName string, f deployCmdFlags) error {
	if err := offlineFlagsError(f); err != nil {
		return err
	}
	ctx = withOfflineLedger(ctx)
	ledger, err := resolveReleaseLedger(ctx, projectDirForKCL(), envName)
	if err != nil {
		return err
	}
	_, bound, err := ledger.Bindings.Current(ctx, envName)
	if err != nil {
		return err
	}
	if !bound {
		return fmt.Errorf("--offline-ledger: the cached ledger says %s was never promoted, so there is no release to redeploy", envName)
	}
	b := ledger.Offline
	// A positional version means the same as --to; naming both differently is
	// a mistake worth stopping on.
	to := f.offlineTo
	if v := f.promote.version; v != "" {
		if to != "" && to != v {
			return fmt.Errorf("--offline-ledger: %q and --to %q name different releases", v, to)
		}
		to = v
	}
	target := b.current.Release
	if to != "" {
		if target, err = b.resolveTarget(envName, to); err != nil {
			return err
		}
	} else if _, ok := b.releases[target]; !ok {
		return fmt.Errorf("--offline-ledger: the offline copy of %s's ledger names release %s but not the digests it pins "+
			"(it was written while the release could not be read). Refresh it with `forge env status %s` while the control plane is up",
			envName, target, envName)
	}
	f.promote.version = target
	offlineBanner(progressWriter(f.jsonOut), envName, target, ledger)
	return dispatchReleaseDeploy(ctx, envName, f)
}

// ─── forge env status: the break-glass options, in advance ───────────────────

// offlineCacheReport is what `forge env status` says this machine could
// deploy with the control plane down. Additive and never part of the verdict:
// a missing or unreadable copy is information, not a failed status.
type offlineCacheReport struct {
	// Warm is whether this machine holds a usable copy.
	Warm        bool   `json:"warm"`
	RefreshedAt string `json:"refreshed_at,omitempty"`
	// Window is how many promotions the copy keeps (FORGE_OFFLINE_LEDGER_WINDOW).
	Window int `json:"window,omitempty"`
	// Releases are deployable offline, newest first; Current marks the one the
	// env runs.
	Releases []offlineCachedRelease `json:"releases,omitempty"`
	Detail   string                 `json:"detail,omitempty"`
}

type offlineCachedRelease struct {
	Release     string `json:"release"`
	PromotionID string `json:"promotion_id,omitempty"`
	PromotedAt  string `json:"promoted_at,omitempty"`
	Current     bool   `json:"current,omitempty"`
	// Deployable is false for a release whose digests were not cached.
	Deployable bool `json:"deployable"`
}

// collectOfflineCache reads this machine's copy for a hosted env. Nil for an
// env whose ledger is not a control plane (there is nothing to be offline from).
func collectOfflineCache(bindings bindingStore, env string) *offlineCacheReport {
	hs, ok := bindings.(*hostedStore)
	if !ok || hs.mirror == nil {
		return nil
	}
	snap, err := hs.mirror.store.HostedSnapshot(env)
	switch {
	case err != nil:
		return &offlineCacheReport{Detail: "the offline copy could not be read: " + err.Error()}
	case snap == nil || snap.Endpoint != hs.mirror.endpoint:
		return &offlineCacheReport{Detail: "no offline copy yet — it is written by a successful ledger read; this status read should have written one"}
	}
	rep := &offlineCacheReport{Warm: true, RefreshedAt: snap.RefreshedAt.Format(time.RFC3339), Window: snap.Window}
	if !snap.Bound || snap.Current == nil {
		rep.Detail = "the env has never been promoted, so there is nothing to deploy offline"
		return rep
	}
	rep.Releases = append(rep.Releases, offlineCachedRelease{
		Release: snap.Current.Release, PromotionID: snap.Current.ID, PromotedAt: formatLedgerTime(snap.Current.PromotedAt),
		Current: true, Deployable: snap.Release != nil,
	})
	for _, h := range snap.History {
		rep.Releases = append(rep.Releases, offlineCachedRelease{
			Release: h.Promotion.Release, PromotionID: h.Promotion.ID, PromotedAt: formatLedgerTime(h.Promotion.PromotedAt),
			Deployable: h.Release != nil,
		})
	}
	if snap.Window == 0 {
		rep.Detail = "only the current release is cached (the history could not be read at the last refresh)"
	}
	return rep
}

// writeOfflineCache prints the report.
func writeOfflineCache(w io.Writer, env string, r *offlineCacheReport) {
	if r == nil {
		return
	}
	fmt.Fprintf(w, "\nOffline copy (break-glass: forge env deploy %s --offline-ledger [--to previous|<release>])\n", env)
	if !r.Warm {
		fmt.Fprintf(w, "  %s\n", r.Detail)
		return
	}
	fmt.Fprintf(w, "  refreshed %s\n", r.RefreshedAt)
	for _, c := range r.Releases {
		tag := ""
		switch {
		case c.Current:
			tag = "  (current)"
		case !c.Deployable:
			tag = "  (digests not cached: not deployable offline)"
		}
		fmt.Fprintf(w, "  %-28s promoted %s%s\n", c.Release, emptyAs(c.PromotedAt, "?"), tag)
	}
	if r.Detail != "" {
		fmt.Fprintf(w, "  %s\n", r.Detail)
	}
}
