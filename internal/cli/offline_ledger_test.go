package cli

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// switchableCP is a fake control plane that can be taken down and brought back
// on the SAME address, which is what an outage looks like to a client: the
// connection dies, and later works again.
type switchableCP struct {
	fake *fakeDeployService
	down atomic.Bool
}

func (s *switchableCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.down.Load() {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	s.fake.ServeHTTP(w, r)
}

const offlineTestDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
const offlineTestImage = "registry.test/acme/api"

// offlineFixture is a ledger-only prod (forge.ControlPlane over cluster
// workloads) whose control plane has v1 cut and promoted, selected through the
// real ledgerForEntities so the mirror is attached exactly as production does.
type offlineFixture struct {
	cp      *switchableCP
	srv     *httptest.Server
	dir     string
	ledger  envLedger
	entity  *KCLEntities
	current release.Promotion
}

func newOfflineFixture(t *testing.T) *offlineFixture {
	t.Helper()
	cp := &switchableCP{fake: newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)
	dir := newLedgerTestProject(t, "acme")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")
	prevName := hostedProjectName
	hostedProjectName = func() string { return "acme" }
	t.Cleanup(func() { hostedProjectName = prevName })

	ents := prodShapedEntities()
	ents.ControlPlane.Endpoint = srv.URL
	l, err := ledgerForEntities("prod", ents, dir)
	if err != nil {
		t.Fatalf("select hosted ledger: %v", err)
	}
	hs, ok := l.Bindings.(*hostedStore)
	if !ok || hs.mirror == nil {
		t.Fatalf("a hosted ledger must be selected WITH its mirror, got %T mirror=%v", l.Bindings, ok && hs.mirror != nil)
	}
	f := &offlineFixture{cp: cp, srv: srv, dir: dir, ledger: l, entity: ents}
	ctx := context.Background()
	rel := ociRelease("v1", map[string]string{offlineTestImage: offlineTestDigest})
	if _, err := hs.Cut(ctx, rel); err != nil {
		t.Fatalf("cut: %v", err)
	}
	f.current, err = hs.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}, appendGuard{})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	return f
}

func (f *offlineFixture) store(t *testing.T) *ledgerfile.Store {
	t.Helper()
	s, err := openMachineLedger(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *offlineFixture) offline(t *testing.T) envLedger {
	t.Helper()
	l, err := offlineLedgerForEntities("prod", f.entity, f.dir)
	if err != nil {
		t.Fatalf("offline ledger: %v", err)
	}
	return l
}

// The cache is filled by a WRITE (the promote above) and by a READ.
func TestHostedCache_PopulatedOnWriteAndRead(t *testing.T) {
	f := newOfflineFixture(t)
	store := f.store(t)

	snap, err := store.HostedSnapshot("prod")
	if err != nil || snap == nil {
		t.Fatalf("a successful promote must leave a snapshot: %v %v", snap, err)
	}
	if !snap.Bound || snap.Current == nil || snap.Current.Release != "v1" || snap.Current.ID != f.current.ID {
		t.Fatalf("snapshot after write = %+v, want bound to v1 / %s", snap, f.current.ID)
	}
	if snap.Release == nil || snap.Release.SharedDigests()[offlineTestImage] != offlineTestDigest {
		t.Fatalf("the snapshot must carry the release's digests, got %+v", snap.Release)
	}
	if snap.Endpoint != f.srv.URL {
		t.Fatalf("snapshot endpoint = %q, want %q", snap.Endpoint, f.srv.URL)
	}

	// A READ refreshes it: age the snapshot, read, and the timestamp moves.
	old := *snap
	old.RefreshedAt = time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	if err := store.PutHostedSnapshot(old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.ledger.Bindings.Current(context.Background(), "prod"); err != nil {
		t.Fatal(err)
	}
	after, _ := store.HostedSnapshot("prod")
	if !after.RefreshedAt.After(old.RefreshedAt.Add(24 * time.Hour)) {
		t.Fatalf("a successful read must refresh the cache; refreshed_at still %s", after.RefreshedAt)
	}

	// A FAILED read must not touch it.
	f.cp.down.Store(true)
	before, _ := store.HostedSnapshot("prod")
	if _, _, err := f.ledger.Bindings.Current(context.Background(), "prod"); err == nil {
		t.Fatal("precondition: the read must fail while the control plane is down")
	}
	still, _ := store.HostedSnapshot("prod")
	if !still.RefreshedAt.Equal(before.RefreshedAt) {
		t.Fatal("a failed read must leave the cache as it was")
	}
}

// "Never promoted" is cached too, as exactly that.
func TestHostedCache_RecordsNeverPromoted(t *testing.T) {
	cp := &switchableCP{fake: newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})}
	srv := httptest.NewServer(cp)
	t.Cleanup(srv.Close)
	dir := newLedgerTestProject(t, "acme")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")
	ents := prodShapedEntities()
	ents.ControlPlane.Endpoint = srv.URL
	l, err := ledgerForEntities("prod", ents, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, bound, err := l.Bindings.Current(context.Background(), "prod"); err != nil || bound {
		t.Fatalf("precondition: want never promoted, got bound=%v err=%v", bound, err)
	}
	store, _ := openMachineLedger(dir)
	snap, serr := store.HostedSnapshot("prod")
	if snap == nil || snap.Bound {
		t.Logf("err=%v dir=%s mirror=%v", serr, store.Dir(), l.Bindings.(*hostedStore).mirror != nil)
		t.Fatalf("snapshot = %+v, want a cached 'never promoted'", snap)
	}
}

// The offline ledger answers from the cache and pins the CACHED digest, with
// the control plane dead.
func TestOfflineLedger_DeploysCachedDigest(t *testing.T) {
	f := newOfflineFixture(t)
	f.srv.Close()

	ctx := withOfflineLedger(context.Background())
	l := f.offline(t)
	cur, bound, err := l.Bindings.Current(ctx, "prod")
	if err != nil || !bound || cur.Release != "v1" {
		t.Fatalf("offline Current = %+v bound=%v err=%v, want v1", cur, bound, err)
	}
	digests, boundRel, err := resolveDeployDigests(ctx, f.dir, "prod", false, l.Bindings, l.Releases)
	if err != nil {
		t.Fatalf("resolve digests offline: %v", err)
	}
	if boundRel != "v1" || digests[offlineTestImage] != offlineTestDigest {
		t.Fatalf("offline deploy pins %v (release %q), want %s@%s", digests, boundRel, offlineTestImage, offlineTestDigest)
	}
	if l.Hosted || l.Offline == nil || !l.ledgerOnly() {
		t.Fatalf("offline ledger must apply from here and never claim a hosted converger: hosted=%v offline=%v ledgerOnly=%v", l.Hosted, l.Offline != nil, l.ledgerOnly())
	}
	if _, err := l.Releases.Cut(ctx, ociRelease("v9", nil)); err == nil {
		t.Fatal("an offline ledger must not cut a release")
	}
	if rel, _ := l.Releases.Get(ctx, "v2"); rel != nil {
		t.Fatal("the offline ledger knows only the cached release")
	}
}

// With no cache there is nothing to deploy, and the error says how to get one.
func TestOfflineLedger_NoCacheIsRefused(t *testing.T) {
	dir := newLedgerTestProject(t, "acme")
	ents := prodShapedEntities()
	_, err := offlineLedgerForEntities("prod", ents, dir)
	if err == nil || !strings.Contains(err.Error(), "no offline copy") || !strings.Contains(err.Error(), "forge env status prod") {
		t.Fatalf("want a refusal naming the missing cache and the remedy, got %v", err)
	}
}

// A cache is for the control plane that wrote it.
func TestOfflineLedger_CacheIsPerEndpoint(t *testing.T) {
	f := newOfflineFixture(t)
	other := prodShapedEntities()
	other.ControlPlane.Endpoint = "https://other.example.com"
	if _, err := offlineLedgerForEntities("prod", other, f.dir); err == nil || !strings.Contains(err.Error(), "different control plane") {
		t.Fatalf("a cache must not serve a different endpoint, got %v", err)
	}
}

// An env the control plane converges is not something this machine can apply.
func TestOfflineLedger_RefusesWhatTheControlPlaneApplies(t *testing.T) {
	f := newOfflineFixture(t)
	withHosted := prodShapedEntities()
	withHosted.ControlPlane.Endpoint = f.srv.URL
	withHosted.Workloads = append(withHosted.Workloads, hostedWL("api"))
	if _, err := offlineLedgerForEntities("prod", withHosted, f.dir); err == nil || !strings.Contains(err.Error(), "converges") {
		t.Fatalf("a hosted tier must be refused, got %v", err)
	}
}

// FORGE_LEDGER=machine and --offline-ledger name two different ledgers.
func TestOfflineLedger_ContradictsMachineOverride(t *testing.T) {
	f := newOfflineFixture(t)
	t.Setenv(ledgerSelectionEnv, "machine")
	if _, err := offlineLedgerForEntities("prod", f.entity, f.dir); err == nil || !strings.Contains(err.Error(), "contradict") {
		t.Fatalf("got %v", err)
	}
}

// Offline Append queues exactly one record (the cached release) and refuses
// any move.
func TestOfflineBindings_AppendQueuesAndRefusesAMove(t *testing.T) {
	f := newOfflineFixture(t)
	f.srv.Close()
	ctx := withOfflineLedger(context.Background())
	l := f.offline(t)

	guard := appendGuard{ExpectedCurrentID: f.current.ID}
	got, err := l.Bindings.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote, Note: "break-glass"}, guard)
	if err != nil || got.ID != f.current.ID {
		t.Fatalf("redeploy of the cached release: %+v %v", got, err)
	}
	q, _ := f.store(t).QueuedPromotions("prod")
	if len(q) != 1 || q[0].Release != "v1" || q[0].ExpectedPromotionID != f.current.ID || q[0].Endpoint != f.srv.URL || q[0].Note != "break-glass" {
		t.Fatalf("queue = %+v", q)
	}

	if _, err := l.Bindings.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote}, guard); err == nil ||
		!strings.Contains(err.Error(), "not in the offline copy") {
		t.Fatalf("an uncached release must be refused, got %v", err)
	}
	if q, _ := f.store(t).QueuedPromotions("prod"); len(q) != 1 {
		t.Fatalf("a refused append must queue nothing; queue = %d", len(q))
	}
}

// The replay: on the next successful contact the queue is sent, once, and
// converges however many times it is attempted.
func TestOfflineReplay_IsIdempotent(t *testing.T) {
	f := newOfflineFixture(t)
	f.cp.down.Store(true)
	ctx := withOfflineLedger(context.Background())
	l := f.offline(t)
	if _, err := l.Bindings.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}, appendGuard{ExpectedCurrentID: f.current.ID}); err != nil {
		t.Fatal(err)
	}
	if err := l.Offline.attachGate("prod", applyOutcomeGate(time.Now().Add(-time.Minute), time.Now(), nil, "sha256:bundle", "")); err != nil {
		t.Fatal(err)
	}
	var notices bytes.Buffer
	prev := hostedCacheNotices
	hostedCacheNotices = &notices
	t.Cleanup(func() { hostedCacheNotices = prev })

	// Still down: the read fails, the queue is untouched.
	hs := f.ledger.Bindings.(*hostedStore)
	if _, _, err := hs.Current(context.Background(), "prod"); err == nil {
		t.Fatal("precondition: control plane is down")
	}
	if q, _ := f.store(t).QueuedPromotions("prod"); len(q) != 1 {
		t.Fatalf("nothing may be replayed while the control plane is away; queue = %d", len(q))
	}

	// Back up: the first successful read replays.
	f.cp.down.Store(false)
	promotionsBefore := len(f.cp.fake.promotions["env-prod-uuid"])
	cur, bound, err := hs.Current(context.Background(), "prod")
	if err != nil || !bound || cur.ID != f.current.ID {
		t.Fatalf("read after recovery: %+v %v %v", cur, bound, err)
	}
	if q, _ := f.store(t).QueuedPromotions("prod"); len(q) != 0 {
		t.Fatalf("a replayed record must leave the queue; queue = %d", len(q))
	}
	if got := len(f.cp.fake.promotions["env-prod-uuid"]); got != promotionsBefore {
		t.Fatalf("redeploying the current release is a no-op on the ledger: promotions %d -> %d", promotionsBefore, got)
	}
	gates := f.cp.fake.gates[f.current.ID]
	if len(gates) != 1 || gates[0].Name != applyGateName {
		t.Fatalf("the apply verdict must reach the ledger once, got %+v", gates)
	}
	if !strings.Contains(notices.String(), "Replayed 1 offline deploy record") {
		t.Fatalf("replay must be announced, got %q", notices.String())
	}

	// Re-run of the SAME queued record (a replay that died after the server
	// accepted it but before the queue was trimmed): converges, no duplicate.
	q := ledgerfile.QueuedPromotion{}
	for _, c := range []ledgerfile.QueuedPromotion{{
		Key: "crashed", Env: "prod", Endpoint: f.srv.URL, Release: "v1", ExpectedPromotionID: f.current.ID,
		QueuedAt: time.Now().UTC(), Gates: []release.Gate{applyOutcomeGate(time.Now().Add(-time.Minute), time.Now(), nil, "", "")},
	}} {
		q = c
	}
	for i := 0; i < 2; i++ {
		if _, _, err := hs.replayOne(context.Background(), q, map[string]string{}); err != nil {
			t.Fatalf("replay attempt %d: %v", i, err)
		}
	}
	if got := len(f.cp.fake.gates[f.current.ID]); got != 2 {
		t.Fatalf("replaying one record twice must record its evidence once (1 earlier + 1), got %d gates", got)
	}
	if got := len(f.cp.fake.promotions["env-prod-uuid"]); got != promotionsBefore {
		t.Fatalf("replay must never add promotions for a redeploy, got %d", got)
	}
}

// If the env moved while this machine was offline, the replay is REFUSED and
// stays queued; it never overwrites what landed.
func TestOfflineReplay_RefusedWhenTheEnvMoved(t *testing.T) {
	f := newOfflineFixture(t)
	ctx := context.Background()
	hs := f.ledger.Bindings.(*hostedStore)
	if _, err := hs.Cut(ctx, ociRelease("v2", map[string]string{offlineTestImage: sha("3")})); err != nil {
		t.Fatal(err)
	}

	f.cp.down.Store(true)
	l := f.offline(t)
	if _, err := l.Bindings.Append(withOfflineLedger(ctx), release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}, appendGuard{ExpectedCurrentID: f.current.ID}); err != nil {
		t.Fatal(err)
	}

	// Meanwhile someone promotes v2 on the control plane (via a second store
	// that shares no mirror, as a colleague's machine would).
	f.cp.down.Store(false)
	other := hostedLedger(hs.client, f.srv.URL, "acme", hs.kind).Bindings.(*hostedStore)
	if _, err := other.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote}, appendGuard{ExpectedCurrentID: f.current.ID}); err != nil {
		t.Fatal(err)
	}

	var notices bytes.Buffer
	prev := hostedCacheNotices
	hostedCacheNotices = &notices
	t.Cleanup(func() { hostedCacheNotices = prev })
	cur, _, err := hs.Current(ctx, "prod")
	if err != nil || cur.Release != "v2" {
		t.Fatalf("the read must still succeed and show what is really current: %+v %v", cur, err)
	}
	if q, _ := f.store(t).QueuedPromotions("prod"); len(q) != 1 {
		t.Fatalf("a refused record stays queued for a human; queue = %d", len(q))
	}
	if !strings.Contains(notices.String(), "was refused") {
		t.Fatalf("the refusal must be reported, got %q", notices.String())
	}
	if p, _ := f.cp.fake.promotions["env-prod-uuid"], 0; p[len(p)-1].ReleaseVersion != "v2" {
		t.Fatal("the replay overwrote the colleague's promotion")
	}
}

func TestOfflineBanner_PrintsAgeAndReplay(t *testing.T) {
	f := newOfflineFixture(t)
	l := f.offline(t)
	l.Offline.now = func() time.Time { return l.Offline.snap.RefreshedAt.Add(9 * 24 * time.Hour) }
	var out bytes.Buffer
	offlineBanner(&out, "prod", "v1", l)
	for _, want := range []string{"NOT being consulted", "216h0m0s ago", "v1", f.current.ID, "WARNING", "queues 1 promotion record", "replayed idempotently"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("banner missing %q:\n%s", want, out.String())
		}
	}
}

func TestOfflineFlagsError(t *testing.T) {
	if err := offlineFlagsError(deployCmdFlags{explain: true, dryRun: true}); err == nil || !strings.Contains(err.Error(), "--explain, --dry-run") {
		t.Fatalf("got %v", err)
	}
	if err := offlineFlagsError(deployCmdFlags{}); err != nil {
		t.Fatal(err)
	}
}

// ─── the CLI, end to end ─────────────────────────────────────────────────────

const offlineProdKCL = `import forge
import forge.workloads as fw

_t = forge.ClusterTarget {cluster = "k3d-prod", namespace = "acme-prod"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {
        endpoint = "%ENDPOINT%"
        token_env = "ACME_CP_TOKEN"
    }
    workloads = [_w | {runtime = forge.OnCluster {target = _t}} if not _w.runtime else _w for _w in [
        fw.Workload {
            name = "api"
            image = "registry.test/acme/api"
            ports = [fw.Port {name = "http", port = 8080}]
        }
    ]]
})
`

func writeOfflineProject(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"forge.yaml":             "name: acme\nmodule_path: github.com/example/acme\n",
		"deploy/kcl/kcl.mod":     "[package]\nname = \"acme_deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/prod/main.k": strings.ReplaceAll(offlineProdKCL, "%ENDPOINT%", endpoint),
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The break-glass, as an operator runs it: the control plane is down (its
// server is CLOSED), the checkout still holds a retired ledger the hosted one
// never imported, and `--offline-ledger` deploys the cached release by digest
// anyway. Then the control plane comes back on the same address and the next
// ledger read replays the queued record.
func TestDeployOfflineLedger_UnreachableControlPlane(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and drives the whole CLI; runs in task test")
	}
	cp := &switchableCP{fake: newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})}
	srv := httptest.NewServer(cp)
	addr := srv.Listener.Addr().String()
	dir := writeOfflineProject(t, srv.URL)
	useLedgerHome(t, t.TempDir())
	t.Chdir(dir)
	t.Setenv("ACME_CP_TOKEN", "rlat_e2e")
	t.Setenv("FORGE_HOME", t.TempDir())
	prevName := hostedProjectName
	hostedProjectName = func() string { return "acme" }
	t.Cleanup(func() { hostedProjectName = prevName })

	// Online: release cut and promoted — this is what fills the cache.
	l, err := ledgerFor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("online ledger: %v", err)
	}
	hs := l.Bindings.(*hostedStore)
	if _, err := hs.Cut(context.Background(), ociRelease("v1", map[string]string{offlineTestImage: offlineTestDigest})); err != nil {
		t.Fatal(err)
	}
	promoted, err := hs.Append(context.Background(), release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}, appendGuard{})
	if err != nil {
		t.Fatal(err)
	}

	// Add a retired in-checkout ledger the hosted one lacks: the refusal that
	// made FORGE_LEDGER=machine useless as a break-glass.
	writeRetiredLedger(t, dir, "prod", []release.Promotion{retiredPromotion("prod", "v0.9.0", "p-old")}, []string{"v0.9.0"})
	gitInit(t, dir)
	retiredLedgerPresence.Delete(dir)

	var apply capturedClientDeploy
	apply.install(t)
	prevWait := runPromoteWait
	runPromoteWait = func(context.Context, string, envWaitOptions) error { return nil }
	t.Cleanup(func() { runPromoteWait = prevWait })

	// The control plane is reachable but has never imported the checkout:
	// WITHOUT the flag the refusal stands.
	if out, err := runForge(t, "env", "deploy", "prod", "v1", "--yes"); !errors.Is(err, errLedgerNotImported) {
		t.Fatalf("without the flag the unimported-ledger refusal must stand; got err=%v\n%s", err, out)
	}
	if len(apply.calls) != 0 {
		t.Fatal("a refused deploy applied something")
	}

	// Outage: the server is gone.
	srv.Close()

	// Still refused without the flag (and the machine override is no way out).
	if _, err := runForge(t, "env", "deploy", "prod", "v1", "--yes"); err == nil {
		t.Fatal("an ordinary deploy must fail while the control plane is down")
	}

	out, err := runForge(t, "env", "deploy", "prod", "--offline-ledger", "--yes")
	if err != nil {
		t.Fatalf("--offline-ledger deploy: %v\n%s", err, out)
	}
	for _, want := range []string{"NOT being consulted", "cache refreshed", "v1", promoted.ID, "queues 1 promotion record"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if len(apply.calls) != 1 {
		t.Fatalf("the cached release must be applied exactly once, got %d", len(apply.calls))
	}
	store, _ := openMachineLedger(dir)
	q, _ := store.QueuedPromotions("prod")
	if len(q) != 1 || q[0].Release != "v1" || len(q[0].Gates) != 1 || q[0].Gates[0].Status != release.GateStatusPassed {
		t.Fatalf("the deploy must queue one record carrying the apply's verdict, got %+v", q)
	}

	// Moving to another release is not an offline decision.
	if _, err := runForge(t, "env", "deploy", "prod", "v2", "--offline-ledger", "--yes"); err == nil ||
		!strings.Contains(err.Error(), "not in this machine's offline copy") {
		t.Fatalf("an uncached release must be refused, got %v", err)
	}

	// Recovery: same address, same control plane, next contact replays.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("could not rebind %s for the recovery half: %v", addr, err)
	}
	back := httptest.NewUnstartedServer(cp)
	back.Listener.Close()
	back.Listener = ln
	back.Start()
	t.Cleanup(back.Close)
	var notices bytes.Buffer
	prev := hostedCacheNotices
	hostedCacheNotices = &notices
	t.Cleanup(func() { hostedCacheNotices = prev })

	fresh, err := selectLedger(context.Background(), dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fresh.Bindings.Current(context.Background(), "prod"); err != nil {
		t.Fatalf("read after recovery: %v", err)
	}
	if q, _ := store.QueuedPromotions("prod"); len(q) != 0 {
		t.Fatalf("recovery must replay the queue, %d left", len(q))
	}
	if g := cp.fake.gates[promoted.ID]; len(g) != 1 || g[0].Name != applyGateName {
		t.Fatalf("the apply's verdict must reach the ledger once, got %+v", g)
	}
}

// ─── rollback: the window, --to previous, replay ─────────────────────────────

// promoteChain cuts and promotes versions in order on the fixture's control
// plane, so the env has a history of len(versions)+1 promotions (v1 first).
func (f *offlineFixture) promoteChain(t *testing.T, versions ...string) {
	t.Helper()
	ctx := context.Background()
	hs := f.ledger.Bindings.(*hostedStore)
	for i, v := range versions {
		if _, err := hs.Cut(ctx, ociRelease(v, map[string]string{offlineTestImage: sha(string(rune('3' + i)))})); err != nil {
			t.Fatalf("cut %s: %v", v, err)
		}
		cur, _, err := hs.Current(ctx, "prod")
		if err != nil {
			t.Fatal(err)
		}
		if f.current, err = hs.Append(ctx, release.Promotion{Env: "prod", Release: v, Kind: release.KindPromote}, appendGuard{ExpectedCurrentID: cur.ID}); err != nil {
			t.Fatalf("promote %s: %v", v, err)
		}
	}
}

// newProcess makes hs behave as a fresh `forge` invocation: the once-per-
// process replay has not run yet.
func newProcess(hs *hostedStore) {
	hs.mu.Lock()
	hs.replayed = nil
	hs.mu.Unlock()
}

func cachedVersions(snap *ledgerfile.HostedSnapshot) []string {
	out := []string{snap.Current.Release}
	for _, h := range snap.History {
		out = append(out, h.Promotion.Release)
	}
	return out
}

// The window keeps the last N promotions, newest first, each WITH the release
// (its digests) — and trims older ones as the env moves on.
func TestHostedCache_WindowTrimsToN(t *testing.T) {
	t.Setenv(offlineWindowEnv, "3")
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2", "v3", "v4", "v5")
	snap, err := f.store(t).HostedSnapshot("prod")
	if err != nil || snap == nil {
		t.Fatalf("snapshot: %v %v", snap, err)
	}
	if got := strings.Join(cachedVersions(snap), ","); got != "v5,v4,v3" {
		t.Fatalf("a window of 3 must hold the last three promotions newest first, got %s", got)
	}
	if snap.Window != 3 {
		t.Fatalf("window = %d, want 3", snap.Window)
	}
	for _, h := range snap.History {
		if h.Release == nil || len(h.Release.SharedDigests()) == 0 {
			t.Fatalf("every cached promotion must carry its release digests, got %+v", h)
		}
	}
}

func TestOfflineWindow_DefaultAndBounds(t *testing.T) {
	for in, want := range map[string]int{"": defaultOfflineWindow, "junk": defaultOfflineWindow, "0": defaultOfflineWindow, "-2": defaultOfflineWindow, "2": 2, "9999": maxOfflineWindow} {
		t.Setenv(offlineWindowEnv, in)
		if got := offlineWindow(); got != want {
			t.Errorf("%s=%q -> %d, want %d", offlineWindowEnv, in, got, want)
		}
	}
}

// A repeated read of the same promotion refreshes the timestamp only: it does
// not re-fetch the history or the releases.
func TestHostedCache_RefreshOfUnchangedPromotionCostsNoExtraReads(t *testing.T) {
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2")
	hs := f.ledger.Bindings.(*hostedStore)
	if _, _, err := hs.Current(context.Background(), "prod"); err != nil {
		t.Fatal(err)
	}
	f.cp.fake.mu.Lock()
	f.cp.fake.calls = nil
	f.cp.fake.mu.Unlock()
	if _, _, err := hs.Current(context.Background(), "prod"); err != nil {
		t.Fatal(err)
	}
	f.cp.fake.mu.Lock()
	defer f.cp.fake.mu.Unlock()
	for _, c := range f.cp.fake.calls {
		if strings.HasSuffix(c, "/GetRelease") {
			t.Fatalf("an unchanged promotion must not refetch releases; calls = %v", f.cp.fake.calls)
		}
	}
}

// The break-glass: v3 is the bad release that took the control plane down;
// `--to previous` deploys v2 by ITS cached digests with the server dead.
func TestOfflineRollback_ToPreviousUsesTheCachedDigest(t *testing.T) {
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2", "v3")
	f.srv.Close()
	ctx := withOfflineLedger(context.Background())
	l := f.offline(t)

	if cur, _, _ := l.Bindings.Current(ctx, "prod"); cur.Release != "v3" {
		t.Fatalf("precondition: current = %s", cur.Release)
	}
	target, err := l.Offline.resolveTarget("prod", "previous")
	if err != nil || target != "v2" {
		t.Fatalf("previous = %q, %v; want v2", target, err)
	}
	if byID, err := l.Offline.resolveTarget("prod", f.current.ID); err != nil || byID != "v3" {
		t.Fatalf("a cached promotion id resolves to its release: %q %v", byID, err)
	}

	plan, err := computePromotePlan(ctx, promotePlanOptions{Env: "prod", Version: "v2", ProjectDir: f.dir, Bindings: l.Bindings, Releases: l.Releases, Git: allCommitsPresent()})
	if err != nil {
		t.Fatalf("offline plan: %v", err)
	}
	if plan.Direction != promoteDirectionBehind {
		t.Fatalf("a rollback must plan as a move BACK, got %v", plan.Direction)
	}
	if got := plan.targetResolved[offlineTestImage]; got != sha("3") {
		t.Fatalf("rollback pins %q, want v2's digest %s", got, sha("3"))
	}

	got, err := l.Bindings.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote, Note: "bad v3"},
		appendGuard{ExpectedCurrentID: f.current.ID})
	if err != nil || got.Release != "v2" || !strings.HasPrefix(got.ID, queuedPromotionPrefix) {
		t.Fatalf("rollback append = %+v %v; want a queued v2 promotion", got, err)
	}
	q, _ := f.store(t).QueuedPromotions("prod")
	if len(q) != 1 || q[0].Release != "v2" || q[0].ExpectedPromotionID != f.current.ID || q[0].AfterKey != "" {
		t.Fatalf("queue = %+v; want one v2 record expecting the cached v3 promotion", q)
	}
	// After the rollback the effective current is v2, so "previous" is now v1.
	if cur, _, _ := l.Bindings.Current(ctx, "prod"); cur.Release != "v2" {
		t.Fatalf("after the rollback the offline env runs v2, got %s", cur.Release)
	}
}

// A release the copy has never held cannot be pinned to digests: refused with
// the list of what can be deployed.
func TestOfflineRollback_UncachedReleaseIsRefused(t *testing.T) {
	t.Setenv(offlineWindowEnv, "2")
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2", "v3") // window 2 keeps v3, v2 — v1 fell out
	l := f.offline(t)

	for _, to := range []string{"v1", "v99", "no-such-promotion-id"} {
		_, err := l.Offline.resolveTarget("prod", to)
		if err == nil || !strings.Contains(err.Error(), "not in this machine's offline copy") ||
			!strings.Contains(err.Error(), "v3 (current), v2") || strings.Contains(err.Error(), "v1,") {
			t.Fatalf("--to %s: want a refusal naming what IS cached, got %v", to, err)
		}
	}
	if _, err := l.Offline.resolveTarget("prod", "v2"); err != nil {
		t.Fatalf("a cached release is accepted: %v", err)
	}
	// And Append backstops it, so a caller that skips resolveTarget still cannot.
	if _, err := l.Bindings.Append(withOfflineLedger(context.Background()), release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote},
		appendGuard{ExpectedCurrentID: f.current.ID}); err == nil {
		t.Fatal("Append of an uncached release must be refused")
	}
	if q, _ := f.store(t).QueuedPromotions("prod"); len(q) != 0 {
		t.Fatalf("a refused rollback queues nothing, got %d", len(q))
	}
}

func TestOfflineRollback_PreviousWithNothingBefore(t *testing.T) {
	f := newOfflineFixture(t) // only v1
	l := f.offline(t)
	if _, err := l.Offline.resolveTarget("prod", "previous"); err == nil || !strings.Contains(err.Error(), "no release before v1") {
		t.Fatalf("got %v", err)
	}
}

// A cached release whose digests could not be read at refresh time is listed
// but not deployable.
func TestOfflineRollback_UndigestedReleaseIsNotDeployable(t *testing.T) {
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2")
	store := f.store(t)
	snap, _ := store.HostedSnapshot("prod")
	snap.History[0].Release = nil
	if err := store.PutHostedSnapshot(*snap); err != nil {
		t.Fatal(err)
	}
	l := f.offline(t)
	if _, err := l.Offline.resolveTarget("prod", "previous"); err == nil || !strings.Contains(err.Error(), "digests were not cached") {
		t.Fatalf("got %v", err)
	}
}

// The replay of a rollback promotes the older release on the control plane as
// a NEW promotion, compare-and-set against the promotion the copy named, with
// the apply verdict riding on it — and a second replay changes nothing.
func TestOfflineReplay_Rollback(t *testing.T) {
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2", "v3")
	badID := f.current.ID
	f.cp.down.Store(true)
	l := f.offline(t)
	if _, err := l.Bindings.Append(withOfflineLedger(context.Background()), release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote, Note: "rollback"},
		appendGuard{ExpectedCurrentID: badID}); err != nil {
		t.Fatal(err)
	}
	if err := l.Offline.attachGate("prod", applyOutcomeGate(time.Now().Add(-time.Minute), time.Now(), nil, "", "")); err != nil {
		t.Fatal(err)
	}
	var notices bytes.Buffer
	prev := hostedCacheNotices
	hostedCacheNotices = &notices
	t.Cleanup(func() { hostedCacheNotices = prev })

	f.cp.down.Store(false)
	hs := f.ledger.Bindings.(*hostedStore)
	newProcess(hs)
	before := len(f.cp.fake.promotions["env-prod-uuid"])
	cur, _, err := hs.Current(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Release != "v2" || cur.ID == badID {
		t.Fatalf("after the replay the control plane runs v2 under a NEW promotion, got %s/%s", cur.Release, cur.ID)
	}
	if got := len(f.cp.fake.promotions["env-prod-uuid"]); got != before+1 {
		t.Fatalf("a rollback is one new promotion: %d -> %d", before, got)
	}
	if g := f.cp.fake.gates[cur.ID]; len(g) != 1 || g[0].Name != applyGateName {
		t.Fatalf("the apply verdict must ride on the rollback promotion, got %+v", g)
	}
	if q, _ := f.store(t).QueuedPromotions("prod"); len(q) != 0 {
		t.Fatalf("queue = %d after replay", len(q))
	}
	if !strings.Contains(notices.String(), "Replayed 1 offline deploy record") {
		t.Fatalf("got %q", notices.String())
	}
	// Idempotent: another contact is a no-op.
	newProcess(hs)
	if _, _, err := hs.Current(context.Background(), "prod"); err != nil {
		t.Fatal(err)
	}
	if got := len(f.cp.fake.promotions["env-prod-uuid"]); got != before+1 {
		t.Fatalf("a second replay added promotions: %d", got)
	}
}

// The moved-env refusal still applies to a rollback: if someone promoted v4
// meanwhile, the rollback is refused and v4 stays.
func TestOfflineReplay_RollbackRefusedWhenTheEnvMoved(t *testing.T) {
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2", "v3")
	badID := f.current.ID
	f.cp.down.Store(true)
	l := f.offline(t)
	if _, err := l.Bindings.Append(withOfflineLedger(context.Background()), release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote},
		appendGuard{ExpectedCurrentID: badID}); err != nil {
		t.Fatal(err)
	}
	f.cp.down.Store(false)
	hs := f.ledger.Bindings.(*hostedStore)
	other := hostedLedger(hs.client, f.srv.URL, "acme", hs.kind).Bindings.(*hostedStore)
	if _, err := other.Cut(context.Background(), ociRelease("v4", map[string]string{offlineTestImage: sha("9")})); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Append(context.Background(), release.Promotion{Env: "prod", Release: "v4", Kind: release.KindPromote}, appendGuard{ExpectedCurrentID: badID}); err != nil {
		t.Fatal(err)
	}
	var notices bytes.Buffer
	prev := hostedCacheNotices
	hostedCacheNotices = &notices
	t.Cleanup(func() { hostedCacheNotices = prev })
	newProcess(hs)
	cur, _, err := hs.Current(context.Background(), "prod")
	if err != nil || cur.Release != "v4" {
		t.Fatalf("v4 must stay current: %+v %v", cur, err)
	}
	if q, _ := f.store(t).QueuedPromotions("prod"); len(q) != 1 || !strings.Contains(notices.String(), "was refused") {
		t.Fatalf("a refused rollback stays queued and is reported: queue=%d notices=%q", len(q), notices.String())
	}
}

// Two rollbacks in one outage chain: the second is queued on top of the first
// and replays only after it, against the promotion the first created.
func TestOfflineReplay_ChainedRollbacks(t *testing.T) {
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2", "v3")
	f.cp.down.Store(true)
	ctx := withOfflineLedger(context.Background())
	l := f.offline(t)
	if _, err := l.Bindings.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote}, appendGuard{ExpectedCurrentID: f.current.ID}); err != nil {
		t.Fatal(err)
	}
	l2 := f.offline(t) // a second `forge` invocation in the same outage
	cur, _, _ := l2.Bindings.Current(ctx, "prod")
	if cur.Release != "v2" || !strings.HasPrefix(cur.ID, queuedPromotionPrefix) {
		t.Fatalf("a second offline deploy must see the first one's queued promotion as current, got %+v", cur)
	}
	if _, err := l2.Bindings.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote}, appendGuard{ExpectedCurrentID: cur.ID}); err != nil {
		t.Fatal(err)
	}
	q, _ := f.store(t).QueuedPromotions("prod")
	if len(q) != 2 || q[1].AfterKey != q[0].Key || q[1].ExpectedPromotionID != "" {
		t.Fatalf("second record must stand on the first: %+v", q)
	}

	f.cp.down.Store(false)
	hs := f.ledger.Bindings.(*hostedStore)
	newProcess(hs)
	if got, _, err := hs.Current(context.Background(), "prod"); err != nil || got.Release != "v1" {
		t.Fatalf("both rollbacks must replay in order, control plane runs %+v %v", got, err)
	}
	if q, _ := f.store(t).QueuedPromotions("prod"); len(q) != 0 {
		t.Fatalf("queue = %d", len(q))
	}
}

// `forge env status` shows the break-glass options in advance.
func TestOfflineCacheReport_ListsWhatIsDeployable(t *testing.T) {
	f := newOfflineFixture(t)
	f.promoteChain(t, "v2", "v3")
	rep := collectOfflineCache(f.ledger.Bindings, "prod")
	if rep == nil || !rep.Warm || len(rep.Releases) != 3 || !rep.Releases[0].Current || rep.Releases[0].Release != "v3" || rep.Releases[2].Release != "v1" {
		t.Fatalf("report = %+v", rep)
	}
	var out bytes.Buffer
	writeOfflineCache(&out, "prod", rep)
	for _, want := range []string{"--offline-ledger", "--to previous", "v3", "(current)", "v2", "v1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status block missing %q:\n%s", want, out.String())
		}
	}
	if collectOfflineCache(emptyBindings{}, "prod") != nil {
		t.Fatal("an env whose ledger is not a control plane has no offline copy to show")
	}
}

// The break-glass rollback as an operator runs it: v2 is a bad release that
// took the control plane down (its server is CLOSED); `--to previous` deploys
// v1 by its cached digest through the real command, behind the same gate.
func TestDeployOfflineLedger_RollbackToPrevious(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and drives the whole CLI; runs in task test")
	}
	cp := &switchableCP{fake: newFakeDeployService(map[string]string{"prod": "env-prod-uuid"})}
	srv := httptest.NewServer(cp)
	dir := writeOfflineProject(t, srv.URL)
	useLedgerHome(t, t.TempDir())
	t.Chdir(dir)
	t.Setenv("ACME_CP_TOKEN", "rlat_e2e")
	t.Setenv("FORGE_HOME", t.TempDir())
	prevName := hostedProjectName
	hostedProjectName = func() string { return "acme" }
	t.Cleanup(func() { hostedProjectName = prevName })

	ctx := context.Background()
	l, err := ledgerFor(ctx, dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	hs := l.Bindings.(*hostedStore)
	var good, bad release.Promotion
	for i, v := range []string{"v1", "v2"} {
		if _, err := hs.Cut(ctx, ociRelease(v, map[string]string{offlineTestImage: sha(string(rune('1' + i)))})); err != nil {
			t.Fatal(err)
		}
		cur, _, _ := hs.Current(ctx, "prod")
		p, err := hs.Append(ctx, release.Promotion{Env: "prod", Release: v, Kind: release.KindPromote}, appendGuard{ExpectedCurrentID: cur.ID})
		if err != nil {
			t.Fatal(err)
		}
		if v == "v1" {
			good = p
		} else {
			bad = p
		}
	}
	// The warm-up an operator does once: `forge env status prod` while the
	// control plane is up (the hosted rollout read itself fails here — the fake
	// serves none — which must not stop the ledger read that warms the cache).
	_, _, _ = hs.Current(ctx, "prod")

	var apply capturedClientDeploy
	apply.install(t)
	prevWait := runPromoteWait
	runPromoteWait = func(context.Context, string, envWaitOptions) error { return nil }
	t.Cleanup(func() { runPromoteWait = prevWait })

	srv.Close()

	// Without --yes the same gate holds: nothing is applied or queued.
	if out, err := runForge(t, "env", "deploy", "prod", "--offline-ledger", "--to", "previous"); err == nil {
		t.Fatalf("the confirmation gate must hold offline too, got success:\n%s", out)
	}
	if len(apply.calls) != 0 {
		t.Fatal("an unconfirmed offline rollback applied something")
	}
	if q, _ := (func() ([]ledgerfile.QueuedPromotion, error) {
		s, _ := openMachineLedger(dir)
		return s.QueuedPromotions("prod")
	})(); len(q) != 0 {
		t.Fatalf("an unconfirmed rollback queued %d record(s)", len(q))
	}

	out, err := runForge(t, "env", "deploy", "prod", "--offline-ledger", "--to", "previous", "--yes")
	if err != nil {
		t.Fatalf("offline rollback: %v\n%s", err, out)
	}
	for _, want := range []string{"ROLLING prod from v2 to v1", "deployable offline: v2 (current), v1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if len(apply.calls) != 1 {
		t.Fatalf("the rollback must be applied exactly once, got %d", len(apply.calls))
	}
	s, _ := openMachineLedger(dir)
	q, _ := s.QueuedPromotions("prod")
	if len(q) != 1 || q[0].Release != "v1" || q[0].ExpectedPromotionID != bad.ID || len(q[0].Gates) != 1 {
		t.Fatalf("queue = %+v; want a v1 record expecting the bad promotion %s, carrying the verdict", q, bad.ID)
	}

	if _, err := runForge(t, "env", "deploy", "prod", "--offline-ledger", "--to", "v0.1", "--yes"); err == nil ||
		!strings.Contains(err.Error(), "not in this machine's offline copy") {
		t.Fatalf("an uncached release must be refused, got %v", err)
	}
	if _, err := runForge(t, "env", "deploy", "prod", "--to", "previous", "--yes"); err == nil || !strings.Contains(err.Error(), "belongs to --offline-ledger") {
		t.Fatalf("--to alone must be refused, got %v", err)
	}
	_ = good
}
