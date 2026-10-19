// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"context"
	"math"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

// unknownWindowStaleAfter is how long a window with neither a known reset nor a
// known length stays believable.
const unknownWindowStaleAfter = 10 * time.Minute

// Effective is a stored window as it applies at now: the one function the
// router and the UI use to read a window.
//
// A window whose reset has passed is a NEW window (used 0, reset unknown). A
// window observed longer ago than its own length says nothing (known false).
// An unknown reset and length goes stale after ten minutes. When known is
// false, used is 0.
func Effective(w core.LimitWindow, observedAt, now time.Time) (used float64, known bool) {
	if !w.ResetsAt.IsZero() && !w.ResetsAt.After(now) {
		return 0, true
	}
	length := time.Duration(w.LengthS) * time.Second
	age := now.Sub(observedAt)
	switch {
	case observedAt.IsZero():
		return 0, false
	case length > 0 && age > length:
		return 0, false
	case length == 0 && w.ResetsAt.IsZero() && age > unknownWindowStaleAfter:
		return 0, false
	}
	switch {
	case w.UsedFraction != nil:
		used = *w.UsedFraction
	case w.Limit != nil && w.Remaining != nil && *w.Limit > 0:
		used = 1 - float64(*w.Remaining)/float64(*w.Limit)
	default:
		return 0, false
	}
	return min(max(used, 0), 1), true
}

// LimitStateFromSnapshot is what a ratelimit.Snapshot persists as. A window's
// used fraction is derived from its limit and remaining counts when the
// provider sent no utilization, so a reader never has to.
func LimitStateFromSnapshot(userID, accountID, source string, s ratelimit.Snapshot, now time.Time) core.LimitState {
	observed := s.ObservedAt
	if observed.IsZero() {
		observed = now
	}
	st := core.LimitState{
		AccountID:            accountID,
		UserID:               userID,
		Status:               string(s.Status),
		RepresentativeWindow: s.RepresentativeWindow,
		Source:               source,
		ObservedAt:           observed.UTC(),
	}
	if st.Status == "" {
		st.Status = string(ratelimit.StatusUnknown)
	}
	for _, w := range s.Windows {
		lw := core.LimitWindow{
			Name:         w.Name,
			UsedFraction: w.UsedFraction,
			Limit:        w.Limit,
			Remaining:    w.Remaining,
			ResetsAt:     w.ResetsAt.UTC(),
			LengthS:      int64(w.Length / time.Second),
		}
		if lw.UsedFraction == nil {
			if f, ok := w.Used(); ok {
				lw.UsedFraction = &f
			}
		}
		st.Windows = append(st.Windows, lw)
	}
	return st
}

// exhaustion is what a limit state says about an exhausted window.
type exhaustion struct {
	// Exhausted is true when an unscoped window is spent (or the provider said
	// it rejected the request and named the window that binds).
	Exhausted bool
	// Until is when the account serves again; zero when no reset is known.
	Until time.Time
	// Window names the window that benches the account, in CanonicalWindow form.
	Window string
}

// exhausted reads whether the account should be benched from a state. Only an
// unscoped window being spent counts: Opus' weekly cap does not stop Sonnet. A
// "rejected" status with no usable window falls back to the representative
// window's reset.
func exhausted(windows []core.LimitWindow, representative, status string, now time.Time) exhaustion {
	var out exhaustion
	for _, w := range windows {
		if isScoped(w.Name) {
			continue
		}
		used, ok := windowUsed(w)
		if !ok || used < 1 || !w.ResetsAt.IsZero() && !w.ResetsAt.After(now) {
			continue
		}
		out.Exhausted = true
		if w.ResetsAt.After(out.Until) {
			out.Until, out.Window = w.ResetsAt, CanonicalWindow(w.Name)
		} else if out.Window == "" {
			out.Window = CanonicalWindow(w.Name)
		}
	}
	if out.Exhausted || status != string(ratelimit.StatusRejected) {
		return out
	}
	for _, w := range windows {
		if w.Name == representative && !isScoped(w.Name) && w.ResetsAt.After(now) {
			return exhaustion{Exhausted: true, Until: w.ResetsAt, Window: CanonicalWindow(w.Name)}
		}
	}
	return out
}

func windowUsed(w core.LimitWindow) (float64, bool) {
	switch {
	case w.UsedFraction != nil:
		return *w.UsedFraction, true
	case w.Limit != nil && w.Remaining != nil && *w.Limit > 0:
		return 1 - float64(*w.Remaining)/float64(*w.Limit), true
	}
	return 0, false
}

// benchStore is the slice of the account store a bench needs.
type benchStore interface {
	BenchAccount(ctx context.Context, userID, accountID string, until time.Time, window string, probe bool) error
	GetAccountLimits(ctx context.Context, accountID string) (*core.LimitState, error)
}

// bench refuses the account until `until`; a zero until means the provider did
// not say, and the account is benched for the next rung of the probe ladder. It
// returns the bench now in force, which can be later than asked when the
// account was already benched past it. A probe never escalates while the
// previous bench is still running, so concurrent refusals cost one rung.
func bench(ctx context.Context, st benchStore, now time.Time, userID, accountID string, until time.Time, window string) (time.Time, error) {
	cur, err := st.GetAccountLimits(ctx, accountID)
	if err != nil {
		return time.Time{}, err
	}
	var existing time.Time
	probes := 0
	if cur != nil {
		probes = cur.LimitedProbes
		if cur.LimitedUntil != nil {
			existing = *cur.LimitedUntil
		}
	}
	probe := false
	if until.IsZero() {
		if existing.After(now) {
			return existing, nil
		}
		until, probe = now.Add(ProbeDelay(probes)), true
	}
	if err := st.BenchAccount(ctx, userID, accountID, until, window, probe); err != nil {
		return time.Time{}, err
	}
	if existing.After(until) {
		return existing, nil
	}
	return until, nil
}

// materiallyDiffers reports whether next differs from prev enough to be
// written before the heartbeat: a new status, a window that appeared, vanished
// or started over, or a used fraction that moved by usedMovedBy.
func materiallyDiffers(prev, next core.LimitState) bool {
	if prev.Status != next.Status || prev.RepresentativeWindow != next.RepresentativeWindow || len(prev.Windows) != len(next.Windows) {
		return true
	}
	byName := make(map[string]core.LimitWindow, len(prev.Windows))
	for _, w := range prev.Windows {
		byName[w.Name] = w
	}
	for _, w := range next.Windows {
		p, ok := byName[w.Name]
		if !ok || resetMoved(p.ResetsAt, w.ResetsAt) {
			return true
		}
		pu, pok := windowUsed(p)
		nu, nok := windowUsed(w)
		if pok != nok || math.Abs(pu-nu) >= usedMovedBy {
			return true
		}
	}
	return false
}

// resetTolerance is how far a window's reset time may drift between two
// observations before it counts as a new window. Providers that state a reset as
// a remaining duration (OpenAI's "6m0s") yield a slightly different absolute time
// on every response; a window that really started over moves by its length.
const resetTolerance = 2 * time.Minute

func resetMoved(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() != b.IsZero()
	}
	d := a.Sub(b)
	return d > resetTolerance || d < -resetTolerance
}
