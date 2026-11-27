package ledgerfile

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

func orderFloor(env, rel string, cut time.Time) release.Floor {
	return release.Floor{
		Env:          env,
		Kind:         release.FloorOrder,
		Source:       release.FloorSourceManual,
		Release:      rel,
		ReleaseCutAt: cut,
		Reason:       "schema change",
	}
}

func decideAdd(floors ...release.Floor) func([]release.Promotion, []release.Floor) (FloorChange, error) {
	return func([]release.Promotion, []release.Floor) (FloorChange, error) {
		return FloorChange{Floors: floors}, nil
	}
}

var cutAt = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func TestFloorsOnAnEmptyLedger(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if got, err := s.Floors("prod"); err != nil || len(got) != 0 {
		t.Errorf("Floors = %v, %v", got, err)
	}
	if id := NewFloorID(); !strings.HasPrefix(id, "flr-") || id == NewFloorID() {
		t.Errorf("NewFloorID = %q, want a unique flr- id", id)
	}
}

func TestChangeFloorsAssignsIDAndTimeAndJoinsLifts(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	got, err := s.ChangeFloors("prod", decideAdd(orderFloor("prod", "v2", cutAt)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Floors) != 1 || !strings.HasPrefix(got.Floors[0].ID, "flr-") || got.Floors[0].SetAt.IsZero() {
		t.Fatalf("recorded change = %+v; want an id and SetAt assigned", got)
	}
	id := got.Floors[0].ID

	floors, err := s.Floors("prod")
	if err != nil || len(floors) != 1 || floors[0].ID != id || !floors[0].Active() {
		t.Fatalf("Floors = %+v, %v; want one active floor", floors, err)
	}

	// Lowering: lift the floor and set a lower one in one change.
	lowered := orderFloor("prod", "v1", cutAt.Add(-time.Hour))
	lowered.ID = "flr-lower"
	liftAt := cutAt.Add(time.Hour)
	_, err = s.ChangeFloors("prod", func(_ []release.Promotion, existing []release.Floor) (FloorChange, error) {
		if len(existing) != 1 || existing[0].ID != id {
			t.Errorf("decide saw floors %+v, want the one already recorded", existing)
		}
		return FloorChange{
			Floors: []release.Floor{lowered},
			Lifts:  []FloorLift{{FloorID: id, FloorLift: release.FloorLift{Reason: "lowering", At: liftAt, ReplacedBy: "flr-lower"}}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	floors, err = s.Floors("prod")
	if err != nil || len(floors) != 2 {
		t.Fatalf("Floors = %+v, %v", floors, err)
	}
	if floors[0].Active() || floors[0].Lift == nil || floors[0].Lift.ReplacedBy != "flr-lower" || !floors[0].Lift.At.Equal(liftAt) {
		t.Errorf("the lifted floor = %+v; want its lift joined", floors[0])
	}
	if !floors[1].Active() || floors[1].ID != "flr-lower" {
		t.Errorf("the replacement = %+v; want it active under the id the decision chose", floors[1])
	}
	if active := release.ActiveFloors(floors); len(active) != 1 || active[0].ID != "flr-lower" {
		t.Errorf("ActiveFloors = %+v", active)
	}
}

func TestChangeFloorsStampsLiftTimeWhenOmitted(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	rec, err := s.ChangeFloors("prod", decideAdd(orderFloor("prod", "v2", cutAt)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.ChangeFloors("prod", func([]release.Promotion, []release.Floor) (FloorChange, error) {
		return FloorChange{Lifts: []FloorLift{{FloorID: rec.Floors[0].ID, FloorLift: release.FloorLift{Reason: "done"}}}}, nil
	})
	if err != nil || out.Lifts[0].At.IsZero() {
		t.Errorf("lift = %+v, %v; want At stamped", out.Lifts, err)
	}
}

func TestChangeFloorsRecordsNothingWhenDecideRefusesOrTheFloorIsInvalid(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	boom := errors.New("refused")
	if _, err := s.ChangeFloors("prod", func([]release.Promotion, []release.Floor) (FloorChange, error) {
		return FloorChange{}, boom
	}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the decision's refusal", err)
	}
	if _, err := s.ChangeFloors("prod", decideAdd(orderFloor("staging", "v2", cutAt))); err == nil || !strings.Contains(err.Error(), `recording a floor of "staging" in prod's log`) {
		t.Errorf("a floor of another env = %v", err)
	}
	noReason := orderFloor("prod", "v2", cutAt)
	noReason.Reason = ""
	if _, err := s.ChangeFloors("prod", decideAdd(noReason)); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("a floor with no reason = %v, want ErrInvalid", err)
	}
	if floors, _ := s.Floors("prod"); len(floors) != 0 {
		t.Errorf("a refused change was recorded: %+v", floors)
	}
}

// decide runs under the same lock as the promotion history, so it judges
// against what is on disk now.
func TestChangeFloorsHandsDecideThePromotionHistory(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, err := s.AppendPromotion(promotion("prod", "v1"), decideOnly); err != nil {
		t.Fatal(err)
	}
	_, err := s.ChangeFloors("prod", func(history []release.Promotion, _ []release.Floor) (FloorChange, error) {
		if len(history) != 1 || history[0].Release != "v1" {
			t.Errorf("history = %+v", history)
		}
		return FloorChange{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFloorsRefuseCorruptOrMisfiledLines(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	writeRaw(t, s.envPath("floors", "prod"), "{not json\n")
	if _, err := s.Floors("prod"); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("a corrupt floor line = %v", err)
	}
	if _, err := s.ChangeFloors("prod", decideAdd()); err == nil {
		t.Error("a corrupt floors file must refuse a change, not overwrite it")
	}

	other := openTest(t)
	if _, err := other.ChangeFloors("staging", decideAdd(orderFloor("staging", "v2", cutAt))); err != nil {
		t.Fatal(err)
	}
	misfiled := openTest(t)
	data := readFileT(t, other.envPath("floors", "staging"))
	writeRaw(t, misfiled.envPath("floors", "prod"), data)
	if _, err := misfiled.Floors("prod"); err == nil || !strings.Contains(err.Error(), `records env "staging", not "prod"`) {
		t.Errorf("a floor filed under another env = %v", err)
	}

	liftsBad := openTest(t)
	writeRaw(t, liftsBad.envPath("floor_lifts", "prod"), "{nope\n")
	if _, err := liftsBad.Floors("prod"); err == nil {
		t.Error("a corrupt lifts file must be an error")
	}
}

func TestAppendPromotionWithFloors(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	raised := orderFloor("prod", "v1", cutAt)
	raise := func(floors []release.Floor, promoted release.Promotion) []release.Floor {
		if promoted.ID == "" || promoted.PromotedAt.IsZero() {
			t.Errorf("raise saw an unrecorded promotion: %+v", promoted)
		}
		return []release.Floor{raised}
	}
	admit := func(history []release.Promotion, floors []release.Floor, p release.Promotion) (*release.Promotion, error) {
		return release.Decide(history, p)
	}

	p, err := s.AppendPromotionWithFloors(promotion("prod", "v1"), admit, raise)
	if err != nil || p.ID == "" {
		t.Fatalf("AppendPromotionWithFloors = %+v, %v", p, err)
	}
	floors, _ := s.Floors("prod")
	if len(floors) != 1 || floors[0].Release != "v1" {
		t.Errorf("the promotion's floor was not recorded with it: %+v", floors)
	}

	// An idempotent re-promote appends nothing but still offers the existing
	// entry to raise, so a floor a first attempt missed is raised now.
	called := false
	again, err := s.AppendPromotionWithFloors(promotion("prod", "v1"), admit, func(_ []release.Floor, promoted release.Promotion) []release.Floor {
		called = true
		if promoted.ID != p.ID {
			t.Errorf("raise saw %q, want the existing entry %q", promoted.ID, p.ID)
		}
		return nil
	})
	if err != nil || again.ID != p.ID || !called {
		t.Errorf("re-promote = %+v, %v (raise called: %v)", again, err, called)
	}
	if hist, _ := s.Promotions("prod"); len(hist) != 1 {
		t.Errorf("a no-op re-promote appended: %d lines", len(hist))
	}

	// With no raise func the promotion is just appended.
	if _, err := s.AppendPromotionWithFloors(promotion("prod", "v2"), admit, nil); err != nil {
		t.Errorf("nil raise: %v", err)
	}
}

func TestAppendPromotionWithFloorsRefusals(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	boom := errors.New("below the floor")
	refuse := func([]release.Promotion, []release.Floor, release.Promotion) (*release.Promotion, error) {
		return nil, boom
	}
	if _, err := s.AppendPromotionWithFloors(promotion("prod", "v1"), refuse, nil); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the admit refusal", err)
	}
	if hist, _ := s.Promotions("prod"); len(hist) != 0 {
		t.Errorf("a refused promotion was recorded: %+v", hist)
	}
	if _, err := s.AppendPromotionWithFloors(release.Promotion{}, refuse, nil); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("an invalid promotion = %v, want ErrInvalid", err)
	}
	// A floor raised for another env refuses the whole call.
	admitAll := func([]release.Promotion, []release.Floor, release.Promotion) (*release.Promotion, error) {
		return nil, nil
	}
	_, err := s.AppendPromotionWithFloors(promotion("prod", "v1"), admitAll, func([]release.Floor, release.Promotion) []release.Floor {
		return []release.Floor{orderFloor("staging", "v1", cutAt)}
	})
	if err == nil {
		t.Error("a raised floor of another env must be refused")
	}
}
