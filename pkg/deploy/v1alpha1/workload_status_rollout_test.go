package v1alpha1

import (
	"encoding/json"
	"testing"
)

// The rollout-completion test a consumer applies, stated once here so the
// field set is pinned to the question it has to answer rather than to one
// operator's implementation. It is the same test `kubectl rollout status`
// applies.
func rolloutComplete(s WorkloadStatus, generation int64) bool {
	return s.ObservedGeneration >= generation &&
		s.DesiredReplicas > 0 &&
		s.UpdatedReplicas >= s.DesiredReplicas &&
		s.ReadyReplicas >= s.DesiredReplicas
}

// WHY these fields exist: ReadyReplicas alone cannot distinguish a finished
// rollout from a failing one. Under RollingUpdate the OLD replicas stay
// ready while the new ones crash-loop, so ReadyReplicas ≥ desired holds the
// whole time — a gate reading only that field passes a release that never
// served a request. UpdatedReplicas is what goes to zero in exactly that
// case.
func TestWorkloadStatus_UpdatedReplicasDistinguishesAFailingRollout(t *testing.T) {
	const generation = 7

	// The trap: the new pod template is live, the old ReplicaSet is still
	// serving, and nothing on the NEW template is ready.
	crashing := WorkloadStatus{
		TierStatus:      TierStatus{ObservedGeneration: generation},
		ReadyReplicas:   2, // the OLD ReplicaSet
		UpdatedReplicas: 0, // the new one has nothing serving
		DesiredReplicas: 2,
	}
	if crashing.ReadyReplicas < crashing.DesiredReplicas {
		t.Fatal("precondition: the old replicas keep the ready count up — that is the whole trap")
	}
	if rolloutComplete(crashing, generation) {
		t.Fatal("a rollout whose new replicas are not serving must not read as complete")
	}

	// Mid-rollout, partially updated: still not complete.
	partial := crashing
	partial.UpdatedReplicas = 1
	if rolloutComplete(partial, generation) {
		t.Fatal("a partially updated rollout must not read as complete")
	}

	// Done: every replica is on the new template and ready.
	done := WorkloadStatus{
		TierStatus:      TierStatus{ObservedGeneration: generation},
		ReadyReplicas:   2,
		UpdatedReplicas: 2,
		DesiredReplicas: 2,
	}
	if !rolloutComplete(done, generation) {
		t.Fatal("a completed rollout must read as complete")
	}

	// A status describing a PREVIOUS generation is not evidence about this
	// one, however healthy its counts look.
	stale := done
	stale.ObservedGeneration = generation - 1
	if rolloutComplete(stale, generation) {
		t.Fatal("a status that lags the generation must not read as complete")
	}
}

// DesiredReplicas is reported rather than inferred from spec.replicas,
// because the two legitimately differ mid-scale; comparing against the spec
// in that window reads a normal scale-up as a stalled rollout.
func TestWorkloadStatus_DesiredReplicasIsReportedNotInferred(t *testing.T) {
	const generation = 3
	// spec.replicas was just raised to 4; the controller is still working
	// toward 2 and has fully updated them.
	scaling := WorkloadStatus{
		TierStatus:      TierStatus{ObservedGeneration: generation},
		ReadyReplicas:   2,
		UpdatedReplicas: 2,
		DesiredReplicas: 2,
	}
	if !rolloutComplete(scaling, generation) {
		t.Fatal("the rollout of this generation's template is complete against the count the controller reports")
	}
	if scaling.UpdatedReplicas >= 4 {
		t.Fatal("precondition: the spec's newer count is higher than what status reports")
	}
}

// Both fields are omitempty, so a status that reports neither serializes
// exactly as it did before they existed — the additive-change guarantee the
// control plane's pin bump relies on.
func TestWorkloadStatus_ReplicaFieldsAreOmitEmpty(t *testing.T) {
	raw, err := json.Marshal(WorkloadStatus{ReadyReplicas: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"updatedReplicas", "desiredReplicas"} {
		if got := string(raw); contains(got, absent) {
			t.Errorf("an unset %s must not serialize: %s", absent, got)
		}
	}

	raw, err = json.Marshal(WorkloadStatus{ReadyReplicas: 2, UpdatedReplicas: 2, DesiredReplicas: 2})
	if err != nil {
		t.Fatal(err)
	}
	var back WorkloadStatus
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.UpdatedReplicas != 2 || back.DesiredReplicas != 2 {
		t.Fatalf("replica fields lost on round trip: %+v", back)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
