package obsquery

import (
	"encoding/json"
	"fmt"
	"time"
)

// QueryPanelProcedure is the Connect route of the control plane's
// ObservabilityService.QueryPanel, the typed read an obs:read credential runs a
// dashboard panel with: POST it to the control plane's base URL with
// "Content-Type: application/json", "Connect-Protocol-Version: 1" and
// "Authorization: Bearer <token>".
const QueryPanelProcedure = "/controlplane.v1.ObservabilityService/QueryPanel"

// PanelRef names a panel of a dashboard the control plane itself ships (its
// own deploy/observability/dashboards/, such as the platform's product-usage
// and product-funnel dashboards; not a project's dashboards), by id. The
// server runs the panel it holds, so a caller that reuses a dashboard's
// definition (a weekly snapshot of the dashboard's numbers) never carries a
// copy of its SQL that can drift. The statement still runs in the scope's
// org, as that org's read-only ClickHouse user.
type PanelRef struct {
	// Dashboard is the dashboard's id, e.g. "product-usage".
	Dashboard string
	// Panel is the panel's id within it, e.g. "engagement-headline".
	Panel string
}

// PanelRefQuery is a QueryPanel request that runs a shipped dashboard panel
// by ref over a scope. Like every QueryPanel read it is admitted by the org:
// the credential must belong to OrgID (an obs:read token) or be a member of it.
type PanelRefQuery struct {
	OrgID string
	// ProjectID narrows the scope to one control-plane project. Empty: the
	// whole org.
	ProjectID string
	// EnvIDs and Services narrow the scope; empty means every one.
	EnvIDs   []string
	Services []string
	// From and To are the half-open time range [From, To).
	From, To time.Time
	Ref      PanelRef
	// StepSeconds is the bucket width for time-series panels; 0 lets the
	// server choose.
	StepSeconds int
	// Explain binds the panel's statement without running it.
	Explain bool
}

// The QueryPanel wire shapes, as protojson writes them (lowerCamelCase).
type (
	wireRange struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	wireScope struct {
		OrgID     string    `json:"orgId"`
		TimeRange wireRange `json:"timeRange"`
		ProjectID *string   `json:"projectId,omitempty"`
		EnvIDs    []string  `json:"envIds,omitempty"`
		Services  []string  `json:"services,omitempty"`
	}
	wirePanelRef struct {
		DashboardID string `json:"dashboardId"`
		PanelID     string `json:"panelId"`
	}
	wireRefRequest struct {
		Scope       wireScope    `json:"scope"`
		PanelRef    wirePanelRef `json:"panelRef"`
		StepSeconds int          `json:"stepSeconds,omitempty"`
		Explain     bool         `json:"explain,omitempty"`
	}
	wireRow struct {
		Group               map[string]string `json:"group"`
		Values              []float64         `json:"values"`
		MissingValueIndexes []uint32          `json:"missingValueIndexes"`
	}
	wireStatement struct {
		SQL    string            `json:"sql"`
		Params map[string]string `json:"params"`
	}
	wireResponse struct {
		PanelID     string            `json:"panelId"`
		Kind        string            `json:"kind"`
		Unit        string            `json:"unit"`
		Columns     []string          `json:"columns"`
		Rows        []wireRow         `json:"rows"`
		Series      []json.RawMessage `json:"series"`
		Truncated   bool              `json:"truncated"`
		CompiledSQL []wireStatement   `json:"compiledSql"`
		Panel       *struct {
			SQL string `json:"sql"`
		} `json:"panel"`
	}
)

// Body returns the request's Connect JSON body. It refuses a query the server
// would refuse for want of a field, before any request is sent.
func (q PanelRefQuery) Body() ([]byte, error) {
	switch {
	case q.OrgID == "":
		return nil, invalid("panel ref query: OrgID is required")
	case q.Ref.Dashboard == "" || q.Ref.Panel == "":
		return nil, invalid("panel ref query: Ref needs a dashboard and a panel id")
	case q.From.IsZero() || q.To.IsZero() || !q.From.Before(q.To):
		return nil, invalid("panel ref query: From must be before To")
	case q.StepSeconds < 0:
		return nil, invalid("panel ref query: StepSeconds must not be negative")
	}
	req := wireRefRequest{
		Scope: wireScope{
			OrgID:     q.OrgID,
			TimeRange: wireRange{Start: q.From.UTC().Format(time.RFC3339Nano), End: q.To.UTC().Format(time.RFC3339Nano)},
			EnvIDs:    q.EnvIDs,
			Services:  q.Services,
		},
		PanelRef:    wirePanelRef{DashboardID: q.Ref.Dashboard, PanelID: q.Ref.Panel},
		StepSeconds: q.StepSeconds,
		Explain:     q.Explain,
	}
	if q.ProjectID != "" {
		p := q.ProjectID
		req.Scope.ProjectID = &p
	}
	return json.Marshal(req)
}

// PanelResult is a QueryPanel answer read as rows: a sql panel that is not a
// time series, or a table panel.
type PanelResult struct {
	PanelID string
	Kind    string
	Unit    string
	// Columns are the numeric columns, in result order.
	Columns []string
	Rows    []PanelRow
	// SQL is the statement the server ran: the shipped panel's own, so a
	// caller can record exactly what its numbers came from. Empty for a panel
	// that is not a sql panel.
	SQL string
	// Compiled is the bound statement, with Explain.
	Compiled []CompiledStatement
	// Truncated reports that a cap cut the rows.
	Truncated bool
	// TimeSeries reports that the answer is a time series (its series are not
	// decoded here): a caller reading rows has the wrong panel.
	TimeSeries bool
}

// PanelRow is one result row: the string columns by name, and the numeric
// columns that have a value (a NULL is absent, never zero).
type PanelRow struct {
	Group  map[string]string
	Values map[string]float64
}

// CompiledStatement is one statement as ClickHouse receives it.
type CompiledStatement struct {
	SQL    string
	Params map[string]string
}

// DecodePanelResult reads a QueryPanel response body (HTTP 200).
func DecodePanelResult(body []byte) (PanelResult, error) {
	var w wireResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return PanelResult{}, fmt.Errorf("obsquery: decode QueryPanel response: %w", err)
	}
	out := PanelResult{
		PanelID: w.PanelID, Kind: w.Kind, Unit: w.Unit, Columns: w.Columns,
		Truncated: w.Truncated, TimeSeries: len(w.Series) > 0,
	}
	if w.Panel != nil {
		out.SQL = w.Panel.SQL
	}
	for _, st := range w.CompiledSQL {
		out.Compiled = append(out.Compiled, CompiledStatement(st))
	}
	for _, r := range w.Rows {
		missing := make(map[uint32]bool, len(r.MissingValueIndexes))
		for _, i := range r.MissingValueIndexes {
			missing[i] = true
		}
		row := PanelRow{Group: r.Group, Values: make(map[string]float64, len(r.Values))}
		for i, v := range r.Values {
			if i < len(w.Columns) && !missing[uint32(i)] {
				row.Values[w.Columns[i]] = v
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}
