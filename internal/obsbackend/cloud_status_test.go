package obsbackend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

func TestCloudOverviewMapsTheWireAnswer(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetServiceOverview: `{"services":[
		{"service":"api","requestCount":"1200","ratePerSecond":3.5,"errorCount":"12","errorRate":0.01,"p50Ms":10,"p95Ms":80,"p99Ms":200,"errorLogCount":"4"}]}`}}
	res, err := cloudWith(f).Overview(context.Background(), OverviewRequest{Narrow: rng()})
	if err != nil || len(res.Services) != 1 {
		t.Fatalf("Overview = %+v, %v", res, err)
	}
	s := res.Services[0]
	if s.Service != "api" || s.Requests != 1200 || s.RatePerSecond != 3.5 || s.Errors != 12 || s.ErrorRate != 0.01 || s.P50Ms != 10 || s.P95Ms != 80 || s.P99Ms != 200 || s.ErrorLogs != 4 {
		t.Errorf("service = %+v", s)
	}
	if scope := f.reqs[procGetServiceOverview]["scope"].(map[string]any); scope["orgId"] != "org-1" {
		t.Errorf("scope = %v", scope)
	}
}

func TestCloudOverviewNamesAControlPlaneThatDoesNotServeIt(t *testing.T) {
	f := &fakeCaller{errs: map[string]error{procGetServiceOverview: &cloud.Error{Code: cloud.CodeUnimplemented}}}
	_, err := cloudWith(f).Overview(context.Background(), OverviewRequest{Narrow: rng()})
	if exitOf(err) != ExitUndetermined || !strings.Contains(err.Error(), "does not serve the service overview yet (GetServiceOverview is unimplemented)") {
		t.Errorf("err = %v", err)
	}
	other := &fakeCaller{errs: map[string]error{procGetServiceOverview: errors.New("boom")}}
	if _, err := cloudWith(other).Overview(context.Background(), OverviewRequest{Narrow: rng()}); err == nil || strings.Contains(err.Error(), "does not serve") {
		t.Errorf("another failure is returned as it is: %v", err)
	}
}

func TestCloudOverviewExplainCompilesLocallyAndNeverCalls(t *testing.T) {
	f := &fakeCaller{}
	res, err := cloudWith(f).Overview(context.Background(), OverviewRequest{Narrow: explainNarrow()})
	if err != nil || len(res.Statements) == 0 || len(f.reqs) != 0 {
		t.Errorf("explain = %+v, %v; calls %v", res, err, f.reqs)
	}
}

func TestCloudAlertsAreOrderedFiringFirstThenOldest(t *testing.T) {
	epoch := "1970-01-01T00:00:00Z"
	f := &fakeCaller{replies: map[string]string{procListAlerts: `{"alerts":[
		{"rule":"slow","state":"PENDING","fingerprint":"f3","activeAt":"2026-10-22T09:00:00Z","firingAt":"` + epoch + `","lastValue":1.5},
		{"rule":"late-fire","state":"FIRING","fingerprint":"f2","activeAt":"2026-10-22T08:00:00Z","firingAt":"2026-10-22T10:00:00Z","annotations":{"summary":"late"}},
		{"rule":"early-fire","state":"FIRING","fingerprint":"f1","activeAt":"2026-10-22T07:00:00Z","firingAt":"2026-10-22T07:30:00Z",
		 "labels":{"service":"api"},"annotations":{"summary":"errors high","why":"{\"observed\":3}"},"lastValue":9},
		{"rule":"older-pending","state":"PENDING","activeAt":"2026-10-22T06:00:00Z","annotations":{"why":"not json"}}]}`}}
	got, err := cloudWith(f).Alerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, a := range got {
		order = append(order, a.Rule)
	}
	if strings.Join(order, ",") != "early-fire,late-fire,older-pending,slow" {
		t.Errorf("order = %v, want firing first, each oldest first", order)
	}
	first := got[0]
	if first.State != AlertFiring || first.Summary != "errors high" || first.Labels["service"] != "api" || first.Value != 9 || string(first.Why) != `{"observed":3}` ||
		first.FiringAt.IsZero() || first.Fingerprint != "f1" {
		t.Errorf("alert = %+v", first)
	}
	// An unset (epoch) timestamp is left out, and a non-JSON why is dropped.
	if slow := got[3]; !slow.FiringAt.IsZero() || slow.State != "pending" || slow.ActiveAt.IsZero() {
		t.Errorf("pending alert = %+v", slow)
	}
	if got[2].Why != nil {
		t.Errorf("a user's own non-JSON why annotation must not become a payload: %q", got[2].Why)
	}
	if req := f.reqs[procListAlerts]; req["orgId"] != "org-1" || req["envId"] != "env-1" {
		t.Errorf("request = %v", req)
	}

	empty := &fakeCaller{replies: map[string]string{procListAlerts: `{}`}}
	c := cloudWith(empty)
	c.EnvID = ""
	if got, err := c.Alerts(context.Background()); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no alerts = %v, %v", got, err)
	}
	if _, has := empty.reqs[procListAlerts]["envId"]; has {
		t.Error("an empty env must not be sent")
	}
	if _, err := cloudWith(&fakeCaller{errs: map[string]error{procListAlerts: &cloud.Error{Code: cloud.CodeUnimplemented}}}).Alerts(context.Background()); !strings.Contains(err.Error(), "ListAlerts is unimplemented") {
		t.Errorf("err = %v", err)
	}
}

func TestCloudAlertHistoryAsksSinceAndMapsTransitions(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procListAlertHistory: `{"events":[
		{"rule":"slow","fingerprint":"f","labels":{"a":"b"},"transition":"FIRING","at":"2026-10-22T10:00:00Z"},
		{"rule":"slow","transition":"RESOLVED","at":"1970-01-01T00:00:00Z"}]}`}}
	since := time.Date(2026, 10, 22, 6, 0, 0, 0, time.FixedZone("x", 3600))
	got, err := cloudWith(f).AlertHistory(context.Background(), since)
	if err != nil || len(got) != 2 {
		t.Fatalf("AlertHistory = %+v, %v", got, err)
	}
	if got[0].Transition != "firing" || got[0].Labels["a"] != "b" || got[0].At.IsZero() || !got[1].At.IsZero() || got[1].Transition != "resolved" {
		t.Errorf("transitions = %+v", got)
	}
	req := f.reqs[procListAlertHistory]
	if req["since"] != "2026-10-22T05:00:00Z" || req["limit"] != float64(alertHistoryLimit) || req["envId"] != "env-1" {
		t.Errorf("request = %v", req)
	}
	if _, err := cloudWith(&fakeCaller{errs: map[string]error{procListAlertHistory: &cloud.Error{Code: cloud.CodeUnimplemented}}}).AlertHistory(context.Background(), since); !strings.Contains(err.Error(), "ListAlertHistory is unimplemented") {
		t.Errorf("err = %v", err)
	}
	c := cloudWith(&fakeCaller{replies: map[string]string{procListAlertHistory: `{}`}})
	c.EnvID = ""
	if got, err := c.AlertHistory(context.Background(), since); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty history = %v, %v", got, err)
	}
}

func TestCloudTelemetryDeliveriesMapStatesAndErrors(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procListTelemetryDeliveries: `{"enabled":true,"deliveries":[
		{"cluster":"c1","namespace":"ns","name":"agent","state":"DELIVERY_STATE_FAILING","expiresAt":"2026-10-23T00:00:00Z","lastVerifiedAt":"1970-01-01T00:00:00Z","lastError":"denied","consecutiveFailures":3}]}`}}
	res, err := cloudWith(f).TelemetryDeliveries(context.Background())
	if err != nil || !res.Enabled || len(res.Deliveries) != 1 {
		t.Fatalf("TelemetryDeliveries = %+v, %v", res, err)
	}
	d := res.Deliveries[0]
	if d.Cluster != "c1" || d.Namespace != "ns" || d.Name != "agent" || d.State != "failing" || d.ExpiresAt.IsZero() || !d.LastVerifiedAt.IsZero() || d.LastError != "denied" || d.ConsecutiveFailures != 3 {
		t.Errorf("delivery = %+v", d)
	}
	if req := f.reqs[procListTelemetryDeliveries]; req["orgId"] != "org-1" || req["environmentId"] != "env-1" {
		t.Errorf("request = %v", req)
	}
	c := cloudWith(&fakeCaller{replies: map[string]string{procListTelemetryDeliveries: `{}`}})
	c.EnvID = ""
	if res, err := c.TelemetryDeliveries(context.Background()); err != nil || res.Enabled || len(res.Deliveries) != 0 {
		t.Errorf("none = %+v, %v", res, err)
	}
	if _, err := cloudWith(&fakeCaller{errs: map[string]error{procListTelemetryDeliveries: &cloud.Error{Code: cloud.CodeUnimplemented}}}).TelemetryDeliveries(context.Background()); !strings.Contains(err.Error(), "ListTelemetryDeliveries is unimplemented") {
		t.Errorf("err = %v", err)
	}
}

func TestUnsetEpochAndWhyOf(t *testing.T) {
	if !unsetEpoch(time.Unix(0, 0)).IsZero() || !unsetEpoch(time.Time{}).IsZero() {
		t.Error("the epoch and the zero time both mean unset")
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 7200))
	if got := unsetEpoch(at); !got.Equal(at) || got.Location() != time.UTC {
		t.Errorf("unsetEpoch = %v", got)
	}
	for name, tc := range map[string]struct {
		in   map[string]string
		want string
	}{
		"none":       {nil, ""},
		"object":     {map[string]string{"why": `{"a":1}`}, `{"a":1}`},
		"not json":   {map[string]string{"why": "plain words"}, ""},
		"an array":   {map[string]string{"why": `[1]`}, ""},
		"json null":  {map[string]string{"why": `null`}, ""},
		"empty text": {map[string]string{"why": ""}, ""},
	} {
		if got := string(whyOf(tc.in)); got != tc.want {
			t.Errorf("%s: whyOf = %q, want %q", name, got, tc.want)
		}
	}
}

func TestCloudSessionsListAndGet(t *testing.T) {
	t1 := "2026-10-22T10:30:00Z"
	f := &fakeCaller{replies: map[string]string{
		procListSessions:       `{"sessions":[{"sessionId":"s-1","service":"web","serviceVersion":"v2","userId":"u","entryPath":"/","startedAt":"` + t1 + `","endedAt":"` + t1 + `","pageViews":"3","interactions":"4","errors":"1","hasReplay":true}],"nextPageToken":"next"}`,
		procGetSession:         `{"session":{"sessionId":"s-1","service":"web","errors":"1"}}`,
		procGetSessionTimeline: `{"entries":[{"time":"` + t1 + `","kind":"SESSION_TIMELINE_KIND_REQUEST_FAILED","name":"GET","path":"/api","status":500,"issueId":"iss-1","props":{"k":"v"}}],"nextPageToken":"more"}`,
	}}
	c := cloudWith(f)
	list, err := c.ListSessions(context.Background(), ListSessionsRequest{Narrow: rng(), HasError: true, UserID: "u", PathContains: "/x", Limit: 5, PageToken: "tok"})
	if err != nil || len(list.Sessions) != 1 || list.NextPageToken != "next" {
		t.Fatalf("ListSessions = %+v, %v", list, err)
	}
	if s := list.Sessions[0]; s.ID != "s-1" || s.PageViews != 3 || s.Interactions != 4 || s.Errors != 1 || !s.HasReplay || s.Version != "v2" {
		t.Errorf("session = %+v", s)
	}
	req := f.reqs[procListSessions]
	if req["hasError"] != true || req["userId"] != "u" || req["pathContains"] != "/x" || req["pageSize"] != float64(5) || req["pageToken"] != "tok" {
		t.Errorf("request = %v", req)
	}
	if scope := req["scope"].(map[string]any); scope["timeRange"] == nil {
		t.Errorf("a list is bounded by the range: %v", scope)
	}

	got, err := c.GetSession(context.Background(), GetSessionRequest{ID: "s-1", Limit: 10})
	if err != nil || got.Session.ID != "s-1" || got.NextPageToken != "more" || len(got.Timeline) != 1 {
		t.Fatalf("GetSession = %+v, %v", got, err)
	}
	if e := got.Timeline[0]; e.Kind != "request_failed" || e.Status != 500 || e.IssueID != "iss-1" || e.Props["k"] != "v" {
		t.Errorf("entry = %+v", e)
	}
	// With no range the control plane finds the session within its own window.
	if scope := f.reqs[procGetSession]["scope"].(map[string]any); scope["timeRange"] != nil {
		t.Errorf("an unset range must not be sent: %v", scope)
	}
	withRange, err := c.GetSession(context.Background(), GetSessionRequest{ID: "s-1", Narrow: rng()})
	if err != nil || withRange.Session.ID != "s-1" {
		t.Fatal(err)
	}
	if scope := f.reqs[procGetSession]["scope"].(map[string]any); scope["timeRange"] == nil {
		t.Errorf("a set range is sent: %v", scope)
	}
}

func TestCloudSessionsRefuseWhatTheyCannotAnswer(t *testing.T) {
	c := cloudWith(&fakeCaller{})
	if _, err := c.ListSessions(context.Background(), ListSessionsRequest{Narrow: explainNarrow()}); exitOf(err) != ExitUndetermined || !strings.Contains(err.Error(), "--explain") {
		t.Errorf("explain list: %v", err)
	}
	if _, err := c.GetSession(context.Background(), GetSessionRequest{ID: "s", Narrow: explainNarrow()}); exitOf(err) != ExitUndetermined {
		t.Errorf("explain get: %v", err)
	}
	filtered := Narrow{Range: rng().Range, Filters: []Filter{{Key: "k", Value: "v"}}}
	if _, err := c.ListSessions(context.Background(), ListSessionsRequest{Narrow: filtered}); exitOf(err) != ExitWrong || !strings.Contains(err.Error(), "--filter") {
		t.Errorf("a filter on sessions: %v", err)
	}
	if _, err := c.GetSession(context.Background(), GetSessionRequest{ID: "s", Narrow: filtered}); exitOf(err) != ExitWrong {
		t.Errorf("a filter on a session: %v", err)
	}

	// An unknown session is the request's fault, from either call.
	for _, proc := range []string{procGetSession, procGetSessionTimeline} {
		f := &fakeCaller{
			replies: map[string]string{procGetSession: `{"session":{"sessionId":"s"}}`},
			errs:    map[string]error{proc: &cloud.Error{Code: cloud.CodeNotFound}},
		}
		_, err := cloudWith(f).GetSession(context.Background(), GetSessionRequest{ID: "ghost"})
		if exitOf(err) != ExitWrong || !strings.Contains(err.Error(), "session ghost not found") {
			t.Errorf("%s not found: %v", proc, err)
		}
	}
	if _, err := cloudWith(&fakeCaller{errs: map[string]error{procListSessions: errors.New("down")}}).ListSessions(context.Background(), ListSessionsRequest{Narrow: rng()}); err == nil {
		t.Error("a failing list must be an error")
	}
	other := &fakeCaller{errs: map[string]error{procGetSession: &cloud.Error{Code: "unavailable"}}}
	if _, err := cloudWith(other).GetSession(context.Background(), GetSessionRequest{ID: "s"}); err == nil || strings.Contains(err.Error(), "not found") {
		t.Errorf("another failure is not 'not found': %v", err)
	}
}
