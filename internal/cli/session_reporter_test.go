package cli

// Where a presence row goes, and whether one is written at all.
//
// The two properties worth pinning are both about AGREEMENT rather than about
// behaviour in isolation: the records backend must be the one ledgerFor
// selects (or bundles and promotions land in different places), and the
// local-env predicate must agree with hostedEnvKindOf on every env that has
// a kind (or forge holds two notions of "local").

import (
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// TestEnvReportsSessionsAgreesWithTheEnvKind is the consistency check.
//
// envReportsSessions reads the FACTS (HasHosted, runsOnOwnCluster) rather
// than the kind enum, because hostedEnvKindOf answers "" for an env with no
// control plane — which is most envs that report sessions. This proves the
// two never disagree where both have an opinion, so reading the facts is an
// EXTENSION of the kind rule rather than a second rule.
func TestEnvReportsSessionsAgreesWithTheEnvKind(t *testing.T) {
	cp := &ControlPlaneEntity{Endpoint: "https://cp"}
	host := hostWL("api")
	hosted := hostedWL("api")
	onCluster := clusterWL("search", "k3d-x", "ns")

	cases := []struct {
		name string
		e    *KCLEntities
		want bool
	}{
		// No control plane at all: the common dev env, and the case a
		// kind-enum predicate would have wrongly excluded.
		{"host only, no control plane", &KCLEntities{Workloads: []WorkloadEntity{host}}, true},
		{"nothing declared", &KCLEntities{}, true},
		{"local control-plane env", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host}}, true},
		{"host + compose", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host, composeWL("pg", "docker-compose.yml")}}, true},
		{"support cluster_target only", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host}, ClusterTarget: &ClusterTargetEntity{Cluster: "k3d-x"}}, true},

		{"hosted workload", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{hosted}}, false},
		{"hosted database", &KCLEntities{ControlPlane: cp, Databases: []DatabaseEntity{{Name: "db", Runtime: RuntimeHosted}}}, false},
		{"cluster workload", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{onCluster}}, false},
		{"cluster database", &KCLEntities{ControlPlane: cp, Workloads: []WorkloadEntity{host}, Databases: []DatabaseEntity{{Name: "db", Runtime: RuntimeCluster}}}, false},
		// A cluster workload with no control plane still runs on a
		// cluster, so it still has no local presence. The absence of a
		// control plane is about WHERE records go, not about what runs.
		{"cluster workload, no control plane", &KCLEntities{Workloads: []WorkloadEntity{onCluster}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			why, got := envReportsSessions(c.e)
			if got != c.want {
				t.Fatalf("envReportsSessions = %v (%q), want %v", got, why, c.want)
			}
			if !got && why == "" {
				t.Fatal("declined to report sessions without saying why; a surface must be able to tell the user which")
			}
			// The agreement: where the env HAS a kind, local is
			// exactly when sessions are reported.
			if kind := hostedEnvKindOf(c.e); kind != "" {
				if wantByKind := kind == deploytarget.HostedEnvLocal; wantByKind != got {
					t.Fatalf("envReportsSessions = %v but hostedEnvKindOf = %q: forge now holds two notions of \"local\"", got, kind)
				}
			}
		})
	}
	if _, ok := envReportsSessions(nil); ok {
		t.Fatal("nil entities reported sessions; an unreadable declaration is not a local env")
	}
}

// TestSessionTargetUsesTheMachineLedgerForASelfManagedProject pins the
// default: no control plane declared means this machine's ledger, which is
// the same answer ledgerForEntities gives.
func TestSessionTargetUsesTheMachineLedgerForASelfManagedProject(t *testing.T) {
	dir := newLedgerTestProject(t, "f6b-machine")
	entities := &KCLEntities{Workloads: []WorkloadEntity{hostWL("api")}}

	target, err := sessionTargetForEntities("dev", entities, dir)
	if err != nil {
		t.Fatalf("sessionTargetForEntities: %v", err)
	}
	if !target.Report {
		t.Fatalf("a host-only env does not report sessions: %s", target.Skip)
	}
	if target.Hosted {
		t.Fatal("an env with no control plane selected the hosted records store")
	}
	if _, ok := target.Reporter.(machineRecordStore); !ok {
		t.Fatalf("reporter is %T, want machineRecordStore", target.Reporter)
	}

	// The agreement that matters: the SAME declaration selects the machine
	// ledger for promotions. A records store chosen by a different rule
	// could put an env's sessions and its promotions in two places.
	ledger, err := ledgerForEntities("dev", entities, dir)
	if err != nil {
		t.Fatalf("ledgerForEntities: %v", err)
	}
	if ledger.Hosted {
		t.Fatal("ledgerForEntities chose hosted where sessionTargetForEntities chose the machine ledger")
	}
}

// TestSessionTargetFollowsTheHostedLedger is the same agreement on the other
// branch: an env that declares a control plane reports to it, through the
// client that ledger is ALREADY holding rather than a second resolution of
// the endpoint and credential.
func TestSessionTargetFollowsTheHostedLedger(t *testing.T) {
	dir := newLedgerTestProject(t, "f6b-hosted")
	t.Setenv("FORGE_TEST_CP_TOKEN", "rlat_test")
	entities := &KCLEntities{
		ControlPlane: &ControlPlaneEntity{
			Type: "control_plane", Endpoint: "https://cp.example.com/", TokenEnv: "FORGE_TEST_CP_TOKEN",
		},
		Workloads: []WorkloadEntity{hostWL("api")},
	}

	// Precondition: a LOCAL control-plane env — the kind that both keeps
	// its ledger on the control plane AND has presence to report. That
	// combination is what makes this branch reachable at all.
	if kind := hostedEnvKindOf(entities); kind != deploytarget.HostedEnvLocal {
		t.Fatalf("precondition: kind = %q, want local", kind)
	}
	ledger, err := ledgerForEntities("dev", entities, dir)
	if err != nil {
		t.Fatalf("ledgerForEntities: %v", err)
	}
	if !ledger.Hosted {
		t.Fatal("precondition: this env must select the hosted ledger")
	}

	target, err := sessionTargetForEntities("dev", entities, dir)
	if err != nil {
		t.Fatalf("sessionTargetForEntities: %v", err)
	}
	if !target.Report || !target.Hosted {
		t.Fatalf("report = %v, hosted = %v, want both true (%s)", target.Report, target.Hosted, target.Skip)
	}
	store, ok := target.Reporter.(hostedRecordStore)
	if !ok {
		t.Fatalf("reporter is %T, want hostedRecordStore", target.Reporter)
	}
	// Same project as the ledger addresses. A records store scoped to a
	// different project would write presence rows the Live view of this
	// project never reads.
	hostedBindings, ok := ledger.Bindings.(*hostedStore)
	if !ok {
		t.Fatalf("hosted ledger's bindings are %T", ledger.Bindings)
	}
	if store.project != hostedBindings.project {
		t.Fatalf("records project %q != ledger project %q", store.project, hostedBindings.project)
	}
}

// TestSessionTargetDeclinesANonLocalEnv proves the hosted branch is not even
// reached for an env with nothing local: the server's trigger would drop the
// row, so a client that reported anyway would spend a round trip a minute
// producing nothing.
func TestSessionTargetDeclinesANonLocalEnv(t *testing.T) {
	dir := newLedgerTestProject(t, "f6b-persistent")
	entities := &KCLEntities{
		ControlPlane: &ControlPlaneEntity{Type: "control_plane", Endpoint: "https://cp.example.com/", TokenEnv: "FORGE_TEST_CP_TOKEN"},
		Workloads:    []WorkloadEntity{hostedWL("api")},
	}

	// No token is set, and that is part of the assertion: declining must
	// happen BEFORE credential resolution, or a prod env with no local
	// token would fail here instead of simply not reporting.
	target, err := sessionTargetForEntities("prod", entities, dir)
	if err != nil {
		t.Fatalf("sessionTargetForEntities must not fail for a non-local env: %v", err)
	}
	if target.Report {
		t.Fatal("a PERSISTENT env reported a local session: the platform runs its workloads")
	}
	if target.Reporter != nil {
		t.Fatalf("reporter is %T, want nil when nothing is reported", target.Reporter)
	}
	if target.Skip == "" {
		t.Fatal("declined without saying why")
	}
}
