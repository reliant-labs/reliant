package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// slowPhaseMS is the wall time from which the summary names a phase: long
// enough that a bot budgeting a run, or a person deciding what to --skip,
// wants to know. Every phase's time is in --json (phases[].duration_ms).
const slowPhaseMS = 30_000

// slowPhasesLine names the phases that took slowPhaseMS or longer, slowest
// first, or "" when none did.
func slowPhasesLine(phases []upgradePhase) string {
	var slow []upgradePhase
	for _, p := range phases {
		if p.DurationMS >= slowPhaseMS {
			slow = append(slow, p)
		}
	}
	if len(slow) == 0 {
		return ""
	}
	sort.SliceStable(slow, func(i, j int) bool { return slow[i].DurationMS > slow[j].DurationMS })
	parts := make([]string, len(slow))
	for i, p := range slow {
		parts[i] = fmt.Sprintf("%s %s", p.Name, (time.Duration(p.DurationMS) * time.Millisecond).Round(time.Second))
	}
	return "Slow phases: " + strings.Join(parts, ", ") + " (phases[].duration_ms in --json)"
}
