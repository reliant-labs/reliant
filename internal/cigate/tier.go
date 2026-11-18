package cigate

import (
	"fmt"
	"slices"
	"strings"
)

// Tier is the gate as one CI event runs it: which of the built-in lanes run,
// and whether the unit suite runs whole or narrowed to the packages a branch
// reaches. The tiers mirror what the scaffolded GitHub workflow (ci.yml) runs
// per event, so a check run driven by any other system (a Reliant workflow on
// the same GitHub events) gives the verdict ci.yml would have:
//
//	pull_request  → pr     checks, test (affected), integration
//	merge_group   → queue  what pr runs, judged against the group's base
//	push (main)   → main   checks, test (whole suite); integration ran on the PR
//	schedule,
//	workflow_dispatch → full  every lane, whole suites: the release or nightly gate
//
// A tier only governs the built-in lanes. A lane a project's extra checks
// declare runs when it is named, under any tier.
type Tier struct {
	// Name is what --tier takes and what the JSON document reports.
	Name string
	// Lanes are the built-in lanes the tier runs; a requested built-in lane
	// outside them is reported DEFERRED.
	Lanes []Lane
	// FullSuite runs the whole unit suite, never affected mode.
	FullSuite bool
	// Events are the GitHub event names the tier answers (--event).
	Events []string
	// About is one line for --help and the skill.
	About string
}

// Tier names.
const (
	TierPR    = "pr"
	TierQueue = "queue"
	TierMain  = "main"
	TierFull  = "full"
)

var builtinLanes = []Lane{LaneChecks, LaneTest, LaneIntegration}

var tiers = []Tier{
	{
		Name: TierPR, Lanes: builtinLanes, Events: []string{"pull_request", "pull_request_target"},
		About: "a pull request: every lane, unit tests narrowed to the packages the branch reaches",
	},
	{
		Name: TierQueue, Lanes: builtinLanes, Events: []string{"merge_group"},
		About: "a merge-queue group: what pr runs, narrowed against the group's base (pass --base)",
	},
	{
		Name: TierMain, Lanes: []Lane{LaneChecks, LaneTest}, FullSuite: true, Events: []string{"push"},
		About: "a push to the default branch: checks and the whole unit suite; integration is deferred to pr, queue and full",
	},
	{
		Name: TierFull, Lanes: builtinLanes, FullSuite: true, Events: []string{"schedule", "workflow_dispatch"},
		About: "the release or nightly gate: every lane, whole suites",
	},
}

// tierAliases are other spellings --tier accepts. The document always reports
// the canonical name.
var tierAliases = map[string]string{"merge": TierQueue}

// Tiers is every tier, in the order an event's change reaches main.
func Tiers() []Tier {
	out := make([]Tier, len(tiers))
	copy(out, tiers)
	return out
}

// ParseTier resolves a --tier value.
func ParseTier(name string) (Tier, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	if alias, ok := tierAliases[n]; ok {
		n = alias
	}
	for _, t := range tiers {
		if t.Name == n {
			return t, nil
		}
	}
	return Tier{}, fmt.Errorf("unknown tier %q: use one of %s", name, strings.Join(tierNames(), ", "))
}

// TierForEvent is the tier a GitHub event runs. It takes the event name as
// GitHub Actions spells it (`github.event_name`: pull_request, merge_group,
// push) or as a Reliant trigger does, with the action after a dot
// (pull_request.synchronize); the action does not change the tier.
func TierForEvent(event string) (Tier, error) {
	e := strings.ToLower(strings.TrimSpace(event))
	if i := strings.IndexByte(e, '.'); i >= 0 {
		e = e[:i]
	}
	for _, t := range tiers {
		if slices.Contains(t.Events, e) {
			return t, nil
		}
	}
	var known []string
	for _, t := range tiers {
		known = append(known, t.Events...)
	}
	return Tier{}, fmt.Errorf("no tier for event %q: known events are %s (or name the tier with --tier)", event, strings.Join(known, ", "))
}

func tierNames() []string {
	names := make([]string, 0, len(tiers))
	for _, t := range tiers {
		names = append(names, t.Name)
	}
	return names
}

// Runs reports whether the tier runs lane. A lane that is not built in (one an
// extra check declares) runs whenever it was asked for.
func (t Tier) Runs(l Lane) bool {
	return !slices.Contains(builtinLanes, l) || slices.Contains(t.Lanes, l)
}

// Defer marks the checks of every built-in lane the tier does not run as
// DEFERRED, naming the tiers that do run it. It returns a copy.
func (t Tier) Defer(checks []Check) []Check {
	out := append([]Check(nil), checks...)
	for i := range out {
		c := &out[i]
		if t.Runs(c.Lane) {
			continue
		}
		var by []string
		for _, o := range tiers {
			if slices.Contains(o.Lanes, c.Lane) {
				by = append(by, o.Name)
			}
		}
		c.Deferred = fmt.Sprintf("the %s tier does not run the %s lane; %s do", t.Name, c.Lane, strings.Join(by, ", "))
	}
	return out
}
