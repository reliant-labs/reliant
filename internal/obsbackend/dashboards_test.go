package obsbackend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestListDashboardsMapsTheIndexAndScopesTheRequest(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procListDashboards: `{"dashboards":[
		{"ref":{"source":"DASHBOARD_SOURCE_GENERATED","projectId":"p1","key":"api"},"title":"API","source":"DASHBOARD_SOURCE_GENERATED","panelCount":4,
		 "release":{"releaseVersion":"v1.2.0"}},
		{"ref":{"source":"DASHBOARD_SOURCE_WORKSPACE","projectId":"p1","key":"costs"},"title":"Costs","viewKind":"DASHBOARD_VIEW_KIND_ORG_COSTS"},
		{"ref":{"source":"DASHBOARD_SOURCE_BUILTIN","key":"x"},"title":"No source of its own","viewKind":"DASHBOARD_VIEW_KIND_UNSPECIFIED"}]}`}}
	got, err := cloudWith(f).ListDashboards(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d dashboards: %+v", len(got), got)
	}
	api := got[0]
	if api.Title != "API" || api.Source != "generated" || api.PanelCount != 4 || api.ReleaseVersion != "v1.2.0" || api.Typed ||
		api.Ref != (DashboardRef{Source: "DASHBOARD_SOURCE_GENERATED", ProjectID: "p1", Key: "api"}) {
		t.Errorf("api = %+v", api)
	}
	// With no top-level source the ref's own names it; a view kind marks a typed view.
	if costs := got[1]; costs.Source != "workspace" || !costs.Typed {
		t.Errorf("costs = %+v, want a typed workspace view", costs)
	}
	if third := got[2]; third.Source != "builtin" || third.Typed {
		t.Errorf("an unspecified view kind is not typed: %+v", third)
	}
	req := f.reqs[procListDashboards]
	if req["orgId"] != "org-1" || req["projectId"] != "p1" || req["envId"] != "env-1" {
		t.Errorf("request = %v", req)
	}

	// No project asks for the whole organization.
	f2 := &fakeCaller{replies: map[string]string{procListDashboards: `{}`}}
	c := cloudWith(f2)
	c.EnvID = ""
	if got, err := c.ListDashboards(context.Background(), ""); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty index = %v, %v; want an empty non-nil list", got, err)
	}
	if _, has := f2.reqs[procListDashboards]["projectId"]; has {
		t.Error("an empty project must not be sent")
	}
	if _, has := f2.reqs[procListDashboards]["envId"]; has {
		t.Error("an empty env must not be sent")
	}
}

func TestListDashboardsSurfacesAFailedCall(t *testing.T) {
	f := &fakeCaller{errs: map[string]error{procListDashboards: &cloud.Error{Code: "unavailable"}}}
	if _, err := cloudWith(f).ListDashboards(context.Background(), ""); err == nil {
		t.Fatal("a failed read must not read as an empty index")
	}
}

func TestGetDashboardKeepsTheRefItWasAskedForWhenTheServerOmitsIt(t *testing.T) {
	ref := DashboardRef{Source: "DASHBOARD_SOURCE_REPO", ProjectID: "p1", Key: "api"}
	f := &fakeCaller{replies: map[string]string{procGetDashboard: `{"summary":{"title":"API","source":"DASHBOARD_SOURCE_REPO"},"spec":{"panels":[]},"version":7}`}}
	doc, err := cloudWith(f).GetDashboard(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Info.Ref != ref || doc.Info.Title != "API" || doc.Info.Source != "repo" || doc.Version != 7 || string(doc.Spec) != `{"panels":[]}` {
		t.Errorf("doc = %+v", doc)
	}
	sent := f.reqs[procGetDashboard]["ref"].(map[string]any)
	if sent["source"] != "DASHBOARD_SOURCE_REPO" || sent["projectId"] != "p1" || sent["key"] != "api" {
		t.Errorf("the ref must go back verbatim: %v", sent)
	}

	// A ref the server echoes is kept as the server wrote it.
	f.replies[procGetDashboard] = `{"summary":{"ref":{"source":"DASHBOARD_SOURCE_REPO","projectId":"p2","key":"canon"}},"spec":{},"version":1}`
	if doc, _ := cloudWith(f).GetDashboard(context.Background(), ref); doc.Info.Ref.Key != "canon" || doc.Info.Ref.ProjectID != "p2" {
		t.Errorf("the server's own ref was overwritten: %+v", doc.Info.Ref)
	}

	f.errs = map[string]error{procGetDashboard: &cloud.Error{Code: "not_found"}}
	if _, err := cloudWith(f).GetDashboard(context.Background(), ref); err == nil {
		t.Error("a failed read must be an error")
	}
}

func TestReadoutCountsWhatTheConsoleWouldDraw(t *testing.T) {
	t1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	t3 := t1.Add(2 * time.Minute)
	line := wirePanelResponse{
		StepSeconds: 60, BucketSeconds: 60, BucketTimezone: "UTC", SeriesTotal: 9, Truncated: true, Warnings: []string{"metric_absent: x"},
		Series: []wirePanelSeriesResult{
			{Points: []wireTimePoint{{Time: t1, Value: 1}, {Time: t2, Null: true}, {Time: t3, Value: 0}}},
			{Points: []wireTimePoint{{Time: t1, Value: 0, Null: true}}},
			{Points: []wireTimePoint{{Time: t1, Value: 2}, {Time: t3, Value: 5}}},
		},
		SQLColumns: []struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			Numeric bool   `json:"numeric"`
			Time    bool   `json:"time"`
		}{{Name: "ts", Type: "DateTime"}},
	}
	got := line.readout("line")
	want := PanelReadout{
		Kind: "line", StepSeconds: 60, Series: 3, Points: 6, NonNull: 4, NonZero: 3, Buckets: 2, SeriesBuckets: []int{2, 2},
		LastDataAt: t3, HasData: true, BucketSeconds: 60, BucketTimezone: "UTC", Truncated: true, SeriesTotal: 9,
		Warnings: []string{"metric_absent: x"}, SQLColumns: []Column{{Name: "ts", Type: "DateTime"}},
	}
	if got.Kind != want.Kind || got.Series != want.Series || got.Points != want.Points || got.NonNull != want.NonNull || got.NonZero != want.NonZero ||
		got.Buckets != want.Buckets || !slices.Equal(got.SeriesBuckets, want.SeriesBuckets) || !got.LastDataAt.Equal(want.LastDataAt) ||
		got.HasData != want.HasData || got.BucketSeconds != want.BucketSeconds || got.BucketTimezone != want.BucketTimezone ||
		got.Truncated != want.Truncated || got.SeriesTotal != want.SeriesTotal || got.StepSeconds != want.StepSeconds ||
		!slices.Equal(got.Warnings, want.Warnings) || !slices.Equal(got.SQLColumns, want.SQLColumns) {
		t.Errorf("readout:\n got %+v\nwant %+v", got, want)
	}

	// A line of nothing but null points draws nothing.
	nulls := wirePanelResponse{Series: []wirePanelSeriesResult{{Points: []wireTimePoint{{Time: t1, Null: true}}}}}
	if r := nulls.readout("line"); r.HasData || r.Buckets != 0 || len(r.SeriesBuckets) != 0 {
		t.Errorf("null-only line = %+v", r)
	}
}

func TestReadoutPerPanelKind(t *testing.T) {
	step := func(n int) []struct {
		Count jsonInt `json:"count"`
	} {
		out := make([]struct {
			Count jsonInt `json:"count"`
		}, n)
		for i := range out {
			out[i].Count = jsonInt(i)
		}
		return out
	}
	raw := []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`)}
	cases := []struct {
		name  string
		kind  string
		resp  wirePanelResponse
		items int
		data  bool
		nz    int
	}{
		{"funnel counts steps entered", "funnel", wirePanelResponse{FunnelSteps: step(3)}, 2, true, 0},
		{"funnel with no entrants", "funnel", wirePanelResponse{FunnelSteps: step(1)}, 0, false, 0},
		{"issues", "issues", wirePanelResponse{Issues: raw}, 2, true, 0},
		{"no issues", "issues", wirePanelResponse{}, 0, false, 0},
		{"sessions", "sessions", wirePanelResponse{Sessions: raw}, 2, true, 0},
		{"heatmap", "heatmap", wirePanelResponse{Heatmap: raw[:1]}, 1, true, 0},
		{"number from a series value", "number", wirePanelResponse{Series: []wirePanelSeriesResult{{Value: 4}, {Value: 0}}}, 0, true, 1},
		{"empty number", "number", wirePanelResponse{}, 0, false, 0},
		{"table counts non-zero cells and skips missing ones", "table", wirePanelResponse{Rows: []struct {
			Group               map[string]string `json:"group"`
			Values              []float64         `json:"values"`
			MissingValueIndexes []int             `json:"missingValueIndexes"`
		}{{Values: []float64{3, 0, 9}, MissingValueIndexes: []int{2}}}}, 0, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.resp.readout(tc.kind)
			if r.Items != tc.items || r.HasData != tc.data || r.NonZero != tc.nz {
				t.Errorf("readout = items %d data %v nonzero %d; want %d %v %d", r.Items, r.HasData, r.NonZero, tc.items, tc.data, tc.nz)
			}
		})
	}
}

func TestQueryPanelReadoutScopesTheRead(t *testing.T) {
	panel := obsquery.Panel{ID: "p", Kind: "table", Signal: "traces"}
	reply := `{"rows":[{"values":[1]}],"stepSeconds":0}`
	run := func(c Cloud, req PanelReadoutRequest) (PanelReadout, map[string]any, error) {
		f := c.Caller.(*fakeCaller)
		f.replies = map[string]string{procQueryPanel: reply}
		r, err := c.QueryPanelReadout(context.Background(), req)
		scope, _ := f.reqs[procQueryPanel]["scope"].(map[string]any)
		return r, scope, err
	}
	base := func() Cloud { return cloudWith(&fakeCaller{}) }

	r, scope, err := run(base(), PanelReadoutRequest{Narrow: narrowC(), Panel: panel, ProjectID: "proj-1"})
	if err != nil || !r.HasData || r.Kind != "table" {
		t.Fatalf("readout = %+v, %v", r, err)
	}
	if scope["projectId"] != "proj-1" || strings.Join(toStrings(scope["envIds"]), ",") != "env-1" {
		t.Errorf("scope = %v, want the env and the project", scope)
	}
	if _, filtered := scope["filters"]; filtered {
		t.Error("a dashboard panel carries none of the caller's filters")
	}

	// AllEnvs lifts the env bound but needs a project to bound it.
	if _, scope, err = run(base(), PanelReadoutRequest{Narrow: narrowC(), Panel: panel, ProjectID: "proj-1", AllEnvs: true}); err != nil {
		t.Fatal(err)
	} else if _, has := scope["envIds"]; has {
		t.Errorf("AllEnvs left the env bound: %v", scope)
	}
	if _, _, err = run(base(), PanelReadoutRequest{Narrow: narrowC(), Panel: panel, AllEnvs: true}); err == nil || !strings.Contains(err.Error(), "needs the forge project") {
		t.Errorf("an all-envs read with no project: %v", err)
	}
	// NoEnv is a platform dashboard's own read and needs no project.
	if _, scope, err = run(base(), PanelReadoutRequest{Narrow: narrowC(), Panel: panel, NoEnv: true}); err != nil {
		t.Fatal(err)
	} else if _, has := scope["envIds"]; has {
		t.Errorf("NoEnv left the env bound: %v", scope)
	}

	// A sql panel is its author's SQL: refused on an instance-narrowed backend.
	narrowed := base()
	narrowed.Instances = Instances{IDs: []string{"inst-1"}}
	_, _, err = run(narrowed, PanelReadoutRequest{Narrow: narrowC(), Panel: obsquery.Panel{ID: "q", Kind: "sql", SQL: "select 1"}})
	if err == nil || !strings.Contains(err.Error(), "sql panel q") {
		t.Errorf("a sql panel on a narrowed backend: %v", err)
	}

	// A failing read surfaces.
	failing := cloudWith(&fakeCaller{errs: map[string]error{procQueryPanel: &cloud.Error{Code: "unavailable"}}})
	if _, err := failing.QueryPanelReadout(context.Background(), PanelReadoutRequest{Narrow: narrowC(), Panel: panel}); err == nil {
		t.Error("a failed panel read must be an error")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func ceWith(code, reason string, status int, msg string) *cloud.Error {
	return &cloud.Error{Code: code, Reason: reason, HTTPStatus: status, Message: msg, Header: http.Header{}}
}

func TestFailureOfClassifiesEveryRefusal(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		class FailureClass
	}{
		{"nil", nil, ClassUnknown},
		{"deadline", context.DeadlineExceeded, ClassTimeout},
		{"transport failure", errors.New("dial tcp: refused"), ClassUnreachable},
		{"401", ceWith("", "", 401, ""), ClassAuth},
		{"403", ceWith("", "", 403, ""), ClassAuth},
		{"unauthenticated code", ceWith("unauthenticated", "", 0, ""), ClassAuth},
		{"permission denied code", ceWith("permission_denied", "", 0, ""), ClassAuth},
		{"unimplemented", ceWith("unimplemented", "", 0, ""), ClassUnsupported},
		{"501", ceWith("", "", 501, ""), ClassUnsupported},
		{"query quota", ceWith("resource_exhausted", "obs_query_quota", 429, ""), ClassQuota},
		{"soft quota", ceWith("resource_exhausted", "obs_query_quota_soft", 429, ""), ClassQuota},
		{"in flight by reason", ceWith("resource_exhausted", "in_flight_limit", 429, ""), ClassInFlight},
		{"rate limited by reason", ceWith("resource_exhausted", "rate_limited", 429, ""), ClassRateLimited},
		{"in flight by wording", ceWith("resource_exhausted", "", 429, "Too many queries IN FLIGHT"), ClassInFlight},
		{"rate limited by wording", ceWith("resource_exhausted", "", 429, "too many telemetry requests"), ClassRateLimited},
		{"too much data by wording", ceWith("resource_exhausted", "", 429, "query touched too much data"), ClassTooMuchData},
		{"exhausted with no hint is the quota", ceWith("resource_exhausted", "", 429, "nope"), ClassQuota},
		{"deadline code", ceWith("deadline_exceeded", "", 0, ""), ClassTimeout},
		{"504", ceWith("", "", 504, ""), ClassTimeout},
		{"invalid", ceWith("invalid_argument", "", 400, ""), ClassInvalid},
		{"not found", ceWith("not_found", "", 404, ""), ClassNotFound},
		{"unavailable is the store", ceWith("unavailable", "", 503, ""), ClassStorage},
		{"failed precondition is the store", ceWith("failed_precondition", "", 400, ""), ClassStorage},
		{"internal", ceWith("internal", "", 500, ""), ClassServer},
		{"5xx with no code", ceWith("", "", 502, ""), ClassServer},
		{"no answer at all", ceWith("", "", 0, ""), ClassUnreachable},
		{"an unrecognised code", ceWith("aborted", "", 409, ""), ClassUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FailureOf(tc.err).Class; got != tc.class {
				t.Errorf("FailureOf(%v).Class = %q, want %q", tc.err, got, tc.class)
			}
		})
	}
}

func TestFailureOfCarriesTheServersOwnFacts(t *testing.T) {
	// A 403 names the missing scope; Retry-After is a number of seconds.
	e := ceWith("permission_denied", "", 403, "needs the deploy:config:read scope")
	e.Header.Set("Retry-After", " 30 ")
	f := FailureOf(e)
	if f.MissingScope != "deploy:config:read" || f.RetryAfter != 30*time.Second || f.HTTPStatus != 403 || f.Code != "permission_denied" || f.Message != "needs the deploy:config:read scope" {
		t.Errorf("failure = %+v", f)
	}
	if f.Transient() {
		t.Error("an auth refusal is not transient")
	}

	// With no usable header the RetryInfo detail gives the wait.
	rate := ceWith("resource_exhausted", "rate_limited", 429, "slow down")
	rate.Header.Set("Retry-After", "soon")
	rate.Details = []cloud.ErrorDetail{{Type: "type.googleapis.com/google.rpc.RetryInfo", Debug: json.RawMessage(`{"retryDelay":"2s"}`)}}
	if f := FailureOf(rate); f.RetryAfter != 2*time.Second || !f.Transient() || f.Class != ClassRateLimited {
		t.Errorf("rate-limited failure = %+v", f)
	}
	inflight := ceWith("resource_exhausted", "in_flight_limit", 429, "")
	if !FailureOf(inflight).Transient() {
		t.Error("an in-flight refusal is transient")
	}

	// A spent quota says when it refills.
	quota := ceWith("resource_exhausted", "obs_query_quota", 429, "spent")
	quota.Details = []cloud.ErrorDetail{{Type: "controlplane.v1.QueryQuotaExhausted", Debug: json.RawMessage(`{"resetsAt":"2026-11-01T10:00:00+02:00"}`)}}
	if f := FailureOf(quota); !f.ResetsAt.Equal(time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)) || f.ResetsAt.Location() != time.UTC {
		t.Errorf("quota failure resets at %v", f.ResetsAt)
	}
	if f := FailureOf(ceWith("resource_exhausted", "obs_query_quota", 429, "")); !f.ResetsAt.IsZero() {
		t.Errorf("a quota refusal with no detail has no reset time: %v", f.ResetsAt)
	}

	// A project the caller cannot see carries the wire status through.
	pu := &cloud.ProjectUnavailableError{ProjectID: "hp", Wire: &cloud.Error{HTTPStatus: 404}}
	if f := FailureOf(pu); f.HTTPStatus != 404 || f.Class != ClassNotFound {
		t.Errorf("project unavailable = %+v", f)
	}
}

func TestRetryInfoDelayAndQuotaResetToleratePoorDetails(t *testing.T) {
	retry := func(debug string) time.Duration {
		e := ceWith("resource_exhausted", "", 429, "")
		if debug != "" {
			e.Details = []cloud.ErrorDetail{{Type: "google.rpc.RetryInfo", Debug: json.RawMessage(debug)}}
		}
		return retryInfoDelay(e)
	}
	for name, tc := range map[string]struct {
		debug string
		want  time.Duration
	}{
		"no detail":    {"", 0},
		"not json":     {"[1]", 0},
		"bad duration": {`{"retryDelay":"soon"}`, 0},
		"non-positive": {`{"retryDelay":"-1s"}`, 0},
		"good":         {`{"retryDelay":"1500ms"}`, 1500 * time.Millisecond},
	} {
		if got := retry(tc.debug); got != tc.want {
			t.Errorf("%s: retryInfoDelay = %v, want %v", name, got, tc.want)
		}
	}
	bad := ceWith("resource_exhausted", "obs_query_quota", 429, "")
	bad.Details = []cloud.ErrorDetail{{Type: "controlplane.v1.QueryQuotaExhausted", Debug: json.RawMessage(`[1]`)}}
	if got := quotaResetsAt(bad); !got.IsZero() {
		t.Errorf("a malformed quota detail = %v", got)
	}
}
