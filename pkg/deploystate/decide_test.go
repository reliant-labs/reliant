package deploystate

import (
	"context"
	"testing"
	"time"
)

func drifting() Record {
	since := testNow.Add(-time.Hour)
	return Record{
		Key:         Key{Env: "prod", Provider: "k8s", Service: "api"},
		Desired:     Desired{Digest: "sha256:declared"},
		Observed:    Observation{Digest: "sha256:running", Measured: true, Serving: true},
		Owned:       []Field{FieldDigest},
		StableSince: &since,
	}
}

// TestDecideObserveNeverConverges is THE policy control. Real drift, a
// backend that knows about it, and the default policy — and forge must
// not propose a write.
func TestDecideObserveNeverConverges(t *testing.T) {
	d := Decide(drifting(), PolicyObserve, testNow, DefaultStabilityWindow)

	if d.State != StateDiverged {
		t.Fatalf("State = %v, want StateDiverged (the drift must still be DETECTED)", d.State)
	}
	if d.Action != ActionReport {
		t.Fatalf("Action = %v, want ActionReport; observe must never converge", d.Action)
	}
	if len(d.Drifted) != 1 || d.Drifted[0] != FieldDigest {
		t.Fatalf("Drifted = %v, want [digest]", d.Drifted)
	}
}

// TestDecideZeroPolicyNeverConverges: the same guarantee through the
// zero value, i.e. a caller that forgot to pass a policy at all.
func TestDecideZeroPolicyNeverConverges(t *testing.T) {
	var forgotten Policy
	d := Decide(drifting(), forgotten, testNow, DefaultStabilityWindow)
	if d.Action == ActionConverge {
		t.Fatal("an unset policy produced ActionConverge")
	}
}

func TestDecideConvergeConverges(t *testing.T) {
	d := Decide(drifting(), PolicyConverge, testNow, DefaultStabilityWindow)
	if d.Action != ActionConverge {
		t.Fatalf("Action = %v, want ActionConverge under PolicyConverge", d.Action)
	}
}

// TestDecidePinnedReportsButNeverConverges: pinned keeps the SIGNAL and
// removes the write. Losing drift reporting during a freeze would remove
// the information at the moment it is most wanted.
func TestDecidePinnedReportsButNeverConverges(t *testing.T) {
	d := Decide(drifting(), PolicyPinned, testNow, DefaultStabilityWindow)

	if d.Action != ActionReport {
		t.Fatalf("Action = %v, want ActionReport", d.Action)
	}
	if d.State != StateDiverged {
		t.Fatalf("State = %v, want StateDiverged: pinned must not suppress detection", d.State)
	}
	if len(d.Drifted) == 0 {
		t.Fatal("pinned suppressed the drifted-field list; the signal must survive the freeze")
	}
}

// TestDecideDegradedNeverConvergesEvenUnderConverge is the crash-loop
// case at the decision layer. Re-applying an identical declaration onto
// a broken workload produces an identical breakage, and does it while
// erasing whatever an engineer changed to diagnose it.
func TestDecideDegradedNeverConvergesEvenUnderConverge(t *testing.T) {
	rec := drifting()
	rec.Observed.Serving = false

	d := Decide(rec, PolicyConverge, testNow, DefaultStabilityWindow)
	if d.State != StateDegraded {
		t.Fatalf("State = %v, want StateDegraded", d.State)
	}
	if d.Action != ActionReport {
		t.Fatalf("Action = %v, want ActionReport; a broken target must not be re-applied automatically", d.Action)
	}
}

// TestDecideUnknownNeverConverges: an unobservable target has no
// measured reality to correct toward, so proposing a write would be
// acting on a wish.
func TestDecideUnknownNeverConverges(t *testing.T) {
	rec := drifting()
	rec.Observed.Measured = false

	d := Decide(rec, PolicyConverge, testNow, DefaultStabilityWindow)
	if d.State != StateUnknown {
		t.Fatalf("State = %v, want StateUnknown", d.State)
	}
	if d.Action != ActionNone {
		t.Fatalf("Action = %v, want ActionNone for an unobservable target", d.Action)
	}
}

func TestDecideConvergingWaits(t *testing.T) {
	rec := serving(DefaultStabilityWindow / 2)

	d := Decide(rec, PolicyConverge, testNow, DefaultStabilityWindow)
	if d.State != StateConverging {
		t.Fatalf("State = %v, want StateConverging", d.State)
	}
	if d.Action != ActionWait {
		t.Fatalf("Action = %v, want ActionWait; the stability clock is still running", d.Action)
	}
}

func TestDecideConvergedDoesNothing(t *testing.T) {
	d := Decide(serving(time.Hour), PolicyConverge, testNow, DefaultStabilityWindow)
	if d.State != StateConverged || d.Action != ActionNone {
		t.Fatalf("State/Action = %v/%v, want converged/none", d.State, d.Action)
	}
}

// TestDecideUnownedDriftIsNotActedOn joins the two controls: an
// autoscaler moved a field forge does not own, under the most permissive
// policy there is. Nothing should happen.
func TestDecideUnownedDriftIsNotActedOn(t *testing.T) {
	rec := serving(time.Hour)
	rec.Desired.Replicas = intp(3)
	rec.Observed.Replicas = intp(11)
	rec.Owned = []Field{FieldDigest}

	d := Decide(rec, PolicyConverge, testNow, DefaultStabilityWindow)
	if d.Action != ActionNone {
		t.Fatalf("Action = %v, want ActionNone: an unowned difference is not drift", d.Action)
	}
	if d.State != StateConverged {
		t.Fatalf("State = %v, want StateConverged", d.State)
	}
}

// TestDecideEnvReadsPolicyEveryCall is the end-to-end form of the
// instant-opt-out guarantee, through the Store interface rather than
// through Local's file. The policy changes between two DecideEnv calls
// on the SAME store and the SAME records, and the second call must
// honour it.
func TestDecideEnvReadsPolicyEveryCall(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	if err := store.Put(ctx, drifting()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.SetPolicy(ctx, "prod", PolicyConverge); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	first, err := DecideEnv(ctx, store, "prod", testNow, DefaultStabilityWindow)
	if err != nil {
		t.Fatalf("DecideEnv: %v", err)
	}
	if len(first) != 1 || first[0].Action != ActionConverge {
		t.Fatalf("first pass = %+v, want a single ActionConverge", first)
	}

	// The engineer pins it mid-incident. No restart, no deploy.
	if err := store.SetPolicy(ctx, "prod", PolicyPinned); err != nil {
		t.Fatalf("SetPolicy pinned: %v", err)
	}

	second, err := DecideEnv(ctx, store, "prod", testNow, DefaultStabilityWindow)
	if err != nil {
		t.Fatalf("DecideEnv after pin: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second pass returned %d decisions, want 1", len(second))
	}
	if second[0].Action != ActionReport {
		t.Fatalf("second pass Action = %v, want ActionReport; the policy change did not take effect "+
			"on the very next pass", second[0].Action)
	}
	if second[0].Policy != PolicyPinned {
		t.Fatalf("second pass Policy = %v, want PolicyPinned", second[0].Policy)
	}
}

// failingPolicyStore fails only the policy read, to pin the direction of
// that failure.
type failingPolicyStore struct{ Store }

func (f failingPolicyStore) Policy(context.Context, string) (Policy, error) {
	return PolicyConverge, errFakePolicy
}

var errFakePolicy = &policyReadError{}

type policyReadError struct{}

func (*policyReadError) Error() string { return "backend down" }

// TestDecideEnvAbortsWhenPolicyUnreadable: a backend outage must not
// silently re-enable convergence on an environment somebody pinned. The
// whole environment is abandoned for this pass rather than defaulted.
func TestDecideEnvAbortsWhenPolicyUnreadable(t *testing.T) {
	ctx := context.Background()
	store := failingPolicyStore{Store: NewLocal(t.TempDir())}

	got, err := DecideEnv(ctx, store, "prod", testNow, DefaultStabilityWindow)
	if err == nil {
		t.Fatal("DecideEnv succeeded despite an unreadable policy")
	}
	if got != nil {
		t.Fatalf("DecideEnv returned %+v alongside an error; a pass with no known policy must decide nothing", got)
	}
}

func TestActionStrings(t *testing.T) {
	cases := map[Action]string{
		ActionNone:     "none",
		ActionReport:   "report",
		ActionConverge: "converge",
		ActionWait:     "wait",
	}
	for a, want := range cases {
		if a.String() != want {
			t.Errorf("Action(%d).String() = %q, want %q", a, a.String(), want)
		}
	}
}
