// Copyright (c) 2025 Reliant Labs

// Package machinepool places a run on one member of a machine pool: a named
// set of the owner's machines (those whose durable labels match the pool's
// selector) that a trigger or workflow targets instead of one machine.
//
// Decide is the whole policy, as a pure function over what placement knows
// about each member, so it is tested without a database. Placer gathers those
// inputs and asks for a wake. The design, and why each rule is the way it is:
// dev-docs/design/machine-pools.md.
//
//forge:exclude-contract: a pure placement policy (Decide) plus the Placer that feeds it; every read goes through the Store interface its caller passes, and the only state is the Placer's handles
package machinepool

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Phase is a member's state as placement sees it: the same vocabulary a
// trigger shows for its machine (Trigger.machine_state).
type Phase string

const (
	PhaseConnected    Phase = "connected"
	PhaseStarting     Phase = "starting"
	PhaseSuspended    Phase = "suspended"
	PhaseDisconnected Phase = "disconnected"
	PhaseFailed       Phase = "failed"
	PhaseRemoved      Phase = "removed"
)

// Pool is a pool's definition, as placement needs it.
type Pool struct {
	ID       string
	UserID   string
	Name     string
	Selector map[string]string
	// MaxRunsPerMember bounds the live runs the pool places on one member;
	// 0 is no bound.
	MaxRunsPerMember int
	MinMembers       int
	MaxMembers       int
	MemberSize       string
}

// Member is one of the pool's machines, as placement sees it now.
type Member struct {
	DaemonID string
	Name     string
	Size     string
	Phase    Phase
	// LiveRuns is how many live runs the pool has placed on this member.
	LiveRuns int
	// Installed: the project is installed on this machine.
	Installed bool
	// CatalogDigest is CatalogDigest of the project catalog this machine last
	// synced; "" when it has synced none.
	CatalogDigest   string
	CatalogPushedAt time.Time
}

func (m Member) label() string {
	if m.Name != "" {
		return m.Name
	}
	return m.DaemonID
}

// Kind is what a placement decided.
type Kind int

const (
	// Queue: no member can take the run now; it waits and is placed again.
	Queue Kind = iota
	// Run on Member, which is connected or already starting.
	Run
	// Wake Member (suspended) and run on it once it is up.
	Wake
)

func (k Kind) String() string {
	switch k {
	case Run:
		return "run"
	case Wake:
		return "wake"
	default:
		return "queue"
	}
}

// Skip is a member placement passed over, and why.
type Skip struct {
	DaemonID string
	Name     string
	Reason   string
}

// Decision is a placement.
type Decision struct {
	Kind   Kind
	Member Member
	// ExpectedCatalog is the catalog digest every member must have to take
	// work; "" when no member has synced the project.
	ExpectedCatalog string
	// Skipped lists every member passed over, in member order, with why.
	Skipped []Skip
}

// Reason explains a Queue decision in one line, naming why each member was
// passed over: what a waiting run and its trigger's event show.
func (d Decision) Reason(poolName string) string {
	if d.Kind != Queue {
		return fmt.Sprintf("placed on %s of pool %s", d.Member.label(), poolName)
	}
	if len(d.Skipped) == 0 {
		return fmt.Sprintf("waiting for a machine in pool %s: it has no members (label machines to match its selector)", poolName)
	}
	parts := make([]string, 0, len(d.Skipped))
	for _, s := range d.Skipped {
		name := s.Name
		if name == "" {
			name = s.DaemonID
		}
		parts = append(parts, name+": "+s.Reason)
	}
	return fmt.Sprintf("waiting for a machine in pool %s (%s)", poolName, strings.Join(parts, "; "))
}

// Reasons a member is passed over.
const (
	ReasonNotInstalled   = "project not installed"
	ReasonNotSynced      = "project catalog not synced"
	ReasonCatalogDiffers = "catalog differs from the pool's"
	ReasonFull           = "full"
	ReasonFailed         = "failed to start"
	ReasonRemoved        = "removed"
	ReasonDisconnected   = "disconnected"
)

// Decide places one run on pool's members.
//
// Rules, in order (the design doc's "Placement"):
//
//  1. A member is eligible when the project is installed on it, it has not
//     failed or been removed, and its catalog equals the pool's expected
//     catalog (ExpectedCatalog: majority, not newest).
//  2. A connected eligible member with a free slot, fewest live runs first.
//  3. Else a starting one, same order: the run's preflight waits for it.
//  4. Else a suspended one: Wake.
//  5. Else Queue.
//
// Ties break on name then id, so the same inputs always give the same answer.
func Decide(pool Pool, members []Member) Decision {
	ordered := sortedByName(members)
	expected := ExpectedCatalog(ordered)
	d := Decision{Kind: Queue, ExpectedCatalog: expected}

	var connected, starting, suspended []Member
	for _, m := range ordered {
		if reason := ineligible(m, expected); reason != "" {
			d.Skipped = append(d.Skipped, Skip{DaemonID: m.DaemonID, Name: m.Name, Reason: reason})
			continue
		}
		// A machine that is down says so before whether it is full: "full"
		// would send the owner looking at load rather than at the machine.
		if m.Phase == PhaseDisconnected {
			d.Skipped = append(d.Skipped, Skip{DaemonID: m.DaemonID, Name: m.Name, Reason: ReasonDisconnected})
			continue
		}
		if pool.MaxRunsPerMember > 0 && m.LiveRuns >= pool.MaxRunsPerMember {
			d.Skipped = append(d.Skipped, Skip{DaemonID: m.DaemonID, Name: m.Name,
				Reason: fmt.Sprintf("%s (%d/%d runs)", ReasonFull, m.LiveRuns, pool.MaxRunsPerMember)})
			continue
		}
		switch m.Phase {
		case PhaseConnected:
			connected = append(connected, m)
		case PhaseStarting:
			starting = append(starting, m)
		case PhaseSuspended:
			suspended = append(suspended, m)
		default:
			d.Skipped = append(d.Skipped, Skip{DaemonID: m.DaemonID, Name: m.Name, Reason: ReasonDisconnected})
		}
	}

	switch {
	case len(connected) > 0:
		d.Kind, d.Member = Run, leastLoaded(connected)
	case len(starting) > 0:
		d.Kind, d.Member = Run, leastLoaded(starting)
	case len(suspended) > 0:
		d.Kind, d.Member = Wake, leastLoaded(suspended)
	}
	return d
}

// sortedByName is a copy of members ordered by name, then id: the order every
// tie in placement breaks on.
func sortedByName(members []Member) []Member {
	ordered := append([]Member(nil), members...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Name != ordered[j].Name {
			return ordered[i].Name < ordered[j].Name
		}
		return ordered[i].DaemonID < ordered[j].DaemonID
	})
	return ordered
}

// ineligible is why m cannot take any work regardless of load, or "".
func ineligible(m Member, expected string) string {
	switch m.Phase {
	case PhaseRemoved:
		return ReasonRemoved
	case PhaseFailed:
		return ReasonFailed
	}
	if !m.Installed {
		return ReasonNotInstalled
	}
	if m.CatalogDigest == "" {
		return ReasonNotSynced
	}
	if m.CatalogDigest != expected {
		return ReasonCatalogDiffers
	}
	return ""
}

// leastLoaded is the member with the fewest live runs; members arrive in name
// order, so a tie keeps the first.
func leastLoaded(ms []Member) Member {
	best := ms[0]
	for _, m := range ms[1:] {
		if m.LiveRuns < best.LiveRuns {
			best = m
		}
	}
	return best
}

// ExpectedCatalog is the catalog digest a member must have to take work: the
// one held by the most members that could run work (any phase but failed and
// removed, installed and synced), a tie going to the most recently pushed.
//
// Majority, not newest: a machine that slept through merges pushes its stale
// catalog the moment it reconnects, which makes it the newest; newest-wins
// would let that one machine evict every current member.
func ExpectedCatalog(members []Member) string {
	type tally struct {
		count  int
		newest time.Time
	}
	tallies := map[string]*tally{}
	for _, m := range members {
		if m.Phase == PhaseFailed || m.Phase == PhaseRemoved || !m.Installed || m.CatalogDigest == "" {
			continue
		}
		t := tallies[m.CatalogDigest]
		if t == nil {
			t = &tally{}
			tallies[m.CatalogDigest] = t
		}
		t.count++
		if m.CatalogPushedAt.After(t.newest) {
			t.newest = m.CatalogPushedAt
		}
	}
	best, bestTally := "", (*tally)(nil)
	for digest, t := range tallies {
		switch {
		case bestTally == nil,
			t.count > bestTally.count,
			t.count == bestTally.count && t.newest.After(bestTally.newest),
			t.count == bestTally.count && t.newest.Equal(bestTally.newest) && digest < best:
			best, bestTally = digest, t
		}
	}
	return best
}

// Matches reports whether a machine's labels contain every selector entry.
// An empty selector matches nothing: a pool must select its members.
func Matches(labels, selector map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// DefaultSelector is the selector a pool declared without one gets.
func DefaultSelector(name string) map[string]string {
	return map[string]string{"pool": name}
}
