package obsbackend

// What `forge obs dashboards audit` replays: the dashboards the console lists
// (DashboardService), and one panel run exactly as the console runs it
// (ObservabilityService.QueryPanel with stepSeconds 0, so the control plane
// picks the bucket). Nothing here compiles panel SQL: the answer is the
// control plane's, read back as counts the audit can reason about.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const (
	procListDashboards = "controlplane.v1.DashboardService/ListDashboards"
	procGetDashboard   = "controlplane.v1.DashboardService/GetDashboard"
)

// DashboardRef addresses one dashboard. Source is the wire enum name
// (DASHBOARD_SOURCE_GENERATED), kept verbatim so a ref read from the index
// is sent back unchanged.
type DashboardRef struct {
	Source    string
	ProjectID string
	Key       string
}

// DashboardInfo is one row of the console's dashboard index.
type DashboardInfo struct {
	Ref DashboardRef
	// Title is the dashboard's title.
	Title string
	// Source is "generated", "repo", "workspace", "template" or "builtin".
	Source     string
	PanelCount int
	// ReleaseVersion is the release that shipped a release dashboard.
	ReleaseVersion string
	// Typed marks a server-owned view (organization costs) that has no panel
	// spec to run.
	Typed bool
}

// DashboardDocument is one dashboard's spec as the control plane stores it:
// a forge.dashboards/v1 dashboard object.
type DashboardDocument struct {
	Info    DashboardInfo
	Spec    json.RawMessage
	Version int
}

type wireDashboardSummary struct {
	Ref struct {
		Source    string `json:"source"`
		ProjectID string `json:"projectId"`
		Key       string `json:"key"`
	} `json:"ref"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	PanelCount int    `json:"panelCount"`
	ViewKind   string `json:"viewKind"`
	Release    *struct {
		ReleaseVersion string `json:"releaseVersion"`
	} `json:"release"`
}

var dashboardSources = map[string]string{
	"DASHBOARD_SOURCE_GENERATED": "generated", "DASHBOARD_SOURCE_REPO": "repo",
	"DASHBOARD_SOURCE_WORKSPACE": "workspace", "DASHBOARD_SOURCE_TEMPLATE": "template",
	"DASHBOARD_SOURCE_BUILTIN": "builtin",
}

func (w wireDashboardSummary) info() DashboardInfo {
	src := w.Source
	if src == "" {
		src = w.Ref.Source
	}
	di := DashboardInfo{
		Ref:        DashboardRef{Source: w.Ref.Source, ProjectID: w.Ref.ProjectID, Key: w.Ref.Key},
		Title:      w.Title,
		Source:     dashboardSources[src],
		PanelCount: w.PanelCount,
		Typed:      w.ViewKind != "" && w.ViewKind != "DASHBOARD_VIEW_KIND_UNSPECIFIED",
	}
	if w.Release != nil {
		di.ReleaseVersion = w.Release.ReleaseVersion
	}
	return di
}

// ListDashboards is the index the console shows for this env: the release
// dashboards its current release carries, and the workspace dashboards scoped
// to it or to every env. project narrows to one forge project; "" asks for the
// whole organization (the console does the same when it knows no project).
func (c Cloud) ListDashboards(ctx context.Context, project string) ([]DashboardInfo, error) {
	in := map[string]any{"orgId": c.OrgID}
	if project != "" {
		in["projectId"] = project
	}
	if c.EnvID != "" {
		in["envId"] = c.EnvID
	}
	var out struct {
		Dashboards []wireDashboardSummary `json:"dashboards"`
	}
	if err := c.call(ctx, procListDashboards, in, &out); err != nil {
		return nil, err
	}
	res := make([]DashboardInfo, 0, len(out.Dashboards))
	for _, d := range out.Dashboards {
		res = append(res, d.info())
	}
	return res, nil
}

// GetDashboard reads one dashboard's current spec.
func (c Cloud) GetDashboard(ctx context.Context, ref DashboardRef) (DashboardDocument, error) {
	in := map[string]any{
		"orgId": c.OrgID,
		"ref":   map[string]any{"source": ref.Source, "projectId": ref.ProjectID, "key": ref.Key},
	}
	var out struct {
		Summary wireDashboardSummary `json:"summary"`
		Spec    json.RawMessage      `json:"spec"`
		Version int                  `json:"version"`
	}
	if err := c.call(ctx, procGetDashboard, in, &out); err != nil {
		return DashboardDocument{}, err
	}
	info := out.Summary.info()
	if info.Ref.Key == "" {
		info.Ref = ref
	}
	return DashboardDocument{Info: info, Spec: out.Spec, Version: out.Version}, nil
}

// PanelReadout is what one QueryPanel answer holds, as counts: enough to say
// whether the console has anything to draw and why not, without carrying the
// data itself.
type PanelReadout struct {
	// Kind echoes the panel kind the control plane ran.
	Kind string
	// StepSeconds is the bucket width the control plane used (0: the kind has
	// no time axis).
	StepSeconds int64
	// Series and Rows are how many entries the answer carries; Points is the
	// points across all series, NonNull those that hold a value, NonZero those
	// (and table cells, and number figures) that are not zero.
	Series, Rows, Points, NonNull, NonZero int
	// Buckets is how many distinct bucket times hold a drawable (non-null)
	// point: null formula results and gaps draw nothing, so a line of one
	// valued point among nulls is one bucket.
	Buckets int
	// SeriesBuckets is, per series that holds a drawable point, how many
	// distinct bucket times it holds: a line is drawn per series, so a series
	// of one bucket draws nothing whatever the others hold.
	SeriesBuckets []int
	// LastDataAt is the newest bucket holding a non-zero value; zero when the
	// answer has no time axis or nothing non-zero.
	LastDataAt time.Time
	// Items counts funnel steps entered, issues, sessions or heatmap cells.
	Items int
	// HasData is true when the console would draw something rather than its
	// empty state.
	HasData bool
	// BucketSeconds and BucketTimezone are the bucket width and zone a sql
	// time series reports; zero and empty when the control plane sends none
	// (older ones do not, and nothing here depends on them).
	BucketSeconds  int64
	BucketTimezone string
	// Truncated says a cap cut the answer; SeriesTotal is how many series
	// matched before the cap (0: unknown).
	Truncated   bool
	SeriesTotal int
	// Warnings are the control plane's non-fatal notes, verbatim
	// ("metric_absent: rpc.server.calls").
	Warnings []string
	// SQLColumns are a sql panel's result columns, in order.
	SQLColumns []Column
}

// PanelReadoutRequest is one panel run.
type PanelReadoutRequest struct {
	// Narrow carries the range and services; its filters and Explain are the
	// audit's to leave empty (the console sends none for a dashboard panel).
	Narrow
	Panel obsquery.Panel
	// ProjectID is the forge project, sent beside the env as the console does
	// (empty: no project narrowing).
	ProjectID string
	// AllEnvs lifts the env bound: the same panel over every environment of
	// ProjectID, the question "does this data exist in another env?".
	AllEnvs bool
	// NoEnv drops the env predicate as a platform-scoped dashboard does: its
	// telemetry belongs to no environment and carries no env id. Unlike
	// AllEnvs it is the dashboard's own read, not a widening, so it needs no
	// project to bound it (who may read which org's data is unchanged).
	NoEnv bool
}

// QueryPanelReadout runs a panel the way the console does and reads the answer
// back as counts. The step is left to the control plane (the console sends 0
// too), and the answer echoes what it chose.
func (c Cloud) QueryPanelReadout(ctx context.Context, req PanelReadoutRequest) (PanelReadout, error) {
	panel, err := panelWireAny(req.Panel)
	if err != nil {
		return PanelReadout{}, err
	}
	n := req.Narrow
	n.Explain = false
	n.Filters = nil
	scope := c.scope(n)
	scope.ProjectID = req.ProjectID
	if req.AllEnvs {
		if req.ProjectID == "" {
			return PanelReadout{}, Wrong("an all-envs read needs the forge project to bound it")
		}
		scope.EnvIDs = nil
	}
	if req.NoEnv {
		scope.EnvIDs = nil
	}
	r, err := c.queryPanelScoped(ctx, scope, panel, 0, false)
	if err != nil {
		return PanelReadout{}, err
	}
	return r.readout(req.Panel.Kind), nil
}

func (r wirePanelResponse) readout(kind string) PanelReadout {
	out := PanelReadout{
		Kind: kind, StepSeconds: r.StepSeconds, Series: len(r.Series), Rows: len(r.Rows),
		Truncated: r.Truncated, SeriesTotal: r.SeriesTotal, Warnings: r.Warnings,
		BucketSeconds: int64(r.BucketSeconds), BucketTimezone: r.BucketTimezone,
	}
	for _, c := range r.SQLColumns {
		out.SQLColumns = append(out.SQLColumns, Column{Name: c.Name, Type: c.Type})
	}
	buckets := map[time.Time]bool{}
	for _, s := range r.Series {
		own := map[time.Time]bool{}
		for _, p := range s.Points {
			out.Points++
			if p.Null {
				continue
			}
			buckets[p.Time.UTC()] = true
			own[p.Time.UTC()] = true
			out.NonNull++
			if p.Value != 0 {
				out.NonZero++
				if t := p.Time.UTC(); t.After(out.LastDataAt) {
					out.LastDataAt = t
				}
			}
		}
		if len(own) > 0 {
			out.SeriesBuckets = append(out.SeriesBuckets, len(own))
		}
		if kind == "number" && s.Value != 0 {
			out.NonZero++
		}
	}
	out.Buckets = len(buckets)
	for _, row := range r.Rows {
		missing := map[int]bool{}
		for _, i := range row.MissingValueIndexes {
			missing[i] = true
		}
		for i, v := range row.Values {
			if !missing[i] && v != 0 {
				out.NonZero++
			}
		}
	}
	steps := 0
	for _, s := range r.FunnelSteps {
		if int64(s.Count) > 0 {
			steps++
		}
	}
	switch kind {
	case "funnel":
		out.Items = steps
		out.HasData = steps > 0
	case "issues":
		out.Items = len(r.Issues)
		out.HasData = out.Items > 0
	case "sessions":
		out.Items = len(r.Sessions)
		out.HasData = out.Items > 0
	case "heatmap":
		out.Items = len(r.Heatmap)
		out.HasData = out.Items > 0
	case "number":
		out.HasData = out.Series > 0 || out.Rows > 0
	default:
		out.HasData = out.Rows > 0 || out.NonNull > 0
	}
	return out
}

// FailureClass says what kind of refusal or fault a failed read was, so a
// reader can tell "the credential may not look" from "the store is down" from
// "the panel is wrong". None of them is "no data".
type FailureClass string

// The failure classes.
const (
	// ClassAuth: the credential was refused (HTTP 401/403, missing scope).
	ClassAuth FailureClass = "auth"
	// ClassQuota: the org's hourly query allowance is spent.
	ClassQuota FailureClass = "quota"
	// ClassRateLimited: this credential's own request budget is spent.
	ClassRateLimited FailureClass = "rate_limited"
	// ClassInFlight: too many concurrent reads for this caller.
	ClassInFlight FailureClass = "in_flight"
	// ClassTimeout: the query ran too long.
	ClassTimeout FailureClass = "timeout"
	// ClassTooMuchData: the query touched more rows or bytes than a read may.
	ClassTooMuchData FailureClass = "too_much_data"
	// ClassStorage: the telemetry store is unavailable or not set up.
	ClassStorage FailureClass = "storage"
	// ClassInvalid: the control plane refused the request as malformed.
	ClassInvalid FailureClass = "invalid"
	// ClassNotFound: the thing asked for does not exist.
	ClassNotFound FailureClass = "not_found"
	// ClassUnsupported: an older control plane that does not serve the call.
	ClassUnsupported FailureClass = "unsupported"
	// ClassUnreachable: no answer came back at all.
	ClassUnreachable FailureClass = "unreachable"
	// ClassServer: the control plane failed internally.
	ClassServer FailureClass = "server"
	// ClassUnknown: none of the above.
	ClassUnknown FailureClass = "unknown"
)

// Reasons the control plane stamps on a refusal (x-forge-error-reason).
const (
	reasonQueryQuota     = "obs_query_quota"
	reasonQueryQuotaSoft = "obs_query_quota_soft"
	reasonInFlight       = "in_flight_limit"
	reasonRateLimited    = "rate_limited"
)

// Failure is a failed control-plane read, classified.
type Failure struct {
	Class FailureClass
	// Code is the Connect code's wire name; Reason the control plane's
	// domain reason. Either may be empty.
	Code   string
	Reason string
	// HTTPStatus is the response status (0: none came back).
	HTTPStatus int
	// Message is the control plane's own text.
	Message string
	// MissingScope names the scope a 403 says the credential lacks.
	MissingScope string
	// RetryAfter is how long the control plane asked to wait; ResetsAt when a
	// spent quota refills. Zero when it said neither.
	RetryAfter time.Duration
	ResetsAt   time.Time
}

// Transient says waiting RetryAfter and asking again is the right response.
func (f Failure) Transient() bool {
	return f.Class == ClassInFlight || f.Class == ClassRateLimited
}

// FailureOf classifies err, a read's failure.
func FailureOf(err error) Failure {
	f := Failure{Class: ClassUnknown}
	if err == nil {
		return f
	}
	f.Message = err.Error()
	var ce *cloud.Error
	if !errors.As(err, &ce) {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			f.Class = ClassTimeout
		default:
			f.Class = ClassUnreachable
		}
		return f
	}
	f.Code, f.Reason, f.HTTPStatus = ce.Code, ce.Reason, ce.HTTPStatus
	if ce.Message != "" {
		f.Message = ce.Message
	}
	if scope, ok := ce.MissingScope(); ok {
		f.MissingScope = scope
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(ce.Header.Get("Retry-After"))); err == nil && secs > 0 {
		f.RetryAfter = time.Duration(secs) * time.Second
	}
	switch {
	case ce.HTTPStatus == http.StatusUnauthorized, ce.HTTPStatus == http.StatusForbidden,
		ce.Code == cloud.CodeUnauthenticated, ce.Code == cloud.CodePermissionDenied:
		f.Class = ClassAuth
	case ce.Code == cloud.CodeUnimplemented, ce.HTTPStatus == http.StatusNotImplemented:
		f.Class = ClassUnsupported
	case ce.Code == cloud.CodeResourceExhausted:
		f.Class = exhaustedClass(ce)
		if f.Class == ClassQuota {
			f.ResetsAt = quotaResetsAt(ce)
		}
	case ce.Code == cloud.CodeDeadlineExceeded, ce.HTTPStatus == http.StatusGatewayTimeout:
		f.Class = ClassTimeout
	case ce.Code == cloud.CodeInvalidArgument:
		f.Class = ClassInvalid
	case ce.Code == cloud.CodeNotFound:
		f.Class = ClassNotFound
	case ce.Code == cloud.CodeUnavailable, ce.Code == cloud.CodeFailedPrecondition:
		f.Class = ClassStorage
	case ce.Code == "internal", ce.HTTPStatus >= 500:
		f.Class = ClassServer
	case ce.Code == "" && ce.HTTPStatus == 0:
		f.Class = ClassUnreachable
	}
	return f
}

// exhaustedClass tells the four ResourceExhausted refusals apart. The reason is
// the committed contract; the control plane's documented "in flight" wording is
// the fallback for one that sends none.
func exhaustedClass(ce *cloud.Error) FailureClass {
	switch ce.Reason {
	case reasonQueryQuota, reasonQueryQuotaSoft:
		return ClassQuota
	case reasonInFlight:
		return ClassInFlight
	case reasonRateLimited:
		return ClassRateLimited
	}
	msg := strings.ToLower(ce.Message)
	switch {
	case strings.Contains(msg, "in flight"):
		return ClassInFlight
	case strings.Contains(msg, "too many telemetry requests"):
		return ClassRateLimited
	case strings.Contains(msg, "too much data"):
		return ClassTooMuchData
	}
	return ClassQuota
}

// quotaResetsAt reads when a spent quota refills from the refusal's detail.
func quotaResetsAt(ce *cloud.Error) time.Time {
	raw, ok := ce.DetailJSON("controlplane.v1.QueryQuotaExhausted")
	if !ok {
		return time.Time{}
	}
	var d struct {
		ResetsAt time.Time `json:"resetsAt"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return time.Time{}
	}
	return d.ResetsAt.UTC()
}
