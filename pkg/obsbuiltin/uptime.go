package obsbuiltin

import (
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// The Uptime dashboard draws the control plane's outside-in checks (a default
// one per public frontend and HTTP route, and every declared forge.UptimeCheck)
// from the gauges the prober writes, with obsquery's availability and incident
// statements. Its panel ids are a contract with the Reliant console, which
// reads the Health section's Uptime card from them.
const (
	UptimeID = "uptime"

	// UptimeSummaryPanel is one row: "Availability" (a fraction),
	// "Checks", "Checks down", "Checks unknown" and "Downtime" (seconds).
	UptimeSummaryPanel = "uptime-summary"
	// UptimeTimelinePanel is each check's availability per {step} bucket:
	// t, "Check", "Availability"; no row is a bucket with no probe.
	UptimeTimelinePanel = "uptime-timeline"
	// UptimeChecksPanel is one row per check (obsquery.UptimeChecksSQL).
	UptimeChecksPanel = "uptime-checks"
	// UptimeIncidentsPanel is every incident that reaches the range
	// (obsquery.UptimeIncidentsSQL).
	UptimeIncidentsPanel = "uptime-incidents"
)

// Uptime is the Uptime dashboard. Hosted environments only: the prober runs in
// the control plane, and a local stack has no samples.
func Uptime(in Input) dashspec.Dashboard {
	summary := dashspec.Panel{ID: UptimeSummaryPanel, Title: "Availability",
		Description: "The share of the range's probes that passed, over every check, and how many checks are down (an incident is open: " +
			"3 failed probes in a row, until one passes) or unknown (no probe in the last 15 minutes) at the end of the range.",
		Kind: dashspec.KindSQL, SQL: obsquery.UptimeSummarySQL(), Unit: dashspec.UnitPercent,
		Display: &dashspec.Display{Style: dashspec.StyleKPI, Color: dashspec.ColorCyan, Good: dashspec.GoodUp,
			Units: &dashspec.ColumnUnits{"Checks": dashspec.UnitCount, "Checks down": dashspec.UnitCount,
				"Checks unknown": dashspec.UnitCount, "Downtime": dashspec.UnitSeconds}},
		Width: 6, Height: 6}
	timeline := dashspec.Panel{ID: UptimeTimelinePanel, Title: "Checks over time",
		Description: "Each check's share of passed probes in each bucket. An empty cell is a bucket with no probe: unknown, not up.",
		Kind:        dashspec.KindSQL,
		SQL:         obsquery.UptimeTimelineSQL(),
		Unit:        dashspec.UnitPercent,
		Display:     &dashspec.Display{Style: dashspec.StyleHeatmap, Color: dashspec.ColorCyan},
		Width:       18, Height: 6}
	return dashspec.Dashboard{
		ID:    UptimeID,
		Title: in.Project + ": uptime",
		Description: "The platform's outside-in HTTP checks of the project's public frontends and routes, and every forge.UptimeCheck: availability, " +
			"incidents (3 failed probes in a row open one, the next passing probe closes it), probe time and certificate expiry. Hosted environments only.",
		Group:        dashspec.GroupOverview,
		Filters:      envFilter(),
		DefaultRange: "24h",
		Panels: []dashspec.Panel{
			summary,
			timeline,
			table("", UptimeChecksPanel, "Checks",
				"Per check: its status at the end of the range, availability (passed probes over probes), incidents and their downtime in the range, "+
					"the open incident's start, the p50 and p95 probe time, and the days left on its TLS certificate.",
				obsquery.UptimeChecksSQL(), 24, 8),
			table("", UptimeIncidentsPanel, "Incidents",
				"Every incident that reaches the range, newest first: when it started (perhaps before the range), when the next passing probe closed it "+
					"(none while open), its duration, its failed probes and the last error.",
				obsquery.UptimeIncidentsSQL(), 24, 8),
		},
	}
}
