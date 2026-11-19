// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWhyColumnIsAPayloadNotALabelAndNeverChangesIdentity(t *testing.T) {
	rule := testRule()
	first, err := buildSeries(rule, []Row{{"service": "api", "value": 1.0, "why": `{"detector":"x","signal":"y","observed":1,"context":{}}`}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildSeries(rule, []Row{{"service": "api", "value": 1.0, "why": `{"detector":"x","signal":"y","observed":99,"context":{}}`}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := first[0].labels["why"]; ok {
		t.Fatal("why became a label: it would be part of the alert's identity")
	}
	if first[0].fp != second[0].fp {
		t.Fatal("a payload that moved changed the fingerprint: every evaluation would be a new alert")
	}
	if string(first[0].why) == string(second[0].why) || !strings.Contains(string(second[0].why), `"observed":99`) {
		t.Fatalf("why = %s / %s", first[0].why, second[0].why)
	}
}

func TestWhyColumnAcceptsAMapAndRefusesWhatIsNotAnObject(t *testing.T) {
	ss, err := buildSeries(testRule(), []Row{{"service": "a", "why": map[string]any{"detector": "d"}}})
	if err != nil || string(ss[0].why) != `{"detector":"d"}` {
		t.Fatalf("map: %v %v", err, ss)
	}
	if ss, err = buildSeries(testRule(), []Row{{"service": "a", "why": nil}, {"service": "b", "why": "  "}}); err != nil || ss[0].why != nil || ss[1].why != nil {
		t.Fatalf("empty: %v %v", err, ss)
	}
	for _, bad := range []any{"not json", `[1]`, `"s"`, `null`, 3, strings.Repeat("x", 10)} {
		if _, err := buildSeries(testRule(), []Row{{"service": "a", "why": bad}}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	big := `{"k":"` + strings.Repeat("x", MaxWhyBytes) + `"}`
	if _, err := buildSeries(testRule(), []Row{{"service": "a", "why": big}}); err == nil {
		t.Error("accepted an oversized payload")
	}
}

func TestWhyRidesTheInstanceTheEventsAndTheNotification(t *testing.T) {
	var st []Instance
	r := rule(0)
	mk := func(obs string) series {
		s := ser(1, "api")
		s.why = json.RawMessage(`{"observed":` + obs + `}`)
		return s
	}
	ch := step(t, r, &st, []series{mk("2")}, t0)
	if got := string(only(t, st).Why); got != `{"observed":2}` {
		t.Fatalf("instance why = %s", got)
	}
	if len(ch.Events) != 1 || string(ch.Events[0].Why) != `{"observed":2}` {
		t.Fatalf("event why = %+v", ch.Events)
	}
	// A later evaluation refreshes the payload and records no event.
	ch = step(t, r, &st, []series{mk("7")}, t0.Add(time.Minute))
	if len(ch.Events) != 0 || string(only(t, st).Why) != `{"observed":7}` {
		t.Fatalf("refresh: events %v why %s", ch.Events, only(t, st).Why)
	}
	alerts, _ := alertsFor(st, time.Minute, t0.Add(time.Minute))
	if len(alerts) != 1 || string(alerts[0].Why) != `{"observed":7}` {
		t.Fatalf("alert why = %+v", alerts)
	}
	// Resolution keeps the last payload, so the resolved notice says what it resolved.
	step(t, r, &st, nil, t0.Add(2*time.Minute))
	ch = step(t, r, &st, nil, t0.Add(3*time.Minute))
	if len(ch.Events) != 1 || ch.Events[0].Transition != TransitionResolved || string(ch.Events[0].Why) != `{"observed":7}` {
		t.Fatalf("resolve: %+v", ch.Events)
	}
}

func newCaptureServer(t *testing.T, into *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, into)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAlertmanagerNotifierPostsWhyAsAnAnnotationAndNeverAsALabel(t *testing.T) {
	var got []map[string]any
	srv := newCaptureServer(t, &got)
	a := Alert{
		Labels: map[string]string{"alertname": "x"}, Annotations: map[string]string{"summary": "s"},
		StartsAt: t0, EndsAt: t0.Add(time.Minute), Why: json.RawMessage(`{"detector":"d"}`),
	}
	if err := NewAlertmanagerNotifier(nil, srv.URL).Notify(context.Background(), []Alert{a}); err != nil {
		t.Fatal(err)
	}
	ann := got[0]["annotations"].(map[string]any)
	if ann[WhyAnnotation] != `{"detector":"d"}` || ann["summary"] != "s" {
		t.Fatalf("annotations = %v", ann)
	}
	if _, ok := got[0]["labels"].(map[string]any)[WhyAnnotation]; ok {
		t.Fatal("why was posted as a label")
	}
	if _, mutated := a.Annotations[WhyAnnotation]; mutated {
		t.Fatal("posting the alert mutated the caller's annotations")
	}
}

func TestWhyJSONRoundTripsAndFitsItsBound(t *testing.T) {
	w := Why{
		Detector: DetectorReleaseRegressed, Signal: "error_rate", Observed: F(0.041), Baseline: F(0.003), Unit: "fraction",
		Window: "15m", Since: t0,
		Context: WhyContext{Releases: []WhyRelease{{Version: "v42", PromotedAt: t0}}},
		Next:    []string{"forge obs check prod"},
	}
	b, err := w.JSON()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseWhy(b)
	if err != nil || back.Detector != DetectorReleaseRegressed || *back.Observed != 0.041 || back.Context.Releases[0].Version != "v42" {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["spread"]; ok {
		t.Fatal("an absent figure must be omitted, not zero")
	}

	for i := 0; i < 400; i++ {
		w.Context.TopIssues = append(w.Context.TopIssues, WhyIssue{ID: "id", Title: strings.Repeat("t", 200), Events: 1})
	}
	if b, err = w.JSON(); err != nil || len(b) > MaxWhyBytes {
		t.Fatalf("oversized payload: %d bytes, %v", len(b), err)
	}
	if back, _ = ParseWhy(b); back.Detector != DetectorReleaseRegressed || len(back.Context.TopIssues) == 0 {
		t.Fatal("trimming dropped what matters")
	}
}
