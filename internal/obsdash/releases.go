package obsdash

// ReleasesID is the id of the dashboard BuildReleases returns, so its file is
// "<project>-releases_gen.json".
const ReleasesID = "releases"

// fieldVersion is the service.version every workload reports, rendered by KCL
// from the release the workload was deployed from (OTEL_RESOURCE_ATTRIBUTES).
const fieldVersion = "resource.attr.service.version"

// BuildReleases is the "Releases" dashboard: how each release behaved, side by
// side. Every panel splits one signal by service.version, so the version that
// replaced another is a new line next to it and a regression is the line that
// departs at the cut-over. The Reliant console draws the deploy markers on
// these charts (and on every other time-series chart) from the dashboard's
// releases annotation, which is declared on all of them by Build.
//
// The comparison table is the before/after view: one row per version over the
// selected time range, so choosing a range that spans a deploy puts the old and
// the new version in adjacent rows.
func BuildReleases(project string) Dashboard {
	d := Dashboard{
		ID:    ReleasesID,
		Title: project + ": releases",
		Group: GroupReleases,
		Description: "Request rate, error rate and latency split by service.version, so a deploy shows as one version replacing another. " +
			"Deploy markers come from the release ledger. Narrow with the service.name and deployment.environment.name filters.",
		Filters: commonFilters(),
	}
	byVersion := []string{fieldVersion}
	server := []Predicate{{Field: "span.kind", Op: OpEq, Value: "server"}}
	d.Panels = []Panel{
		{ID: "request-rate-by-version", Title: "Requests by version", Kind: KindLine, Signal: Traces, Where: server,
			Series: []Series{{Label: "requests", Agg: AggCount}}, GroupBy: byVersion, Unit: UnitCount, Width: 12, Height: 7},
		{ID: "error-rate-by-version", Title: "Error rate by version",
			Description: "Server faults (span status Error) over all requests, per version.",
			Kind:        KindLine, Signal: Traces, Where: server, Ratio: true,
			Series:  []Series{{Label: "errors", Agg: AggCount, Where: []Predicate{errorSpans}}, {Label: "requests", Agg: AggCount}},
			GroupBy: byVersion, Unit: UnitPercent, Width: 12, Height: 7},
		{ID: "p95-by-version", Title: "p95 latency by version", Kind: KindLine, Signal: Traces, Where: server,
			Series:  []Series{{Label: "p95", Agg: AggQuantile, Quantile: 0.95, Field: "span.duration"}},
			GroupBy: byVersion, Unit: UnitMillis, Width: 12, Height: 7},
		{ID: "p99-by-version", Title: "p99 latency by version", Kind: KindLine, Signal: Traces, Where: server,
			Series:  []Series{{Label: "p99", Agg: AggQuantile, Quantile: 0.99, Field: "span.duration"}},
			GroupBy: byVersion, Unit: UnitMillis, Width: 12, Height: 7},
		{ID: "version-comparison", Title: "Version comparison",
			Description: "Requests, errors and latency per service.version over the selected range. Pick a range that spans a deploy to read old against new.",
			Kind:        KindTable, Signal: Traces, Where: server,
			Series:  append([]Series{{Label: "requests", Agg: AggCount}, {Label: "errors", Agg: AggCount, Where: []Predicate{errorSpans}}}, latencySeries()...),
			GroupBy: byVersion, Width: 24, Height: 8},
		{ID: "error-logs-by-version", Title: "Error logs by version", Kind: KindLine, Signal: Logs,
			Where:  []Predicate{errorLogs},
			Series: []Series{{Label: "errors", Agg: AggCount}}, GroupBy: byVersion, Unit: UnitCount, Width: 24, Height: 7},
	}
	return d
}
