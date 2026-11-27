package statuspage

// severity orders the measured states, worst last. No data ranks above
// operational: a component with one silent check is not shown as healthy.
// It ranks below degraded and outage, which are evidence of a problem.
var severity = map[State]int{
	StateOperational: 0,
	StateNoData:      1,
	StateDegraded:    2,
	StateOutage:      3,
}

// Resolve is a component's state from its checks' measured states:
//
//   - override, when set, wins, and overridden is true (a person corrected
//     the probes, for example a single-region false positive; the page says
//     so);
//   - otherwise an active maintenance window gives [StateMaintenance];
//   - otherwise the worst measured state wins. No checks, or a state that is
//     not a measured one, counts as [StateNoData].
func Resolve(measured []State, inMaintenance bool, override State) (state State, overridden bool) {
	if override != "" {
		return override, true
	}
	if inMaintenance {
		return StateMaintenance, false
	}
	if len(measured) == 0 {
		return StateNoData, false
	}
	worst := StateOperational
	for _, s := range measured {
		if _, ok := severity[s]; !ok {
			s = StateNoData
		}
		if severity[s] > severity[worst] {
			worst = s
		}
	}
	return worst, false
}

// Overall is the page's banner: one sentence and the state it is drawn as.
type Overall struct {
	State   State
	Message string
}

// OverallOf is the banner for a set of components: an outage anywhere is a
// partial outage (a major one when every component is out), then degraded,
// then maintenance, then no data; otherwise every system is operational.
func OverallOf(components []Component) Overall {
	count := map[State]int{}
	for _, c := range components {
		count[c.State]++
	}
	switch n := len(components); {
	case n == 0:
		return Overall{StateNoData, "No components"}
	case count[StateOutage] == n:
		return Overall{StateOutage, "Major outage"}
	case count[StateOutage] > 0:
		return Overall{StateOutage, "Partial outage"}
	case count[StateDegraded] > 0:
		return Overall{StateDegraded, "Degraded performance"}
	case count[StateMaintenance] > 0:
		return Overall{StateMaintenance, "Maintenance in progress"}
	case count[StateNoData] == n:
		return Overall{StateNoData, "No data yet"}
	case count[StateNoData] > 0:
		return Overall{StateNoData, "Some systems have no recent data"}
	default:
		return Overall{StateOperational, "All systems operational"}
	}
}
