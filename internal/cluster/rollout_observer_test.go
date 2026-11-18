package cluster

import (
	"context"
	"errors"
	"testing"
)

// TestClassifyWaitError_TimeoutIsNeitherSuccessNorFailure is the source of the
// three-way distinction the whole deploy report is built on.
//
// kubectl's own "timed out waiting for the condition" is the ONLY evidence that
// separates "we stopped looking" from "we saw it break", and it exists nowhere
// except the wait's own output — which is why the classification has to happen
// here rather than being reconstructed by a caller from an aggregate error.
func TestClassifyWaitError_TimeoutIsNeitherSuccessNorFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		output string
		want   RolloutState
	}{
		{
			name: "nil error is ready",
			err:  nil, output: "deployment \"api\" successfully rolled out",
			want: RolloutStateReady,
		},
		{
			name: "kubectl's timeout message means the budget expired",
			err:  errors.New("exit status 1"),
			output: "Waiting for deployment \"api\" rollout to finish: 0 of 1 updated replicas are available...\n" +
				"error: timed out waiting for the condition",
			want: RolloutStateTimedOut,
		},
		{
			name:   "the message is matched case-insensitively",
			err:    errors.New("exit status 1"),
			output: "Error: TIMED OUT WAITING FOR THE CONDITION",
			want:   RolloutStateTimedOut,
		},
		{
			name: "a cancelled context is a non-answer, not a failure",
			err:  context.Canceled, output: "",
			want: RolloutStateTimedOut,
		},
		{
			name: "a deadline-exceeded context is a non-answer too",
			err:  context.DeadlineExceeded, output: "",
			want: RolloutStateTimedOut,
		},
		{
			// Graded a FAILURE deliberately. Defaulting an unattributable
			// error to timed_out would look safer and is wrong: it would
			// quietly downgrade real, diagnosable failures into "unknown" and
			// callers would stop treating them as failures.
			name:   "an unattributable error is a FAILURE, not an unknown",
			err:    errors.New("exit status 1"),
			output: "Error from server (NotFound): deployments.apps \"api\" not found",
			want:   RolloutStateFailed,
		},
		{
			name:   "a wrapped context deadline is still recognised",
			err:    errors.Join(errors.New("waiting for api"), context.DeadlineExceeded),
			output: "",
			want:   RolloutStateTimedOut,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWaitError(tc.err, tc.output); got != tc.want {
				t.Errorf("classifyWaitError = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestRolloutState_ZeroValueIsUnknown pins the zero-value discipline at this
// end too: an unreported resource must never read as ready.
func TestRolloutState_ZeroValueIsUnknown(t *testing.T) {
	var state RolloutState
	if state != RolloutStateUnknown {
		t.Errorf("zero RolloutState = %s, want unknown", state)
	}
	if state == RolloutStateReady {
		t.Fatal("the zero value must not be ready — that would make silence read as health")
	}
	if got := state.String(); got != "unknown" {
		t.Errorf("String() = %q, want %q", got, "unknown")
	}
}

// TestObserveRollout_NilCallbackIsANoOp is the additive-by-construction
// guarantee: with no observer installed, every existing Apply path behaves
// exactly as before.
func TestObserveRollout_NilCallbackIsANoOp(t *testing.T) {
	// No callback, no panic — this is precisely what text-mode deploy does.
	observeRollout(ApplyOpts{}, "Deployment", "api", RolloutStateReady, nil)
}

// TestObserveRollout_ForwardsTheObservation confirms the observation reaches an
// installed callback intact, error included.
func TestObserveRollout_ForwardsTheObservation(t *testing.T) {
	var got []RolloutObservation
	opts := ApplyOpts{OnRollout: func(o RolloutObservation) { got = append(got, o) }}

	wantErr := errors.New("timed out waiting for the condition")
	observeRollout(opts, "Deployment", "web", RolloutStateTimedOut, wantErr)
	observeRollout(opts, "Job", "migrate", RolloutStateFailed, errors.New("job migrate failed"))

	if len(got) != 2 {
		t.Fatalf("want 2 observations, got %d", len(got))
	}
	if got[0].Kind != "Deployment" || got[0].Name != "web" || got[0].State != RolloutStateTimedOut {
		t.Errorf("first observation = %+v", got[0])
	}
	if !errors.Is(got[0].Err, wantErr) {
		t.Errorf("the underlying wait error must be carried, got %v", got[0].Err)
	}
	// A Job that reported condition=failed is a positively-observed failure,
	// materially different from one whose budget expired while still running.
	if got[1].State != RolloutStateFailed {
		t.Errorf("second observation state = %s, want failed", got[1].State)
	}
}

// TestChartStreams_MatchesDryRunFoldOrder pins that an observing caller and the
// human dry run are shown the same stream. Two arrangements of the same
// documents would make `--dry-run --json` an untrustworthy preview of what a
// real apply reports.
func TestChartStreams_MatchesDryRunFoldOrder(t *testing.T) {
	charts := []renderedChart{
		{crds: "kind: CustomResourceDefinition", manifests: "kind: Deployment", extra: "kind: ClusterIssuer"},
	}
	got := chartStreams(charts)
	want := []string{"kind: CustomResourceDefinition", "kind: Deployment", "kind: ClusterIssuer"}
	if len(got) != len(want) {
		t.Fatalf("want %d parts, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %q, want %q (CRDs, then manifests, then riding extras)", i, got[i], want[i])
		}
	}
}

// TestApplyOpts_ObserversDefaultToNil confirms the hooks are opt-in, so an
// ApplyOpts written before this feature is unaffected.
func TestApplyOpts_ObserversDefaultToNil(t *testing.T) {
	var opts ApplyOpts
	if opts.OnStream != nil {
		t.Error("OnStream must default to nil")
	}
	if opts.OnRollout != nil {
		t.Error("OnRollout must default to nil")
	}
}
