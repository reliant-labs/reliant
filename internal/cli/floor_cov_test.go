package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// covBFloorProject is a project on a private machine ledger holding v1 and v2
// (v2 declares a schema change, so promoting it raises a declared floor),
// with `prod` promoted to v1. The project dir is pinned so the env floor
// commands find it without a chdir.
func covBFloorProject(t *testing.T) (dir string, bindings machineBindingStore) {
	t.Helper()
	dir = newLedgerTestProject(t, "floors")
	mustWriteFile(t, filepath.Join(dir, "forge.yaml"), "name: floors\n")
	if err := cmdutil.SetProjectDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })

	drop := release.FloorDeclaration{Source: floorMig, ID: "20260102000001_drop.up.sql", Kind: release.DeclarationSchema, Reason: "dropped legacy"}
	ctx := context.Background()
	releases := testReleases(t, dir)
	for _, r := range []release.Release{
		floorRelease("v1", 1, []string{"20260101000001_init.up.sql"}),
		floorRelease("v2", 2, []string{"20260101000001_init.up.sql", "20260102000001_drop.up.sql"}, drop),
	} {
		if _, err := releases.Cut(ctx, r); err != nil {
			t.Fatalf("cut %s: %v", r.Version, err)
		}
	}
	bindings = testBindings(t, dir)
	covBPromote(t, bindings, releases, "v1")
	return dir, bindings
}

func covBPromote(t *testing.T, b machineBindingStore, rl machineReleaseLedger, version string) release.Promotion {
	t.Helper()
	r, err := rl.Get(context.Background(), version)
	if err != nil || r == nil {
		t.Fatalf("get %s: %v", version, err)
	}
	p := release.NewPromotion("prod", *r, release.KindPromote)
	p.PromotedBy = release.Actor{Actor: "ci"}
	got, err := b.Append(context.Background(), p, appendGuard{Target: r})
	if err != nil {
		t.Fatalf("promote %s: %v", version, err)
	}
	return got
}

// covBFloor runs `forge env floor <args>` and returns what it wrote to the
// command's stdout, to the process's stdout (the --json documents) and its error.
func covBFloor(t *testing.T, args ...string) (out, doc string, err error) {
	t.Helper()
	cmd := newEnvFloorCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	doc = captureStdout(t, func() { err = cmd.ExecuteContext(context.Background()) })
	return buf.String(), doc, err
}

func covBExit(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	return exitCodeForError(err)
}

func TestEnvFloorShowSetCurrentAndLiftThroughTheCommands(t *testing.T) {
	covBFloorProject(t)

	out, _, err := covBFloor(t, "show", "prod")
	if err != nil || !strings.Contains(out, "prod has no rollback floor: any release can be deployed to it.") {
		t.Fatalf("show on a floorless env = %q, %v", out, err)
	}
	_, doc, err := covBFloor(t, "show", "prod", "--json")
	var shown floorShowDocument
	if err != nil || json.Unmarshal([]byte(doc), &shown) != nil {
		t.Fatalf("show --json = %q, %v", doc, err)
	}
	if !shown.Supported || shown.Floors == nil || len(shown.Floors) != 0 || shown.Schema != floorShowSchema || shown.Env != "prod" {
		t.Errorf("a ledger with no floors must say so with [] not null: %+v", shown)
	}

	// --current floors the env at the release it runs, by actor.
	out, _, err = covBFloor(t, "set", "prod", "--current", "--reason", "v1 is roll-forward only", "--actor", "rc-train")
	if err != nil {
		t.Fatalf("set --current: %v", err)
	}
	for _, want := range []string{"Set a rollback floor on prod:", "order: nothing cut before v1", "roll_forward_only", `"v1 is roll-forward only"`, "by rc-train"} {
		if !strings.Contains(out, want) {
			t.Errorf("set --current output missing %q:\n%s", want, out)
		}
	}
	out, _, err = covBFloor(t, "show", "prod")
	if err != nil || !strings.Contains(out, "prod's rollback floors (") || !strings.Contains(out, "nothing cut before v1") {
		t.Fatalf("show after set = %q, %v", out, err)
	}

	// A stale --expect-current is a promotion conflict (exit 3), not a floor.
	_, _, err = covBFloor(t, "set", "prod", "--current", "--reason", "x", "--expect-current", "pr_stale")
	var refused *promoteRefusedError
	if !errors.As(err, &refused) || refused.Reason != reasonPromotionConflict {
		t.Fatalf("stale --expect-current = %v; want a promotion_conflict refusal", err)
	}
	// --expect-current unbound while the env is bound is a conflict too.
	if _, _, err = covBFloor(t, "set", "prod", "--current", "--reason", "x", "--expect-current", expectUnboundLiteral); !errors.As(err, &refused) {
		t.Fatalf("--expect-current %s on a bound env = %v; want a refusal", expectUnboundLiteral, err)
	}

	// Lowering to v1 lifts nothing (no floor sits above v1) but sets a manual
	// floor at v1 beside the existing one.
	out, _, err = covBFloor(t, "lower", "prod", "v1", "--reason", "incident 4411")
	if err != nil || strings.Contains(out, "Lifted on prod:") || !strings.Contains(out, "Set on prod:") || !strings.Contains(out, "incident 4411") {
		t.Fatalf("lower = %q, %v; want only a new floor set", out, err)
	}
	// clear lifts every order floor, both of them; the second clear has nothing to do.
	if out, _, err = covBFloor(t, "clear", "prod", "--reason", "all clear"); err != nil || strings.Count(out, "[LIFTED]") != 2 {
		t.Fatalf("clear = %q, %v; want both order floors lifted", out, err)
	}
	out, _, err = covBFloor(t, "clear", "prod", "--reason", "again")
	if err != nil || !strings.Contains(out, "prod has no order floor to lift; nothing changed.") {
		t.Fatalf("clear with nothing to lift = %q, %v", out, err)
	}
	// --all shows the lifted floors with their lift line.
	out, _, err = covBFloor(t, "show", "prod", "--all")
	if err != nil || !strings.Contains(out, "[LIFTED]") || !strings.Contains(out, `lifted `) || !strings.Contains(out, `"all clear"`) {
		t.Fatalf("show --all = %q, %v", out, err)
	}
}

func TestEnvFloorSetRefusals(t *testing.T) {
	_, bindings := covBFloorProject(t)
	for _, tc := range []struct {
		name     string
		args     []string
		wantExit int
		wantMsg  string
	}{
		{"neither a release nor --current", []string{"set", "prod", "--reason", "r"}, exitWrong, "name exactly one of a release or --current"},
		{"both a release and --current", []string{"set", "prod", "v1", "--current", "--reason", "r"}, exitWrong, "name exactly one of a release or --current"},
		{"no reason", []string{"set", "prod", "v1"}, exitWrong, `needs --reason "<why>"`},
		{"blank reason", []string{"lift", "prod", "flr_1", "--reason", "   "}, exitWrong, `forge env floor lift needs --reason`},
		{"a release the ledger does not hold", []string{"set", "prod", "v9", "--reason", "r"}, exitWrong, `release "v9" is not in`},
		{"--current on an env that was never promoted", []string{"set", "staging", "--current", "--reason", "r"}, exitWrong, "staging has never been promoted"},
		{"lower to an unknown release", []string{"lower", "prod", "v9", "--reason", "r"}, exitWrong, `release "v9" is not in this ledger`},
		{"lift an unknown floor id", []string{"lift", "prod", "flr_nope", "--reason", "r"}, exitWrong, `has no active rollback floor "flr_nope"`},
		{"malformed acknowledgement", []string{"lift", "prod", "flr_1", "--reason", "r", "--acknowledge", "nocolon"}, exitWrong, "is not <source>:<id>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := covBFloor(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v; want it to contain %q", err, tc.wantMsg)
			}
			if got := covBExit(t, err); got != tc.wantExit {
				t.Errorf("exit = %d, want %d", got, tc.wantExit)
			}
		})
	}
	// A floor above the release the env runs would refuse the env's own
	// release, so the ledger refuses it and records nothing.
	_, _, err := covBFloor(t, "set", "prod", "v2", "--reason", "too high")
	if err == nil || !strings.Contains(err.Error(), "the environment runs v1, which this floor would refuse") {
		t.Errorf("a floor above current = %v", err)
	}
	if floors, _ := bindings.RollbackFloors(context.Background(), "prod", true); len(floors) != 0 {
		t.Errorf("a refused floor was recorded: %+v", floors)
	}
}

// A declared floor lifts only with every declaration it requires acknowledged,
// and the refusal prints the flags to add.
func TestEnvFloorLiftNamesTheMissingAcknowledgements(t *testing.T) {
	dir, bindings := covBFloorProject(t)
	covBPromote(t, bindings, testReleases(t, dir), "v2")
	floors, err := bindings.RollbackFloors(context.Background(), "prod", false)
	if err != nil || len(floors) != 1 || floors[0].Kind != release.FloorDeclared {
		t.Fatalf("fixture: promoting v2 must raise one declared floor, got %+v, %v", floors, err)
	}
	id := floors[0].ID
	ref := floors[0].Requires[0].Ref().String()

	_, _, err = covBFloor(t, "lift", "prod", id, "--reason", "never migrated")
	if err == nil || !strings.Contains(err.Error(), "a declared floor lifts only with each declaration it requires acknowledged") ||
		!strings.Contains(err.Error(), "--acknowledge "+ref) {
		t.Fatalf("unacknowledged lift = %v; want a refusal that prints --acknowledge %s", err, ref)
	}
	out, _, err := covBFloor(t, "show", "prod")
	if err != nil || !strings.Contains(out, "declared: must carry schema 20260102000001_drop.up.sql") || !strings.Contains(out, "dropped legacy") {
		t.Fatalf("show must render the declared floor and its reason: %q, %v", out, err)
	}

	out, _, err = covBFloor(t, "lift", "prod", id, "--reason", "never migrated", "--acknowledge", ref)
	if err != nil || !strings.Contains(out, "Lifted on prod:") || !strings.Contains(out, "[LIFTED]") {
		t.Fatalf("acknowledged lift = %q, %v", out, err)
	}
	_, doc, err := covBFloor(t, "show", "prod", "--json")
	var shown floorShowDocument
	if err != nil || json.Unmarshal([]byte(doc), &shown) != nil || len(shown.Floors) != 0 {
		t.Fatalf("a lifted floor is gone from the active list: %q, %v", doc, err)
	}
}

func TestEnvFloorCheckVerdicts(t *testing.T) {
	dir, bindings := covBFloorProject(t)
	covBPromote(t, bindings, testReleases(t, dir), "v2")
	if _, _, err := covBFloor(t, "set", "prod", "v2", "--reason", "roll forward"); err != nil {
		t.Fatal(err)
	}

	// below the floor: exit 1, a JSON refusal, and no "admit" line.
	_, doc, err := covBFloor(t, "check", "prod", "v1", "--json")
	var below floorCheckDocument
	if json.Unmarshal([]byte(doc), &below) != nil || below.Verdict != "below_floor" || below.Refusal == nil || below.Schema != "forge.env.floor-check/v1" {
		t.Fatalf("check v1 --json = %q", doc)
	}
	if covBExit(t, err) != exitWrong {
		t.Errorf("below a floor must exit %d, got %v", exitWrong, err)
	}

	out, doc, err := covBFloor(t, "check", "prod", "v2")
	if err != nil || doc != "" || !strings.Contains(out, "prod's rollback floors admit v2.") {
		t.Fatalf("check v2 = out %q doc %q err %v", out, doc, err)
	}
	_, doc, err = covBFloor(t, "check", "prod", "v2", "--json")
	var ok floorCheckDocument
	if err != nil || json.Unmarshal([]byte(doc), &ok) != nil || ok.Verdict != "admitted" || ok.Floor == nil || len(ok.Floor.Active) != 2 {
		t.Fatalf("check v2 --json = %q, %v; want admitted with both floors active", doc, err)
	}

	// A release the ledger does not hold cannot be judged: exit 2 (undetermined).
	_, doc, err = covBFloor(t, "check", "prod", "v9", "--json")
	var unknown floorCheckDocument
	if json.Unmarshal([]byte(doc), &unknown) != nil || unknown.Verdict != "unknown" || unknown.Floor == nil || unknown.Detail == "" {
		t.Fatalf("check v9 --json = %q", doc)
	}
	if covBExit(t, err) != exitUndetermined {
		t.Errorf("an unjudgeable release must exit %d, got %v", exitUndetermined, err)
	}
	if _, _, err = covBFloor(t, "check", "prod", "v9"); covBExit(t, err) != exitUndetermined {
		t.Errorf("text mode must exit %d too, got %v", exitUndetermined, err)
	}
}

func TestFloorRenderingHelpers(t *testing.T) {
	at := time.Date(2026, 11, 7, 12, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"zero time", formatFloorTime(time.Time{}), "unknown time"},
		{"time is UTC", formatFloorTime(at.In(time.FixedZone("x", 3600))), "2026-11-07 12:30Z"},
		{"user and actor", floorActor(release.Actor{User: "ana", Actor: "ci"}), ", by ana (ci)"},
		{"actor only", floorActor(release.Actor{Actor: "ci"}), ", by ci"},
		{"user only", floorActor(release.Actor{User: "ana"}), ", by ana"},
		{"nobody", floorActor(release.Actor{}), ""},
		{"token actor", tokenActor("t1"), "token:t1"},
		{"no token", tokenActor(""), ""},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	declared := release.Floor{Kind: release.FloorDeclared, Release: "v2", Requires: []release.FloorDeclaration{
		{Source: floorMig, ID: "a.sql", Kind: release.DeclarationSchema}, {Source: floorMig, ID: "b", Kind: release.DeclarationAPI}}}
	order := release.Floor{Kind: release.FloorOrder, Release: "v1", Reason: "bad"}
	for _, tc := range []struct{ name, got, want string }{
		{"declared long", describeFloor(declared), "after it, no release without schema a.sql, api b can deploy here"},
		{"order long", describeFloor(order), `after it, no release cut before v1 can deploy here ("bad")`},
		{"declared short", describeFloorShort(declared), "must carry a.sql, b (since v2)"},
		{"order short", describeFloorShort(order), `nothing cut before v1 — "bad"`},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	var b bytes.Buffer
	printStatusFloors(&b, nil, false)
	if b.Len() != 0 {
		t.Errorf("an unsupported ledger prints no floor line, got %q", b.String())
	}
	printStatusFloors(&b, []release.Floor{}, true)
	if b.String() != "  floor         none (any release can be deployed)\n" {
		t.Errorf("empty floors line = %q", b.String())
	}
	b.Reset()
	order.ID, order.Source = "flr_1", release.FloorSourceManual
	printStatusFloors(&b, []release.Floor{order}, true)
	if want := "  floor         order manual: nothing cut before v1 — \"bad\" [flr_1]\n"; b.String() != want {
		t.Errorf("floor line = %q, want %q", b.String(), want)
	}
}

func TestRenderFloorVerdict(t *testing.T) {
	raised := release.Floor{Kind: release.FloorOrder, Source: release.FloorSourceRollForwardOnly, Release: "v2", Reason: "r"}
	for _, tc := range []struct {
		name string
		v    *promoteFloorVerdict
		want []string
	}{
		{"nil prints nothing", nil, nil},
		{"admitted with no floors", &promoteFloorVerdict{Verdict: "admitted"}, []string{"floor     admitted (no rollback floor)"}},
		{"admitted over active floors", &promoteFloorVerdict{Verdict: "admitted", Active: []release.Floor{raised, raised}}, []string{"admitted (2 active rollback floor(s))"}},
		{"raises", &promoteFloorVerdict{Verdict: "raises", Raises: []release.Floor{raised}}, []string{"raises a roll_forward_only floor: after it, no release cut before v2"}},
		{"unknown names its note", &promoteFloorVerdict{Verdict: "unknown", Note: "ledger down"}, []string{"floor     unknown — ledger down"}},
		{"a note rides along an admitted verdict", &promoteFloorVerdict{Verdict: "admitted", Note: "no declarations recorded"}, []string{"note: no declarations recorded"}},
	} {
		var b bytes.Buffer
		renderFloorVerdict(&b, tc.v)
		if tc.want == nil && b.Len() != 0 {
			t.Errorf("%s: printed %q", tc.name, b.String())
		}
		for _, w := range tc.want {
			if !strings.Contains(b.String(), w) {
				t.Errorf("%s: %q missing %q", tc.name, b.String(), w)
			}
		}
	}
}

func TestFloorsFromWireKeepsOrderAndLifts(t *testing.T) {
	cut := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lifted := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	ws := []wireRollbackFloor{
		{ID: "a", Kind: "order", Version: "v1", ReleaseCutAt: cut, SetByActor: "ci", SetByTokenID: "ignored"},
		{ID: "b", Kind: "order", Version: "v2", ReleaseCutAt: cut, Lift: &struct {
			Reason          string    `json:"reason"`
			LiftedByUserID  string    `json:"liftedByUserId,omitempty"`
			LiftedByTokenID string    `json:"liftedByTokenId,omitempty"`
			LiftedByActor   string    `json:"liftedByActor,omitempty"`
			LiftedAt        time.Time `json:"liftedAt"`
			ReplacedBy      string    `json:"replacedByFloorId,omitempty"`
			Acknowledged    []struct {
				Source string `json:"source"`
				ID     string `json:"id"`
			} `json:"acknowledged,omitempty"`
		}{Reason: "oops", LiftedByTokenID: "t9", LiftedAt: lifted, ReplacedBy: "c", Acknowledged: []struct {
			Source string `json:"source"`
			ID     string `json:"id"`
		}{{Source: "src", ID: "id1"}}}},
	}
	got := floorsFromWire("prod", ws)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("floors = %+v", got)
	}
	if got[0].SetBy.Actor != "ci" || !got[0].Active() {
		t.Errorf("an explicit actor beats the token fallback and the floor is active: %+v", got[0])
	}
	l := got[1].Lift
	if l == nil || got[1].Active() || l.By.Actor != "token:t9" || l.ReplacedBy != "c" || !l.At.Equal(lifted) ||
		len(l.Acknowledged) != 1 || l.Acknowledged[0] != (release.DeclarationRef{Source: "src", ID: "id1"}) {
		t.Errorf("lift = %+v", l)
	}
}

// covBControlPlane is a fake control plane serving the floor rpcs: it answers
// GetCapabilities, records each call's request, and returns the programmed
// response (or error) per procedure.
type covBControlPlane struct {
	floors  bool
	replies map[string]any
	errs    map[string]error
	calls   []string
	reqs    map[string]map[string]any
}

func (c *covBControlPlane) Call(_ context.Context, procedure string, req, out any) error {
	c.calls = append(c.calls, procedure)
	if c.reqs == nil {
		c.reqs = map[string]map[string]any{}
	}
	if m, ok := req.(map[string]any); ok {
		c.reqs[procedure] = m
	}
	if err := c.errs[procedure]; err != nil {
		return err
	}
	if procedure == procDeployCapabilities {
		b, _ := json.Marshal(map[string]any{"rollbackFloors": c.floors})
		return json.Unmarshal(b, out)
	}
	reply, ok := c.replies[procedure]
	if !ok {
		return errors.New("unexpected call " + procedure)
	}
	b, _ := json.Marshal(reply)
	return json.Unmarshal(b, out)
}

type covBResolver map[string]error

func (r covBResolver) ResolveEnvironmentID(_ context.Context, env string) (string, error) {
	if err := r[env]; err != nil {
		return "", err
	}
	return "env-" + env, nil
}

func covBHosted(cp *covBControlPlane) *hostedStore {
	return &hostedStore{client: cp, resolver: covBResolver{"ghost": &cloud.Error{Code: cloud.CodeNotFound, Message: "no such env"}, "broken": errors.New("resolver down")}, endpoint: "https://cp.test"}
}

func TestHostedFloorStoreWireRoundTrips(t *testing.T) {
	ctx := context.Background()
	cut := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	wire := map[string]any{"id": "flr_1", "kind": "order", "source": "manual", "version": "v1", "releaseCutAt": cut, "reason": "bad", "setAt": cut}
	cp := &covBControlPlane{floors: true, replies: map[string]any{
		procListRollbackFloors: map[string]any{"floors": []any{wire}},
		procSetRollbackFloor:   map[string]any{"floor": wire},
		procLiftRollbackFloor:  map[string]any{"lifted": []any{wire}, "set": wire},
	}}
	s := covBHosted(cp)

	got, err := s.RollbackFloors(ctx, "prod", true)
	if err != nil || len(got) != 1 || got[0].ID != "flr_1" || got[0].Env != "prod" {
		t.Fatalf("RollbackFloors = %+v, %v", got, err)
	}
	if req := cp.reqs[procListRollbackFloors]; req["environmentId"] != "env-prod" || req["includeLifted"] != true {
		t.Errorf("list request = %v; want the resolved env id and includeLifted", req)
	}
	if _, err := s.RollbackFloors(ctx, "prod", false); err != nil {
		t.Fatal(err)
	}
	if _, set := cp.reqs[procListRollbackFloors]["includeLifted"]; set {
		t.Errorf("includeLifted must be omitted when false: %v", cp.reqs[procListRollbackFloors])
	}
	// The env id is resolved once per process and the capability asked once.
	caps := 0
	for _, c := range cp.calls {
		if c == procDeployCapabilities {
			caps++
		}
	}
	if caps != 1 {
		t.Errorf("capabilities asked %d times, want once", caps)
	}

	// An env the control plane has never heard of has no floors.
	if floors, err := s.RollbackFloors(ctx, "ghost", false); err != nil || floors != nil {
		t.Errorf("unknown env = %+v, %v; want no floors, no error", floors, err)
	}
	if _, err := s.RollbackFloors(ctx, "broken", false); err == nil || !strings.Contains(err.Error(), "resolver down") {
		t.Errorf("a resolver failure must surface: %v", err)
	}

	// set: --current vs a version, the guard, and the actor are all on the wire.
	if _, err := s.SetRollbackFloor(ctx, "prod", floorSetRequest{Current: true, Reason: "r", Guard: appendGuard{ExpectedCurrentID: "pr_1"}, By: release.Actor{Actor: "ci"}}); err != nil {
		t.Fatal(err)
	}
	req := cp.reqs[procSetRollbackFloor]
	if req["current"] != true || req["expectedCurrentPromotionId"] != "pr_1" || req["setByActor"] != "ci" || req["reason"] != "r" {
		t.Errorf("set --current request = %v", req)
	}
	if _, has := req["version"]; has {
		t.Errorf("--current sends no version: %v", req)
	}
	if _, err := s.SetRollbackFloor(ctx, "prod", floorSetRequest{Version: "v1", Reason: "r", Guard: appendGuard{ExpectUnbound: true}}); err != nil {
		t.Fatal(err)
	}
	if req := cp.reqs[procSetRollbackFloor]; req["version"] != "v1" || req["expectUnbound"] != true || req["current"] != nil {
		t.Errorf("set <version> request = %v", req)
	}

	// lift: ids, lower-to, clear and acknowledgements.
	res, err := s.LiftRollbackFloors(ctx, "prod", floorLiftRequest{
		FloorIDs: []string{"flr_1"}, LowerTo: "v0", Clear: true, Reason: "r", By: release.Actor{Actor: "ci"},
		Acknowledged: []release.DeclarationRef{{Source: "src", ID: "id"}},
	})
	if err != nil || len(res.Lifted) != 1 || res.Set == nil || res.Set.ID != "flr_1" {
		t.Fatalf("LiftRollbackFloors = %+v, %v", res, err)
	}
	req = cp.reqs[procLiftRollbackFloor]
	if req["lowerToVersion"] != "v0" || req["clearOrderFloors"] != true || req["liftedByActor"] != "ci" {
		t.Errorf("lift request = %v", req)
	}
	if acks, _ := req["acknowledged"].([]map[string]string); len(acks) != 1 || acks[0]["source"] != "src" || acks[0]["id"] != "id" {
		t.Errorf("acknowledgements = %#v", req["acknowledged"])
	}
}

func TestHostedFloorStoreFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("connection reset")

	// A control plane that cannot answer GetCapabilities is neither "supported" nor "unsupported".
	cp := &covBControlPlane{errs: map[string]error{procDeployCapabilities: boom}}
	if _, err := covBHosted(cp).RollbackFloors(ctx, "prod", false); err == nil || isFloorsUnsupported(err) || !strings.Contains(err.Error(), "whether it holds rollback floors") {
		t.Errorf("capability failure = %v; want a hard error that is not 'unsupported'", err)
	}

	cp = &covBControlPlane{floors: true, errs: map[string]error{procListRollbackFloors: boom, procSetRollbackFloor: boom, procLiftRollbackFloor: boom}}
	s := covBHosted(cp)
	if _, err := s.RollbackFloors(ctx, "prod", false); !errors.Is(err, boom) || !strings.Contains(err.Error(), "read prod's rollback floors from https://cp.test") {
		t.Errorf("list failure = %v", err)
	}
	if _, err := s.SetRollbackFloor(ctx, "prod", floorSetRequest{Version: "v1", Reason: "r"}); !errors.Is(err, boom) || !strings.Contains(err.Error(), "set a rollback floor on prod") {
		t.Errorf("set failure = %v", err)
	}
	if _, err := s.LiftRollbackFloors(ctx, "prod", floorLiftRequest{Clear: true, Reason: "r"}); !errors.Is(err, boom) || !strings.Contains(err.Error(), "lift prod's rollback floors") {
		t.Errorf("lift failure = %v", err)
	}
	// Resolver failures stop a set and a lift before any request.
	if _, err := s.SetRollbackFloor(ctx, "broken", floorSetRequest{Version: "v1"}); err == nil || !strings.Contains(err.Error(), "resolver down") {
		t.Errorf("set on an unresolvable env = %v", err)
	}
	if _, err := s.LiftRollbackFloors(ctx, "broken", floorLiftRequest{}); err == nil || !strings.Contains(err.Error(), "resolver down") {
		t.Errorf("lift on an unresolvable env = %v", err)
	}

	// An older control plane: every floor verb says "unsupported" with the endpoint.
	old := covBHosted(&covBControlPlane{floors: false})
	_, err := old.SetRollbackFloor(ctx, "prod", floorSetRequest{Version: "v1"})
	if !isFloorsUnsupported(err) || !strings.Contains(err.Error(), "https://cp.test") || exitCodeForError(err) != exitWrong {
		t.Errorf("unsupported set = %v (exit %d)", err, exitCodeForError(err))
	}
	if _, err := old.LiftRollbackFloors(ctx, "prod", floorLiftRequest{}); !isFloorsUnsupported(err) {
		t.Errorf("unsupported lift = %v", err)
	}

	// A refusal the control plane attaches to a set (a stale guard) is a typed refusal.
	refusal := &cloud.Error{Code: "failed_precondition", Reason: reasonPromotionConflict, Message: "moved", Details: []cloud.ErrorDetail{
		{Type: promoteRefusalType, Debug: json.RawMessage(`{"reason":"promotion_conflict","detail":"prod moved on"}`)},
	}}
	cp = &covBControlPlane{floors: true, errs: map[string]error{procSetRollbackFloor: refusal}}
	_, err = covBHosted(cp).SetRollbackFloor(ctx, "prod", floorSetRequest{Current: true, Reason: "r"})
	var refused *promoteRefusedError
	if !errors.As(err, &refused) || refused.Reason != reasonPromotionConflict || refused.Detail != "prod moved on" {
		t.Errorf("set refusal = %v; want a promotion_conflict refusal carrying the server's detail", err)
	}
}

func TestCheckDeployFloorsVerdicts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	v1 := floorRelease("v1", 1, nil)
	v2 := floorRelease("v2", 2, nil)
	rels := newMemReleaseLedger(v1, v2)

	// An unreadable target is a note, not a refusal: the plan reports it.
	mem := newMemBindingStore(nil)
	ledger := envLedger{Bindings: mem, Releases: rels}
	v, target, err := checkDeployFloors(ctx, io.Discard, dir, "prod", "v9", ledger, nil, "")
	if err != nil || target != nil || v.Verdict != "unknown" || !strings.Contains(v.Note, "target release could not be read") {
		t.Errorf("unknown target = %+v %v %v", v, target, err)
	}
	// A ledger without a floor seam: unknown, or a refusal when --floor needs one.
	v, target, err = checkDeployFloors(ctx, io.Discard, dir, "prod", "v1", ledger, nil, "")
	if err != nil || target == nil || v.Verdict != "unknown" || v.Note != "this ledger holds no rollback floors" {
		t.Errorf("floorless ledger = %+v %v %v", v, target, err)
	}
	if _, _, err = checkDeployFloors(ctx, io.Discard, dir, "prod", "v1", ledger, nil, "roll forward"); err == nil || !strings.Contains(err.Error(), "--floor: memory://promotions holds no rollback floors") || exitCodeForError(err) != exitWrong {
		t.Errorf("--floor on a floorless ledger = %v", err)
	}

	// An unsupported control plane: a verdict, or an error under --floor.
	old := envLedger{Bindings: covBHosted(&covBControlPlane{floors: false}), Releases: rels}
	v, _, err = checkDeployFloors(ctx, io.Discard, dir, "prod", "v1", old, nil, "")
	if err != nil || v.Verdict != "unsupported" || !strings.Contains(v.Note, "predates them") {
		t.Errorf("unsupported = %+v %v", v, err)
	}
	if _, _, err = checkDeployFloors(ctx, io.Discard, dir, "prod", "v1", old, nil, "why"); !isFloorsUnsupported(err) || !strings.Contains(err.Error(), "--floor:") {
		t.Errorf("--floor on an unsupported control plane = %v", err)
	}
	// An unreadable ledger: noted on the writer, verdict unknown, never a refusal.
	down := envLedger{Bindings: covBHosted(&covBControlPlane{floors: true, errs: map[string]error{procListRollbackFloors: errors.New("503")}}), Releases: rels}
	var note bytes.Buffer
	v, target, err = checkDeployFloors(ctx, &note, dir, "prod", "v1", down, nil, "")
	if err != nil || target == nil || v.Verdict != "unknown" || !strings.Contains(note.String(), "[deploy] Note: prod's rollback floors could not be read") {
		t.Errorf("unreadable floors = %+v %v note %q", v, err, note.String())
	}
	if _, _, err = checkDeployFloors(ctx, io.Discard, dir, "prod", "v1", down, nil, "why"); err == nil || !strings.Contains(err.Error(), "--floor:") {
		t.Errorf("--floor with unreadable floors must refuse, got %v", err)
	}

	// A release recorded without declarations in a project that has migrations gets a note.
	mustWriteFile(t, filepath.Join(dir, "db", "migrations", "001_init.up.sql"), "select 1;\n")
	bare := release.Release{Version: "v3", CreatedAt: time.Now(), Artifacts: v1.Artifacts}
	bareLedger := envLedger{Bindings: covBHosted(&covBControlPlane{floors: true, replies: map[string]any{procListRollbackFloors: map[string]any{"floors": []any{}}}}), Releases: newMemReleaseLedger(bare)}
	v, _, err = checkDeployFloors(ctx, io.Discard, dir, "prod", "v3", bareLedger, nil, "")
	if err != nil || v.Verdict != "admitted" || !strings.Contains(v.Note, "records no rollback-floor declarations") {
		t.Errorf("a pre-declaration release = %+v %v", v, err)
	}
}

func TestFloorsRaisedByAndStatusFloors(t *testing.T) {
	ctx := context.Background()
	_, bindings := covBFloorProject(t)
	if _, err := bindings.SetRollbackFloor(ctx, "prod", floorSetRequest{Current: true, Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	cur, _, _ := bindings.Current(ctx, "prod")
	ledger := envLedger{Bindings: bindings}

	if got := floorsRaisedBy(ctx, ledger, "prod", &cur); len(got) != 1 || got[0].PromotionID != cur.ID {
		t.Errorf("floorsRaisedBy = %+v; want the one floor naming promotion %s", got, cur.ID)
	}
	for name, got := range map[string][]release.Floor{
		"nil promotion":     floorsRaisedBy(ctx, ledger, "prod", nil),
		"promotion no id":   floorsRaisedBy(ctx, ledger, "prod", &release.Promotion{}),
		"ledger no floors":  floorsRaisedBy(ctx, envLedger{Bindings: newMemBindingStore(nil)}, "prod", &cur),
		"unreadable ledger": floorsRaisedBy(ctx, envLedger{Bindings: covBHosted(&covBControlPlane{floors: true})}, "prod", &cur),
	} {
		if len(got) != 0 {
			t.Errorf("%s: floorsRaisedBy = %+v, want none", name, got)
		}
	}

	if floors, ok := statusFloors(ctx, bindings, "prod"); !ok || len(floors) != 1 {
		t.Errorf("statusFloors = %+v, %v", floors, ok)
	}
	if floors, ok := statusFloors(ctx, bindings, "staging"); !ok || floors == nil || len(floors) != 0 {
		t.Errorf("an env with no floors is [] and supported: %#v, %v", floors, ok)
	}
	if _, ok := statusFloors(ctx, newMemBindingStore(nil), "prod"); ok {
		t.Error("a ledger without a floor seam is unknown")
	}
	if _, ok := statusFloors(ctx, covBHosted(&covBControlPlane{floors: false}), "prod"); ok {
		t.Error("an unsupported control plane is unknown, not 'no floors'")
	}
}
