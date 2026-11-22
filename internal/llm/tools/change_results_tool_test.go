package tools

import (
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

func runResults(t *testing.T, f *fakeChanges) ToolResponse {
	t.Helper()
	tool := NewChangeResultsTool(triggerCallerStore{chat: &db.Chat{UserID: "owner", ProjectID: "proj"}}, f)
	resp, err := tool.Run(changeCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolChangeResults, Input: `{"change_id":"chg-1"}`})
	require.NoError(t, err)
	return resp
}

func testReadout() ChangeReadout {
	rel := 0.25
	return ChangeReadout{
		ChangeID: "chg-1", Key: "new-checkout", RunID: "r_abcdefgh", Verdict: "treatment_better", BestVariant: "treatment",
		Arms: []ChangeReadoutArm{
			{Variant: "control", WeightBP: 5000, Assigned: 1000, WindowComplete: 900, Conversions: 90, Rate: 0.1, CILow: 0.082, CIHigh: 0.121, Pending: 100},
			{Variant: "treatment", WeightBP: 5000, Assigned: 1010, WindowComplete: 905, Conversions: 113, Rate: 0.125, CILow: 0.105, CIHigh: 0.148, Pending: 105},
		},
		Comparisons: []ChangeReadoutComparison{{Treatment: "treatment", Control: "control", AbsLift: 0.025, RelLift: &rel, DiffCILow: 0.004, DiffCIHigh: 0.046, PValue: 0.012, Alpha: 0.05, Significant: true, Verdict: "treatment_better"}},
		Assigned:    2010, Pending: 205, ReadoutStart: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), ReadoutEnd: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
		ReadoutEndReason: "now", Metric: &ChangeMetric{Event: "checkout_completed", WindowHours: 168, MinSamplePerArm: 100, ControlVariant: "control"},
		Alpha: 0.05, HorizonNote: "Decide once, at the pre-registered sample size.",
	}
}

func TestChangeResultsSummarizesTheReadout(t *testing.T) {
	resp := runResults(t, &fakeChanges{readout: testReadout()})
	require.False(t, resp.IsError, resp.Content)
	for _, want := range []string{
		"Results of new-checkout, run r_abcdefgh: the new version is better (best: treatment).",
		"Counted from 2026-11-01T00:00:00Z to now (still collecting)",
		"Still collecting.",
		"- treatment: 1010 assigned, 905 window complete, 113 converted, rate 12.5% [10.5%, 14.8%], 105 pending",
		"- treatment vs control: +2.5 pts [+0.4, +4.6], relative +25.0%, p=0.012 at alpha 0.05",
		"Decide once, at the pre-registered sample size.",
	} {
		require.Contains(t, resp.Content, want)
	}
}

func TestChangeResultsWarnsWhenTheSampleRatioCheckFails(t *testing.T) {
	r := testReadout()
	r.Verdict, r.BestVariant, r.Final, r.ReadoutEndReason = "srm_failed", "", true, "run_ended"
	r.SRM = ChangeReadoutSRM{PValue: 0.0001, Failed: true, UnplannedVariants: []string{"beta"}}
	r.Conflicted, r.TruncatedByRetention = 3, true
	resp := runResults(t, &fakeChanges{readout: r})
	require.False(t, resp.IsError, resp.Content)
	for _, want := range []string{"sample-ratio check FAILED", "no effect here can be trusted", "unplanned variants: beta", "Do not read the lifts above as a result", "Final.", "3 people were assigned more than one version", "older than their retention"} {
		require.Contains(t, resp.Content, want)
	}
}

func TestChangeResultsExplainsRefusals(t *testing.T) {
	for _, tc := range []struct {
		err  *ChangeAPIError
		want string
	}{
		{&ChangeAPIError{Code: "failed_precondition", Reason: "no_experiment_run"}, "never run an experiment"},
		{&ChangeAPIError{Code: "failed_precondition", Reason: "run_not_experiment"}, "was a rollout"},
		{&ChangeAPIError{Code: "failed_precondition", Reason: "metric_missing"}, "without a success metric"},
		{&ChangeAPIError{Code: "failed_precondition", Reason: "readout_plan_invalid"}, "no control and treatment"},
		{&ChangeAPIError{Code: "permission_denied", Reason: "scope_missing"}, "obs:read"},
		{&ChangeAPIError{Code: "unavailable"}, "observability"},
		{&ChangeAPIError{Code: "unimplemented"}, "readout service is not deployed"},
	} {
		resp := runResults(t, &fakeChanges{readoutErr: tc.err})
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, tc.want, tc.err.Reason+tc.err.Code)
	}
}
