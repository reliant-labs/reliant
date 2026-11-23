package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func fastAdmissionProbe(t *testing.T, timeout time.Duration) {
	t.Helper()
	poll, to := admissionProbePoll, admissionProbeTimeout
	admissionProbePoll, admissionProbeTimeout = 10*time.Millisecond, timeout
	t.Cleanup(func() { admissionProbePoll, admissionProbeTimeout = poll, to })
}

// TestApply_SecretStoreWaitsForTheAdmissionPolicyToTakeEffect is the SC7
// rehearsal failure (defect D1). The ESO chart's eso-store-pin policy lists
// the servers a SecretStore may dial, derived from this release's stores. On
// the release that introduces the first store the previous list is empty and
// policy updates are enforced asynchronously, so the first SecretStore was
// refused ("denied request") on both adoptions and the deploy failed.
//
// The fake refuses the first two requests that carry a SecretStore, as the
// previous policy does; the apply must retry through the lag and apply every
// store only once the API server admits it.
//
// Mutation that fails it: drop awaitAdmission from applySecretStages, and the
// first real SecretStore apply is the refused one.
func TestApply_SecretStoreWaitsForTheAdmissionPolicyToTakeEffect(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fastSecretPoll(t)
	fastAdmissionProbe(t, 2*time.Minute)
	calls := fakeGateKubectl(t, jobCompletes, "admin-server")
	fakeES(t, esJSON(1, 1, "True", "SecretSynced"))
	t.Setenv("FAKE_SS_DENY", "2")

	if err := applyRendered(context.Background(), gateOpts(RolloutWait), storeStream()); err != nil {
		t.Fatalf("a policy that is merely late must not fail the deploy: %v", err)
	}
	got := calls()
	isProbe := func(c kubectlCall) bool {
		return strings.Contains(c.Args, "--dry-run=server") && strings.Contains(c.Stdin, "kind: SecretStore")
	}
	probes := 0
	lastProbe := -1
	for i, c := range got {
		if isProbe(c) {
			probes++
			lastProbe = i
		}
	}
	if probes < 2+admissionProbeAgree {
		t.Errorf("%d probes: two were refused and %d consecutive admits are required\ncalls:\n%s", probes, admissionProbeAgree, describeCalls(got))
	}
	realStore := callIndex(got, func(c kubectlCall) bool {
		return c.IsApply() && strings.Contains(c.Stdin, "kind: SecretStore")
	})
	if realStore < 0 || realStore < lastProbe {
		t.Errorf("the SecretStore was applied (call %d) before the API server admitted a dry run of it (call %d)\ncalls:\n%s", realStore, lastProbe, describeCalls(got))
	}
	bootstrap := callIndex(got, func(c kubectlCall) bool { return c.IsApply() && strings.Contains(c.Stdin, "name: openbao-unseal") })
	if bootstrap < lastProbe {
		t.Errorf("the bootstrap ExternalSecret was applied (call %d) before admission was proven (call %d)", bootstrap, lastProbe)
	}
	for _, c := range got {
		if strings.Contains(c.Args, "--dry-run") && c.Stdin != "" && !strings.Contains(c.Stdin, "kind: SecretStore") && !strings.Contains(c.Stdin, "kind: ExternalSecret") {
			t.Errorf("the probe must carry only the ESO kinds the policies judge:\n%s", c.Stdin)
		}
	}
}

// TestApply_AdmissionRefusalThatNeverClearsIsAVerdict pins the other half: a
// policy that genuinely does not list the store is not lag. The deploy stops
// before any store, ExternalSecret or workload is applied, and says so.
func TestApply_AdmissionRefusalThatNeverClearsIsAVerdict(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fastAdmissionProbe(t, 300*time.Millisecond)
	calls := fakeGateKubectl(t, jobCompletes, "admin-server")
	t.Setenv("FAKE_SS_DENY", "1000000")

	err := applyRendered(context.Background(), gateOpts(RolloutWait), storeStream())
	if !errors.Is(err, ErrAdmissionNotEffective) {
		t.Fatalf("want ErrAdmissionNotEffective, got %v", err)
	}
	if !strings.Contains(err.Error(), "eso-store-pin") {
		t.Errorf("the error must carry the policy's own message: %v", err)
	}
	for _, c := range calls() {
		if c.IsApply() && (strings.Contains(c.Stdin, "kind: SecretStore") || strings.Contains(c.Stdin, "name: openbao-unseal") || strings.Contains(c.Stdin, "kind: Deployment") || strings.Contains(c.Stdin, "kind: StatefulSet")) {
			t.Fatalf("nothing past the admission barrier may be applied after a standing refusal:\n%s", c.Stdin)
		}
	}
}

// TestAdmissionRefusal pins what counts as lag: a policy denial and nothing else.
func TestAdmissionRefusal(t *testing.T) {
	for stderr, want := range map[string]bool{
		`Error from server (Forbidden): secretstores... ValidatingAdmissionPolicy 'eso-store-pin' with binding 'eso-store-pin' denied request: x`: true,
		`Error from server (NotFound): namespaces "x" not found`:                                                                                  false,
		`error: no matches for kind "SecretStore"`:                                                                                                false,
		``: false,
	} {
		if got := admissionRefusal(stderr); got != want {
			t.Errorf("admissionRefusal(%q) = %v, want %v", stderr, got, want)
		}
	}
}
