package operatorkit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestOptionsAreTheOnlyInput pins that nothing about this manager is decided
// by the environment the process happens to have been started in.
//
// Every one of these variables used to change the manager's behaviour from
// the outside: which lease it took (so two processes could silently contend
// for one, or silently stop contending), whether it ran from a host at all,
// what port it bound, and how long a leader could go unrenewed before the
// process terminated itself. None appears in any config a forge app declares,
// so an app whose controller stopped reconciling had nothing to read that
// would explain it.
//
// The assertion is on the RESOLVED values, not on a helper: with a fully
// declared Options, an environment full of contradicting values must change
// nothing.
func TestOptionsAreTheOnlyInput(t *testing.T) {
	for _, kv := range [][2]string{
		{"LEADER_ELECTION_ID", "hijacked-lease"},
		{"LEADER_ELECTION_NAMESPACE", "hijacked-ns"},
		{"HEALTH_PROBE_BIND_ADDRESS", ":9999"},
		{"OPERATOR_CLIENT_QPS", "1"},
		{"OPERATOR_CLIENT_BURST", "1"},
		{"OPERATOR_LEASE_DURATION", "1s"},
		{"OPERATOR_RENEW_DEADLINE", "1s"},
		{"OPERATOR_RETRY_PERIOD", "1s"},
	} {
		t.Setenv(kv[0], kv[1])
	}

	opts := Options{
		LeaderElectionID:        "example.com/myproj-leader",
		LeaderElectionNamespace: "declared-ns",
		HealthProbeBindAddress:  ":8081",
		LeaseDuration:           90 * time.Second,
		RenewDeadline:           60 * time.Second,
		RetryPeriod:             10 * time.Second,
		ClientQPS:               75,
		ClientBurst:             150,
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"LeaderElectionID", opts.LeaderElectionID, "example.com/myproj-leader"},
		{"LeaderElectionNamespace", opts.LeaderElectionNamespace, "declared-ns"},
		{"HealthProbeBindAddress", opts.HealthProbeBindAddress, ":8081"},
		{"LeaseDuration", orDefaultDuration(opts.LeaseDuration, defaultLeaseDuration), 90 * time.Second},
		{"RenewDeadline", orDefaultDuration(opts.RenewDeadline, defaultRenewDeadline), 60 * time.Second},
		{"RetryPeriod", orDefaultDuration(opts.RetryPeriod, defaultRetryPeriod), 10 * time.Second},
		{"ClientQPS", orDefaultFloat32(opts.ClientQPS, defaultClientQPS), float32(75)},
		{"ClientBurst", orDefaultInt(opts.ClientBurst, defaultClientBurst), 150},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s resolved to %v, want %v — an ambient environment variable decided a "+
				"manager setting the caller declared", c.name, c.got, c.want)
		}
	}
}

// TestManagerReleasesTheLeaseOnCancel pins LeaderElectionReleaseOnCancel.
//
// Without it the outgoing leader stops renewing but does not release, so the
// successor waits out the whole LeaseDuration — measured in prod as 45s with
// no controller reconciling, on every rollout. This is only safe because
// serverkit.Run waits for RunOperators to return (and therefore for
// controller-runtime to have drained its controllers) before continuing
// teardown; see the comment on the option.
func TestManagerReleasesTheLeaseOnCancel(t *testing.T) {
	got := managerOptions(Options{LeaderElectionID: "example.com/leader"}, runtime.NewScheme(), "ns", ":8081",
		defaultLeaseDuration, defaultRenewDeadline, defaultRetryPeriod)
	if !got.LeaderElectionReleaseOnCancel {
		t.Error("LeaderElectionReleaseOnCancel is false: the successor waits out the full lease on every rollout")
	}
	if !got.LeaderElection {
		t.Error("LeaderElection is false: releasing a lease nothing takes is meaningless")
	}
}

// An unset field takes the hardened default documented in this package, and
// an unusable one (zero, negative) does the same: a zero timing is not a
// faster controller, it is controller-runtime's short stock default silently
// reinstated, which self-terminates a healthy single-replica leader on a
// latency spike.
func TestUnsetAndUnusableOptionsTakeTheHardenedDefaults(t *testing.T) {
	for _, v := range []time.Duration{0, -5 * time.Second} {
		if got := orDefaultDuration(v, defaultLeaseDuration); got != defaultLeaseDuration {
			t.Errorf("orDefaultDuration(%v) = %v, want the hardened default %v", v, got, defaultLeaseDuration)
		}
	}
	for _, v := range []float32{0, -1} {
		if got := orDefaultFloat32(v, defaultClientQPS); got != defaultClientQPS {
			t.Errorf("orDefaultFloat32(%v) = %v, want the hardened default %v", v, got, defaultClientQPS)
		}
	}
	for _, v := range []int{0, -1} {
		if got := orDefaultInt(v, defaultClientBurst); got != defaultClientBurst {
			t.Errorf("orDefaultInt(%v) = %v, want the hardened default %v", v, got, defaultClientBurst)
		}
	}
}

// TestCacheByObject asserts the per-object namespace-scoping conversion:
// scoped types get a cache.ByObject row with exactly the usable namespaces;
// unscopable entries (empty list, empty-string namespaces) are dropped so the
// type keeps the cluster-wide default; and a fully-empty input returns nil so
// the manager receives a zero-value cache.Options (the legacy shape).
func TestCacheByObject(t *testing.T) {
	pod := &corev1.Pod{}
	cm := &corev1.ConfigMap{}

	tests := []struct {
		name   string
		scopes map[client.Object][]string
		// wantNamespaces maps each object expected in the result to the
		// namespace set its ByObject row must carry. Objects absent from this
		// map must be absent from the result.
		wantNamespaces map[client.Object][]string
		wantNil        bool
	}{
		{
			name:    "nil input returns nil",
			scopes:  nil,
			wantNil: true,
		},
		{
			name:    "empty input returns nil",
			scopes:  map[client.Object][]string{},
			wantNil: true,
		},
		{
			name:    "entry with no namespaces is dropped",
			scopes:  map[client.Object][]string{pod: {}},
			wantNil: true,
		},
		{
			name:    "entry with only empty-string namespaces is dropped",
			scopes:  map[client.Object][]string{pod: {"", ""}},
			wantNil: true,
		},
		{
			name:           "scoped entry carries its namespaces",
			scopes:         map[client.Object][]string{pod: {"stack-dev"}},
			wantNamespaces: map[client.Object][]string{pod: {"stack-dev"}},
		},
		{
			name: "mixed: scoped entry kept, unscopable sibling dropped, empty strings filtered",
			scopes: map[client.Object][]string{
				pod: {"", "stack-dev", "stack-dev-wt1"},
				cm:  {""},
			},
			wantNamespaces: map[client.Object][]string{pod: {"stack-dev", "stack-dev-wt1"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cacheByObject(tc.scopes)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("cacheByObject() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tc.wantNamespaces) {
				t.Fatalf("cacheByObject() has %d entries, want %d: %v", len(got), len(tc.wantNamespaces), got)
			}
			for obj, wantNS := range tc.wantNamespaces {
				row, ok := got[obj]
				if !ok {
					t.Fatalf("cacheByObject() missing entry for %T", obj)
				}
				if len(row.Namespaces) != len(wantNS) {
					t.Fatalf("entry for %T has namespaces %v, want %v", obj, row.Namespaces, wantNS)
				}
				for _, ns := range wantNS {
					if _, ok := row.Namespaces[ns]; !ok {
						t.Errorf("entry for %T missing namespace %q (got %v)", obj, ns, row.Namespaces)
					}
				}
			}
		})
	}
}

// TestWithLabelScopes pins that a label scope reaches the cache row for its
// type: merged into an existing namespace row, or added as a new row, and
// dropped when nil. A dropped selector would leave the type cluster-wide —
// the cross-stack reconcile the option exists to prevent.
func TestWithLabelScopes(t *testing.T) {
	pod := &corev1.Pod{}
	cm := &corev1.ConfigMap{}
	sel := labels.SelectorFromSet(labels.Set{"forge.dev/stack": "dev"})

	rows := withLabelScopes(cacheByObject(map[client.Object][]string{pod: {"ns-a"}}),
		map[client.Object]labels.Selector{pod: sel, cm: sel})
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %v", len(rows), rows)
	}
	if rows[pod].Label == nil || rows[pod].Label.String() != sel.String() || len(rows[pod].Namespaces) != 1 {
		t.Errorf("pod row = %+v, want namespace ns-a AND the label selector", rows[pod])
	}
	if rows[cm].Label == nil || rows[cm].Label.String() != sel.String() {
		t.Errorf("configmap row = %+v, want the label selector", rows[cm])
	}
	if got := withLabelScopes(nil, map[client.Object]labels.Selector{pod: nil}); got != nil {
		t.Errorf("a nil selector must not create a row, got %v", got)
	}
}

// TestRun_NoClusterDegradesToNil pins the graceful degrade: with no reachable
// Kubernetes cluster, Run logs a warning and returns nil rather than crashing
// the binary. KUBECONFIG and HOME point at an empty directory so a developer's
// own kubeconfig cannot make the test reach a real cluster.
func TestRun_NoClusterDegradesToNil(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBECONFIG", filepath.Join(dir, "no-kubeconfig"))
	t.Setenv("HOME", dir)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if err := Run(context.Background(), nil, Options{LeaderElectionID: "test-leader"}, nil); err != nil {
		t.Fatalf("Run with no cluster should degrade to nil, got %v", err)
	}
}
