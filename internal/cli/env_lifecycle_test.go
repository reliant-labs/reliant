package cli

import (
	"strings"
	"testing"
)

// TestDirectApplyAllowed is the predicate's truth table.
//
// It pins the two REASONS separately, because they are different facts that
// happen to share an answer: a DECLARED local/ephemeral env may be applied
// to because forge owns its cluster, and a cluster-less env may be applied
// to because there is nothing to apply. Collapsing them would hide the case
// that actually matters — an undeclared env that DOES target a cluster,
// which is a real environment and the only false.
func TestDirectApplyAllowed(t *testing.T) {
	t.Parallel()

	// onCluster is an env that targets a kubectl context, which is what
	// makes the lifecycle declaration load-bearing: with no cluster every
	// env is trivially direct-appliable.
	onCluster := func(lifecycle string) *KCLEntities {
		return &KCLEntities{
			Lifecycle:     lifecycle,
			ClusterTarget: &ClusterTargetEntity{Cluster: "k3d-dev", Namespace: "app-dev"},
		}
	}

	tests := []struct {
		name     string
		entities *KCLEntities
		want     bool
		reason   string
	}{
		{
			name:     "declared local on a cluster",
			entities: onCluster(lifecycleLocal),
			want:     true,
			reason:   "a developer's own cluster — direct apply is the point",
		},
		{
			name:     "declared ephemeral on a cluster",
			entities: onCluster(lifecycleEphemeral),
			want:     true,
			reason:   "a throwaway per-run cluster has nothing to converge to",
		},
		{
			name:     "undeclared, targets a cluster",
			entities: onCluster(""),
			want:     false,
			reason:   "THE ONE FALSE: a real env, reconciled from its bundle",
		},
		{
			name: "undeclared, targets no cluster",
			entities: &KCLEntities{Workloads: []WorkloadEntity{
				{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost}},
			}},
			want:   true,
			reason: "host-only: nothing to apply, so no reconciler could help",
		},
		{
			name:     "undeclared and empty",
			entities: &KCLEntities{},
			want:     true,
			reason:   "declares no cluster at all",
		},
		{
			name:     "nil entities",
			entities: nil,
			want:     true,
			reason:   "no render, so nothing is known to need a reconciler",
		},
		{
			name: "declared local with a cluster workload",
			entities: &KCLEntities{
				Lifecycle: lifecycleLocal,
				Workloads: []WorkloadEntity{{
					Name:    "api",
					Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}},
				}},
			},
			want:   true,
			reason: "the declaration wins; KCL already proved the cluster is forge's",
		},
		{
			name:     "undeclared with a cluster database only",
			entities: &KCLEntities{Databases: []DatabaseEntity{{Name: "db"}}},
			want:     false,
			reason:   "a non-hosted database occupies a cluster, so there is something to reconcile",
		},
		{
			name:     "an unrecognized lifecycle value is not a licence",
			entities: onCluster("staging"),
			want:     false,
			reason:   "only local/ephemeral permit direct apply; anything else is a real env",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DirectApplyAllowed(tc.entities); got != tc.want {
				t.Errorf("DirectApplyAllowed() = %v, want %v (%s)", got, tc.want, tc.reason)
			}
		})
	}
}

// TestLifecycleNoticeIsOneLine pins the notice's shape, because its whole
// purpose is to be printed into an apply's stdout: an embedded newline would
// interleave with the apply's own progress output, and the notice is supposed
// to be one line a reader can skim past.
func TestLifecycleNoticeIsOneLine(t *testing.T) {
	t.Parallel()
	if strings.ContainsAny(lifecycleNoticeLine, "\n\r") {
		t.Errorf("lifecycleNoticeLine spans lines — it is printed with fmt.Println into an apply's output: %q", lifecycleNoticeLine)
	}
	for _, want := range []string{"none of `lifecycle`", `applier = "forge"`, "`hub_converges`", "will be refused"} {
		if !strings.Contains(lifecycleNoticeLine, want) {
			t.Errorf("lifecycleNoticeLine does not mention %q: %s", want, lifecycleNoticeLine)
		}
	}
}

// TestParseKCLEntitiesReadsLifecycle proves the field survives the wire.
//
// The predicate is only as good as the parse: a renamed or dropped JSON key
// would leave every env looking undeclared, which is the silent failure mode
// — the notice would fire everywhere and, once direct apply is retired, a
// developer's own dev cluster would be refused.
func TestParseKCLEntitiesReadsLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		json string
		want string
	}{
		{name: "local", json: `{"output":{"project":"p","lifecycle":"local"}}`, want: "local"},
		{name: "ephemeral", json: `{"output":{"project":"p","lifecycle":"ephemeral"}}`, want: "ephemeral"},
		{name: "null is a real env", json: `{"output":{"project":"p","lifecycle":null}}`, want: ""},
		{name: "absent is a real env", json: `{"output":{"project":"p"}}`, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseKCLEntities([]byte(tc.json))
			if err != nil {
				t.Fatalf("parseKCLEntities: %v", err)
			}
			if got.Lifecycle != tc.want {
				t.Errorf("Lifecycle = %q, want %q", got.Lifecycle, tc.want)
			}
		})
	}
}
