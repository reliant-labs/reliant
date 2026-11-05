package cmdutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// ownSlots counts the heavy slots this process currently holds itself
// (HoldHeavySlot took one, not re-entrantly, and has not released it).
var ownSlots atomic.Int32

// HoldHeavySlot takes a host-wide heavy slot (pkg/hostslot) for a forge
// command that does heavy work in its own process — `forge generate`,
// `forge lint` — and returns the release. While every slot is held it waits,
// printing who holds them to stderr.
//
// Only a cancelled context fails the command. A slot directory forge cannot
// create or lock is a broken limiter, not a reason to refuse to lint or
// generate: the command warns once and runs unthrottled.
func HoldHeavySlot(ctx context.Context, stderr io.Writer) (release func(), err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := hostslot.Acquire(ctx, hostslot.Heavy, hostslot.Options{Out: stderr, Env: os.Environ()})
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		fmt.Fprintf(stderr, "⚠️  forge: running without a heavy slot: %v\n", err)
		return func() {}, nil
	}
	own := s.Index() >= 0
	if own {
		ownSlots.Add(1)
	}
	var once atomic.Bool
	return func() {
		if !once.CompareAndSwap(false, true) {
			return
		}
		if own {
			ownSlots.Add(-1)
		}
		_ = s.Release()
	}, nil
}

// HoldsOwnHeavySlot reports whether this process holds a heavy slot of its
// own right now: the host-wide limiter, not the tool, is what bounds how many
// heavy jobs run beside this one. False when the limit is off, when the slot
// was inherited from a parent (the parent's slot may be shared by several
// children), and when acquisition failed and the command runs unthrottled.
//
// A tool with its own machine-global mutex (golangci-lint's lock) can drop it
// while this is true: the slot already bounds the concurrency the mutex was
// protecting, and keeping both serializes every lint on the machine behind
// one lock while each waiter holds a slot.
func HoldsOwnHeavySlot() bool { return ownSlots.Load() > 0 }
