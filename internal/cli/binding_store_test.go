package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

// ─── Shared fakes ────────────────────────────────────────────────────────────

// mustTime parses an RFC3339 literal for a fixture.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad fixture time %q: %v", s, err)
	}
	return ts
}

// ociRelease is a release of shared OCI images, for fixtures.
func ociRelease(version string, images map[string]string) release.Release {
	arts := map[string]release.Artifact{}
	for name, d := range images {
		arts[name] = release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: d}}
	}
	return release.Release{Version: version, Artifacts: arts}
}

// memBindingStore is a complete bindingStore that touches no filesystem, and
// applies the same release.Decide rules both real backends apply — so a test
// running a consumer against it exercises real append semantics, not a map.
//
// It is also the proof the seam is real: if a method wanted a projectDir or a
// path, a map could not satisfy it, and neither could the hosted backend.
type memBindingStore struct {
	history map[string][]release.Promotion
	err     error
	n       int
	// guards records every guard Append was called with, so a test can
	// assert what a promote SENT.
	guards []appendGuard
}

// newMemBindingStore seeds each env with ONE current entry. Env and Kind are
// filled in when a fixture leaves them blank.
func newMemBindingStore(current map[string]release.Promotion) *memBindingStore {
	m := &memBindingStore{history: map[string][]release.Promotion{}}
	for env, p := range current {
		p.Env = env
		if p.Kind == "" {
			p.Kind = release.KindPromote
		}
		m.history[env] = []release.Promotion{p}
	}
	return m
}

func (m *memBindingStore) Current(_ context.Context, env string) (release.Promotion, bool, error) {
	if m.err != nil {
		return release.Promotion{}, false, m.err
	}
	h := m.history[env]
	if len(h) == 0 {
		return release.Promotion{}, false, nil
	}
	return h[len(h)-1], true, nil
}

func (m *memBindingStore) Append(_ context.Context, p release.Promotion, guard appendGuard) (release.Promotion, error) {
	m.guards = append(m.guards, guard)
	if m.err != nil {
		return release.Promotion{}, m.err
	}
	existing, err := admitPromotion(m.history[p.Env], p, guard)
	if err != nil {
		return release.Promotion{}, err
	}
	if existing != nil {
		return *existing, nil
	}
	m.n++
	p.ID = fmt.Sprintf("mem-%d", m.n)
	p.PromotedAt = time.Date(2026, 9, 23, 12, 0, m.n, 0, time.UTC)
	m.history[p.Env] = append(m.history[p.Env], p)
	return p, nil
}

func (m *memBindingStore) Location() string { return "memory://promotions" }

// memReleaseLedger is the release half.
type memReleaseLedger struct {
	releases map[string]release.Release
}

func newMemReleaseLedger(rels ...release.Release) *memReleaseLedger {
	m := &memReleaseLedger{releases: map[string]release.Release{}}
	for _, r := range rels {
		m.releases[r.Version] = r
	}
	return m
}

func (m *memReleaseLedger) Cut(_ context.Context, r release.Release) (bool, error) {
	if old, ok := m.releases[r.Version]; ok {
		return false, release.CheckRecut(old, r)
	}
	m.releases[r.Version] = r
	return true, nil
}

func (m *memReleaseLedger) Get(_ context.Context, v string) (*release.Release, error) {
	r, ok := m.releases[v]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (m *memReleaseLedger) List(context.Context) ([]release.Release, error) {
	out := make([]release.Release, 0, len(m.releases))
	for _, r := range m.releases {
		out = append(out, r)
	}
	sortReleasesNewestFirst(out)
	return out, nil
}

func (m *memReleaseLedger) Location() string { return "memory://releases" }

// ─── The file backend ────────────────────────────────────────────────────────

// A project that has never been promoted has no log, and asking about an env
// there is a normal "not bound", not an error.
func TestFileBindingStore_MissingIsUnbound(t *testing.T) {
	store := testBindings(t, newLedgerTestProject(t, "scratch"))
	p, bound, err := store.Current(context.Background(), "prod")
	if err != nil {
		t.Fatalf("missing ledger must not error: %v", err)
	}
	if bound {
		t.Errorf("prod must be unbound in an empty project, got %+v", p)
	}
}

// Every promote APPENDS one line; the current binding is the last line and
// earlier lines are never rewritten. A backend that rewrote the file (the
// retired env-releases.json behaviour) fails the byte-prefix assertion.
func TestFileBindingStore_AppendsOneLinePerPromotion(t *testing.T) {
	dir := t.TempDir()
	store := testBindings(t, dir)
	ctx := context.Background()

	first, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote,
		Resolved: map[string]string{"api": sha("a")}}, appendGuard{})
	if err != nil {
		t.Fatalf("append v1: %v", err)
	}
	if first.ID == "" || first.PromotedAt.IsZero() {
		t.Errorf("the backend must stamp ID and PromotedAt, got %+v", first)
	}
	path := testPromotionLogPath(t, dir, "prod")
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the log must live at .forge/promotions/prod.jsonl: %v", err)
	}

	if _, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v2", Kind: release.KindPromote,
		Resolved: map[string]string{"api": sha("b")}}, appendGuard{}); err != nil {
		t.Fatalf("append v2: %v", err)
	}
	afterSecond, _ := os.ReadFile(path)
	if !bytes.HasPrefix(afterSecond, afterFirst) {
		t.Fatalf("the first entry was rewritten, not appended to:\nbefore:\n%s\nafter:\n%s", afterFirst, afterSecond)
	}
	if n := bytes.Count(afterSecond, []byte("\n")); n != 2 {
		t.Fatalf("want exactly 2 lines, got %d:\n%s", n, afterSecond)
	}

	cur, bound, err := store.Current(ctx, "prod")
	if err != nil || !bound || cur.Release != "v2" || cur.Resolved["api"] != sha("b") {
		t.Fatalf("current must be the LAST line (v2), got bound=%v %+v err=%v", bound, cur, err)
	}
	if _, bound, _ := store.Current(ctx, "staging"); bound {
		t.Error("a different env must stay unbound — one log per env")
	}
}

// A retry of the env's current state appends NOTHING and returns the existing
// entry — the CI-retry rule. v1→v2→v1 is still three real moves.
func TestFileBindingStore_RetryIsNoOp(t *testing.T) {
	dir := t.TempDir()
	store := testBindings(t, dir)
	ctx := context.Background()
	p := func(v string) release.Promotion {
		return release.Promotion{Env: "prod", Release: v, Kind: release.KindPromote, Resolved: map[string]string{"api": sha("a")}}
	}

	first, _ := store.Append(ctx, p("v1"), appendGuard{})
	again, err := store.Append(ctx, p("v1"), appendGuard{})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("a retry must return the EXISTING entry %s, got %s", first.ID, again.ID)
	}
	_, _ = store.Append(ctx, p("v2"), appendGuard{})
	_, _ = store.Append(ctx, p("v1"), appendGuard{})
	history, _ := storeHistory(t, store, "prod")
	if len(history) != 3 {
		t.Fatalf("v1, v1(retry), v2, v1 must record 3 entries, got %d", len(history))
	}
}

// A LEDGER WRITTEN BEFORE ROLLBACK WAS REMOVED still parses. Its
// `"kind":"rollback"` lines bound the env to their release exactly as a
// promote does, so they read as promotes of that release — the env's
// current binding is unchanged by the upgrade, and a new promote appends
// after them.
func TestFileBindingStore_LegacyRollbackLinesStillParse(t *testing.T) {
	dir := newLedgerTestProject(t, "rollback-project")
	path := testPromotionLogPath(t, dir, "prod")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"id":"a","env":"prod","release":"v1","kind":"promote","resolved":{"api":"` + sha("1") + `"},"promoted_at":"2026-09-01T00:00:00Z"}
{"id":"b","env":"prod","release":"v2","kind":"promote","resolved":{"api":"` + sha("2") + `"},"promoted_at":"2026-09-02T00:00:00Z"}
{"id":"c","env":"prod","release":"v1","kind":"rollback","resolved":{"api":"` + sha("1") + `"},"note":"5xx spike","promoted_at":"2026-09-03T00:00:00Z"}
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := testBindings(t, dir)
	ctx := context.Background()
	cur, bound, err := store.Current(ctx, "prod")
	if err != nil || !bound {
		t.Fatalf("a ledger with a legacy rollback line must still read: bound=%v err=%v", bound, err)
	}
	if cur.Release != "v1" || cur.Kind != release.KindPromote || cur.Note != "5xx spike" || cur.Resolved["api"] != sha("1") {
		t.Fatalf("legacy rollback line must read as the promote of v1, got %+v", cur)
	}
	// A retry of the state the env is in appends nothing, legacy line or not.
	if _, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote,
		Resolved: map[string]string{"api": sha("1")}}, appendGuard{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, release.Promotion{Env: "prod", Release: "v3", Kind: release.KindPromote,
		Resolved: map[string]string{"api": sha("3")}}, appendGuard{}); err != nil {
		t.Fatal(err)
	}
	history, _ := storeHistory(t, store, "prod")
	if len(history) != 4 || history[3].Release != "v3" {
		t.Fatalf("want v1, v2, v1(legacy), v3 — got %d entries: %+v", len(history), history)
	}
	raw, _ := os.ReadFile(path)
	if strings.Count(string(raw), `"kind":"rollback"`) != 1 {
		t.Errorf("the legacy line must be preserved verbatim and nothing new may write kind rollback:\n%s", raw)
	}
}

// A line that does not validate poisons the log: its LAST line cannot be
// trusted as current if an earlier one is unreadable.
func TestFileBindingStore_CorruptLineIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := testPromotionLogPath(t, dir, "prod")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(`{"env":"prod","release":"v1","kind":"sideways","resolved":{}}`+"\n"), 0o644)
	if _, _, err := testBindings(t, dir).Current(context.Background(), "prod"); err == nil {
		t.Fatal("an unknown promotion kind must be refused, not read as a promote")
	}
}

// The retired formats' tests are GONE with the code they tested.
//
// TestFileBindingStore_LegacyFileIsRefused covered .forge/env-releases.json,
// and TestConvertLegacyLedger covered `forge release convert-ledger`, which
// rewrote it into .forge/promotions/<env>.jsonl. Both of those locations are
// now retired: the ledger is the control plane or the machine store, and the
// path from a committed in-checkout ledger to either one is
// `forge ledger import --from-git`, which imports history rather than
// rewriting files in the tree.
//
// The safety property those tests protected — a checkout whose committed
// ledger is not in the selected store must FAIL LOUD rather than read as
// "never promoted" and deploy mutable tags — did not go away with them. It
// moved, and it is now pinned by TestRefusesWhenCheckoutHoldsAnUnimportedLedger
// and its neighbours in ledger_unimported_test.go, which assert it for BOTH
// backends rather than only the file one.

// Location names the MACHINE ledger, which is outside the checkout. That it
// is outside is the property worth pinning: a Location under the project
// directory would mean the ledger had slid back into the tree that produces
// the artifact it records.
func TestMachineBindingStore_LocationIsOutsideTheCheckout(t *testing.T) {
	dir := newLedgerTestProject(t, "location-project")
	got := testBindings(t, dir).Location()
	if got == "" {
		t.Fatal("Location() must name where promotions are recorded")
	}
	if strings.HasPrefix(got, dir) {
		t.Errorf("Location() = %q, which is inside the checkout %q", got, dir)
	}
}

// A release version can be re-cut only with the same bytes.
func TestWriteRelease_Immutable(t *testing.T) {
	dir := t.TempDir()
	r := ociRelease("v1", map[string]string{"api": sha("a")})
	if err := testCutRelease(t, dir, r); err != nil {
		t.Fatalf("cut: %v", err)
	}
	if err := testCutRelease(t, dir, r); err != nil {
		t.Fatalf("an identical re-cut is a retry, not an error: %v", err)
	}
	if err := testCutRelease(t, dir, ociRelease("v1", map[string]string{"api": sha("b")})); !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("a different artifact set under v1 must be ErrReleaseConflict, got %v", err)
	}
	got, _ := testGetRelease(t, dir, "v1")
	if got.Artifacts["api"].Digests[release.SharedVariant] != sha("a") {
		t.Error("a refused re-cut must not have overwritten the release")
	}
}

// ─── Selection ───────────────────────────────────────────────────────────────

// THE SELECTION RULE: the DECLARATION decides the store, and the env's KIND
// does not enter into it.
//
// A LOCAL env that declares a control plane now records THERE. It previously
// fell back to the checkout, on the reasoning that the platform runs nothing
// of a LOCAL env so there is no hosted release to bind — reasoning about
// PLACEMENT, applied to RECORDING. It split one project's history across two
// stores by kind, which made "where is this env's history" a question with
// two answers.
//
// Mutation: restore the old `|| isLocalControlPlaneEnv(entities)` clause and
// the LOCAL case below fails.
func TestLedgerForEntities_DeclarationSelectsTheStoreWhateverTheKind(t *testing.T) {
	dir := newLedgerTestProject(t, "selection-project")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")

	cp := func() *ControlPlaneEntity {
		return &ControlPlaneEntity{
			Type: "control_plane", Endpoint: "https://cp.example.com/", TokenEnv: "FORGE_TEST_CP_TOKEN",
		}
	}

	// A hosted tier (the database) makes the env PERSISTENT.
	hosted, err := ledgerForEntities("prod", &KCLEntities{
		ControlPlane: cp(),
		Databases:    []DatabaseEntity{{Name: "orders", Runtime: RuntimeHosted}},
	}, dir)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !hosted.Hosted {
		t.Fatal("an env declaring forge.ControlPlane must use the hosted ledger")
	}
	if _, isHosted := hosted.Bindings.(*hostedStore); !isHosted {
		t.Errorf("bindings = %T, want *hostedStore", hosted.Bindings)
	}
	if got := hosted.Bindings.Location(); got != "https://cp.example.com" {
		t.Errorf("a hosted ledger's label must be the endpoint URL, got %q", got)
	}

	// A control plane with NO hosted tier is a LOCAL env — and it keeps its
	// ledger on that control plane all the same. This is the changed case.
	local, err := ledgerForEntities("dev", &KCLEntities{ControlPlane: cp()}, dir)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !local.Hosted {
		t.Fatal("a LOCAL env that declares a control plane keeps its ledger THERE: " +
			"the declaration selects the store, the kind does not")
	}
	if _, isHosted := local.Bindings.(*hostedStore); !isHosted {
		t.Errorf("a LOCAL control-plane env's bindings = %T, want *hostedStore", local.Bindings)
	}

	// No declaration at all: this machine's ledger.
	machine, err := ledgerForEntities("dev", &KCLEntities{}, dir)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if machine.Hosted {
		t.Fatal("an env declaring no control plane must use the machine ledger")
	}
	if _, isMachine := machine.Bindings.(machineBindingStore); !isMachine {
		t.Errorf("bindings = %T, want machineBindingStore", machine.Bindings)
	}
	// And that ledger is NOT in the checkout — the whole point of the move.
	if loc := machine.Bindings.Location(); strings.HasPrefix(loc, dir) {
		t.Errorf("the machine ledger must live outside the checkout, got %q inside %q", loc, dir)
	}
}

// bindingStoreFor reads the env's rendered KCL: the same declaration selects
// hosted end to end.
func TestBindingStoreFor_ReadsTheEnvDeclaration(t *testing.T) {
	dir := t.TempDir()
	declareEnvDir(t, dir, "prod")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t,
		`{"output":{"control_plane":{"type":"control_plane","endpoint":"https://cp.example.com","token_env":"FORGE_TEST_CP_TOKEN"},"databases":[{"name":"orders","runtime":"hosted"}]}}`))

	store, err := bindingStoreFor(context.Background(), dir, "prod")
	if err != nil {
		t.Fatalf("bindingStoreFor: %v", err)
	}
	if _, ok := store.(*hostedStore); !ok {
		t.Fatalf("bindingStoreFor = %T, want *hostedStore for an env declaring forge.ControlPlane", store)
	}
}

// A render failure for a DECLARED env is an error, never a silent fallback to
// files — for a hosted env that fallback reads "never promoted".
func TestBindingStoreFor_RenderFailureIsNotAFallback(t *testing.T) {
	dir := t.TempDir()
	declareEnvDir(t, dir, "prod")
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", filepath.Join(dir, "does-not-exist.json"))
	if _, err := bindingStoreFor(context.Background(), dir, "prod"); err == nil {
		t.Fatal("an unrenderable env must not silently get the file ledger")
	}
}

// ─── Consumers against a non-file backend ────────────────────────────────────

// `forge env status` runs end to end with its ledger served from memory, and
// reaches the real DRIFT verdict.
func TestRunEnvVerify_AgainstNonFileBackend(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	store := newMemBindingStore(map[string]release.Promotion{
		"prod": {Release: "v1.5.13", Resolved: map[string]string{"control-plane": sha("a")},
			PromotedAt: mustTime(t, "2026-09-11T12:00:00Z")},
	})
	lister := &stubLister{images: []cluster.WorkloadImage{{
		Kind: "Deployment", Name: "control-plane", Container: "app",
		Image: "ghcr.io/acme/control-plane@" + sha("b"),
	}}}

	err := runEnvStatusRelease(context.Background(), "prod", envStatusOptions{
		Lister:   lister,
		Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
		Bindings: store,
	})
	if got := exitCodeOf(t, err); got != 1 {
		t.Fatalf("drift against a memory-backed ledger must exit 1, got %d (err: %v)", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".forge", "promotions")); !os.IsNotExist(err) {
		t.Errorf("no promotion log should exist; the binding came from memory (stat err: %v)", err)
	}
}

func TestRunEnvVerify_UnboundAgainstNonFileBackend(t *testing.T) {
	t.Chdir(t.TempDir())
	err := runEnvStatusRelease(context.Background(), "prod", envStatusOptions{
		Lister:   &stubLister{},
		Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
		Bindings: newMemBindingStore(nil),
	})
	if err != nil {
		t.Fatalf("an unbound env is not a failure, got: %v", err)
	}
}

// The deploy digest resolver: a bound promotion overrides the build state.
func TestResolveDeployDigests_AgainstNonFileBackend(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBuildState(dir, "prod", BuildState{
		Image: "control-plane", Tag: "old", Pushed: true, PushedAt: nowRFC3339(), Digest: sha("0"),
	}); err != nil {
		t.Fatalf("write build state: %v", err)
	}
	store := newMemBindingStore(map[string]release.Promotion{
		"prod": {Release: "v1.4.0", Resolved: map[string]string{"control-plane": sha("a")}},
	})
	digests, boundRel, err := resolveDeployDigests(context.Background(), dir, "prod", false, store, testReleases(t, dir))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if boundRel != "v1.4.0" || digests["control-plane"] != sha("a") {
		t.Errorf("release must override build state: rel=%q digests=%v", boundRel, digests)
	}
}

// runPromote with no injected seams resolves the env's DECLARED backend — for
// a project with no KCL, the files — and appends there.
func TestRunPromote_AppendsThroughTheDeclaredStore(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := testCutRelease(t, dir, ociRelease("v1.4.0", map[string]string{"control-plane": sha("a")})); err != nil {
		t.Fatalf("write release: %v", err)
	}
	captureStdout(t, func() {
		if err := runPromote(context.Background(), "v1.4.0", "staging", promoteOptions{ProjectDir: dir, Git: allCommitsPresent(), Ledger: declaredLedger(t, dir, "staging")}); err != nil {
			t.Errorf("promote: %v", err)
		}
	})
	cur, bound, err := testBindings(t, dir).Current(context.Background(), "staging")
	if err != nil || !bound || cur.Release != "v1.4.0" || cur.Kind != release.KindPromote {
		t.Fatalf("promote must append through the store, got bound=%v %+v err=%v", bound, cur, err)
	}
}

// A promote to an OLDER release is an ordinary promote — no special kind, no
// "must have run it before" rule — end to end through the command's run
// function. Recovery is roll forward; moving backwards is possible and is
// labelled BEHIND by the plan, never recorded as a rollback.
func TestRunPromote_BackwardsIsAPlainPromote(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, v := range []string{"v1", "v2", "v3"} {
		if err := testCutRelease(t, dir, ociRelease(v, map[string]string{"api": sha(v[1:])})); err != nil {
			t.Fatal(err)
		}
	}
	run := func(v string) (string, error) {
		var err error
		out := captureStdout(t, func() {
			err = runPromote(context.Background(), v, "prod", promoteOptions{ProjectDir: dir, Note: "why", Git: allCommitsPresent(), Ledger: declaredLedger(t, dir, "prod")})
		})
		return out, err
	}
	for _, v := range []string{"v2", "v3"} {
		if _, err := run(v); err != nil {
			t.Fatal(err)
		}
	}
	// v1 never ran in prod — a backwards promote to it is still just a promote.
	out, err := run("v1")
	if err != nil {
		t.Fatalf("a backwards promote must be allowed: %v", err)
	}
	if !strings.Contains(out, "direction BEHIND") || !strings.Contains(out, "moves BACKWARDS") {
		t.Errorf("a backwards promote must be labelled loudly, got:\n%s", out)
	}
	history, _ := storeHistory(t, testBindings(t, dir), "prod")
	got := make([]string, 0, len(history))
	for _, p := range history {
		got = append(got, p.Release+":"+string(p.Kind))
	}
	if s := strings.Join(got, ","); s != "v2:promote,v3:promote,v1:promote" {
		t.Errorf("ledger = %s, want v2:promote,v3:promote,v1:promote", s)
	}
}
