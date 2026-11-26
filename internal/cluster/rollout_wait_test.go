package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for WaitRollouts: the rollout wait, run once over every cluster a
// deploy applied. They drive applyRenderedNoWait — ApplyNoWait minus the KCL
// render — against a fake kubectl that models one namespace per context.

// fakeMultiClusterKubectl installs a fake `kubectl` on PATH and returns a
// reader for its invocations as "<context> <argv>", in order.
//
//   - apply echoes `<kind>/<name> serverside-applied` per document and
//     remembers which Deployments landed on which context.
//   - `get deployments` lists the Deployments applied to that context.
//   - `rollout status deployment/<name>` fails with kubectl's own timeout
//     message when "<context>/<name>" is listed in failing; it is ready
//     otherwise.
func fakeMultiClusterKubectl(t *testing.T, failing ...string) func() []string {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	script := `#!/bin/sh
state='` + dir + `'
ctx=""
if [ "$1" = "--context" ]; then ctx="$2"; shift 2; fi
printf '%s %s\n' "$ctx" "$*" >> "$state/calls.log"
case "$1" in
  apply)
    in=$(mktemp "$state/stdin.XXXXXX")
    cat > "$in"
    awk -v dfile="$state/deployments-$ctx" '
      function emit() { if (k != "" && n != "") { print k "/" n " serverside-applied"; if (k == "deployment") print n >> dfile } k = ""; n = ""; m = 0 }
      /^---$/ { emit(); next }
      /^kind:/ { k = tolower($2) }
      /^metadata:/ { m = 1; next }
      /^[^ ]/ { m = 0 }
      m && $1 == "name:" && n == "" { n = $2 }
      END { emit() }' "$in"
    rm -f "$in"
    exit 0 ;;
  rollout)
    dep=${3#deployment/}
    for f in $FAKE_FAILING; do
      if [ "$f" = "$ctx/$dep" ]; then echo "error: timed out waiting for the condition" >&2; exit 1; fi
    done
    exit 0 ;;
  get)
    if [ "$2" = "deployments" ]; then sort -u "$state/deployments-$ctx" 2>/dev/null; fi
    exit 0 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_FAILING", strings.Join(failing, " "))
	return func() []string {
		data, err := os.ReadFile(filepath.Join(dir, "calls.log"))
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

// deploymentDoc is a forge-shaped Deployment in namespace app.
func deploymentDoc(name string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ` + name + `
  namespace: app
  labels:
    app.kubernetes.io/name: ` + name
}

// applyTwoClusters applies one stream per context with the given policy and
// returns their pending rollouts, labelled the way the k8s provider labels
// them.
func applyTwoClusters(t *testing.T, policy RolloutPolicy, streams map[string]string) []*PendingRollout {
	t.Helper()
	var pending []*PendingRollout
	for _, kctx := range []string{"k3d-alpha", "k3d-beta"} {
		p, err := applyRenderedNoWait(context.Background(), ApplyOpts{
			Namespace: "app", Context: kctx, Rollout: policy,
		}, streams[kctx])
		if err != nil {
			t.Fatalf("apply %s: %v", kctx, err)
		}
		pending = append(pending, p.WithLabel("cluster="+kctx))
	}
	return pending
}

// waitTimeout keeps the fake's waits short without changing any verdict.
const waitTimeout = 5 * time.Second

var twoClusterStreams = map[string]string{
	"k3d-alpha": deploymentDoc("api") + docDelimiter + deploymentDoc("bridge"),
	"k3d-beta":  deploymentDoc("proxy"),
}

// TestWaitRollouts_EveryClusterIsAppliedBeforeAnyWait: ApplyNoWait sends a
// cluster's manifests and asks it nothing about readiness, so a deploy can
// apply every cluster before the first wait.
func TestWaitRollouts_EveryClusterIsAppliedBeforeAnyWait(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeMultiClusterKubectl(t)
	pending := applyTwoClusters(t, RolloutPolicy{Timeout: waitTimeout}, twoClusterStreams)
	for _, c := range readCalls() {
		if strings.Contains(c, " rollout ") || strings.Contains(c, " get deployments") {
			t.Fatalf("ApplyNoWait waited on a rollout: %s", c)
		}
	}
	if err := WaitRollouts(context.Background(), pending...); err != nil {
		t.Fatalf("WaitRollouts: %v", err)
	}
	var waited []string
	for _, c := range readCalls() {
		if strings.Contains(c, " rollout status ") {
			waited = append(waited, strings.Fields(c)[0]+" "+strings.Fields(c)[3])
		}
	}
	want := []string{"k3d-alpha deployment/api", "k3d-alpha deployment/bridge", "k3d-beta deployment/proxy"}
	if strings.Join(waited, ",") != strings.Join(want, ",") {
		t.Errorf("waited on %v, want every cluster's Deployments %v", waited, want)
	}
}

// TestWaitRollouts_FailuresOnEveryClusterAreReportedTogether: under the
// default policy a failure on one cluster does not stop the wait on another,
// and the verdict names each cluster and each resource.
func TestWaitRollouts_FailuresOnEveryClusterAreReportedTogether(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fakeMultiClusterKubectl(t, "k3d-alpha/bridge", "k3d-beta/proxy")
	pending := applyTwoClusters(t, RolloutPolicy{Timeout: waitTimeout}, twoClusterStreams)
	err := WaitRollouts(context.Background(), pending...)
	var agg *RolloutFailedError
	if !errors.As(err, &agg) || len(agg.Failures) != 2 {
		t.Fatalf("want a RolloutFailedError naming both clusters, got %v", err)
	}
	for _, want := range []string{
		"cluster=k3d-alpha: rollout failed: bridge (k3d-alpha/app) did not become ready",
		"cluster=k3d-beta: rollout failed: proxy (k3d-beta/app) did not become ready",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("verdict does not contain %q:\n%v", want, err)
		}
	}
}

// TestWaitRollouts_OneFailingClusterKeepsItsOwnError: when only one of the
// clusters fails, the verdict is that cluster's error, labelled — not an
// aggregate of one.
func TestWaitRollouts_OneFailingClusterKeepsItsOwnError(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fakeMultiClusterKubectl(t, "k3d-beta/proxy")
	pending := applyTwoClusters(t, RolloutPolicy{Timeout: waitTimeout}, twoClusterStreams)
	err := WaitRollouts(context.Background(), pending...)
	want := "cluster=k3d-beta: rollout failed: proxy (k3d-beta/app) did not become ready"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// TestWaitRollouts_WarnReportsAndSucceeds: RolloutWarn tolerates a failed
// rollout on any cluster, as it does on one.
func TestWaitRollouts_WarnReportsAndSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	fakeMultiClusterKubectl(t, "k3d-alpha/bridge", "k3d-beta/proxy")
	pending := applyTwoClusters(t, RolloutPolicy{Mode: RolloutWarn, Timeout: waitTimeout}, twoClusterStreams)
	if err := WaitRollouts(context.Background(), pending...); err != nil {
		t.Fatalf("warn mode must not fail the deploy, got %v", err)
	}
}

// TestWaitRollouts_FailFastStopsAcrossClusters: --rollout-fail-fast stops at
// the first failure anywhere; the other cluster's Deployments are never
// awaited.
func TestWaitRollouts_FailFastStopsAcrossClusters(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeMultiClusterKubectl(t, "k3d-alpha/api")
	pending := applyTwoClusters(t, RolloutPolicy{FailFast: true, Timeout: waitTimeout}, twoClusterStreams)
	err := WaitRollouts(context.Background(), pending...)
	if err == nil || !strings.Contains(err.Error(), "cluster=k3d-alpha: rollout failed: api (k3d-alpha/app)") {
		t.Fatalf("want the first failure, labelled, got %v", err)
	}
	for _, c := range readCalls() {
		if strings.HasPrefix(c, "k3d-beta rollout ") {
			t.Errorf("fail-fast went on to wait on another cluster: %s", c)
		}
	}
}

// TestWaitRollouts_OrderSpansClusters: --rollout-order is one ordering across
// every cluster, so the resource it names first is awaited first even when it
// runs on the cluster dispatched last.
func TestWaitRollouts_OrderSpansClusters(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	readCalls := fakeMultiClusterKubectl(t)
	pending := applyTwoClusters(t, RolloutPolicy{Order: []string{"proxy", "bridge"}, Timeout: waitTimeout}, twoClusterStreams)
	if err := WaitRollouts(context.Background(), pending...); err != nil {
		t.Fatalf("WaitRollouts: %v", err)
	}
	var waited []string
	for _, c := range readCalls() {
		if strings.Contains(c, " rollout status ") {
			waited = append(waited, strings.TrimPrefix(strings.Fields(c)[3], "deployment/"))
		}
	}
	if want := "proxy,bridge,api"; strings.Join(waited, ",") != want {
		t.Errorf("wait order = %v, want %s", waited, want)
	}
}

// TestWaitRollouts_SkipLeavesNothingToWaitFor: under RolloutSkip the apply
// returns no pending rollout, and WaitRollouts over nothing asks the cluster
// nothing.
func TestWaitRollouts_SkipLeavesNothingToWaitFor(t *testing.T) {
	readCalls := fakeMultiClusterKubectl(t)
	pending := applyTwoClusters(t, RolloutPolicy{Mode: RolloutSkip}, twoClusterStreams)
	for _, p := range pending {
		if p != nil {
			t.Fatalf("skip mode returned a pending rollout: %+v", p)
		}
	}
	if err := WaitRollouts(context.Background(), pending...); err != nil {
		t.Fatalf("WaitRollouts: %v", err)
	}
	for _, c := range readCalls() {
		if strings.Contains(c, " rollout ") || strings.Contains(c, " get deployments") {
			t.Errorf("skip mode waited: %s", c)
		}
	}
}

// TestWaitRollouts_SingleClusterErrorIsUnchanged: one pending rollout's
// verdict is byte-identical to what Apply has always returned — unlabelled,
// and naming the bare Deployment.
func TestWaitRollouts_SingleClusterErrorIsUnchanged(t *testing.T) {
	fakeMultiClusterKubectl(t, "k3d-alpha/bridge")
	err := applyRendered(context.Background(), ApplyOpts{
		Namespace: "app", Context: "k3d-alpha", Rollout: RolloutPolicy{Timeout: waitTimeout},
	}, twoClusterStreams["k3d-alpha"])
	if want := "rollout failed: bridge did not become ready"; err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}
