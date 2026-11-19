package hostslot

import (
	"fmt"
	mrand "math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// Lanes. A slot class's N slots are shared by two lanes:
//
//   - bulk, the default: anything heavy, however long it runs — a whole-repo
//     lint, a full test lane, a gate step.
//   - express: a command its caller declares bounded to minutes — the tests
//     or lint of named packages. Express waiters queue apart from bulk
//     waiters and go first, and the top R slot indices are reserved for them,
//     so a one-minute command never waits behind hour-long bulk holders.
//
// Total concurrency is still N: the lanes divide the machine's budget, they
// do not raise it. The lane is a declaration, not something Acquire measures;
// R bounds how many slots bulk work can lose to it, but an express holder may
// take a bulk slot too, so a caller that runs long work express takes slots
// from bulk work ahead of its turn. `forge slot status` flags an express
// holder older than ExpressOverdue.
const (
	LaneBulk    = "bulk"
	LaneExpress = "express"
)

// EnvLane selects the lane when Options.Lane is empty: FORGE_SLOT_LANE=express
// puts an acquisition in the express lane. Any other value, or none, is bulk —
// a stray value never fails a command. A wrapper that knows a command is
// scoped (one package's tests, one package's lint) sets it in that command's
// environment; a forge that predates lanes ignores it.
const EnvLane = "FORGE_SLOT_LANE"

// ExpressOverdue is the age past which `forge slot status` flags an express
// holder: the lane is for commands bounded to minutes.
const ExpressOverdue = 15 * time.Minute

// expressQueueDir is the express lane's ticket directory, beside the bulk
// lane's "queue" (the name every forge before lanes uses, so old and new
// bulk waiters share one FIFO).
const expressQueueDir = "queue-express"

func normLane(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// lane resolves which lane an acquisition waits in: Options.Lane when set
// (and then it must name a lane), else FORGE_SLOT_LANE from Options.Env, else
// bulk.
func (o Options) lane() (string, error) {
	if o.Lane != "" {
		switch l := normLane(o.Lane); l {
		case LaneBulk, LaneExpress:
			return l, nil
		}
		return "", fmt.Errorf("slot lane %q: want %q or %q", o.Lane, LaneBulk, LaneExpress)
	}
	if normLane(lookupEnv(o.Env, EnvLane)) == LaneExpress {
		return LaneExpress, nil
	}
	return LaneBulk, nil
}

// ExpressSlotsEnv is the variable that sets how many of a class's slots are
// reserved for the express lane: FORGE_HEAVY_EXPRESS_SLOTS for "heavy". A
// non-negative integer (clamped to N-1, so bulk work always keeps a slot);
// 0 or "off" reserves none, leaving express only its queue priority.
func ExpressSlotsEnv(class string) string {
	return "FORGE_" + strings.ToUpper(strings.ReplaceAll(class, "-", "_")) + "_EXPRESS_SLOTS"
}

// DefaultExpressSlots is how many of n slots are reserved for the express
// lane by default: a quarter, at least one — and none when n is 1, where
// reserving the only slot would leave bulk work nothing.
func DefaultExpressSlots(n int) int {
	if n < 2 {
		return 0
	}
	return max(1, n/4)
}

// expressFor resolves the reserved count for a class of n slots.
func expressFor(class string, n int, env []string) (int, string, error) {
	name := ExpressSlotsEnv(class)
	v := strings.TrimSpace(lookupEnv(env, name))
	var r int
	var src string
	switch strings.ToLower(v) {
	case "":
		if n < 2 {
			return 0, "default: none of 1 slot", nil
		}
		return DefaultExpressSlots(n), fmt.Sprintf("default max(1, %d/4)", n), nil
	case "off", "none":
		r, src = 0, name+"="+v
	default:
		var err error
		r, err = strconv.Atoi(v)
		if err != nil || r < 0 {
			return 0, "", fmt.Errorf("%s=%q: want a number of slots (0 or more), or off", name, v)
		}
		src = name + "=" + v
	}
	if r > n-1 {
		r = max(0, n-1)
		src += fmt.Sprintf(", clamped to %d so bulk work keeps a slot", r)
	}
	return r, src, nil
}

// bulkSlots is how many slot indices bulk holders may lock: [0, bulkSlots).
func (c Capacity) bulkSlots() int { return c.Slots - c.Express }

// laneOrder is the slot indices a holder of lane may lock, in the order to
// try them. Each range starts at a random index so concurrent waiters do not
// all contend on its first slot. Express tries the reserved indices first,
// leaving the bulk ones for bulk work, then falls back to those.
func (c Capacity) laneOrder(lane string) []int {
	order := make([]int, 0, c.Slots)
	rotate := func(lo, hi int) {
		n := hi - lo
		if n <= 0 {
			return
		}
		off := mrand.IntN(n)
		for k := 0; k < n; k++ {
			order = append(order, lo+(off+k)%n)
		}
	}
	b := c.bulkSlots()
	if lane == LaneExpress {
		rotate(b, c.Slots)
	}
	rotate(0, b)
	return order
}
