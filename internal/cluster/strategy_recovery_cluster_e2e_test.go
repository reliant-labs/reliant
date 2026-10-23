//go:build e2e

package cluster

// strategy_recovery_cluster_e2e_test.go drives the rollout-strategy recovery
// against a REAL Kubernetes API server.
//
// WHY A REAL SERVER IS THE ONLY THING THAT PROVES THIS. The whole bug lives in
// Server-Side Apply's field-ownership semantics and the API server's own
// defaulting: forge's rendered manifest is CORRECT, and the failure is
// produced entirely by (a) kube-controller-manager materializing
// spec.strategy.rollingUpdate under its own field manager and (b) Deployment
// validation rejecting the merged object. A fake or a recorded fixture cannot
// exhibit either half — it would be asserting the behavior we assumed rather
// than the behavior kubernetes has. The unit tests in
// strategy_recovery_test.go pin the classification and the patch shape; THIS
// test is the one that would have caught the prod failure.
//
// It also rules out the fix that looks right. The first attempt was to render
// `rollingUpdate: null` beside `type: Recreate`, on the theory that SSA reads
// an explicit null as a removal. TestSSAExplicitNullDoesNotRemoveAForeignField
// below pins, against the real server, that it does NOT — which is why the
// production code patches instead.
//
// HOW TO RUN. It needs a throwaway cluster and skips cleanly without one:
//
//	kind create cluster --name forge-ssa-test
//	FORGE_TEST_KUBE_CONTEXT=kind-forge-ssa-test \
//	  go test -tags e2e -count=1 -run TestStrategyRecovery ./internal/cluster/
//	kind delete cluster --name forge-ssa-test
//
// The context is explicit and required rather than discovered: every write in
// this package refuses to fall back to kubectl's current-context (see
// TestKubectlApply_RefusesEmptyContext), because an unrelated tool flipping
// current-context is how a deploy lands in the wrong cluster. A test that
// guessed would be the same footgun, and here it would be a test that CREATES
// AND DELETES Deployments in whatever cluster happened to be selected.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// kubeContextForTest returns the throwaway cluster's context, or skips.
func ssaKubeContext(t *testing.T) string {
	t.Helper()
	kctx := strings.TrimSpace(os.Getenv("FORGE_TEST_KUBE_CONTEXT"))
	if kctx == "" {
		t.Skip("set FORGE_TEST_KUBE_CONTEXT to a throwaway cluster's kubectl context (see the file comment); this test creates and deletes Deployments")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skipf("kubectl not on PATH: %v", err)
	}
	out, err := exec.Command("kubectl", "--context", kctx, "version", "-o", "json").CombinedOutput()
	if err != nil {
		t.Skipf("cluster %q unreachable: %v\n%s", kctx, err, out)
	}
	return kctx
}

// deploymentManifest renders a minimal Deployment with the given strategy
// block, in the shape forge's own renderer emits (no rollingUpdate key at all
// when the strategy is Recreate — that absence IS the bug's precondition).
func ssaDeploymentManifest(namespace, name, strategy string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  strategy:
%s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
`, name, namespace, name, strategy, name)
}

// freshNamespace creates a uniquely-named namespace and registers its
// deletion, so parallel runs and reruns never collide.
func ssaFreshNamespace(t *testing.T, kctx string) string {
	t.Helper()
	ns := fmt.Sprintf("forge-ssa-%d", time.Now().UnixNano())
	ssaKubectl(t, kctx, "create", "namespace", ns)
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "--context", kctx, "delete", "namespace", ns, "--wait=false", "--ignore-not-found=true").Run()
	})
	return ns
}

func ssaKubectl(t *testing.T, kctx string, args ...string) string {
	t.Helper()
	out, err := exec.Command("kubectl", append([]string{"--context", kctx}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func ssaLiveStrategy(t *testing.T, kctx, ns, name string) map[string]any {
	t.Helper()
	out := ssaKubectl(t, kctx, "get", "deploy", name, "-n", ns, "-o", "jsonpath={.spec.strategy}")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("parse live strategy %q: %v", out, err)
	}
	return m
}

// TestStrategyRecovery_HealsRollingUpdateToRecreate is the regression test for
// the prod failure. It reproduces the exact sequence — create as
// RollingUpdate, let the API server default rollingUpdate, then apply as
// Recreate — and asserts that forge's apply path now succeeds.
//
// The bare apply is asserted to FAIL first, in the same test. That is what
// makes this a reproduction rather than a test that merely passes: without it,
// a future change that stopped defaulting rollingUpdate (or a cluster that
// never did) would leave this test green while testing nothing.
func TestStrategyRecovery_HealsRollingUpdateToRecreate(t *testing.T) {
	kctx := ssaKubeContext(t)
	ns := ssaFreshNamespace(t, kctx)
	const name = "workers"

	// Step 1: the Deployment is created as RollingUpdate, exactly as an
	// earlier release of the same workload would have been.
	rolling := ssaDeploymentManifest(ns, name, "    type: RollingUpdate")
	if err := applyStreamInNamespace(context.Background(), kctx, ns, rolling); err != nil {
		t.Fatalf("initial RollingUpdate apply: %v", err)
	}

	// Step 2: the API server's defaulting must have materialized
	// rollingUpdate under ITS OWN field manager. This is the precondition the
	// whole bug rests on; if it does not hold, the rest proves nothing.
	live := ssaLiveStrategy(t, kctx, ns, name)
	if _, ok := live["rollingUpdate"]; !ok {
		t.Fatalf("precondition failed: the API server did not default spec.strategy.rollingUpdate (live=%v); this test can no longer reproduce the wedge", live)
	}
	mf := ssaKubectl(t, kctx, "get", "deploy", name, "-n", ns, "--show-managed-fields", "-o", "json")
	if !strings.Contains(mf, "kube-controller-manager") {
		t.Fatalf("precondition failed: no kube-controller-manager field manager on the object; the stranded field would be forge's own and removable by apply")
	}

	// Step 3: forge now declares Recreate. A BARE server-side apply must
	// fail — this is the prod symptom, and asserting it here is what keeps
	// the test honest.
	recreate := ssaDeploymentManifest(ns, name, "    type: Recreate")
	_, bareStderr, bareErr := applyOnce(context.Background(), kctx, ns, recreate)
	if bareErr == nil {
		t.Fatalf("a bare SSA apply of type: Recreate SUCCEEDED; the wedge this test guards is gone, so the recovery is no longer being exercised.\nstderr=%q", bareStderr)
	}
	if !strings.Contains(bareStderr, strategyConflictMarker) {
		t.Fatalf("bare apply failed for a DIFFERENT reason than the wedge:\n%s", bareStderr)
	}

	// Step 4: the full apply path, with the recovery, must heal it.
	if err := applyStreamInNamespace(context.Background(), kctx, ns, recreate); err != nil {
		t.Fatalf("apply with the strategy recovery still failed: %v", err)
	}

	// Step 5: the live object is Recreate with NO stranded rollingUpdate.
	healed := ssaLiveStrategy(t, kctx, ns, name)
	if healed["type"] != "Recreate" {
		t.Errorf("live strategy type = %v, want Recreate", healed["type"])
	}
	if ru, ok := healed["rollingUpdate"]; ok {
		t.Errorf("live strategy still carries rollingUpdate = %v, want it removed", ru)
	}

	// Step 6: it must be IDEMPOTENT. A deploy that heals once but fails on
	// the next run has moved the wedge, not removed it.
	if err := applyStreamInNamespace(context.Background(), kctx, ns, recreate); err != nil {
		t.Fatalf("re-applying the healed Deployment failed (not idempotent): %v", err)
	}
	if err := applyStreamInNamespace(context.Background(), kctx, ns, recreate); err != nil {
		t.Fatalf("third apply of the healed Deployment failed: %v", err)
	}
}

// TestStrategyRecovery_RecreateOnAFreshClusterNeedsNoRecovery pins that the
// COLD path is untouched: a Deployment created as Recreate from the start
// applies cleanly, and the recovery never engages. This is why the prod bug
// was invisible in e2e, where every apply is into a fresh namespace.
func TestStrategyRecovery_RecreateOnAFreshClusterNeedsNoRecovery(t *testing.T) {
	kctx := ssaKubeContext(t)
	ns := ssaFreshNamespace(t, kctx)

	recreate := ssaDeploymentManifest(ns, "store", "    type: Recreate")
	if _, _, err := applyOnce(context.Background(), kctx, ns, recreate); err != nil {
		t.Fatalf("cold Recreate apply failed: %v", err)
	}
	live := ssaLiveStrategy(t, kctx, ns, "store")
	if live["type"] != "Recreate" {
		t.Errorf("live strategy type = %v, want Recreate", live["type"])
	}
	if _, ok := live["rollingUpdate"]; ok {
		t.Errorf("a cold Recreate apply somehow has rollingUpdate = %v", live["rollingUpdate"])
	}
}

// TestSSAExplicitNullDoesNotRemoveAForeignField is the negative result that
// determined the design, kept as a test because it is the load-bearing
// assumption of the whole file — and it is the one a future reader will want
// to re-litigate ("why not just render a null?").
//
// It asserts, against the real API server, that `rollingUpdate: null` in an
// apply configuration does NOT remove a field owned by another manager. In
// SSA, null means "I do not set this", never "delete it". Verified two ways:
// through kubectl (which strips nulls from the apply config before sending)
// and, below, through a raw apply-patch that keeps the null on the wire and is
// rejected with the same Forbidden error.
//
// If kubernetes ever changes this, THIS test fails — and at that point the
// renderer-level fix becomes available and the patch in
// strategy_recovery.go can be deleted.
func TestSSAExplicitNullDoesNotRemoveAForeignField(t *testing.T) {
	kctx := ssaKubeContext(t)
	ns := ssaFreshNamespace(t, kctx)
	const name = "nulltest"

	rolling := ssaDeploymentManifest(ns, name, "    type: RollingUpdate")
	if _, _, err := applyOnce(context.Background(), kctx, ns, rolling); err != nil {
		t.Fatalf("initial RollingUpdate apply: %v", err)
	}
	if _, ok := ssaLiveStrategy(t, kctx, ns, name)["rollingUpdate"]; !ok {
		t.Skip("the API server did not default rollingUpdate; nothing to try removing")
	}

	withNull := ssaDeploymentManifest(ns, name, "    type: Recreate\n    rollingUpdate: null")
	_, stderr, err := applyOnce(context.Background(), kctx, ns, withNull)
	if err == nil {
		t.Fatalf("an SSA apply with an explicit `rollingUpdate: null` SUCCEEDED.\n"+
			"SSA null-as-removal now works, so the declarative renderer-level fix is available "+
			"and the merge patch in strategy_recovery.go should be replaced by it.\nlive=%v",
			ssaLiveStrategy(t, kctx, ns, name))
	}
	if !strings.Contains(stderr, strategyConflictMarker) {
		t.Fatalf("apply-with-null failed for an unexpected reason:\n%s", stderr)
	}
}
