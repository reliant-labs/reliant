package obsbackend

import (
	"context"
	"regexp"
	"slices"
	"sort"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// ── ops ─────────────────────────────────────────────────────────────────
//
// An op is a named unit of work a project's code runs: an rpc (an
// otelconnect server span, `<proto pkg>.<Service>/<Method>`), another server
// endpoint (an HTTP route), or a component method (the span forge's generated
// decorator opens, `<pkg>.<Method>`, kind internal). `forge obs ops` ranks
// them; release health and blast radius read the same figures for the ops a
// change touched.
//
// Every figure comes from obsquery panels, so the hosted backend runs them
// through the control plane's generic QueryPanel and needs no op-specific
// endpoint of its own.

// Op kinds.
const (
	OpKindRPC       = "rpc"
	OpKindEndpoint  = "endpoint"
	OpKindComponent = "component"
)

// OpsRequest ranks a project's ops.
type OpsRequest struct {
	Narrow
	// Sort is "calls" (default), "p95", "errors" or "rate".
	Sort string
	Top  int
	// Names, when set, restricts the answer to these span names: the ops a
	// release or a diff touched, over any window.
	Names []string
	// Kinds restricts the answer to these OpKind* values; empty is every kind.
	Kinds []string
	// ByVersion splits each op's figures by the service.version of its spans
	// (OpStats.Version): one row per service, op and version. Release health
	// reads a hosted env's after window this way, so a changed op is judged on
	// the judged release's own calls, not on an old version's still in
	// rotation. Unset, each op is one row over every version.
	ByVersion bool
}

// OpStats is the RED summary of one op of one service, and where its code
// is when the telemetry says.
type OpStats struct {
	Service       string
	Name          string
	Kind          string
	Calls         float64
	Errors        float64
	ErrorRatio    float64
	RatePerSecond float64
	P50Ms         float64
	P95Ms         float64
	P99Ms         float64
	// Version is the service.version (a resource attribute) of the spans the
	// figures count, set only for an OpsRequest.ByVersion answer; "" there
	// is the spans that carry none.
	Version string
	// CodeFilePath and CodeFunction are the span's code.file.path and
	// code.function.name: set for component ops whose build emits the
	// attribute contract, empty otherwise.
	CodeFilePath string
	CodeFunction string
}

// OpsResult is the ranked ops.
type OpsResult struct {
	Ops []OpStats
	// Omitted counts ops the Top cap cut.
	Omitted    int
	Statements []Statement
}

// rpcOpPattern is the name otelconnect gives an rpc's server span:
// `<dotted proto package>.<Service>/<Method>`. It is RE2, so the same text
// narrows a panel in ClickHouse.
const rpcOpPattern = `^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+/[A-Za-z_][A-Za-z0-9_]*$`

// componentOpPattern is the name of the span forge's generated decorator
// opens around a component method: `<Go package>.<Method>`. A span that
// carries forge.op is named this by contract. No server span has this shape
// (an rpc's has a slash, an HTTP route's a space), which is what lets a
// requested name be sent to one panel.
const componentOpPattern = `^[a-z][a-z0-9_]*\.[A-Z][A-Za-z0-9_]*$`

var (
	rpcOpRE       = regexp.MustCompile(rpcOpPattern)
	componentOpRE = regexp.MustCompile(componentOpPattern)
)

// OpKindOf classifies a span name by the shape forge's instrumentation gives
// it: component when it is `<pkg>.<Method>`, rpc when it is a Connect
// procedure, and endpoint otherwise.
func OpKindOf(name string) string {
	switch {
	case componentOpRE.MatchString(name):
		return OpKindComponent
	case rpcOpRE.MatchString(name):
		return OpKindRPC
	}
	return OpKindEndpoint
}

// opsSeries are the RED figures of each ops panel. Counts and quantiles are
// weighted for tail sampling by the panel compiler (pkg/obsquery weight.go).
func opsSeries() []obsquery.PanelSeries {
	return []obsquery.PanelSeries{
		{Label: "calls", Agg: "count"},
		{Label: "errors", Agg: "count", Where: []obsquery.PanelPredicate{{Field: "span.status", Op: "eq", Value: "error"}}},
		{Label: "p50", Agg: "quantile", Quantile: 0.5, Field: "span.duration"},
		{Label: "p95", Agg: "quantile", Quantile: 0.95, Field: "span.duration"},
		{Label: "p99", Agg: "quantile", Quantile: 0.99, Field: "span.duration"},
	}
}

// opsVersionField is the service.version a release stamps on its spans'
// resource: what the control plane's version share counts too.
const opsVersionField = "resource.attr.service.version"

// opsGroupBy is the figures panels' grouping: service and span name, then
// the version when byVersion (obsquery.MaxPanelGroupBy fields).
func opsGroupBy(byVersion bool) []string {
	if byVersion {
		return []string{"service.name", "span.name", opsVersionField}
	}
	return []string{"service.name", "span.name"}
}

// opsServerPanel is server spans by service and span name (and version, when
// byVersion): rpcs and endpoints. rpcOnly narrows it to Connect procedures,
// so a request for rpcs alone is not crowded out of the row cap by HTTP
// routes.
func opsServerPanel(names []string, rpcOnly, byVersion bool) obsquery.Panel {
	where := []obsquery.PanelPredicate{{Field: "span.kind", Op: "eq", Value: "server"}}
	if rpcOnly {
		where = append(where, obsquery.PanelPredicate{Field: "span.name", Op: "match", Value: rpcOpPattern})
	}
	if len(names) > 0 {
		where = append(where, obsquery.PanelPredicate{Field: "span.name", Op: "in", Values: names})
	}
	return obsquery.Panel{
		ID: "ops-server", Kind: "table", Signal: "traces", Where: where, Series: opsSeries(),
		GroupBy: opsGroupBy(byVersion), Limit: obsquery.MaxTableRows,
	}
}

// componentWhere selects the decorator's component spans.
func componentWhere(names []string) []obsquery.PanelPredicate {
	where := []obsquery.PanelPredicate{
		{Field: "span.kind", Op: "eq", Value: "internal"},
		{Field: "span.name", Op: "match", Value: componentOpPattern},
	}
	if len(names) > 0 {
		where = append(where, obsquery.PanelPredicate{Field: "span.name", Op: "in", Values: names})
	}
	return where
}

// opsComponentPanel is component spans by service and span name (and
// version, when byVersion).
func opsComponentPanel(names []string, byVersion bool) obsquery.Panel {
	return obsquery.Panel{
		ID: "ops-component", Kind: "table", Signal: "traces", Where: componentWhere(names), Series: opsSeries(),
		GroupBy: opsGroupBy(byVersion), Limit: obsquery.MaxTableRows,
	}
}

// opsCodePanel is where each component op's code is, as its spans report it:
// span name by code.file.path and code.function.name, most spans first. It is
// a panel of its own because a panel groups by at most
// obsquery.MaxPanelGroupBy fields, and grouping the figures by the code
// attributes would split one op in two while some of its spans predate them.
func opsCodePanel(names []string) obsquery.Panel {
	where := append(componentWhere(names), obsquery.PanelPredicate{Field: "span.attr.code.file.path", Op: "exists"})
	return obsquery.Panel{
		ID: "ops-code", Kind: "table", Signal: "traces", Where: where,
		Series:  []obsquery.PanelSeries{{Label: "spans", Agg: "count"}},
		GroupBy: []string{"span.name", "span.attr.code.file.path", "span.attr.code.function.name"},
		Limit:   obsquery.MaxTableRows,
	}
}

// panelRow is one row of a table panel, backend-neutral: the group values in
// GroupBy order, then the series values.
type panelRow struct {
	group  []string
	values []float64
}

// panelRun runs one table panel (or, with Narrow.Explain, only compiles it)
// and returns its rows and statements.
type panelRun func(ctx context.Context, purpose string, p obsquery.Panel) ([]panelRow, []Statement, error)

// opsPlan validates req and says which panels it needs.
type opsPlan struct {
	server, rpcOnly, component  bool
	serverNames, componentNames []string
	kinds                       map[string]bool
}

func planOps(req OpsRequest) (opsPlan, error) {
	switch req.Sort {
	case "", "calls", "p95", "errors", "rate":
	default:
		return opsPlan{}, Wrong("--sort %q: use calls, p95, errors or rate", req.Sort)
	}
	p := opsPlan{kinds: map[string]bool{}}
	for _, k := range req.Kinds {
		switch k {
		case OpKindRPC, OpKindEndpoint, OpKindComponent:
			p.kinds[k] = true
		default:
			return opsPlan{}, Wrong("--kind %q: use rpc, endpoint or component", k)
		}
	}
	if len(p.kinds) == 0 {
		p.kinds = map[string]bool{OpKindRPC: true, OpKindEndpoint: true, OpKindComponent: true}
	}
	p.server = p.kinds[OpKindRPC] || p.kinds[OpKindEndpoint]
	p.rpcOnly = !p.kinds[OpKindEndpoint]
	p.component = p.kinds[OpKindComponent]
	// A name --kind leaves out would be asked nowhere, and its empty answer
	// would read as "no traffic" rather than "not asked".
	for _, n := range req.Names {
		if k := OpKindOf(n); !p.kinds[k] {
			return opsPlan{}, Wrong("--name %q is a %s op, which --kind leaves out", n, k)
		}
	}
	if len(req.Names) > 0 {
		for _, n := range req.Names {
			if componentOpRE.MatchString(n) {
				p.componentNames = append(p.componentNames, n)
			} else {
				p.serverNames = append(p.serverNames, n)
			}
		}
		// A named request asks only the panels its names can be in.
		p.server = p.server && len(p.serverNames) > 0
		p.component = p.component && len(p.componentNames) > 0
	}
	return p, nil
}

// nameChunks splits names into the most an `in` predicate takes; no names is
// one unrestricted chunk.
func nameChunks(names []string) [][]string {
	if len(names) == 0 {
		return [][]string{nil}
	}
	return slices.Collect(slices.Chunk(names, obsquery.MaxPanelValues))
}

// ops runs the panels req needs through run and ranks the answer. Local and
// Cloud differ only in run.
func ops(ctx context.Context, req OpsRequest, run panelRun) (OpsResult, error) {
	plan, err := planOps(req)
	if err != nil {
		return OpsResult{}, err
	}
	var res OpsResult
	secs := req.Range.To.Sub(req.Range.From).Seconds()
	add := func(kind string, r panelRow) {
		if len(r.group) < 2 || len(r.values) < 5 {
			return
		}
		if kind == "" {
			kind = OpKindOf(r.group[1])
		}
		if !plan.kinds[kind] {
			return
		}
		v := func(i int) float64 { return finite(r.values[i]) }
		o := OpStats{Service: r.group[0], Name: r.group[1], Kind: kind, Calls: v(0), Errors: v(1), P50Ms: v(2), P95Ms: v(3), P99Ms: v(4)}
		if req.ByVersion && len(r.group) > 2 {
			o.Version = r.group[2]
		}
		if o.Calls > 0 {
			o.ErrorRatio = o.Errors / o.Calls
		}
		if secs > 0 {
			o.RatePerSecond = o.Calls / secs
		}
		res.Ops = append(res.Ops, o)
	}
	// each runs panel over every chunk of names and hands its rows to f.
	each := func(purpose string, names []string, panel func([]string) obsquery.Panel, f func(panelRow)) error {
		for _, chunk := range nameChunks(names) {
			rows, st, err := run(ctx, purpose, panel(chunk))
			res.Statements = append(res.Statements, st...)
			if err != nil {
				return err
			}
			for _, r := range rows {
				f(r)
			}
		}
		return nil
	}
	if plan.server {
		server := func(n []string) obsquery.Panel { return opsServerPanel(n, plan.rpcOnly, req.ByVersion) }
		if err := each("ops server", plan.serverNames, server, func(r panelRow) { add("", r) }); err != nil {
			return res, err
		}
	}
	if plan.component {
		component := func(n []string) obsquery.Panel { return opsComponentPanel(n, req.ByVersion) }
		if err := each("ops component", plan.componentNames, component, func(r panelRow) { add(OpKindComponent, r) }); err != nil {
			return res, err
		}
		// The code panel's rows come heaviest first, so the first location
		// seen for a name is the one most of its spans report.
		type loc struct{ file, fn string }
		code := map[string]loc{}
		err := each("ops code", plan.componentNames, opsCodePanel, func(r panelRow) {
			if len(r.group) < 3 || r.group[1] == "" {
				return
			}
			if _, seen := code[r.group[0]]; !seen {
				code[r.group[0]] = loc{r.group[1], r.group[2]}
			}
		})
		if err != nil {
			return res, err
		}
		for i, o := range res.Ops {
			if c, ok := code[o.Name]; ok && o.Kind == OpKindComponent {
				res.Ops[i].CodeFilePath, res.Ops[i].CodeFunction = c.file, c.fn
			}
		}
	}
	if req.Explain {
		res.Ops = nil
		return res, nil
	}
	rankOps(&res, req)
	return res, nil
}

// rankOps sorts by req.Sort and applies req.Top.
func rankOps(res *OpsResult, req OpsRequest) {
	key := map[string]func(OpStats) float64{
		"":       func(o OpStats) float64 { return o.Calls },
		"calls":  func(o OpStats) float64 { return o.Calls },
		"rate":   func(o OpStats) float64 { return o.RatePerSecond },
		"p95":    func(o OpStats) float64 { return o.P95Ms },
		"errors": func(o OpStats) float64 { return o.ErrorRatio },
	}[req.Sort]
	sort.SliceStable(res.Ops, func(i, j int) bool { return key(res.Ops[i]) > key(res.Ops[j]) })
	if req.Top > 0 && len(res.Ops) > req.Top {
		res.Omitted = len(res.Ops) - req.Top
		res.Ops = res.Ops[:req.Top]
	}
}

// Ops implements OpsReader over the local ClickStack.
func (l Local) Ops(ctx context.Context, req OpsRequest) (OpsResult, error) {
	return ops(ctx, req, func(ctx context.Context, purpose string, p obsquery.Panel) ([]panelRow, []Statement, error) {
		q := obsquery.PanelQuery{Scope: l.scope(req.Narrow), Panel: p}
		st, shape, err := q.Compile()
		if err != nil {
			return nil, nil, compileErr(err)
		}
		stmts := []Statement{stmt(purpose, st)}
		if req.Explain {
			return nil, stmts, nil
		}
		r, err := l.run(ctx, st, DefaultLimits())
		if err != nil {
			return nil, stmts, err
		}
		var rows []panelRow
		for _, row := range r.Rows {
			if len(row) < 1+shape.Series {
				return nil, stmts, Undetermined("the local ClickStack returned %d columns for %s, want %d", len(row), purpose, 1+shape.Series)
			}
			if len(rows) == shape.Rows {
				break
			}
			labels := AsStrings(row[0])
			if len(labels) < len(p.GroupBy) {
				continue
			}
			pr := panelRow{group: labels}
			for i := range shape.Series {
				pr.values = append(pr.values, AsFloat(row[1+i]))
			}
			rows = append(rows, pr)
		}
		return rows, stmts, nil
	})
}

// Ops implements OpsReader: the ops panels, run by QueryPanel. The control
// plane weights spans for tail sampling, so counts are real calls. A control
// plane that refuses a panel feature answers with its error, never with
// figures made up here.
func (c Cloud) Ops(ctx context.Context, req OpsRequest) (OpsResult, error) {
	return ops(ctx, req, func(ctx context.Context, purpose string, p obsquery.Panel) ([]panelRow, []Statement, error) {
		r, err := c.queryPanel(ctx, req.Narrow, p, 0)
		if err != nil {
			return nil, nil, err
		}
		stmts := compiledStatements(purpose, r)
		if req.Explain {
			return nil, stmts, nil
		}
		var rows []panelRow
		for _, row := range r.Rows {
			if len(row.Values) < len(p.Series) {
				return nil, stmts, Undetermined("the control plane returned %d values per row of %s, want %d", len(row.Values), purpose, len(p.Series))
			}
			pr := panelRow{values: row.Values}
			for _, g := range p.GroupBy {
				pr.group = append(pr.group, row.Group[g])
			}
			rows = append(rows, pr)
		}
		return rows, stmts, nil
	})
}
