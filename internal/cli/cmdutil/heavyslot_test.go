package cmdutil

import (
	"context"
	"io"
	"testing"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

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
