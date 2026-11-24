package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

const floorMig = "github.com/acme/app/db/migrations"

// floorRelease is a release cut at day d carrying files, declaring decls.
func floorRelease(version string, day int, files []string, decls ...release.FloorDeclaration) release.Release {
	r := ociRelease(version, map[string]string{"registry.test/acme/api": sha(version)})
	r.CreatedAt = time.Date(2026, 1, day, 12, 0, 0, 0, time.UTC)
	r.Migrations = &release.MigrationManifest{
		Sets:                 []release.MigrationSet{{Source: floorMig, Files: files}},
		Declarations:         decls,
		DeclarationsRecorded: true,
	}
	return r
}

func wantBelowFloor(t *testing.T, err error) *promoteRefusedError {
	t.Helper()
	var refused *promoteRefusedError
	if !errors.As(err, &refused) || refused.Reason != reasonBelowRollbackFloor {
		t.Fatalf("err = %v; want a %s refusal", err, reasonBelowRollbackFloor)
	}
	if refused.ExitCode() != exitWrong || len(refused.Floors) == 0 {
		t.Fatalf("refusal = %+v (exit %d); want exit 1 naming the floors", refused, refused.ExitCode())
	}
	return refused
}

// A below_rollback_floor refusal is exit 1 — the same code a forge that
// predates the reason gives any unknown reason, so old and new CLIs agree.
func TestBelowRollbackFloorIsExitOneLikeAnUnknownReason(t *testing.T) {
	if got := exitCodeForRefusal(reasonBelowRollbackFloor); got != exitWrong {
		t.Fatalf("below_rollback_floor exit = %d; want %d", got, exitWrong)
	}
	if got := exitCodeForRefusal("a_reason_from_a_newer_server"); got != exitWrong {
		t.Fatalf("unknown reason exit = %d; want %d (what an old forge does with below_rollback_floor)", got, exitWrong)
	}
	if !strings.Contains(refusalHint(reasonBelowRollbackFloor), "forge env floor") {
		t.Errorf("hint does not name the floor verbs: %q", refusalHint(reasonBelowRollbackFloor))
	}
}

// The machine ledger end to end: a promote of a declaring release raises a
// declared floor in the same lock hold; an older release, and a hotfix cut
// later without the migration, are refused; --floor raises an order floor;
// set --current, lower and a lift that must name its declaration.
func TestMachineLedgerRollbackFloors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bindings, releases := testBindings(t, dir), testReleases(t, dir)
	m1, m2 := "20260101000001_init.up.sql", "20260102000001_drop.up.sql"
	drop := release.FloorDeclaration{Source: floorMig, ID: m2, Kind: release.DeclarationSchema, Reason: "dropped legacy"}
	v1 := floorRelease("v1", 1, []string{m1})
	v2 := floorRelease("v2", 2, []string{m1, m2}, drop)
	hotfix := floorRelease("v1.9.1", 3, []string{m1})
	v3 := floorRelease("v3", 4, []string{m1, m2}, drop)
	for _, r := range []release.Release{v1, v2, hotfix, v3} {
		if _, err := releases.Cut(ctx, r); err != nil {
			t.Fatalf("cut %s: %v", r.Version, err)
		}
	}
	promote := func(r release.Release, reason string) (release.Promotion, error) {
		p := release.NewPromotion("prod", r, release.KindPromote)
		p.PromotedBy = release.Actor{Actor: "ci"}
		target := r
		return bindings.Append(ctx, p, appendGuard{Target: &target, RaiseFloorReason: reason})
	}
	if _, err := promote(v1, ""); err != nil {
		t.Fatal(err)
	}
	p2, err := promote(v2, "")
	if err != nil {
		t.Fatal(err)
	}
	floors, err := bindings.RollbackFloors(ctx, "prod", false)
	if err != nil || len(floors) != 1 || floors[0].Kind != release.FloorDeclared || floors[0].PromotionID != p2.ID {
		t.Fatalf("floors after v2 = %+v, %v; want one declared floor raised by v2's promotion", floors, err)
	}
	if again, err := promote(v2, ""); err != nil || again.ID != p2.ID {
		t.Fatalf("re-promote = %+v, %v; want the existing entry", again, err)
	}
	if fs, _ := bindings.RollbackFloors(ctx, "prod", false); len(fs) != 1 {
		t.Fatalf("a re-promote raised again: %+v", fs)
	}
	_, err = promote(v1, "")
	wantBelowFloor(t, err)
	_, err = promote(hotfix, "")
	if r := wantBelowFloor(t, err); !strings.Contains(r.Detail, m2) {
		t.Fatalf("detail %q does not name the migration", r.Detail)
	}

	// --floor on v3: an order floor at it, beside the declared one.
	p3, err := promote(v3, "v3 is roll-forward only")
	if err != nil {
		t.Fatal(err)
	}
	floors, _ = bindings.RollbackFloors(ctx, "prod", false)
	if len(floors) != 2 || floors[1].Kind != release.FloorOrder || floors[1].Source != release.FloorSourceRollForwardOnly || floors[1].PromotionID != p3.ID {
		t.Fatalf("floors after --floor = %+v", floors)
	}
	_, err = promote(v2, "")
	wantBelowFloor(t, err)

	// set --current under a stale guard is a conflict, not a floor.
	if _, err := bindings.SetRollbackFloor(ctx, "prod", floorSetRequest{Current: true, Reason: "r", Guard: appendGuard{ExpectedCurrentID: p2.ID}}); !isConflict(err) {
		t.Fatalf("stale --current = %v; want promotion_conflict", err)
	}
	// A floor above what the env runs is refused.
	v4 := floorRelease("v4", 5, []string{m1, m2}, drop)
	if _, err := releases.Cut(ctx, v4); err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.SetRollbackFloor(ctx, "prod", floorSetRequest{Version: "v4", Reason: "too high"}); err == nil {
		t.Fatal("a floor above current was set")
	}

	// lower to v2: lifts v3's order floor, sets a manual one at v2. The
	// declared floor stays, and v2 carries it, so v2 is admitted.
	res, err := bindings.LiftRollbackFloors(ctx, "prod", floorLiftRequest{LowerTo: "v2", Reason: "incident 4411", By: release.Actor{Actor: "ci"}})
	if err != nil || len(res.Lifted) != 1 || res.Set == nil || res.Set.Release != "v2" || res.Lifted[0].Lift.ReplacedBy != res.Set.ID {
		t.Fatalf("lower = %+v, %v", res, err)
	}
	if _, err := promote(v2, ""); err != nil {
		t.Fatalf("v2 after lower: %v", err)
	}

	// The declared floor lifts only with its declaration acknowledged.
	declared := floors[0].ID
	if _, err := bindings.LiftRollbackFloors(ctx, "prod", floorLiftRequest{FloorIDs: []string{declared}, Reason: "never migrated"}); err == nil ||
		!strings.Contains(err.Error(), drop.Ref().String()) {
		t.Fatalf("unacknowledged lift = %v; want a refusal naming %s", err, drop.Ref())
	}
	if _, err := bindings.LiftRollbackFloors(ctx, "prod", floorLiftRequest{FloorIDs: []string{declared}, Reason: "never migrated", Acknowledged: []release.DeclarationRef{drop.Ref()}}); err != nil {
		t.Fatalf("acknowledged lift: %v", err)
	}
	all, err := bindings.RollbackFloors(ctx, "prod", true)
	if err != nil || len(all) != 3 {
		t.Fatalf("history = %+v, %v; want every floor kept, lifted ones marked", all, err)
	}
	for _, f := range all {
		if f.ID == declared && (f.Lift == nil || len(f.Lift.Acknowledged) != 1 || f.Lift.Reason != "never migrated") {
			t.Fatalf("declared floor's lift = %+v", f.Lift)
		}
	}
}

// The deploy refuses below a floor BEFORE the plan, the confirmation or the
// write — exit 1, nothing recorded — and `--floor` sets one in the write.
func TestRunPromote_RefusesBelowAFloorBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for i, v := range []string{"v1", "v2"} {
		r := ociRelease(v, map[string]string{"api": sha(v[1:])})
		r.CreatedAt = time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC)
		if err := testCutRelease(t, dir, r); err != nil {
			t.Fatal(err)
		}
	}
	run := func(v, floor string) error {
		var err error
		captureStdout(t, func() {
			err = runPromote(context.Background(), v, "staging", promoteOptions{
				ProjectDir: dir, Git: allCommitsPresent(), Ledger: declaredLedger(t, dir, "staging"), RaiseFloorReason: floor,
			})
		})
		return err
	}
	if err := run("v1", ""); err != nil {
		t.Fatal(err)
	}
	if err := run("v2", "v2 is roll-forward only"); err != nil {
		t.Fatal(err)
	}
	b := testBindings(t, dir)
	floors, err := b.RollbackFloors(context.Background(), "staging", false)
	if err != nil || len(floors) != 1 || floors[0].Release != "v2" || floors[0].Source != release.FloorSourceRollForwardOnly {
		t.Fatalf("floors = %+v, %v; want deploy --floor's order floor at v2", floors, err)
	}
	wantBelowFloor(t, run("v1", ""))
	if cur, _, _ := b.Current(context.Background(), "staging"); cur.Release != "v2" {
		t.Fatalf("a refused deploy moved the env to %s", cur.Release)
	}
	// `floor check` is the same judgement, alone, for a pipeline to ask
	// before it tags: exit 1 below the floor, exit 0 at it.
	wantBelowFloor(t, runFloorCheck(context.Background(), io.Discard, "staging", "v1", false))
	if err := runFloorCheck(context.Background(), io.Discard, "staging", "v2", false); err != nil {
		t.Fatalf("check v2 = %v; want admitted", err)
	}
}

func isConflict(err error) bool {
	var refused *promoteRefusedError
	return errors.As(err, &refused) && refused.Reason == reasonPromotionConflict
}

// The offline copy judges its cached floors before the local apply, says
// "unknown" for a copy that predates floors, and refuses --floor.
func TestOfflineAppendJudgesCachedFloors(t *testing.T) {
	v1 := floorRelease("v1", 1, nil)
	floor := release.NewOrderFloor("prod", floorRelease("v2", 2, nil), release.FloorSourceManual, "incident", release.Actor{}, time.Unix(1, 0), "")
	floor.ID = "flr-1"
	b := &offlineBindings{snap: ledgerfile.HostedSnapshot{Floors: []release.Floor{floor}, FloorsRecorded: true}, releases: map[string]release.Release{"v1": v1}}
	target := v1
	_, err := b.Append(context.Background(), release.NewPromotion("prod", v1, release.KindPromote), appendGuard{Target: &target})
	wantBelowFloor(t, err)
	if _, err := b.Append(context.Background(), release.NewPromotion("prod", v1, release.KindPromote), appendGuard{Target: &target, RaiseFloorReason: "r"}); err == nil {
		t.Fatal("--floor offline was accepted")
	}
	old := &offlineBindings{snap: ledgerfile.HostedSnapshot{RefreshedAt: time.Unix(1, 0)}}
	if _, err := old.RollbackFloors(context.Background(), "prod", false); err == nil || !strings.Contains(err.Error(), "predates rollback floors") {
		t.Fatalf("a copy that predates floors = %v; want unknown, not none", err)
	}
}

// A copy whose floors are unknown (it predates them, or their read failed)
// refuses the local apply with exit 2 before anything is queued: unknown is
// never read as "no floors" (review R1.B1).
func TestOfflineAppendRefusesUnknownFloors(t *testing.T) {
	v1 := floorRelease("v1", 1, nil)
	// No store: reaching the queue would panic, so a pass also proves nothing
	// was queued.
	b := &offlineBindings{snap: ledgerfile.HostedSnapshot{RefreshedAt: time.Unix(1, 0)}, releases: map[string]release.Release{"v1": v1}}
	target := v1
	_, err := b.Append(context.Background(), release.NewPromotion("prod", v1, release.KindPromote), appendGuard{Target: &target})
	var coded exitCodeError
	if !errors.As(err, &coded) || coded.code != exitUndetermined {
		t.Fatalf("Append from a copy without floors = %v; want exit %d (undetermined)", err, exitUndetermined)
	}
	if !strings.Contains(err.Error(), "forge env status prod") {
		t.Fatalf("the refusal does not say how to refresh the copy: %v", err)
	}
}

// fillFloors records a control plane that has no floors as "none" (its own
// Promote, which a queued promotion replays through, judges none either),
// and leaves a failed read "unknown".
func TestFillFloorsRecordsNoneOnlyFromAFloorlessControlPlane(t *testing.T) {
	for name, tc := range map[string]struct {
		caller       cloudCaller
		wantRecorded bool
	}{
		"no field": {&capsCaller{floors: false}, true},
		"no rpc":   {&capsCaller{unimpl: true}, true},
		"failed":   {failingCaller{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := &hostedStore{client: tc.caller, endpoint: "https://cp.test"}
			var snap ledgerfile.HostedSnapshot
			s.fillFloors(context.Background(), "prod", &snap)
			if snap.FloorsRecorded != tc.wantRecorded || len(snap.Floors) != 0 {
				t.Fatalf("snapshot = recorded %v floors %v; want recorded %v, none", snap.FloorsRecorded, snap.Floors, tc.wantRecorded)
			}
		})
	}
}

// A refresh that finds the current promotion unchanged still reads the
// floors while the copy has them as unknown, so `forge env status` is enough
// to unblock an offline deploy; once recorded, they are carried as they are.
func TestRememberRereadsUnknownFloors(t *testing.T) {
	store, err := ledgerfile.Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	const endpoint = "https://cp.test"
	v1 := floorRelease("v1", 1, nil)
	p := release.NewPromotion("prod", v1, release.KindPromote)
	p.ID = "prm-1"
	prev := ledgerfile.HostedSnapshot{Env: "prod", Endpoint: endpoint, Bound: true, Current: &p, Release: &v1, Window: offlineWindow()}
	if err := store.PutHostedSnapshot(prev); err != nil {
		t.Fatal(err)
	}
	caller := &capsCaller{floors: false}
	s := &hostedStore{client: caller, endpoint: endpoint, mirror: &hostedMirror{store: store, endpoint: endpoint}}
	s.remember(context.Background(), "prod", p, true)
	got, err := store.HostedSnapshot("prod")
	if err != nil || got == nil || !got.FloorsRecorded {
		t.Fatalf("after a refresh the floors are still unknown: %+v %v", got, err)
	}
	if len(caller.calls) != 1 {
		t.Fatalf("calls = %v; want one capability probe", caller.calls)
	}
	// A fresh process (no cached capability answer) carries them.
	again := &capsCaller{floors: false}
	s2 := &hostedStore{client: again, endpoint: endpoint, mirror: &hostedMirror{store: store, endpoint: endpoint}}
	s2.remember(context.Background(), "prod", p, true)
	if len(again.calls) != 0 {
		t.Fatalf("recorded floors were read again: %v", again.calls)
	}
}

// failingCaller fails every call, as an unreachable control plane does.
type failingCaller struct{}

func (failingCaller) Call(context.Context, string, any, any) error {
	return errors.New("connection refused")
}

// floorFakeGit answers ancestry from a table.
type floorFakeGit struct{ ancestors map[[2]string]bool }

func (g floorFakeGit) HasCommit(context.Context, string, string) bool { return true }
func (g floorFakeGit) CommitsBetween(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}
func (g floorFakeGit) IsAncestor(_ context.Context, _ string, a, b string) (bool, error) {
	return a == b || g.ancestors[[2]string{a, b}], nil
}

// The client strengthens an order floor with git: a hotfix cut later from a
// commit that is an ancestor of the floor's is refused, though the server's
// cut-order rule would admit it.
func TestFloorAncestryRefusesOlderCodeCutLater(t *testing.T) {
	floorCommit, oldCommit := strings.Repeat("b", 40), strings.Repeat("a", 40)
	atRel := floorRelease("v2", 2, nil)
	atRel.Git.Commit = floorCommit
	floor := release.NewOrderFloor("prod", atRel, release.FloorSourceManual, "v1.8 corrupts invoices", release.Actor{}, time.Unix(1, 0), "")
	hotfix := floorRelease("v1.9.1", 3, nil)
	hotfix.Git.Commit = oldCommit
	if ok, _ := floor.Admits(hotfix); !ok {
		t.Fatal("fixture: cut order should admit the later hotfix")
	}
	git := floorFakeGit{ancestors: map[[2]string]bool{{oldCommit, floorCommit}: true}}
	if r := floorAncestryRefusal(context.Background(), git, t.TempDir(), "prod", hotfix, []release.Floor{floor}); r == nil || r.Reason != reasonBelowRollbackFloor {
		t.Fatalf("ancestry refusal = %+v; want below_rollback_floor", r)
	}
	if r := floorAncestryRefusal(context.Background(), floorFakeGit{}, t.TempDir(), "prod", hotfix, []release.Floor{floor}); r != nil {
		t.Fatalf("unrelated commits refused: %+v", r)
	}
}

// capsCaller answers GetCapabilities and records what else was called.
type capsCaller struct {
	floors bool
	unimpl bool
	calls  []string
}

func (c *capsCaller) Call(_ context.Context, procedure string, _, out any) error {
	c.calls = append(c.calls, procedure)
	if procedure != procDeployCapabilities {
		return errors.New("unexpected call " + procedure)
	}
	if c.unimpl {
		return &cloud.Error{Code: cloud.CodeUnimplemented, Message: "no such rpc"}
	}
	caps := out.(*struct {
		RollbackFloors bool `json:"rollbackFloors"`
	})
	caps.RollbackFloors = c.floors
	return nil
}

// A control plane that does not say it holds floors is "unsupported", which
// --floor refuses and `floor show` reports as unknown — never a silently
// discarded raise_floor_reason.
func TestHostedFloorsNeedTheCapability(t *testing.T) {
	for name, caller := range map[string]*capsCaller{"no field": {floors: false}, "no rpc": {unimpl: true}} {
		t.Run(name, func(t *testing.T) {
			s := &hostedStore{client: caller, endpoint: "https://cp.test"}
			_, err := s.RollbackFloors(context.Background(), "prod", false)
			if !isFloorsUnsupported(err) {
				t.Fatalf("err = %v; want unsupported", err)
			}
			if len(caller.calls) != 1 {
				t.Fatalf("calls = %v; want only the capability probe", caller.calls)
			}
			if _, err := s.RollbackFloors(context.Background(), "prod", false); !isFloorsUnsupported(err) || len(caller.calls) != 1 {
				t.Fatalf("second read re-asked or changed answer: %v %v", err, caller.calls)
			}
		})
	}
}

// The wire floor decodes into forge's type, lift and all.
func TestFloorFromWire(t *testing.T) {
	cut := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	w := wireRollbackFloor{ID: "flr_1", Kind: "declared", Source: "declared", Version: "v2", ReleaseCutAt: cut, Reason: "x",
		Requires: []wireFloorDeclaration{{Source: floorMig, ID: "20260102_drop.up.sql", Kind: "schema"}}, SetByTokenID: "tok-1"}
	f := floorFromWire("prod", w)
	if f.Kind != release.FloorDeclared || f.Env != "prod" || len(f.Requires) != 1 || f.Requires[0].Kind != release.DeclarationSchema ||
		f.SetBy.Actor != "token:tok-1" || !f.ReleaseCutAt.Equal(cut) || !f.Active() {
		t.Fatalf("floor = %+v", f)
	}
}
