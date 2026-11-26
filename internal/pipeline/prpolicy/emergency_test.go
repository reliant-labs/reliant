package prpolicy

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestIncidentSeverityAtLeast(t *testing.T) {
	cases := []struct {
		s, min IncidentSeverity
		want   bool
	}{
		{IncidentSeverityCritical, IncidentSeverityHigh, true},
		{IncidentSeverityHigh, IncidentSeverityHigh, true},
		{IncidentSeverityMedium, IncidentSeverityHigh, false},
		{IncidentSeverityLow, IncidentSeverityLow, true},
		{"", IncidentSeverityLow, false},
		{"sev1", IncidentSeverityLow, false},
	}
	for _, tc := range cases {
		if got := tc.s.AtLeast(tc.min); got != tc.want {
			t.Errorf("%q.AtLeast(%q) = %v, want %v", tc.s, tc.min, got, tc.want)
		}
	}
}

func TestEvaluateEmergency(t *testing.T) {
	inc := func(src IncidentSource, st IncidentState, sev IncidentSeverity) *LinkedIncident {
		return &LinkedIncident{Source: src, State: st, Severity: sev}
	}
	prodHigh := inc(IncidentSourceProdAlert, IncidentStateResponding, IncidentSeverityHigh)
	in := func(f func(*EmergencyInput)) EmergencyInput {
		e := EmergencyInput{Head: "h1", VerdictPassed: true, Incident: prodHigh, Policy: core.PipelineEmergencyLandAsk, DailyCap: 3}
		f(&e)
		return e
	}
	ask := EmergencyEligibility{Eligible: true, Head: "h1", Approval: EmergencyApprovalAsk, Reason: EmergencyEligible}
	no := func(r EmergencyReason) EmergencyEligibility { return EmergencyEligibility{Head: "h1", Reason: r} }
	cases := []struct {
		name string
		in   EmergencyInput
		want EmergencyEligibility
	}{
		{"ask", in(func(*EmergencyInput) {}), ask},
		{"empty policy is the default ask", in(func(e *EmergencyInput) { e.Policy = "" }), ask},
		{"unknown policy asks", in(func(e *EmergencyInput) { e.Policy = "yolo" }), ask},
		{"auto_allow under the cap", in(func(e *EmergencyInput) { e.Policy = core.PipelineEmergencyLandAutoAllow; e.AutoApprovedToday = 2 }),
			EmergencyEligibility{Eligible: true, Head: "h1", Approval: EmergencyApprovalAutoAllow, Reason: EmergencyEligible}},
		{"auto_allow at the cap falls back to ask", in(func(e *EmergencyInput) { e.Policy = core.PipelineEmergencyLandAutoAllow; e.AutoApprovedToday = 3 }),
			EmergencyEligibility{Eligible: true, Head: "h1", Approval: EmergencyApprovalAsk, Reason: EmergencyDailyCapReached}},
		{"auto_allow with a zero cap asks", in(func(e *EmergencyInput) { e.Policy = core.PipelineEmergencyLandAutoAllow; e.DailyCap = 0 }),
			EmergencyEligibility{Eligible: true, Head: "h1", Approval: EmergencyApprovalAsk, Reason: EmergencyDailyCapReached}},
		{"auto_allow with a negative count asks", in(func(e *EmergencyInput) {
			e.Policy = core.PipelineEmergencyLandAutoAllow
			e.AutoApprovedToday = -1
		}), EmergencyEligibility{Eligible: true, Head: "h1", Approval: EmergencyApprovalAsk, Reason: EmergencyDailyCapReached}},
		{"auto_allow with a negative cap and a lower count asks", in(func(e *EmergencyInput) {
			e.Policy = core.PipelineEmergencyLandAutoAllow
			e.DailyCap = -1
			e.AutoApprovedToday = -5
		}), EmergencyEligibility{Eligible: true, Head: "h1", Approval: EmergencyApprovalAsk, Reason: EmergencyDailyCapReached}},
		{"off", in(func(e *EmergencyInput) { e.Policy = core.PipelineEmergencyLandOff }), no(EmergencyPolicyOff)},
		{"no review pass", in(func(e *EmergencyInput) { e.VerdictPassed = false }), no(EmergencyNotPassed)},
		{"no head", in(func(e *EmergencyInput) { e.Head = "" }), EmergencyEligibility{Reason: EmergencyNotPassed}},
		{"no linked incident", in(func(e *EmergencyInput) { e.Incident = nil }), no(EmergencyNoIncident)},
		{"resolved incident", in(func(e *EmergencyInput) {
			e.Incident = inc(IncidentSourceProdAlert, IncidentStateResolved, IncidentSeverityCritical)
		}), no(EmergencyIncidentDone)},
		{"unknown incident state", in(func(e *EmergencyInput) { e.Incident = inc(IncidentSourceProdAlert, "closed", IncidentSeverityCritical) }), no(EmergencyIncidentDone)},
		{"needs_person is still open", in(func(e *EmergencyInput) {
			e.Incident = inc(IncidentSourceRelease, IncidentStateNeedsPerson, IncidentSeverityHigh)
		}), ask},
		{"medium severity", in(func(e *EmergencyInput) {
			e.Incident = inc(IncidentSourceProdAlert, IncidentStateOpen, IncidentSeverityMedium)
		}), no(EmergencyLowSeverity)},
		{"unknown severity", in(func(e *EmergencyInput) { e.Incident = inc(IncidentSourceProdAlert, IncidentStateOpen, "") }), no(EmergencyLowSeverity)},
		{"critical release", in(func(e *EmergencyInput) {
			e.Incident = inc(IncidentSourceRelease, IncidentStateOpen, IncidentSeverityCritical)
		}), ask},
		{"high vuln (D16)", in(func(e *EmergencyInput) { e.Incident = inc(IncidentSourceVuln, IncidentStateOpen, IncidentSeverityHigh) }), ask},
		{"main incident never", in(func(e *EmergencyInput) {
			e.Incident = inc(IncidentSourceMain, IncidentStateOpen, IncidentSeverityCritical)
		}), no(EmergencyMainIncident)},
		{"unknown source", in(func(e *EmergencyInput) { e.Incident = inc("pager", IncidentStateOpen, IncidentSeverityCritical) }), no(EmergencyUnknownSrc)},
		{"ineligible beats policy off", in(func(e *EmergencyInput) { e.Policy = core.PipelineEmergencyLandOff; e.VerdictPassed = false }), no(EmergencyNotPassed)},
	}
	for _, tc := range cases {
		if got := EvaluateEmergency(tc.in); got != tc.want {
			t.Errorf("%s: EvaluateEmergency = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Every source but main is emergency-eligible at high severity; the list is
// the closed set, so a new source has to choose.
func TestEmergencyEligibleSources(t *testing.T) {
	for _, src := range IncidentSources {
		got := EvaluateEmergency(EmergencyInput{Head: "h", VerdictPassed: true,
			Incident: &LinkedIncident{Source: src, State: IncidentStateOpen, Severity: IncidentSeverityHigh}})
		if want := src != IncidentSourceMain; got.Eligible != want {
			t.Errorf("source %q: eligible = %v, want %v", src, got.Eligible, want)
		}
	}
}
