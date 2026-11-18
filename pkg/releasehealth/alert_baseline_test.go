package releasehealth

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// identityLatency is an instance of control-plane's IdentityLatency rule: the
// query's sample counts (n, slow) are labels, so each count fingerprints as
// its own instance.
func identityLatency(fp string, slow string, at time.Time) Alert {
	return Alert{Rule: "IdentityLatency", Fingerprint: fp, FiringAt: at,
		Labels: map[string]string{"alertname": "IdentityLatency", "severity": "warning", "n": "15", "slow": slow}}
}

func event(rule, fp, transition string, at time.Time, labels map[string]string) AlertEvent {
	return AlertEvent{Rule: rule, Fingerprint: fp, Transition: transition, At: at, Labels: labels}
}

func latencyLabels(slow string) map[string]string {
	return map[string]string{"alertname": "IdentityLatency", "severity": "warning", "n": "15", "slow": slow}
}

// alertInput is a passing service plus alerts and their history, with
// alert_firing in fail_on (the default).
func alertInput(t *testing.T, alerts []Alert, history []AlertEvent) Input {
	in := input(t, true, svc("api", 1000, 1000, 0.01, 0.01, 100, 100))
	in.Alerts, in.AlertHistory = alerts, history
	return in
}

func alertFindings(r Report) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Code == release.HealthAlertFiring {
			out = append(out, f)
		}
	}
	return out
}

// control-plane v1.21.0-rc.6 (2026-11-17): IdentityLatency, which fired
// 2-27 times a day and twice that day before any rc.6 pod existed, opened
// after the promotion as two instances (slow=4, slow=6) and failed prod. The
// same alert fired in the baseline window, so it is the env's noise: each
// instance is a warn finding saying so, listed in baseline_alerts, and the
// release passes on the rest of its evidence.
func TestAlertThatFiredInTheBaselineDoesNotFail(t *testing.T) {
	history := []AlertEvent{
		// Newest first, as ListAlertHistory answers: after-window flaps,
		// then the day's firings before the promotion.
		event("IdentityLatency", "fp6", TransitionFiring, t0.Add(10*time.Minute), latencyLabels("6")),
		event("IdentityLatency", "fp4", TransitionFiring, t0.Add(5*time.Minute), latencyLabels("4")),
		event("IdentityLatency", "fp3", TransitionResolved, t0.Add(-5*time.Minute), latencyLabels("3")),
		event("IdentityLatency", "fp3", TransitionFiring, t0.Add(-20*time.Minute), latencyLabels("3")),
		event("IdentityLatency", "fp5", TransitionResolved, t0.Add(-100*time.Minute), latencyLabels("5")),
		event("IdentityLatency", "fp5", TransitionFiring, t0.Add(-2*time.Hour), latencyLabels("5")),
	}
	alerts := []Alert{identityLatency("fp4", "4", t0.Add(5*time.Minute)), identityLatency("fp6", "6", t0.Add(10*time.Minute))}
	r := Evaluate(alertInput(t, alerts, history))

	if r.Verdict != Pass {
		t.Fatalf("verdict = %s, want PASS; findings %+v", r.Verdict, r.Findings)
	}
	fs := alertFindings(r)
	if len(fs) != 2 {
		t.Fatalf("alert_firing findings = %+v, want both instances listed", fs)
	}
	for _, f := range fs {
		if f.Policy != PolicyWarn || !strings.Contains(f.Unattributed, "fired 2 time(s) in the 24h before the promotion") ||
			!strings.Contains(f.Unattributed, "flapping") || !strings.Contains(f.Message, "alert IdentityLatency opened after the promotion") ||
			!strings.Contains(f.Message, "not counted against the release") {
			t.Errorf("finding = %+v, want a warn naming the baseline firings", f)
		}
	}
	if len(r.BaselineAlerts) != 2 || r.BaselineAlerts[0].BaselineFirings != 2 || r.FiringAlerts != nil {
		t.Errorf("baseline_alerts = %+v, firing_alerts = %+v", r.BaselineAlerts, r.FiringAlerts)
	}
	// --fail-on re-judges every finding but a discounted one.
	if got := Rejudge(r, []string{release.HealthAlertFiring}); got.Verdict != Pass {
		t.Errorf("Rejudge(alert_firing) = %s, want PASS", got.Verdict)
	}
}

// A genuinely new alert fails as before: one whose rule never fired in the
// baseline window, one that fired only for something else (another
// service), only before the baseline window, only after the promotion, or
// never got past pending.
func TestNewAlertStillFails(t *testing.T) {
	down := Alert{Rule: "ServiceDown", Fingerprint: "d1", FiringAt: t0.Add(5 * time.Minute),
		Labels: map[string]string{"severity": "warning", "service": "api"}}
	other := map[string]string{"severity": "warning", "service": "billing"}
	same := map[string]string{"severity": "warning", "service": "api"}
	tests := map[string][]AlertEvent{
		"no history":                    {},
		"another service's alert":       {event("ServiceDown", "d0", TransitionFiring, t0.Add(-time.Hour), other)},
		"another rule":                  {event("ServiceSlow", "s0", TransitionFiring, t0.Add(-time.Hour), same)},
		"before the baseline window":    {event("ServiceDown", "d0", TransitionFiring, t0.Add(-25*time.Hour), same)},
		"only after the promotion":      {event("ServiceDown", "d1", TransitionFiring, t0.Add(5*time.Minute), same)},
		"pending, never fired":          {event("ServiceDown", "d0", TransitionPending, t0.Add(-time.Hour), same), event("ServiceDown", "d0", TransitionInactive, t0.Add(-50*time.Minute), same)},
		"at the promotion, not before":  {event("ServiceDown", "d0", TransitionFiring, t0, same)},
		"a label the history lacks":     {event("ServiceDown", "d0", TransitionFiring, t0.Add(-time.Hour), map[string]string{"severity": "warning"})},
		"a different severity of alert": {event("ServiceDown", "d0", TransitionFiring, t0.Add(-time.Hour), map[string]string{"severity": "info", "service": "api"})},
	}
	for name, history := range tests {
		t.Run(name, func(t *testing.T) {
			r := Evaluate(alertInput(t, []Alert{down}, history))
			if r.Verdict != Fail {
				t.Fatalf("verdict = %s, want FAIL; findings %+v", r.Verdict, r.Findings)
			}
			if fs := alertFindings(r); len(fs) != 1 || fs[0].Policy != PolicyFail || fs[0].Unattributed != "" {
				t.Errorf("findings = %+v", fs)
			}
			if r.BaselineAlerts != nil || len(r.FiringAlerts) != 1 {
				t.Errorf("baseline_alerts = %+v, firing_alerts = %+v", r.BaselineAlerts, r.FiringAlerts)
			}
		})
	}
}

// The identity match is exact over the non-numeric labels, not a subset test
// (pr-review B1 on forge#2290): an alert whose only labels are alertname and
// measurements has an empty identity, which a subset test matches against
// every instance of its rule, so another service's flapping would discount
// it. Exact, an empty identity matches only a baseline firing whose identity
// is empty too: the same series. IdentityLatency, whose instances differ only
// by numeric labels, is still discounted.
func TestAlertIdentityMatchIsExact(t *testing.T) {
	bare := Alert{Rule: "QueueDepth", Fingerprint: "q1", FiringAt: t0.Add(5 * time.Minute),
		Labels: map[string]string{"alertname": "QueueDepth", "depth": "812"}}
	tests := map[string]struct {
		alert   Alert
		history []AlertEvent
		verdict Verdict
	}{
		"empty identity, baseline firing for service=X": {bare,
			[]AlertEvent{event("QueueDepth", "q0", TransitionFiring, t0.Add(-time.Hour), map[string]string{"alertname": "QueueDepth", "service": "X", "depth": "90"})}, Fail},
		"empty identity, baseline firing with no labels": {bare,
			[]AlertEvent{event("QueueDepth", "q0", TransitionFiring, t0.Add(-time.Hour), nil)}, Pass},
		"empty identity, baseline firing with only numeric labels": {bare,
			[]AlertEvent{event("QueueDepth", "q0", TransitionFiring, t0.Add(-time.Hour), map[string]string{"alertname": "QueueDepth", "depth": "90"})}, Pass},
		"service=api, baseline firing with an extra label": {
			Alert{Rule: "ErrorRateHigh", Fingerprint: "e1", FiringAt: t0.Add(5 * time.Minute), Labels: map[string]string{"service": "api", "rate": "0.07"}},
			[]AlertEvent{event("ErrorRateHigh", "e0", TransitionFiring, t0.Add(-time.Hour), map[string]string{"service": "api", "uri": "/login", "rate": "0.05"})}, Fail},
		"IdentityLatency, differing only by slow": {identityLatency("fp4", "4", t0.Add(5*time.Minute)),
			[]AlertEvent{event("IdentityLatency", "fp3", TransitionFiring, t0.Add(-20*time.Minute), latencyLabels("3"))}, Pass},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := Evaluate(alertInput(t, []Alert{tc.alert}, tc.history))
			if r.Verdict != tc.verdict {
				t.Fatalf("verdict = %s, want %s; findings %+v", r.Verdict, tc.verdict, r.Findings)
			}
			if discounted := len(r.BaselineAlerts) == 1; discounted != (tc.verdict == Pass) {
				t.Errorf("baseline_alerts = %+v", r.BaselineAlerts)
			}
		})
	}
}

// Without a history nothing proves an alert was flapping, so it counts: the
// rule can only be as strict as before when the history was not read.
func TestNoAlertHistoryCountsEveryAlert(t *testing.T) {
	r := Evaluate(alertInput(t, []Alert{identityLatency("fp4", "4", t0.Add(5*time.Minute))}, nil))
	if r.Verdict != Fail || r.BaselineAlerts != nil {
		t.Fatalf("verdict = %s, baseline_alerts = %+v, want FAIL and none", r.Verdict, r.BaselineAlerts)
	}
}

// A critical alert pages whatever its history, so it fails the release even
// when it flapped before it.
func TestCriticalAlertThatFlappedStillFails(t *testing.T) {
	a := identityLatency("fp4", "4", t0.Add(5*time.Minute))
	a.Labels["severity"] = "critical"
	labels := latencyLabels("3")
	labels["severity"] = "critical"
	in := alertInput(t, []Alert{a}, []AlertEvent{event("IdentityLatency", "fp3", TransitionFiring, t0.Add(-time.Hour), labels)})
	in.Thresholds.FailOn = nil // a critical alert fails whatever fail_on says
	if r := Evaluate(in); r.Verdict != Fail || r.BaselineAlerts != nil {
		t.Fatalf("verdict = %s, baseline_alerts = %+v, want FAIL and none", r.Verdict, r.BaselineAlerts)
	}
}

// alert_baseline_min_firings: 0 is the rule before it (every alert counts);
// above 1 it asks for a flap rate. An instance already firing when the
// baseline window opened (its first transition there resolves it) is one
// firing.
func TestAlertBaselineMinFirings(t *testing.T) {
	a := identityLatency("fp4", "4", t0.Add(5*time.Minute))
	history := []AlertEvent{
		event("IdentityLatency", "fp1", TransitionResolved, t0.Add(-23*time.Hour), latencyLabels("1")), // firing since before the window
		event("IdentityLatency", "fp2", TransitionFiring, t0.Add(-6*time.Hour), latencyLabels("2")),
		event("IdentityLatency", "fp2", TransitionResolved, t0.Add(-5*time.Hour), latencyLabels("2")),
	}
	for _, tc := range []struct {
		min     int
		verdict Verdict
	}{{0, Fail}, {1, Pass}, {2, Pass}, {3, Fail}} {
		in := alertInput(t, []Alert{a}, history)
		in.Thresholds.AlertBaselineMinFirings = tc.min
		r := Evaluate(in)
		if r.Verdict != tc.verdict {
			t.Errorf("alert_baseline_min_firings %d: verdict = %s, want %s (%+v)", tc.min, r.Verdict, tc.verdict, r.Findings)
		}
		if tc.verdict == Pass && (len(r.BaselineAlerts) != 1 || r.BaselineAlerts[0].BaselineFirings != 2) {
			t.Errorf("alert_baseline_min_firings %d: baseline_alerts = %+v, want 2 firings", tc.min, r.BaselineAlerts)
		}
	}
}

// Within one rule only the flapping alert is discounted: a new one beside it
// (another service) keeps failing.
func TestOnlyTheFlappingAlertOfARuleIsDiscounted(t *testing.T) {
	alerts := []Alert{
		{Rule: "ErrorRateHigh", Fingerprint: "a", FiringAt: t0.Add(2 * time.Minute), Labels: map[string]string{"service": "api", "rate": "0.07"}},
		{Rule: "ErrorRateHigh", Fingerprint: "b", FiringAt: t0.Add(3 * time.Minute), Labels: map[string]string{"service": "billing", "rate": "0.2"}},
	}
	history := []AlertEvent{event("ErrorRateHigh", "a0", TransitionFiring, t0.Add(-3*time.Hour), map[string]string{"service": "api", "rate": "0.05"})}
	r := Evaluate(alertInput(t, alerts, history))
	if r.Verdict != Fail {
		t.Fatalf("verdict = %s, want FAIL", r.Verdict)
	}
	var policies []string
	for _, f := range alertFindings(r) {
		policies = append(policies, f.Policy)
	}
	slices.Sort(policies)
	if !reflect.DeepEqual(policies, []string{PolicyFail, PolicyWarn}) {
		t.Errorf("alert_firing policies = %v, want one fail and one warn", policies)
	}
	if len(r.BaselineAlerts) != 1 || r.BaselineAlerts[0].Labels["service"] != "api" ||
		len(r.FiringAlerts) != 1 || r.FiringAlerts[0].Labels["service"] != "billing" {
		t.Errorf("baseline_alerts = %+v, firing_alerts = %+v", r.BaselineAlerts, r.FiringAlerts)
	}
}

// The server-side judge (a control plane's GetReleaseHealth and
// ReleaseRegressed call Evaluate) and the CLI's fold over its answer agree:
// over Evaluate's report the fold changes nothing.
func TestWithAlertBaselineOverEvaluateIsANoOp(t *testing.T) {
	history := []AlertEvent{event("IdentityLatency", "fp3", TransitionFiring, t0.Add(-20*time.Minute), latencyLabels("3"))}
	live := []Alert{identityLatency("fp4", "4", t0.Add(5*time.Minute))}
	for name, in := range map[string]Input{
		"discounted": alertInput(t, live, history),
		"counted":    alertInput(t, live, []AlertEvent{}),
	} {
		r := Evaluate(in)
		if got := WithAlertBaseline(r, in.AlertHistory, live, nil); !reflect.DeepEqual(got, r) {
			t.Errorf("%s: the fold changed Evaluate's report:\n got  %+v\n want %+v", name, got, r)
		}
	}
}

// A control plane that judged without the rule failed the release on a
// flapping alert, its finding without a ref (lost on the wire). `forge obs
// check` holds the answer to the rule with the history it reads: the
// finding warns, the alert is listed in baseline_alerts, the threshold is
// listed, and the verdict becomes the PASS the rest earns. An override of 0
// keeps the old rule; an answer with no alert arrays is matched through the
// live alert state.
func TestWithAlertBaselineOverAControlPlaneAnswer(t *testing.T) {
	answer := func(withAlerts bool) Report {
		in := alertInput(t, nil, nil)
		r := Evaluate(in)
		r.Findings = []Finding{{Code: release.HealthAlertFiring, Value: 1, Threshold: 1, Policy: PolicyFail,
			Message: "alert IdentityLatency opened after the promotion and is still firing"}}
		r.Thresholds = slices.DeleteFunc(slices.Clone(r.Thresholds), func(th Threshold) bool { return th.Name == ThresholdAlertBaselineMinFirings })
		if withAlerts {
			r.FiringAlerts = []Alert{identityLatency("fp4", "4", t0.Add(5*time.Minute))}
		}
		r.Verdict = Fail
		return r
	}
	history := []AlertEvent{event("IdentityLatency", "fp3", TransitionFiring, t0.Add(-20*time.Minute), latencyLabels("3"))}
	live := []Alert{identityLatency("fp4", "4", t0.Add(5*time.Minute))}

	got := WithAlertBaseline(answer(true), history, nil, nil)
	if got.Verdict != Pass || got.Findings[0].Policy != PolicyWarn || got.Findings[0].Unattributed == "" ||
		len(got.BaselineAlerts) != 1 || got.FiringAlerts != nil {
		t.Errorf("fold = verdict %s, findings %+v, baseline_alerts %+v", got.Verdict, got.Findings, got.BaselineAlerts)
	}
	if i := slices.IndexFunc(got.Thresholds, func(th Threshold) bool { return th.Name == ThresholdAlertBaselineMinFirings }); i < 0 ||
		got.Thresholds[i] != (Threshold{ThresholdAlertBaselineMinFirings, "1", SourceDefault}) {
		t.Errorf("thresholds = %+v, want alert_baseline_min_firings=1 listed", got.Thresholds)
	}
	if again := WithAlertBaseline(got, history, live, nil); !reflect.DeepEqual(again, got) {
		t.Errorf("the fold is not idempotent:\n got  %+v\n want %+v", again, got)
	}

	if got := WithAlertBaseline(answer(false), history, live, nil); got.Verdict != Pass || len(got.BaselineAlerts) != 1 {
		t.Errorf("no alert arrays: verdict %s, baseline_alerts %+v; want PASS through the live state", got.Verdict, got.BaselineAlerts)
	}
	if got := WithAlertBaseline(answer(false), history, nil, nil); got.Verdict != Fail {
		t.Errorf("no alert known for the finding: verdict %s, want FAIL (nothing names it flapping)", got.Verdict)
	}

	got = WithAlertBaseline(answer(true), history, nil, map[string]string{ThresholdAlertBaselineMinFirings: "0"})
	if got.Verdict != Fail || got.BaselineAlerts != nil {
		t.Errorf("override 0: verdict %s, baseline_alerts %+v, want FAIL", got.Verdict, got.BaselineAlerts)
	}
	if !slices.Contains(got.Thresholds, Threshold{ThresholdAlertBaselineMinFirings, "0", SourceRequest}) {
		t.Errorf("override 0: thresholds = %+v", got.Thresholds)
	}
}

// The threshold resolves from the env's policy, then the request; 0 is a
// value, and below it the request is refused.
func TestResolveAlertBaselineMinFirings(t *testing.T) {
	th := defaults(t)
	if th.AlertBaselineMinFirings != release.DefaultAlertBaselineMinFirings || !slices.Contains(th.List(), Threshold{ThresholdAlertBaselineMinFirings, "1", SourceDefault}) {
		t.Errorf("default = %d, list %+v", th.AlertBaselineMinFirings, th.List())
	}
	zero := 0
	shape := release.DefaultReleaseHealth()
	shape.AlertBaselineMinFirings = &zero
	th, err := Resolve(Options{Shape: &shape})
	if err != nil || th.AlertBaselineMinFirings != 0 || th.Sources[ThresholdAlertBaselineMinFirings] != SourceReleaseShape {
		t.Errorf("shape 0: %d from %s, %v", th.AlertBaselineMinFirings, th.Sources[ThresholdAlertBaselineMinFirings], err)
	}
	ov, err := ParseOverrides([]string{"alert_baseline_min_firings=3"})
	if err != nil {
		t.Fatal(err)
	}
	if th, err = Resolve(Options{Overrides: ov}); err != nil || th.AlertBaselineMinFirings != 3 || th.Sources[ThresholdAlertBaselineMinFirings] != SourceRequest {
		t.Errorf("override 3: %d, %v", th.AlertBaselineMinFirings, err)
	}
	if _, err := Resolve(Options{Overrides: map[string]string{ThresholdAlertBaselineMinFirings: "-1"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("override -1: err = %v, want ErrInvalid", err)
	}
}
