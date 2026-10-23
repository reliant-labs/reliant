package cluster

import (
	"context"
	"strings"
	"testing"
)

// operatorStream is a RenderedWorkload that ships an operator's CRD together
// with an instance of it and the operator itself — the shape a plain
// manifest stream carries (control-plane's vendored CNPG render is the CRD +
// controller half of it). Render order deliberately puts the instance BEFORE
// the CRD: nothing about the stream's order may be what makes it safe.
const operatorStream = `apiVersion: v1
kind: Namespace
metadata:
  name: widgets-system
---
apiVersion: example.com/v1
kind: Widget
metadata:
  name: default
  namespace: widgets-system
spec: {}
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: widget-operator-config
  namespace: widgets-system
data: {}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: widget-operator
  namespace: widgets-system
spec: {}`

func crdFirstOpts() ApplyOpts {
	return ApplyOpts{
		Namespace: "widgets-system",
		Context:   "k3d-test",
		Rollout:   RolloutPolicy{Mode: RolloutSkip},
	}
}

// TestApplyRendered_CRDsEstablishBeforeAnythingThatUsesThem is D3: the env's
// own manifest stream must apply its CRDs first and wait for them to be
// Established before sending anything else, exactly as the chart path does.
//
// On the pre-fix apply there is no Established wait at all and the CRD rides
// the same apply as the Widget that instantiates it.
func TestApplyRendered_CRDsEstablishBeforeAnythingThatUsesThem(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)
	if err := applyRendered(context.Background(), crdFirstOpts(), operatorStream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	calls := readCalls()

	crdApply := firstIndex(calls, appliesKind("CustomResourceDefinition"))
	wait := firstIndex(calls, func(c kubectlCall) bool {
		return strings.Contains(c.Args, "--for=condition=Established") && strings.Contains(c.Args, "crd/widgets.example.com")
	})
	instance := firstIndex(calls, appliesKind("Widget"))
	if crdApply < 0 || wait < 0 || instance < 0 {
		t.Fatalf("missing a step (crd apply=%d, established wait=%d, instance apply=%d):\n%v", crdApply, wait, instance, calls)
	}
	if !(crdApply < wait && wait < instance) {
		t.Errorf("order: crd apply=%d, established wait=%d, instance apply=%d — the instance must follow the wait", crdApply, wait, instance)
	}
	// The CRD's apply carries nothing that depends on it.
	for _, kind := range []string{"Widget", "Deployment", "ConfigMap"} {
		if strings.Contains(calls[crdApply].Stdin, "\nkind: "+kind+"\n") {
			t.Errorf("%s rode the CRD's early batch:\n%s", kind, calls[crdApply].Stdin)
		}
	}
	// The Namespace lands in the early batch with the CRD, so the config pass
	// (the namespaced ConfigMap) never precedes it.
	if !strings.Contains(calls[crdApply].Stdin, "\nkind: Namespace\n") {
		t.Errorf("the Namespace did not ride the early batch:\n%s", calls[crdApply].Stdin)
	}
	if cfg := firstIndex(calls, appliesKind("ConfigMap")); cfg < wait {
		t.Errorf("config pass (%d) ran before the CRD was Established (%d)", cfg, wait)
	}
}

// TestApplyRendered_NoCRDKeepsTheHistoricalPasses: a stream without a CRD is
// applied exactly as before — no early batch, no Established wait, the
// Namespace in the config pass.
func TestApplyRendered_NoCRDKeepsTheHistoricalPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeKubectlRecorder(t)
	stream := strings.Replace(operatorStream, `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
---
`, "", 1)
	if err := applyRendered(context.Background(), crdFirstOpts(), stream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	calls := readCalls()
	for _, c := range calls {
		if strings.Contains(c.Args, "Established") {
			t.Errorf("a CRD-free stream waited for Established: %s", c.Args)
		}
	}
	applies := applies(calls)
	if len(applies) != 2 {
		t.Fatalf("got %d applies, want the historical 2 (config, rest):\n%v", len(applies), applies)
	}
	if !strings.Contains(applies[0].Stdin, "\nkind: Namespace\n") || !strings.Contains(applies[0].Stdin, "\nkind: ConfigMap\n") {
		t.Errorf("first apply is not the config pass:\n%s", applies[0].Stdin)
	}
}
