package deploystate

import (
	"testing"
	"time"
)

func intp(v int) *int { return &v }

var testNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// serving builds a record that is measured, serving, digest-matching and
// owned — the baseline each test perturbs in exactly one way.
func serving(stableFor time.Duration) Record {
	since := testNow.Add(-stableFor)
	return Record{
		Key:         Key{Env: "prod", Provider: "k8s", Service: "api"},
		Desired:     Desired{Digest: "sha256:aaa"},
		Observed:    Observation{Digest: "sha256:aaa", Measured: true, Serving: true},
		Owned:       []Field{FieldDigest},
		StableSince: &since,
	}
}

// TestStateZeroValueIsUnknown pins the same guarantee as the policy zero
// value, for the other enum: a Record built by any path reads unknown.
func TestStateZeroValueIsUnknown(t *testing.T) {
	var s State
	if s != StateUnknown {
		t.Fatalf("zero State = %v, want StateUnknown", s)
	}
	var rec Record
	if got := rec.Evaluate(testNow, DefaultStabilityWindow); got != StateUnknown {
		t.Fatalf("zero Record evaluates to %v; an unfilled record must never read as green", got)
	}
}

// TestEvaluateUnmeasuredIsUnknownEvenWhenDesiredLooksPerfect is the
// opaque-External case. The desired half being immaculate proves nothing
// — desired state is a wish.
func TestEvaluateUnmeasuredIsUnknownEvenWhenDesiredLooksPerfect(t *testing.T) {
	rec := serving(time.Hour)
	rec.Observed.Measured = false

	if got := rec.Evaluate(testNow, DefaultStabilityWindow); got != StateUnknown {
		t.Fatalf("Evaluate = %v, want StateUnknown for an unobservable target", got)
	}
}

// TestEvaluateNotServingBeatsMatching is the crash-loop case: an exact
// digest match onto a workload that is not serving is DEGRADED, never
// converged. This is the check that stops "the apply succeeded" being
// reported as a successful promotion.
func TestEvaluateNotServingBeatsMatching(t *testing.T) {
	rec := serving(time.Hour)
	rec.Observed.Serving = false

	if got := rec.Evaluate(testNow, DefaultStabilityWindow); got != StateDegraded {
		t.Fatalf("Evaluate = %v, want StateDegraded: a perfect match on a non-serving target is not converged", got)
	}
}

func TestEvaluateStabilityWindow(t *testing.T) {
	window := DefaultStabilityWindow

	young := serving(window / 2)
	if got := young.Evaluate(testNow, window); got != StateConverging {
		t.Fatalf("inside the window: Evaluate = %v, want StateConverging", got)
	}

	old := serving(window * 2)
	if got := old.Evaluate(testNow, window); got != StateConverged {
		t.Fatalf("past the window: Evaluate = %v, want StateConverged", got)
	}

	// Exactly at the boundary counts as converged (>= window).
	exact := serving(window)
	if got := exact.Evaluate(testNow, window); got != StateConverged {
		t.Fatalf("at the boundary: Evaluate = %v, want StateConverged", got)
	}
}

// TestEvaluateMatchingWithNoStableSinceIsConverging: a target that has
// never been stamped has not demonstrated stability, so it cannot be
// converged regardless of how good the snapshot looks.
func TestEvaluateMatchingWithNoStableSinceIsConverging(t *testing.T) {
	rec := serving(time.Hour)
	rec.StableSince = nil

	if got := rec.Evaluate(testNow, DefaultStabilityWindow); got != StateConverging {
		t.Fatalf("Evaluate = %v, want StateConverging when stability was never established", got)
	}
}

func TestEvaluateDigestDrift(t *testing.T) {
	rec := serving(time.Hour)
	rec.Observed.Digest = "sha256:bbb"

	if got := rec.Evaluate(testNow, DefaultStabilityWindow); got != StateDiverged {
		t.Fatalf("Evaluate = %v, want StateDiverged", got)
	}
	drifted := rec.DriftedFields()
	if len(drifted) != 1 || drifted[0] != FieldDigest {
		t.Fatalf("DriftedFields = %v, want [digest]", drifted)
	}
}

// TestUnownedFieldIsNotDrift is the autoscaler case, and the reason the
// Owned list exists at all. Replicas moved from 3 to 7 and forge does
// not own replicas, so this is NOT drift — flagging it is what teaches
// people to ignore the badge.
func TestUnownedFieldIsNotDrift(t *testing.T) {
	rec := serving(time.Hour)
	rec.Desired.Replicas = intp(3)
	rec.Observed.Replicas = intp(7)
	rec.Owned = []Field{FieldDigest} // replicas deliberately NOT owned

	if drifted := rec.DriftedFields(); len(drifted) != 0 {
		t.Fatalf("DriftedFields = %v, want none: replicas are not forge's to own here", drifted)
	}
	if got := rec.Evaluate(testNow, DefaultStabilityWindow); got != StateConverged {
		t.Fatalf("Evaluate = %v, want StateConverged: an unowned difference is not drift", got)
	}

	// The SAME record, with replicas owned, must report drift — otherwise
	// the test above would pass against an implementation that simply
	// never compares replicas, which would prove nothing.
	rec.Owned = []Field{FieldDigest, FieldReplicas}
	drifted := rec.DriftedFields()
	if len(drifted) != 1 || drifted[0] != FieldReplicas {
		t.Fatalf("with replicas owned: DriftedFields = %v, want [replicas]", drifted)
	}
}

// TestEmptyOwnedReportsNoDrift pins the DIRECTION of the default. A
// record whose Owned was never populated reports nothing rather than
// reporting everything.
func TestEmptyOwnedReportsNoDrift(t *testing.T) {
	rec := serving(time.Hour)
	rec.Observed.Digest = "sha256:totally-different"
	rec.Desired.Replicas = intp(1)
	rec.Observed.Replicas = intp(99)
	rec.Owned = nil

	if drifted := rec.DriftedFields(); len(drifted) != 0 {
		t.Fatalf("DriftedFields = %v, want none when nothing is owned", drifted)
	}
}

// TestUndeterminedValuesAreNotDrift covers the mutable-tag case: an
// image pinned by tag yields an empty desired digest, and an observer
// that cannot resolve one yields an empty observed digest. Neither is
// drift; both are legal and common.
func TestUndeterminedValuesAreNotDrift(t *testing.T) {
	noDesired := serving(time.Hour)
	noDesired.Desired.Digest = ""
	if drifted := noDesired.DriftedFields(); len(drifted) != 0 {
		t.Errorf("empty desired digest: DriftedFields = %v, want none", drifted)
	}

	noObserved := serving(time.Hour)
	noObserved.Observed.Digest = ""
	if drifted := noObserved.DriftedFields(); len(drifted) != 0 {
		t.Errorf("empty observed digest: DriftedFields = %v, want none", drifted)
	}

	nilReplicas := serving(time.Hour)
	nilReplicas.Owned = []Field{FieldReplicas}
	nilReplicas.Desired.Replicas = intp(3)
	nilReplicas.Observed.Replicas = nil // tier has no replica concept
	if drifted := nilReplicas.DriftedFields(); len(drifted) != 0 {
		t.Errorf("nil observed replicas: DriftedFields = %v, want none", drifted)
	}
}

// TestWithStabilityCarriesTheClockForward is the control that makes the
// stability window mean anything. A writer that re-stamps on every pass
// resets the clock forever and nothing is ever converged.
func TestWithStabilityCarriesTheClockForward(t *testing.T) {
	first := serving(0)
	first.StableSince = nil
	first = first.WithStability(nil, testNow)
	if first.StableSince == nil || !first.StableSince.Equal(testNow) {
		t.Fatalf("first good observation: StableSince = %v, want %v", first.StableSince, testNow)
	}

	// A second pass, a minute later, with the target unchanged. The
	// clock must NOT move.
	later := testNow.Add(time.Minute)
	second := serving(0)
	second.StableSince = nil
	second = second.WithStability(&first, later)
	if second.StableSince == nil || !second.StableSince.Equal(testNow) {
		t.Fatalf("unchanged target: StableSince = %v, want it carried forward as %v",
			second.StableSince, testNow)
	}
	if got := second.Evaluate(later, DefaultStabilityWindow); got != StateConverged {
		t.Fatalf("after a minute of stability: Evaluate = %v, want StateConverged", got)
	}
}

// TestWithStabilityResetsOnDegradation: the clock clears the moment the
// target leaves the good shape, so a flap cannot be papered over by an
// old timestamp.
func TestWithStabilityResetsOnDegradation(t *testing.T) {
	good := serving(time.Hour).WithStability(nil, testNow.Add(-time.Hour))

	bad := serving(0)
	bad.Observed.Serving = false
	bad = bad.WithStability(&good, testNow)
	if bad.StableSince != nil {
		t.Fatalf("StableSince = %v, want nil once the target stopped serving", bad.StableSince)
	}

	// And recovery re-stamps rather than restoring the old clock: a
	// target that just came back has not been stable for an hour.
	recovered := serving(0)
	recovered.StableSince = nil
	recovered = recovered.WithStability(&bad, testNow)
	if recovered.StableSince == nil || !recovered.StableSince.Equal(testNow) {
		t.Fatalf("after recovery: StableSince = %v, want a fresh %v", recovered.StableSince, testNow)
	}
	if got := recovered.Evaluate(testNow, DefaultStabilityWindow); got != StateConverging {
		t.Fatalf("a just-recovered target evaluates to %v, want StateConverging", got)
	}
}

// TestWithStabilityResetsOnNewDeclaration: shipping a new digest starts
// a new stability clock. Otherwise a deploy inherits the previous
// release's good standing and is reported converged before it has run
// for a second — which is precisely the promotion bug.
func TestWithStabilityResetsOnNewDeclaration(t *testing.T) {
	old := serving(time.Hour).WithStability(nil, testNow.Add(-time.Hour))

	fresh := Record{
		Key:      old.Key,
		Desired:  Desired{Digest: "sha256:new"},
		Observed: Observation{Digest: "sha256:new", Measured: true, Serving: true},
		Owned:    []Field{FieldDigest},
	}
	fresh = fresh.WithStability(&old, testNow)

	if fresh.StableSince == nil || !fresh.StableSince.Equal(testNow) {
		t.Fatalf("new declaration: StableSince = %v, want a fresh %v", fresh.StableSince, testNow)
	}
	if got := fresh.Evaluate(testNow, DefaultStabilityWindow); got != StateConverging {
		t.Fatalf("a just-shipped release evaluates to %v, want StateConverging", got)
	}
}

func TestStateStringsAndJSON(t *testing.T) {
	cases := map[State]string{
		StateUnknown:    "unknown",
		StateConverging: "converging",
		StateConverged:  "converged",
		StateDiverged:   "diverged",
		StateDegraded:   "degraded",
	}
	for s, want := range cases {
		if s.String() != want {
			t.Errorf("State(%d).String() = %q, want %q", s, s.String(), want)
		}
		data, err := s.MarshalJSON()
		if err != nil {
			t.Fatalf("marshal %v: %v", s, err)
		}
		var back State
		if err := back.UnmarshalJSON(data); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		if back != s {
			t.Errorf("round trip %v -> %s -> %v", s, data, back)
		}
	}
}

func TestKeyValid(t *testing.T) {
	good := Key{Env: "prod", Provider: "k8s", Service: "api"}
	if !good.Valid() {
		t.Fatal("complete key reported invalid")
	}
	for _, bad := range []Key{
		{Provider: "k8s", Service: "api"},
		{Env: "prod", Service: "api"},
		{Env: "prod", Provider: "k8s"},
		{},
	} {
		if bad.Valid() {
			t.Errorf("%+v reported valid; an empty segment collides on disk", bad)
		}
	}
}
