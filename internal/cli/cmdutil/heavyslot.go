package cmdutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
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
//
// While the slot is held, every process the command starts runs inside it:
// the slot's token (hostslot.EnvHeld) is put into this process's environment,
// so a child exec'd with the inherited environment — buf, and the
// `forge protoc-gen-*` plugins buf runs, or anything built on os.Environ or
// goexec.Env — takes no slot of its own. Without it a child that wants a slot
// (a plugin whose pin hand-off must build forge) waits for the one its parent
// holds while the parent waits for it: on a host with one bulk slot, a
// deadlock (`forge generate` hung for 6h in E2E Scaffold, forge#948).
func HoldHeavySlot(ctx context.Context, stderr io.Writer) (release func(), err error) {
	return HoldHeavySlotInLane(ctx, stderr, "")
}

// HoldHeavySlotInLane is HoldHeavySlot for a command that knows which lane
// its work belongs in (hostslot.LaneExpress for one bounded to minutes). An
// explicit FORGE_SLOT_LANE in the environment still wins: the caller's
// declaration is a default, and the person running the command may know
// better. lane "" is FORGE_SLOT_LANE, else bulk.
func HoldHeavySlotInLane(ctx context.Context, stderr io.Writer, lane string) (release func(), err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(os.Getenv(hostslot.EnvLane)) != "" {
		lane = ""
	}
	s, err := hostslot.Acquire(ctx, hostslot.Heavy, hostslot.Options{Out: stderr, Env: os.Environ(), Lane: lane})
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		fmt.Fprintf(stderr, "⚠️  forge: running without a heavy slot: %v\n", err)
		return func() {}, nil
	}
	own := s.Index() >= 0
	unexport := func() {}
	if own {
		ownSlots.Add(1)
		unexport = exportSlotEnv(s.Env())
	}
	var once atomic.Bool
	return func() {
		if !once.CompareAndSwap(false, true) {
			return
		}
		if own {
			ownSlots.Add(-1)
		}
		unexport()
		_ = s.Release()
	}, nil
}

// exportSlotEnv sets the KEY=VALUE entries of a slot's Env in this process's
// environment, and returns what puts the previous values back. An entry some
// later holder has since replaced is left alone: it is that holder's to undo.
func exportSlotEnv(env []string) (restore func()) {
	type prior struct {
		key, val, was string
		had           bool
	}
	var set []prior
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		was, had := os.LookupEnv(key)
		if os.Setenv(key, val) != nil {
			continue
		}
		set = append(set, prior{key: key, val: val, was: was, had: had})
	}
	return func() {
		for _, p := range set {
			if os.Getenv(p.key) != p.val {
				continue
			}
			if p.had {
				_ = os.Setenv(p.key, p.was)
			} else {
				_ = os.Unsetenv(p.key)
			}
		}
	}
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
