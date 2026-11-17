package cli

// up_cluster_health_test.go — the cluster half of `forge env up`'s readiness
// gate.
//
// The defect these pin: `forge env up` blocked only on HOST service readiness
// and exited 0 while the Kubernetes workloads it had just applied were
// crashlooping. On control-plane that meant a green banner over an OOMKilled,
// CrashLoopBackOff daemon-gateway, and — separately — five pods crashlooping
// for ten hours behind the same all-green output.
//
// The judgement itself belongs to doctor.CheckClusterWorkloads and is tested
// there. What is tested HERE is the wiring, which is the part that decides
// whether `forge env up` exits 0: which runs gate at all, what a verdict does
// to the exit code, and what the summary box says about it. The check is
// swapped for a fabricated verdict (clusterWorkloadCheck is the seam), which
// is the only way to pin "crashlooping ⇒ non-zero" without a cluster to break.

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// stubFeatures is a featureReader with an explicit deploy setting, so the
// feature arm of the gate is exercised without a project on disk.
type stubFeatures struct{ deploy bool }

func (s stubFeatures) Features() projectstore.FeatureSet {
	return config.FeaturesConfig{}.With(config.FeatureDeploy, s.deploy)
}

// withClusterCheck swaps the doctor check for a fabricated verdict for the
// duration of one test.
func withClusterCheck(t *testing.T, res doctor.CheckResult) {
	t.Helper()
	prev := clusterWorkloadCheck
	clusterWorkloadCheck = func(context.Context, *doctor.Environment) doctor.CheckResult { return res }
	t.Cleanup(func() { clusterWorkloadCheck = prev })
}

// clusterEntities is an env with a cluster workload in it — the shape whose
// health `forge env up` must assert on.
func clusterEntities() *KCLEntities {
	return &KCLEntities{
		Workloads: []WorkloadEntity{
			hostWL("admin-server"),
			{Name: "daemon-gateway", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}}},
		},
		Frontends: []FrontendEntity{{Name: "reliant-web", Port: 3000}},
	}
}

// crashloopVerdict is the daemon-gateway incident, as doctor reports it.
func crashloopVerdict() doctor.CheckResult {
	return doctor.CheckResult{
		Status: doctor.StatusFail,
		Message: "daemon-gateway (pod daemon-gateway-8598f99486-frs5m): 0/1 Ready  CrashLoopBackOff " +
			"last=OOMKilled(exit 137)  restarts=37  [1 failing, 0 warning in k3d-control-plane/control-plane-dev]",
	}
}

// TestUpFailsOnCrashloopingWorkload is the whole point: a workload that is
// not running must not come out as a successful `forge env up`. Before this
// gate the command returned nil here, and the summary box painted the host
// half green with nothing at all said about the cluster.
func TestUpFailsOnCrashloopingWorkload(t *testing.T) {
	withClusterCheck(t, crashloopVerdict())

	health := evalClusterWorkloads(context.Background(), true, t.TempDir(), "dev")
	if health == nil {
		t.Fatal("a FAIL verdict must survive as a summary; got nil (nothing to report)")
	}
	if health.status != doctor.StatusFail {
		t.Fatalf("status = %q, want fail", health.status)
	}

	err := clusterWorkloadError("dev", health)
	if err == nil {
		t.Fatal("a crashlooping workload must fail `forge env up`; got a nil error (exit 0)")
	}
	// The message must END the investigation, not announce that one exists:
	// the pod name and the OOMKill are the facts that were invisible for an
	// hour, so they have to survive into the error the user actually reads.
	for _, want := range []string{
		"daemon-gateway-8598f99486-frs5m",
		"CrashLoopBackOff",
		"OOMKilled",
		"env=dev",
		"forge env status dev -v",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q:\n%s", want, err)
		}
	}
}

// TestUpUnreachableClusterDoesNotFail pins the three-state contract at the
// gate. UNDETERMINED is not a failure: a developer on a plane, behind a dead
// VPN, or without kubectl installed must still be able to bring their stack
// up. It is also not silence — a hole in the report that prints nothing is
// the defect this whole path exists to close.
func TestUpUnreachableClusterDoesNotFail(t *testing.T) {
	withClusterCheck(t, doctor.CheckResult{
		Status:  doctor.StatusUnknown,
		Message: "k3d-control-plane/control-plane-dev: could not list pods (6 workload(s) unreported) — dial tcp 127.0.0.1:6443: connect: connection refused — the rest could not be judged, so this is NOT a pass",
	})

	health := evalClusterWorkloads(context.Background(), true, t.TempDir(), "dev")
	if health == nil {
		t.Fatal("UNDETERMINED must be reported, not swallowed as nothing-to-say")
	}
	if err := clusterWorkloadError("dev", health); err != nil {
		t.Fatalf("an unreachable cluster must not fail `forge env up`: %v", err)
	}

	var b strings.Builder
	renderUpSummary(&b, "dev", nil, "starting", false, nil, health)
	out := b.String()
	if !strings.Contains(out, "Cluster workloads") {
		t.Errorf("summary box never mentioned the cluster:\n%s", out)
	}
	if !strings.Contains(out, "could not list pods") {
		t.Errorf("summary box hid WHY the answer is undetermined:\n%s", out)
	}
	if strings.Contains(out, "NOT RUNNING") {
		t.Errorf("UNDETERMINED must not render as a failure banner:\n%s", out)
	}
}

// TestUpWarningDoesNotFail pins the other half of the false-alarm guard: a
// slow first rollout reports as a WARNING (the check's own 90s startup
// grace), and a command that failed on it would be its own kind of false
// alarm — the kind that trains people to ignore the gate.
func TestUpWarningDoesNotFail(t *testing.T) {
	withClusterCheck(t, doctor.CheckResult{
		Status:  doctor.StatusWarn,
		Message: "reliant-api-server (pod reliant-api-server-77c8-9tzqp): 0/1 Ready  Pending — starting (12s old)  [0 failing, 1 warning in k3d-control-plane/control-plane-dev]",
	})

	health := evalClusterWorkloads(context.Background(), true, t.TempDir(), "dev")
	if health == nil || health.status != doctor.StatusWarn {
		t.Fatalf("warn verdict lost: %+v", health)
	}
	if err := clusterWorkloadError("dev", health); err != nil {
		t.Fatalf("a mid-rollout warning must not fail `forge env up`: %v", err)
	}

	var b strings.Builder
	renderUpSummary(&b, "dev", nil, "starting", false, nil, health)
	if out := b.String(); !strings.Contains(out, "starting (12s old)") {
		t.Errorf("the warning must still be visible in the box:\n%s", out)
	}
}

// TestUpNoKubernetesWorkloadsUnaffected pins the SKIP arm. An env that
// deploys nothing to Kubernetes — a --kind cli project, a compose-only env —
// has answered the question by its own shape: no output, no failure, and no
// cluster section in the box.
func TestUpNoKubernetesWorkloadsUnaffected(t *testing.T) {
	withClusterCheck(t, doctor.CheckResult{
		Status:  doctor.StatusSkip,
		Message: `env "dev" deploys no Kubernetes workloads`,
	})

	health := evalClusterWorkloads(context.Background(), true, t.TempDir(), "dev")
	if health != nil {
		t.Fatalf("SKIP must collapse to nothing-to-say, got %+v", health)
	}
	if err := clusterWorkloadError("dev", health); err != nil {
		t.Fatalf("an env with no k8s workloads must not fail: %v", err)
	}

	// And with no host rows either, the box must not print at all — a
	// cluster-only run against a project with no cluster has nothing to say.
	var b strings.Builder
	renderUpSummary(&b, "dev", nil, "starting", false, nil, health)
	if out := b.String(); out != "" {
		t.Errorf("nothing to report should print nothing, got:\n%s", out)
	}
}

// TestUpClusterGateHostTargetUnaffected pins the host-side dev loop. The gate
// must not merely pass there — it must not RUN. Nothing this run selected
// touches a cluster, and paying a KCL render + kubectl round trips to reach
// that conclusion is its own defect.
func TestUpClusterGateHostTargetUnaffected(t *testing.T) {
	// A verdict that would fail the command if the gate ever consulted it.
	withClusterCheck(t, crashloopVerdict())

	e := clusterEntities()
	store := stubFeatures{deploy: true}

	// This is what replaced `--host-only`: the developer names the host
	// service they are iterating on, the target closure has no cluster edge,
	// and the gate goes quiet — for the same reason as before, but derived
	// from what the run actually selected rather than asserted by a flag.
	if upClusterGateEnabled(store, e, []string{"admin-server"}) {
		t.Fatal("a run targeting only a host service must not assert on cluster workloads")
	}

	// The seam is only reached when the gate says so, so a disabled gate is
	// a nil verdict without ever calling the check.
	if health := evalClusterWorkloads(context.Background(), false, t.TempDir(), "dev"); health != nil {
		t.Fatalf("a disabled gate must produce no verdict, got %+v", health)
	}
	if err := clusterWorkloadError("dev", nil); err != nil {
		t.Fatalf("a disabled gate must not fail the command: %v", err)
	}

	// Contrast: the same env on a full run DOES gate. Without this the test
	// above would pass just as well against a gate that is never on.
	if !upClusterGateEnabled(store, e, nil) {
		t.Error("a full `forge env up` over a cluster env must assert on its workloads")
	}
}

// TestUpClusterGateScope pins the rest of the enable rule — each false arm
// being a run that has said, by what it selected or configured, that forge is
// not responsible for a cluster this time.
func TestUpClusterGateScope(t *testing.T) {
	e := clusterEntities()
	on := stubFeatures{deploy: true}

	tests := []struct {
		name    string
		store   featureReader
		targets []string
		want    bool
	}{
		{"full run", on, nil, true},
		{"deploy feature off", stubFeatures{deploy: false}, nil, false},
		// --no-deploy is deliberately NOT an arm: it means "skip the apply",
		// not "don't care", and a run leaning on an already-deployed cluster
		// is the one that most needs to hear the cluster is broken.
		{"target is a cluster service", on, []string{"daemon-gateway"}, true},
		{"target is a host service", on, []string{"admin-server"}, false},
		{"target is a dev-served frontend", on, []string{"reliant-web"}, false},
		// Naming BOTH sides still gates: one of them deploys to the cluster,
		// so the run is responsible for it. This is the arm that would break
		// if the target closure were ever reduced to "the first target wins".
		{"targets span host and cluster", on, []string{"admin-server", "daemon-gateway"}, true},
		// A nil store is the permissive default isFeatureEnabled documents.
		{"no project store", nil, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := upClusterGateEnabled(tc.store, e, tc.targets); got != tc.want {
				t.Errorf("upClusterGateEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUpSummaryReportsClusterWorkloads pins point four: the box has to tell
// the truth about what was brought up. It listed "Host services" and
// "Frontends" and said nothing at all about the cluster — the same silence as
// the exit code, one layer up.
func TestUpSummaryReportsClusterWorkloads(t *testing.T) {
	rows := []upServiceRow{
		{Name: "admin-server", Kind: "host", URL: "http://localhost:8090", Port: 8090, Listening: true, Log: ".forge/logs/dev/admin-server.log"},
	}

	t.Run("healthy", func(t *testing.T) {
		var b strings.Builder
		renderUpSummary(&b, "dev", rows, "starting", false, nil, &clusterWorkloadSummary{
			status:  doctor.StatusPass,
			message: "12 workload(s), 14 pod(s) Ready — k3d-control-plane/control-plane-dev, k3d-cp-daemon/control-plane-dev",
		})
		out := b.String()
		if !strings.Contains(out, "Cluster workloads") {
			t.Errorf("healthy box omits the cluster group:\n%s", out)
		}
		if !strings.Contains(out, "14 pod(s) Ready") {
			t.Errorf("healthy box omits the verdict:\n%s", out)
		}
		if strings.Contains(out, "forge env status dev -v") {
			t.Errorf("a pass should not point at evidence there is no reason to read:\n%s", out)
		}
	})

	t.Run("crashlooping", func(t *testing.T) {
		var b strings.Builder
		v := crashloopVerdict()
		renderUpSummary(&b, "dev", rows, "starting", false, []string{"Ctrl-C to stop."},
			&clusterWorkloadSummary{status: v.Status, message: v.Message})
		out := b.String()
		// The banner is the loud line, in the same register as the existing
		// duplicate-process alarm: the box is where someone IS looking.
		if !strings.Contains(out, "CLUSTER WORKLOADS NOT RUNNING") {
			t.Errorf("a failing cluster must be flagged at the top of the box:\n%s", out)
		}
		if !strings.Contains(out, "daemon-gateway-8598f99486-frs5m") {
			t.Errorf("the box must name the pod, not a count:\n%s", out)
		}
		if !strings.Contains(out, "forge env status dev -v") {
			t.Errorf("the box must point at the per-pod detail:\n%s", out)
		}
		// The host half is unchanged — this is additive, not a replacement.
		if !strings.Contains(out, "admin-server") {
			t.Errorf("host rows must survive the cluster group:\n%s", out)
		}
	})

	t.Run("no verdict prints no group", func(t *testing.T) {
		var b strings.Builder
		renderUpSummary(&b, "dev", rows, "starting", false, nil, nil)
		if out := b.String(); strings.Contains(out, "Cluster workloads") {
			t.Errorf("a run with nothing to say about a cluster must print no group:\n%s", out)
		}
	})
}

// TestPrintUpSummaryTrailerWithoutRows pins the trailer honesty fix that
// falls out of a box which can now print with no host rows: "Ctrl-C to stop."
// is advice about children that a purely in-cluster run never started.
func TestPrintUpSummaryTrailerWithoutRows(t *testing.T) {
	var b strings.Builder
	renderUpSummary(&b, "dev", nil, "starting", false, nil, &clusterWorkloadSummary{
		status:  doctor.StatusPass,
		message: "6 workload(s), 6 pod(s) Ready — k3d-control-plane/control-plane-dev",
	})
	out := b.String()
	if strings.Contains(out, "Ctrl-C to stop.") || strings.Contains(out, "Detached") {
		t.Errorf("no host children means no lifecycle trailer:\n%s", out)
	}
	if !strings.Contains(out, "6 pod(s) Ready") {
		t.Errorf("cluster-only box must still carry its verdict:\n%s", out)
	}
}

// TestClusterWorkloadErrorIsReportOnly pins the "detect and report only"
// contract the host half already keeps: the error names what to do next and
// nothing is killed, so the children this run started stay reachable.
func TestClusterWorkloadErrorIsReportOnly(t *testing.T) {
	v := crashloopVerdict()
	err := clusterWorkloadError("dev", &clusterWorkloadSummary{status: v.Status, message: v.Message})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "waited on their rollouts") {
		t.Errorf("the error should say forge already gave the rollout its window:\n%s", err)
	}
	for _, status := range []doctor.Status{doctor.StatusPass, doctor.StatusWarn, doctor.StatusUnknown} {
		if e := clusterWorkloadError("dev", &clusterWorkloadSummary{status: status}); e != nil {
			t.Errorf("status %q must not fail the command: %v", status, e)
		}
	}
}

// TestEvalClusterWorkloadsBoundsTheCheck pins that a cancelled context does
// not turn into a failure. The check itself degrades cancellation to
// UNDETERMINED; what is pinned here is that the gate passes a live context
// through rather than swallowing it, and that a Fail-shaped verdict is the
// ONLY thing that fails the command.
func TestEvalClusterWorkloadsBoundsTheCheck(t *testing.T) {
	var gotDeadline bool
	prev := clusterWorkloadCheck
	clusterWorkloadCheck = func(ctx context.Context, env *doctor.Environment) doctor.CheckResult {
		_, gotDeadline = ctx.Deadline()
		if env.Env != "dev" {
			t.Errorf("env = %q, want dev", env.Env)
		}
		return doctor.CheckResult{Status: doctor.StatusPass, Message: "ok"}
	}
	t.Cleanup(func() { clusterWorkloadCheck = prev })

	health := evalClusterWorkloads(context.Background(), true, t.TempDir(), "dev")
	if !gotDeadline {
		t.Error("the check must run under a deadline — the last step of `forge env up` must not hang")
	}
	if health == nil || health.status != doctor.StatusPass {
		t.Fatalf("pass verdict lost: %+v", health)
	}
	if err := clusterWorkloadError("dev", health); err != nil {
		t.Fatalf("a healthy cluster must not fail: %v", err)
	}
}
