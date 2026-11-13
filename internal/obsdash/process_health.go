package obsdash

import "sort"

// An empty process inventory matches nothing, not every tenant service.
// Eq on an impossible service name is valid spec even with no deployments.
func identityScope(field string, values []string) []Predicate {
	if len(values) == 0 {
		return []Predicate{{Field: field, Op: OpEq, Value: "__forge_no_deployed_process__"}}
	}
	return []Predicate{{Field: field, Op: OpIn, Values: values}}
}

func sortedIdentities(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, v := range values {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// rpcHealthPanels reads counters/histograms, not sampled server spans. Error
// classification is exactly the observe interceptor's error_class attribute.
func rpcHealthPanels() []Panel {
	calls := func(agg Agg) Series {
		return Series{Label: "requests", Agg: agg, Field: "rpc.server.calls", MetricType: "sum"}
	}
	errors := func(agg Agg) Series {
		return Series{Label: "server faults", Agg: agg, Field: "rpc.server.errors", MetricType: "sum", Where: []Predicate{{Field: "metric.attr.error_class", Op: OpEq, Value: "server"}}}
	}
	duration := func(q float64) Series {
		return Series{Label: "p95", Agg: AggQuantile, Quantile: q, Field: "rpc.server.duration", MetricType: "histogram"}
	}
	return []Panel{
		{ID: "requests", Title: "Requests", Kind: KindNumber, Signal: Metrics, Series: []Series{calls(AggIncrease)}, Unit: UnitCount, Width: 8, Height: 4},
		{ID: "errors", Title: "Server faults", Kind: KindNumber, Signal: Metrics, Series: []Series{errors(AggIncrease)}, Unit: UnitCount, Width: 8, Height: 4},
		{ID: "p95", Title: "p95 RPC duration", Kind: KindNumber, Signal: Metrics, Series: []Series{duration(.95)}, Unit: UnitSeconds, Width: 8, Height: 4},
		{ID: "request-rate", Title: "Request rate by process", Kind: KindLine, Signal: Metrics, Series: []Series{calls(AggRate)}, Unit: UnitCount, Width: 8, Height: 7},
		{ID: "error-rate", Title: "Server fault rate by process", Kind: KindLine, Signal: Metrics, Series: []Series{errors(AggRate), calls(AggRate)}, Ratio: true, Unit: UnitPercent, Width: 8, Height: 7},
		{ID: "latency", Title: "p95 duration by process", Kind: KindLine, Signal: Metrics, Series: []Series{duration(.95)}, Unit: UnitSeconds, Width: 8, Height: 7},
	}
}

// splitServiceFilter keeps the dashboard's service.name filter off the panels
// it would blank. Node-derived panels (saturation, restarts) are keyed by
// WORKLOAD: the node agent stamps their service.name with the workload name, so
// a selection of the application's own service.name (OTEL_SERVICE_NAME) finds
// nothing in them, and a selection of the workload name finds nothing in the
// application's panels. When the two sets of names differ, the service filter
// is limited to the other panels (Filter.PanelIDs, the spec's mechanism for a
// filter that must not blank a panel) and a workload filter takes the node
// panels, so each panel is selected by the identity its data carries and none
// receives a value it cannot match.
//
// When every service.name is also a workload name there is no split, and the
// filter stays dashboard-wide.
func splitServiceFilter(d *Dashboard, nodePanels, serviceNames, workloads []string) {
	if len(nodePanels) == 0 || sameNames(serviceNames, workloads) {
		return
	}
	node := map[string]bool{}
	for _, id := range nodePanels {
		node[id] = true
	}
	var others []string
	for _, p := range d.Panels {
		if !node[p.ID] {
			others = append(others, p.ID)
		}
	}
	var filters []Filter
	for _, f := range d.Filters {
		if f.Field == FieldService && len(f.PanelIDs) == 0 {
			if len(others) == 0 {
				continue
			}
			f.PanelIDs = others
		}
		filters = append(filters, f)
	}
	filters = append(filters, Filter{ID: "workload", Label: "k8s.workload.name", Field: fieldWorkload, Signal: Metrics, PanelIDs: nodePanels})
	d.Filters = filters
}

func sameNames(a, b []string) bool {
	x, y := sortedIdentities(append([]string(nil), a...)), sortedIdentities(append([]string(nil), b...))
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
