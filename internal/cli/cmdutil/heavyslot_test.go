package cmdutil

import (
	"context"
	"io"
	"testing"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// HoldHeavySlotInLane waits in the lane the command names, unless
// FORGE_SLOT_LANE is set: the person running it decides last.
func TestHoldHeavySlotInLane(t *testing.T) {
	cases := []struct {
		name, lane, env, want string
	}{
		{"command's lane", hostslot.LaneExpress, "", hostslot.LaneExpress},
		{"no lane is bulk", "", "", hostslot.LaneBulk},
		{"FORGE_SLOT_LANE wins over the command", hostslot.LaneExpress, hostslot.LaneBulk, hostslot.LaneBulk},
		{"FORGE_SLOT_LANE alone", "", hostslot.LaneExpress, hostslot.LaneExpress},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(hostslot.EnvDir, dir)
			t.Setenv(hostslot.EnvHeld, "")
			t.Setenv(hostslot.SlotsEnv(hostslot.Heavy), "4")
			t.Setenv(hostslot.EnvLane, tc.env)
			release, err := HoldHeavySlotInLane(context.Background(), io.Discard, tc.lane)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			rep, err := hostslot.Status(hostslot.Heavy, hostslot.Options{Env: []string{hostslot.EnvDir + "=" + dir, hostslot.SlotsEnv(hostslot.Heavy) + "=4"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Holders) != 1 || rep.Holders[0].Lane != tc.want {
				t.Fatalf("holders = %+v, want one in the %s lane", rep.Holders, tc.want)
			}
		})
	}
}

// HoldsOwnHeavySlot is true exactly while HoldHeavySlot holds a slot it took
// itself: not with the limit off, and not once released (twice is harmless).
func TestHoldsOwnHeavySlot(t *testing.T) {
	t.Setenv(hostslot.EnvDir, t.TempDir())
	t.Setenv(hostslot.EnvHeld, "")

	t.Run("own slot", func(t *testing.T) {
		t.Setenv(hostslot.SlotsEnv(hostslot.Heavy), "2")
		if HoldsOwnHeavySlot() {
			t.Fatal("true before any slot was taken")
		}
		release, err := HoldHeavySlot(context.Background(), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if !HoldsOwnHeavySlot() {
			t.Fatal("false while holding a slot of its own")
		}
		release()
		release()
		if HoldsOwnHeavySlot() {
			t.Fatal("true after the slot was released")
		}
	})
	t.Run("limit off", func(t *testing.T) {
		t.Setenv(hostslot.SlotsEnv(hostslot.Heavy), "off")
		release, err := HoldHeavySlot(context.Background(), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if HoldsOwnHeavySlot() {
			t.Fatal("true with the limit off: nothing bounds this run")
		}
	})
}
