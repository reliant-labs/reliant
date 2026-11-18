package cluster

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestRidingFluxInstancesNamesEachInstance: the FluxInstance riding the Flux
// Operator chart is what `forge cluster up` waits on before calling the chart
// installed. The operator installs Flux's controllers from it after the apply
// returns; forge used to return first, and an env that declared
// `forge.flux_chart()` came up with flux-system holding only the operator
// (TestE2EClusterUpInstallsADeclaredFluxChart failed 13 main runs in 5 days).
func TestRidingFluxInstancesNamesEachInstance(t *testing.T) {
	manifests := `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: flux-controller-bindings
---
apiVersion: fluxcd.controlplane.io/v1
kind: FluxInstance
metadata:
  name: flux
spec: {}
---
apiVersion: fluxcd.controlplane.io/v1
kind: FluxInstance
metadata:
  name: other
  namespace: elsewhere
spec: {}
---
apiVersion: fluxcd.controlplane.io/v1
kind: FluxReport
metadata:
  name: flux
`
	got := ridingFluxInstances(manifests, "flux-system")
	want := []namespacedName{{namespace: "flux-system", name: "flux"}, {namespace: "elsewhere", name: "other"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ridingFluxInstances = %+v, want %+v", got, want)
	}
	if got := ridingFluxInstances("apiVersion: cert-manager.io/v1\nkind: ClusterIssuer\nmetadata:\n  name: le\n", "cert-manager"); len(got) != 0 {
		t.Fatalf("a chart with no FluxInstance waits on nothing, got %+v", got)
	}
}

// TestWaitFluxInstanceReady_Command pins the wait's kubectl invocation.
func TestWaitFluxInstanceReady_Command(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through a shell subprocess; runs in task test")
	}
	logPath := fakeKubectlLog(t)
	if err := waitFluxInstanceReady(context.Background(), "k3d-test", "flux-system", "flux", time.Minute); err != nil {
		t.Fatalf("waitFluxInstanceReady: %v", err)
	}
	data, _ := os.ReadFile(logPath)
	got := strings.TrimSpace(string(data))
	for _, want := range []string{"wait", "--for=condition=Ready", "fluxinstance/flux", "-n flux-system", "--timeout=1m0s"} {
		if !strings.Contains(got, want) {
			t.Errorf("wait command missing %q; got %q", want, got)
		}
	}
}
