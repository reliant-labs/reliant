package cluster

import (
	"strings"
	"testing"
	"time"
)

// TestRolloutPolicyNormalize pins the SAFE DEFAULT.
//
// This is the whole point of the type: a caller that leaves Rollout unset
// must get wait-and-fail, not the old warn-and-exit-0. If this test ever
// goes green with Mode == RolloutWarn, `forge env deploy` is back to
// reporting success over a broken environment.
func TestRolloutPolicyNormalize(t *testing.T) {
	got := RolloutPolicy{}.Normalize()
	if got.Mode != RolloutWait {
		t.Errorf("zero-value Mode = %q, want %q — an unset policy MUST fail on a bad rollout", got.Mode, RolloutWait)
	}
	if got.Timeout != DefaultRolloutTimeout {
		t.Errorf("zero-value Timeout = %v, want %v", got.Timeout, DefaultRolloutTimeout)
	}
	if got.FailFast {
		t.Error("zero-value FailFast = true; the default should report EVERY failure, not just the first")
	}

	// An explicit choice is preserved.
	custom := RolloutPolicy{Mode: RolloutWarn, Timeout: 90 * time.Second, FailFast: true}.Normalize()
	if custom.Mode != RolloutWarn || custom.Timeout != 90*time.Second || !custom.FailFast {
		t.Errorf("Normalize clobbered an explicit policy: %+v", custom)
	}

	// A nonsensical timeout falls back rather than passing --timeout=0s
	// to kubectl, which means "wait forever" and would hang a deploy.
	if z := (RolloutPolicy{Timeout: -1}).Normalize().Timeout; z != DefaultRolloutTimeout {
		t.Errorf("negative Timeout normalized to %v, want %v", z, DefaultRolloutTimeout)
	}
}

// TestRolloutPolicyValidate pins that a typo is REJECTED rather than
// silently falling back to a default the caller did not ask for. The
// failure class this whole type exists to remove is "the wrong behavior
// was chosen quietly", and `--rollout=wan` silently meaning `wait` would
// be a fresh instance of exactly that.
func TestRolloutPolicyValidate(t *testing.T) {
	for _, mode := range []RolloutMode{"", RolloutWait, RolloutWarn, RolloutSkip} {
		if err := (RolloutPolicy{Mode: mode}).Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", mode, err)
		}
	}

	err := RolloutPolicy{Mode: "wan"}.Validate()
	if err == nil {
		t.Fatal("a typo'd mode was accepted; it must be rejected")
	}
	// The message has to name the valid values — an error that says only
	// "invalid" makes the caller go read the source.
	for _, want := range []string{"wan", "wait", "warn", "skip"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// TestDefaultRolloutTimeoutIsCloudRealistic guards the value itself.
//
// The previous hardcoded 60s was a local-k3d number applied to cloud: a
// cold rollout that pulls a fresh image on a new node routinely exceeds a
// minute, so it failed exactly the deploys that mattered most.
func TestDefaultRolloutTimeoutIsCloudRealistic(t *testing.T) {
	if DefaultRolloutTimeout < 2*time.Minute {
		t.Errorf("DefaultRolloutTimeout = %v — too short for a cold cloud rollout that pulls an image", DefaultRolloutTimeout)
	}
}

// orderNames runs names through the wait's real ordering, orderWaitQueue, as
// the Deployments of one cluster.
func orderNames(p RolloutPolicy, names []string) []string {
	w := &rolloutWait{p: &PendingRollout{policy: p}}
	queue := make([]deploymentWait, len(names))
	for i, n := range names {
		queue[i] = deploymentWait{w: w, name: n}
	}
	orderWaitQueue(queue)
	out := make([]string, len(queue))
	for i, item := range queue {
		out[i] = item.name
	}
	return out
}

// TestRolloutOrderPutsNamedFirst pins the phased-wait ordering: named
// resources are reported first, in the caller's order, and everything
// else keeps its natural order behind them.
func TestRolloutOrderPutsNamedFirst(t *testing.T) {
	all := []string{"web", "worker", "api", "migrate"}

	got := orderNames(RolloutPolicy{Order: []string{"migrate", "api"}}, all)
	want := []string{"migrate", "api", "web", "worker"}
	if !equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}

	// A name that is not in the namespace (e.g. excluded by --target) is
	// skipped rather than erroring or inserting a phantom entry.
	got = orderNames(RolloutPolicy{Order: []string{"nope", "api"}}, all)
	want = []string{"api", "web", "worker", "migrate"}
	if !equal(got, want) {
		t.Errorf("unknown name in Order changed the set: got %v, want %v", got, want)
	}

	// No ordering is a pure pass-through — same slice, same order.
	if got := orderNames(RolloutPolicy{}, all); !equal(got, all) {
		t.Errorf("empty Order reordered: got %v, want %v", got, all)
	}

	// Every input must survive: an ordering must not DROP a resource, or
	// the deploy would silently stop waiting on it.
	got = orderNames(RolloutPolicy{Order: []string{"worker"}}, all)
	if len(got) != len(all) {
		t.Errorf("order dropped resources: got %v from %v", got, all)
	}
}

// TestRolloutErrorNamesEveryFailure pins the message. A deploy that fails
// should say WHICH resources failed — the shell gate this replaces printed
// them, and losing that would make forge's version worse than the thing it
// replaced.
func TestRolloutErrorNamesEveryFailure(t *testing.T) {
	if err := rolloutError(nil); err != nil {
		t.Errorf("rolloutError(nil) = %v, want nil", err)
	}

	one := rolloutError([]string{"api"})
	if one == nil || !strings.Contains(one.Error(), "api") {
		t.Errorf("single failure must name the resource; got %v", one)
	}

	many := rolloutError([]string{"api", "worker", "web"})
	if many == nil {
		t.Fatal("multiple failures returned nil")
	}
	for _, name := range []string{"api", "worker", "web"} {
		if !strings.Contains(many.Error(), name) {
			t.Errorf("error %q omits failed resource %q", many, name)
		}
	}
	if !strings.Contains(many.Error(), "3") {
		t.Errorf("error %q should state how many failed", many)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
